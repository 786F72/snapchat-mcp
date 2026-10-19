package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

type scheduledText struct {
	Conversations []string `json:"conversation_ids"`
	Text          string   `json:"text"`
}
type JobDelivery struct {
	ConversationID string `json:"conversation_id"`
	IdempotencyKey string `json:"idempotency_key"`
	State          string `json:"state"`
	ErrorCode      string `json:"error_code,omitempty"`
}
type jobSummary struct {
	RunAt           string        `json:"run_at"`
	ConversationIDs []string      `json:"conversation_ids"`
	Results         []JobDelivery `json:"results"`
	CompletedAt     string        `json:"completed_at,omitempty"`
}

// CreateTextJob stores encrypted content and public progress separately. No send occurs here.
func (s *Store) CreateTextJob(ctx context.Context, account string, conversations []string, text, runAt, key string) (Job, error) {
	if len(conversations) < 1 || len(conversations) > 25 || strings.TrimSpace(text) == "" || len(text) > 16000 || strings.TrimSpace(key) == "" || len(key) > 128 {
		return Job{}, fail("invalid_argument", "provide 1–25 conversation_ids, text up to 16000 bytes, and an idempotency_key up to 128 bytes", 400)
	}
	unlock, err := s.lockAccount(ctx, account)
	if err != nil {
		return Job{}, err
	}
	defer unlock()
	// A fixed immediate sentinel keeps an omitted run_at stable across caller retries.
	due := int64(0)
	canonicalTime := "immediate"
	if runAt != "" {
		t, err := time.Parse(time.RFC3339Nano, runAt)
		if err != nil {
			return Job{}, fail("invalid_argument", "run_at must be an RFC3339 timestamp with timezone", 400)
		}
		due = t.UnixMilli()
		canonicalTime = t.UTC().Format(time.RFC3339Nano)
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range conversations {
		u := parseUUID(id)
		if u == nil {
			return Job{}, fail("invalid_argument", "conversation_ids must contain UUIDs", 400)
		}
		id = uuidString(u)
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	sort.Strings(ids)
	raw, _ := json.Marshal(scheduledText{ids, text})
	hash := sendHash(string(raw), canonicalTime, "jobs.text")
	var existing, previous string
	err = s.db.QueryRowContext(ctx, "SELECT job_id,payload_hash FROM scheduled_jobs WHERE account_id=? AND idempotency_key=?", account, key).Scan(&existing, &previous)
	if err == nil {
		if previous != hash {
			return Job{}, fail("conflict", "job key belongs to different content, schedule or destinations", 409)
		}
		return s.Job(ctx, account, existing, false)
	}
	if err != sql.ErrNoRows {
		return Job{}, err
	}
	for _, id := range ids {
		if _, err = s.contentInput(ctx, account, id, text, "", "send", "text", "", ""); err != nil {
			return Job{}, err
		}
	}
	id := newID()
	sealed, err := s.vault.Seal(account+"/job/"+id, raw)
	if err != nil {
		return Job{}, fail("vault_unavailable", "cannot encrypt scheduled message", 409)
	}
	summary, _ := json.Marshal(jobSummary{RunAt: canonicalTime, ConversationIDs: ids, Results: []JobDelivery{}})
	j := Job{ID: id, State: "queued", Data: summary, CreatedAt: now()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	// Serializes duplicate creators even across service processes.
	err = tx.QueryRowContext(ctx, "SELECT job_id,payload_hash FROM scheduled_jobs WHERE account_id=? AND idempotency_key=?", account, key).Scan(&existing, &previous)
	if err == nil {
		tx.Rollback()
		if previous != hash {
			return Job{}, fail("conflict", "job key belongs to another job", 409)
		}
		return s.Job(ctx, account, existing, false)
	}
	if err != sql.ErrNoRows {
		return Job{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO jobs VALUES(?,?,?,?,?)", id, account, j.State, summary, j.CreatedAt); err != nil {
		return Job{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO scheduled_jobs VALUES(?,?,?,?,?,?)", id, account, key, hash, due, sealed); err != nil {
		return Job{}, err
	}
	if err = appendEvent(ctx, tx, account, "job.created", map[string]any{"job_id": id, "count": len(ids), "run_at": canonicalTime}); err != nil {
		return Job{}, err
	}
	return j, tx.Commit()
}

// RunDueJob claims one job atomically. A claimed job is never replayed after a crash.
// Deterministic per-destination keys allow the operator to inspect every attempted send.
func (s *Store) RunDueJob(ctx context.Context, account, id string) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var sealed []byte
	var due int64
	var j Job
	err = tx.QueryRowContext(ctx, "SELECT j.id,j.state,j.data,j.created_at,q.run_at,q.sealed FROM jobs j JOIN scheduled_jobs q ON q.job_id=j.id WHERE j.account_id=? AND j.id=?", account, id).Scan(&j.ID, &j.State, &j.Data, &j.CreatedAt, &due, &sealed)
	if err == sql.ErrNoRows {
		return Job{}, fail("not_found", "scheduled job not found for this account", 404)
	}
	if err != nil {
		return Job{}, err
	}
	if j.State != "queued" {
		return Job{}, fail("conflict", "only queued jobs may run; inspect saved delivery results for running or uncertain jobs", 409)
	}
	if due > time.Now().UnixMilli() {
		return Job{}, fail("not_due", "scheduled time has not arrived", 409)
	}
	plain, err := s.vault.Open(account+"/job/"+id, sealed)
	if err != nil {
		return Job{}, fail("vault_unavailable", "cannot decrypt job", 409)
	}
	var payload scheduledText
	var summary jobSummary
	if json.Unmarshal(plain, &payload) != nil || json.Unmarshal(j.Data, &summary) != nil {
		return Job{}, fail("internal_error", "invalid stored job", 500)
	}
	claimed, err := tx.ExecContext(ctx, "UPDATE jobs SET state='running' WHERE id=? AND account_id=? AND state='queued'", id, account)
	if err != nil {
		return Job{}, err
	}
	n, err := claimed.RowsAffected()
	if err != nil || n != 1 {
		return Job{}, fail("conflict", "another worker owns this job", 409)
	}
	if err = appendEvent(ctx, tx, account, "job.started", map[string]string{"job_id": id}); err != nil {
		return Job{}, err
	}
	if err = tx.Commit(); err != nil {
		return Job{}, err
	}
	successes := 0
	for index, conversation := range payload.Conversations {
		delivery := JobDelivery{ConversationID: conversation, IdempotencyKey: "job:" + id + ":" + conversation, State: "not_sent"}
		if ctx.Err() != nil {
			delivery.ErrorCode = "interrupted"
		} else {
			send := s.jobSend
			if send == nil {
				send = func(c context.Context, a, v, t, k string) (any, error) { return s.SendText(c, a, v, t, "", k) }
			}
			var result any
			_, sendErr := s.contentInput(ctx, account, conversation, payload.Text, "", "send", "text", "", "")
			if sendErr == nil {
				result, sendErr = send(ctx, account, conversation, payload.Text, delivery.IdempotencyKey)
			}
			if sendErr == nil {
				if receipt, ok := result.(map[string]any); ok && receipt["acknowledged"] == true {
					delivery.State = "sent"
					successes++
				} else {
					delivery.State = "unknown"
					delivery.ErrorCode = "delivery_unknown"
				}
			} else {
				delivery.ErrorCode = publicError(sendErr).Code
				// Read the durable ledger instead of guessing whether a native failure submitted.
				var state string
				if err := s.db.QueryRow("SELECT state FROM deliveries WHERE account_id=? AND idempotency_key=?", account, delivery.IdempotencyKey).Scan(&state); err == nil {
					delivery.State = state
				} else if err != sql.ErrNoRows {
					delivery.State = "unknown"
				}
			}
		}
		summary.Results = append(summary.Results, delivery)
		data, _ := json.Marshal(summary)
		persist, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = s.db.ExecContext(persist, "UPDATE jobs SET data=? WHERE account_id=? AND id=? AND state='running'", data, account, id)
		cancel()
		if err != nil {
			return Job{}, fail("job_unknown", "could not persist progress; job will not be automatically replayed", 409)
		}
		if index+1 < len(payload.Conversations) && ctx.Err() == nil {
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-timer.C:
			case <-ctx.Done():
			}
			timer.Stop()
		}
	}
	state := "failed"
	if successes == len(payload.Conversations) {
		state = "completed"
	} else if successes > 0 {
		state = "partial"
	}
	for _, r := range summary.Results {
		if r.State == "unknown" || r.State == "pending" {
			state = "unknown"
		}
	}
	summary.CompletedAt = now()
	data, _ := json.Marshal(summary)
	persist, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err = s.db.BeginTx(persist, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(persist, "UPDATE jobs SET state=?,data=? WHERE account_id=? AND id=? AND state='running'", state, data, account, id); err != nil {
		return Job{}, err
	}
	if err = appendEvent(persist, tx, account, "job.finished", map[string]any{"job_id": id, "state": state, "sent": successes}); err != nil {
		return Job{}, err
	}
	if err = tx.Commit(); err != nil {
		return Job{}, err
	}
	j.State = state
	j.Data = data
	return j, nil
}

// RunScheduler services persisted due jobs while serve is running. It returns on shutdown.
func (s *Store) RunScheduler(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		rows, err := s.db.QueryContext(ctx, "SELECT j.account_id,j.id FROM jobs j JOIN scheduled_jobs q ON q.job_id=j.id WHERE j.state='queued' AND q.run_at<=? ORDER BY q.run_at,j.created_at LIMIT 10", time.Now().UnixMilli())
		type target struct{ account, id string }
		targets := []target{}
		if err == nil {
			for rows.Next() {
				var t target
				if rows.Scan(&t.account, &t.id) == nil {
					targets = append(targets, t)
				}
			}
			rows.Close()
		}
		for _, t := range targets {
			if ctx.Err() != nil {
				return
			}
			_, _ = s.RunDueJob(ctx, t.account, t.id)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
