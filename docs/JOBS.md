# Scheduled sends and batches

`jobs.create` queues one text for up to 25 explicit conversation UUIDs. Use
`conversations.list` to select chats. The service sends queued jobs while `serve`
is running, with two seconds between destinations. It checks for due jobs every
five seconds when idle. Scheduling is best effort; a busy or stopped server can
run a job late. Queued jobs survive restarts and run when the server returns.

MCP tool: `snapchat_jobs_create`.
HTTP: `POST /v1/accounts/{account_id}/jobs/create`.

```json
{
  "conversation_ids": ["CONVERSATION_UUID"],
  "text": "See you tomorrow!",
  "run_at": "2026-10-20T18:00:00-04:00",
  "idempotency_key": "tomorrow-reminder-001"
}
```

Include `account_id` in MCP arguments. Omit `run_at` for immediate execution.
Only text is supported. This takes conversation IDs, including groups, rather
than friend IDs or usernames. Duplicate IDs are removed. The text is encrypted
in the database and omitted from job summaries and events.

Reusing a key with the same text, destinations and time returns the existing job.
Changing those inputs with the same key returns `conflict`. Destination order and
equivalent timezone representations do not create a different job.

## Progress and cancellation

Use `jobs.get` with `job_id`, or `jobs.list`. Their MCP names are
`snapchat_jobs_get` and `snapchat_jobs_list`. Results include the scheduled time,
destination IDs, and per-destination results. `jobs.cancel` cancels queued jobs;
a running job cannot be cancelled through that tool. `accounts.logout` cancels
queued jobs and prevents later sends from using the removed session; a request
already in flight can still finish.

`jobs.run` explicitly executes one queued job whose time has arrived. It waits
for completion. The background worker can claim it first; in that case the tool
returns `conflict`, and `jobs.get` shows its progress. Long batches can outlast a
client timeout; remaining destinations then report interruption.

| Job state | Meaning |
| --- | --- |
| `queued` | Waiting for the scheduled time and a worker |
| `running` | Claimed; inspect progress before doing anything else |
| `completed` | Every destination acknowledged the message |
| `partial` | Some destinations succeeded; others did not send |
| `failed` | No destination acknowledged and no attempt is uncertain |
| `unknown` | At least one attempt might have reached Snapchat |
| `cancelled` | Cancelled before execution |

Each result includes a stable `idempotency_key` for `deliveries.get`, a delivery
state, and an error code when applicable. A preflight failure may have no delivery
record because nothing reached the send stage. Friendship, membership and
exclusions are checked again at execution. Keep friends synchronized: direct
texts require a friend snapshot less than one hour old. An expired session or
changed exclusions can stop a previously queued send.

Failures and uncertain outcomes are never retried automatically. A process crash
can leave a job `running`; restarting does not replay it. Inspect its recorded
results and each destination's delivery key before submitting new work. For a
missing result, the delivery key is `job:JOB_ID:CONVERSATION_UUID`. Do not create
a new key to retry a pending or unknown delivery.

Job lifecycle events appear in `events.list` and the HTTP `events/stream`.
