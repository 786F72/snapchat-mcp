package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const Version = "0.6.0"

const ProtocolBlocker = "Conversation and text-history pagination, paged message search, direct/group texts, encrypted media upload, and guarded My Story publication are implemented. Direct EEL and group TLS-only JPEG snap delivery are live-validated. Incoming friend requests, public profile lookup, own story status reads, and bounded inbox polling are implemented. Owned story metadata is implemented but awaits live validation. Remote story feed listing/deletion and other unvalidated operations remain unavailable."

type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Status  int            `json:"-"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func fail(code, message string, status int) *Error {
	return &Error{Code: code, Message: message, Status: status}
}
func publicError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return fail("internal_error", "local service operation failed", 500)
}

type Result struct {
	Data  any    `json:"data,omitempty"`
	Error *Error `json:"error,omitempty"`
}
type Operation struct {
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	Available         bool           `json:"available"`
	Mutation          bool           `json:"mutation"`
	Account           bool           `json:"account_scoped"`
	Properties        map[string]any `json:"-"`
	Required          []string       `json:"-"`
	UnavailableReason string         `json:"unavailable_reason,omitempty"`
}

func prop(t string) map[string]any { return map[string]any{"type": t} }
func arrayProp() map[string]any {
	return map[string]any{"type": "array", "items": prop("string"), "maxItems": 10000}
}
func Operations() []Operation {
	ops := []Operation{
		{Name: "accounts.list", Description: "List locally registered accounts (not a network synchronization).", Available: true, Properties: map[string]any{"cursor": prop("string"), "limit": prop("integer")}},
		{Name: "accounts.create", Description: "Register an isolated local account record; this does not authenticate to Snapchat.", Available: true, Mutation: true, Properties: map[string]any{"label": prop("string")}, Required: []string{"label"}},
		{Name: "friends.list", Description: "Synchronize confirmed friends over the standalone Atlas endpoint, refresh the send eligibility snapshot, and list nonexcluded friends.", Available: true, Account: true, Properties: map[string]any{"cursor": prop("string"), "limit": prop("integer")}},
		{Name: "media.upload", Description: "Encrypt and upload PNG, JPEG or H.264/AAC MP4 (16 MiB maximum). Returns an account-bound media handle, never keys. Does not send or publish.", Available: true, Mutation: true, Account: true, Properties: map[string]any{"content_base64": prop("string"), "mime_type": prop("string")}, Required: []string{"content_base64", "mime_type"}},
		{Name: "snaps.prepare", Description: "Prepare and validate a direct/group snap packet without sending. Direct EEL and group TLS-only preparation are validated.", Available: true, Account: true, Properties: map[string]any{"conversation_id": prop("string"), "media_id": prop("string")}, Required: []string{"conversation_id", "media_id"}},
		{Name: "snaps.send", Description: "Send an uploaded snap with exclusions, membership validation, durable idempotency, and matching RPC acknowledgment. Direct EEL and group TLS-only JPEG delivery are live-validated. Video snap delivery has not been live-tested.", Available: true, Mutation: true, Account: true, Properties: map[string]any{"conversation_id": prop("string"), "media_id": prop("string"), "idempotency_key": prop("string")}, Required: []string{"conversation_id", "media_id", "idempotency_key"}},
		{Name: "requests.list", Description: "Synchronize incoming friend requests through the standalone friend-request service. Filters ignored and excluded users; never accepts requests or marks them viewed. UUID cursors paginate a freshly synchronized full snapshot.", Available: true, Account: true, Properties: map[string]any{"cursor": prop("string"), "limit": prop("integer")}},
		{Name: "profiles.get", Description: "Read public Snapchat identity information for one recipient UUID, including username, display name, official/popular/chatbot flags. Enforces local exclusions. Does not return private profile information or modify friendships.", Available: true, Account: true, Properties: map[string]any{"recipient_id": prop("string")}, Required: []string{"recipient_id"}},
		{Name: "stories.status", Description: "Read this account's latest story post timestamp and active story counters without opening stories. Counters do not establish whether a particular publication is visible or deleted.", Available: true, Account: true},
		{Name: "stories.get", Description: "Unavailable pending live validation: read remote metadata for one owned, acknowledged story published by this service, using its returned story_id as content_id. Never downloads media, exports media keys, or sends a view receipt. Missing metadata is reported as unknown, not confirmed deletion.", Available: false, UnavailableReason: "Owned story metadata adapter is implemented but has not been live-validated; a renewed signed-in session is required before enabling it.", Account: true, Properties: map[string]any{"content_id": prop("string")}, Required: []string{"content_id"}},
		{Name: "stories.published", Description: "List acknowledged story publications recorded by this service, with story IDs and idempotency keys. Local history only; does not verify current remote visibility, expiry, or deletion.", Available: true, Account: true, Properties: map[string]any{"cursor": prop("string"), "limit": prop("integer")}},
		{Name: "stories.delete", Description: "Unavailable: delete one owned story using its publication story_id as content_id. The standalone deletion backend is not implemented or validated; returns unsupported_capability without changing the story.", UnavailableReason: "Standalone story deletion is not implemented or validated. The signed-in Public Profile manager does not expose friends-only My Story deletion. Delete the story in the Snapchat mobile app; no deletion was submitted.", Mutation: true, Account: true, Properties: map[string]any{"content_id": prop("string")}, Required: []string{"content_id"}},
		{Name: "stories.prepare", Description: "Prepare a friends-only My Story packet without publishing. Reports whether the audience is fresh, verified, nonempty, and allowed by exclusions; ignore_exclusions explicitly overrides exclusions for this request only.", Available: true, Account: true, Properties: map[string]any{"ignore_exclusions": map[string]any{"type": "boolean", "default": false, "description": "Allow excluded viewers for this story request only; never alters the exclusion list."}, "audience": map[string]any{"type": "string", "enum": []string{"friends", "custom"}}, "media_id": prop("string")}, Required: []string{"audience", "media_id"}},
		{Name: "stories.publish", Description: "Publish uploaded media to friends-only My Story with an explicit friends or saved custom audience and durable idempotency. One friends-audience image publication was acknowledged by Snapchat. Exclusions apply by default; ignore_exclusions explicitly overrides them for this request only. Returns audience_not_verified without publishing if the audience is empty, stale, unverified, or includes an excluded viewer without an explicit override.", Available: true, Mutation: true, Account: true, Properties: map[string]any{"ignore_exclusions": map[string]any{"type": "boolean", "default": false, "description": "Allow excluded viewers for this story request only; never alters the exclusion list."}, "audience": map[string]any{"type": "string", "enum": []string{"friends", "custom"}}, "media_id": prop("string"), "idempotency_key": prop("string")}, Required: []string{"audience", "media_id", "idempotency_key"}},
		{Name: "messages.prepare", Description: "Build and validate an encrypted direct or group text packet without submitting it.", Available: true, Account: true, Properties: map[string]any{"conversation_id": prop("string"), "text": prop("string")}, Required: []string{"conversation_id", "text"}},
		{Name: "messages.send", Description: "Send direct or group text using the native SDK encryption policy (EEL for direct texts; transport encryption for groups). Requires a synchronized conversation and durable idempotency key. Checks exclusions against synchronized membership; quoted replies are unavailable.", Available: true, Mutation: true, Account: true, Properties: map[string]any{"conversation_id": prop("string"), "text": prop("string"), "reply_to": prop("string"), "idempotency_key": prop("string")}, Required: []string{"conversation_id", "text", "idempotency_key"}},
		{Name: "messages.list", Description: "Fetch paginated text history from a synchronized conversation, newest server IDs first. Continue with the opaque next_cursor, bound to account/conversation and expiring after one hour. Media-only pages may have no text items and still have more history. Never marks messages read or opens snaps.", Available: true, Account: true, Properties: map[string]any{"conversation_id": prop("string"), "cursor": prop("string"), "limit": prop("integer")}, Required: []string{"conversation_id"}},
		{Name: "conversations.list", Description: "Synchronize conversation metadata through the standalone native client. Does not open snaps or mark chats read.", Available: true, Account: true, Properties: map[string]any{"cursor": prop("string"), "limit": prop("integer")}},
		{Name: "accounts.verify_session", Description: "Offline native messaging session initialization probe. Network and key writes remain disabled.", Available: true, Account: true},
		{Name: "accounts.verify_keys", Description: "Offline key restoration probe using pinned Snapchat WASM under Node; no network or key registration. Requires captured root key and installed runtime assets.", Available: true, Account: true},
		{Name: "accounts.refresh", Description: "Refresh a captured Snapchat session directly over HTTP, without a browser. Requires an unlocked OS keyring; never returns tokens.", Available: true, Mutation: true, Account: true},
		{Name: "accounts.get", Description: "Get local account status.", Available: true, Account: true},
		{Name: "capabilities.list", Description: "Get operation availability and standalone protocol blockers.", Available: true, Account: true},
		{Name: "exclusions.list", Description: "List the account's persistent recipient exclusions.", Available: true, Account: true},
		{Name: "exclusions.replace", Description: "Replace all exclusions; an empty array clears them. No network action.", Available: true, Account: true, Mutation: true, Properties: map[string]any{"recipients": arrayProp()}, Required: []string{"recipients"}},
		{Name: "events.list", Description: "Poll durable local events after a cursor. Remote Snapchat events require the standalone protocol.", Available: true, Account: true, Properties: map[string]any{"cursor": prop("string"), "limit": prop("integer")}},
		{Name: "jobs.preview", Description: "Preview explicit recipient IDs against local exclusions. Does not verify friendship or send anything.", Available: true, Account: true, Properties: map[string]any{"recipients": arrayProp()}, Required: []string{"recipients"}},
		{Name: "jobs.list", Description: "List this account's persisted jobs.", Available: true, Account: true, Properties: map[string]any{"cursor": prop("string"), "limit": prop("integer")}},
		{Name: "jobs.get", Description: "Get a job owned by this account.", Available: true, Account: true, Properties: map[string]any{"job_id": prop("string")}, Required: []string{"job_id"}},
		{Name: "jobs.cancel", Description: "Cancel an existing job owned by this account.", Available: true, Account: true, Mutation: true, Properties: map[string]any{"job_id": prop("string")}, Required: []string{"job_id"}},
	}
	ops = append(ops,
		Operation{Name: "messages.search", Description: "Search one page of this conversation's text history by literal substring, optionally filtering sender UUID and matching case. Follow next_cursor to scan older pages; an empty result can still have more history. Cursors expire after one hour and are bound to account, conversation, and filters. Never opens snaps or marks messages read.", Available: true, Account: true, Properties: map[string]any{"conversation_id": prop("string"), "query": prop("string"), "sender_id": prop("string"), "match_case": prop("boolean"), "cursor": prop("string"), "limit": prop("integer")}, Required: []string{"conversation_id", "query"}},
		Operation{Name: "accounts.rename", Description: "Rename a local account label without changing Snapchat identity, credentials, or profile. Records an event only when the label changes.", Available: true, Mutation: true, Account: true, Properties: map[string]any{"label": prop("string")}, Required: []string{"label"}},
		Operation{Name: "accounts.diagnostics", Description: "Check local Node runtime, stored session material, installed runtime files, and account-scoped cache counts. Never contacts Snapchat or exports credentials. ready_for_verification is not a live authentication guarantee.", Available: true, Account: true},
		Operation{Name: "conversations.search", Description: "Search cached conversation titles, IDs, or participant UUIDs without network access. Applies exclusions and optional participant_id filtering. Each result includes snapshot age; refresh conversations.list to update the cache. Cursor is the last returned conversation UUID; retain filters across pages.", Available: true, Account: true, Properties: map[string]any{"query": prop("string"), "participant_id": prop("string"), "cursor": prop("string"), "limit": prop("integer")}, Required: []string{"query"}},
		Operation{Name: "groups.list", Description: "List cached user-created groups with optional title/UUID query and participant UUID filtering. Applies exclusions and reports snapshot age. No remote membership changes or synchronization. Retain filters across cursor pages.", Available: true, Account: true, Properties: map[string]any{"query": prop("string"), "participant_id": prop("string"), "cursor": prop("string"), "limit": prop("integer")}},
		Operation{Name: "deliveries.list", Description: "List this account's local delivery attempts and redacted receipts, optionally filtered by state. Orders by idempotency key, not time. Reuse next_cursor with the same filter; never sends or retries content. Only not_sent attempts permit same-key retry.", Available: true, Account: true, Properties: map[string]any{"state": prop("string"), "cursor": prop("string"), "limit": prop("integer")}},
		Operation{Name: "accounts.session_status", Description: "Inspect this account's encrypted local session and runtime installation. Returns capture/refresh timestamps and a suggested next action, never credentials. No network requests; captured does not mean the session is still valid. Use accounts.verify_session for a live check.", Available: true, Account: true},
		Operation{Name: "inbox.poll", Description: "Poll incoming texts and snap metadata without opening media or marking chats read. Returns a durable account-bound next_cursor, stable event IDs, and explicit synchronization errors/gap indicators. First poll may include recent backlog. Fetches bounded recent windows only for changed/uninitialized cached conversations; use messages.list for older history pagination.", Available: true, Account: true, Properties: map[string]any{"cursor": prop("string"), "limit": prop("integer"), "max_conversations": prop("integer")}},
		Operation{Name: "friends.search", Description: "Search the synchronized confirmed-friend snapshot by username, display name, or UUID. Applies exclusions, reports snapshot age, and never performs a network sync. Call friends.list to refresh.", Available: true, Account: true, Properties: map[string]any{"query": prop("string"), "cursor": prop("string"), "limit": prop("integer")}, Required: []string{"query"}},
		Operation{Name: "recipients.resolve", Description: "Resolve an exact username, UUID, or unique exact display name from the friend snapshot. Partial or ambiguous matches return candidates without choosing a recipient. Exclusions and self filtering apply; stale data does not prove send eligibility.", Available: true, Account: true, Properties: map[string]any{"query": prop("string")}, Required: []string{"query"}},
		Operation{Name: "conversations.get", Description: "Get one account-scoped cached conversation, including title, participants, version, and snapshot age. Blocks excluded participants. Refresh conversations.list first when missing or stale.", Available: true, Account: true, Properties: map[string]any{"conversation_id": prop("string")}, Required: []string{"conversation_id"}},
		Operation{Name: "groups.get", Description: "Get one cached user-created group and its participants. Blocks excluded members and reports snapshot age. Does not synchronize membership or open messages.", Available: true, Account: true, Properties: map[string]any{"conversation_id": prop("string")}, Required: []string{"conversation_id"}},
		Operation{Name: "exclusions.add", Description: "Atomically add one recipient UUID to this account's local exclusions without replacing other entries. Idempotent; no friendship or network change.", Available: true, Account: true, Mutation: true, Properties: map[string]any{"recipient_id": prop("string")}, Required: []string{"recipient_id"}},
		Operation{Name: "exclusions.remove", Description: "Atomically remove one recipient UUID from this account's local exclusions without replacing other entries. Idempotent; no friendship or network change.", Available: true, Account: true, Mutation: true, Properties: map[string]any{"recipient_id": prop("string")}, Required: []string{"recipient_id"}},
		Operation{Name: "deliveries.get", Description: "Look up this account's durable delivery status by idempotency key. Returns a redacted receipt and same-key retry eligibility; never sends or retries content.", Available: true, Account: true, Properties: map[string]any{"idempotency_key": prop("string")}, Required: []string{"idempotency_key"}},
		Operation{Name: "media.upload_file", Description: "Encrypt and upload a local PNG/JPEG/MP4 file from an operator-configured media root on the HTTP server host. Requires serve --media-root. Returns a media handle, not file contents or encryption keys. Does not send or publish.", Available: true, Account: true, Mutation: true, Properties: map[string]any{"path": prop("string"), "mime_type": prop("string")}, Required: []string{"path", "mime_type"}},
	)

	groups := []struct {
		names      string
		mutation   bool
		properties map[string]any
		required   []string
	}{
		{"accounts.login accounts.logout", true, nil, nil},
		{"friends.add friends.remove friends.block friends.unblock requests.accept requests.reject", true, map[string]any{"recipient_id": prop("string")}, []string{"recipient_id"}},
		{"search.users", false, map[string]any{"query": prop("string")}, []string{"query"}},
		{"stories.list", false, map[string]any{"cursor": prop("string"), "limit": prop("integer")}, nil},
		{"messages.react messages.save messages.delete", true, map[string]any{"conversation_id": prop("string"), "message_id": prop("string"), "reaction": prop("string"), "idempotency_key": prop("string")}, []string{"conversation_id", "message_id", "idempotency_key"}},
		{"groups.create", true, map[string]any{"recipients": arrayProp(), "title": prop("string"), "idempotency_key": prop("string")}, []string{"recipients", "idempotency_key"}},
		{"groups.add_members groups.remove_members", true, map[string]any{"conversation_id": prop("string"), "recipients": arrayProp(), "idempotency_key": prop("string")}, []string{"conversation_id", "recipients", "idempotency_key"}},
		{"snaps.open stories.view", true, map[string]any{"content_id": prop("string")}, []string{"content_id"}},
		{"conversations.mark_read", true, map[string]any{"conversation_id": prop("string")}, []string{"conversation_id"}},
		{"jobs.create", true, map[string]any{"recipients": arrayProp(), "text": prop("string"), "media_id": prop("string"), "idempotency_key": prop("string")}, []string{"recipients", "idempotency_key"}},
	}
	for _, g := range groups {
		for _, n := range strings.Fields(g.names) {
			ops = append(ops, Operation{Name: n, Description: "Unavailable standalone operation: " + n + ". " + ProtocolBlocker, Mutation: g.mutation, Account: true, Properties: g.properties, Required: g.required})
		}
	}
	for i := range ops {
		enrichOperation(&ops[i])
	}
	return ops
}
func (o Operation) Method() string {
	if o.Mutation || o.Name == "inbox.poll" || o.Name == "jobs.preview" || o.Name == "messages.prepare" || o.Name == "snaps.prepare" || o.Name == "stories.prepare" {
		return "POST"
	}
	return "GET"
}
func (o Operation) Schema() map[string]any {
	p := map[string]any{}
	for k, v := range o.Properties {
		p[k] = v
	}
	required := append([]string{}, o.Required...)
	if o.Account {
		p["account_id"] = map[string]any{"type": "string", "description": "Local account ID returned by accounts.list or accounts.create; not a Snapchat user UUID.", "minLength": 1, "maxLength": 128}
		required = append(required, "account_id")
	}
	return map[string]any{"type": "object", "properties": p, "required": required, "additionalProperties": false}
}
func validate(o Operation, in map[string]any) error {
	p := o.Schema()["properties"].(map[string]any)
	for _, k := range o.Schema()["required"].([]string) {
		v, ok := in[k]
		if !ok || v == nil {
			return fail("invalid_argument", "missing "+k, 400)
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			return fail("invalid_argument", k+" must not be empty", 400)
		}
	}
	for k, v := range in {
		schema, ok := p[k]
		if !ok {
			return fail("invalid_argument", "unknown argument "+k, 400)
		}
		t := schema.(map[string]any)["type"]
		valid := false
		switch t {
		case "string":
			_, valid = v.(string)
		case "boolean":
			_, valid = v.(bool)
		case "integer":
			n, ok := v.(float64)
			valid = ok && n == float64(int64(n))
		case "array":
			a, ok := v.([]any)
			valid = ok && len(a) <= 10000
			for _, x := range a {
				str, ok := x.(string)
				valid = valid && ok && strings.TrimSpace(str) != "" && len(str) <= 128
			}
		}
		bounds := schema.(map[string]any)
		if text, ok := v.(string); ok {
			n := utf8.RuneCountInString(text)
			if min, ok := bounds["minLength"].(int); ok {
				valid = valid && n >= min
			}
			if max, ok := bounds["maxLength"].(int); ok {
				valid = valid && n <= max
			}
		}
		if n, ok := v.(float64); ok {
			if min, ok := bounds["minimum"].(int); ok {
				valid = valid && n >= float64(min)
			}
			if max, ok := bounds["maximum"].(int); ok {
				valid = valid && n <= float64(max)
			}
		}
		if choices, ok := schema.(map[string]any)["enum"].([]string); ok {
			choice, _ := v.(string)
			matched := false
			for _, allowed := range choices {
				if choice == allowed {
					matched = true
				}
			}
			valid = valid && matched
		}
		if !valid {
			return fail("invalid_argument", "invalid type or value for "+k, 400)
		}
	}
	return nil
}
func (s *Store) Call(ctx context.Context, name string, in map[string]any) Result {
	data, err := s.call(ctx, name, in)
	if err != nil {
		return Result{Error: publicError(err)}
	}
	return Result{Data: data}
}
func (s *Store) call(ctx context.Context, name string, in map[string]any) (any, error) {
	var op *Operation
	for _, o := range Operations() {
		if o.Name == name {
			copy := o
			op = &copy
			break
		}
	}
	if op == nil {
		return nil, fail("not_found", "unknown operation", 404)
	}
	if err := validate(*op, in); err != nil {
		return nil, err
	}
	account, _ := in["account_id"].(string)
	if op.Account {
		if _, err := s.Account(ctx, account); err != nil {
			return nil, err
		}
	}
	if !op.Available {
		reason := op.UnavailableReason
		if reason == "" {
			reason = ProtocolBlocker
		}
		return nil, fail("unsupported_capability", reason, 501)
	}
	limit := 100
	if n, ok := in["limit"].(float64); ok {
		if n < 1 || n > 500 {
			return nil, fail("invalid_argument", "limit must be 1–500", 400)
		}
		limit = int(n)
	}
	cursor, _ := in["cursor"].(string)
	switch name {
	case "messages.search":
		if _, ok := in["limit"]; !ok {
			limit = 40
		}
		sender, _ := in["sender_id"].(string)
		return s.SearchMessages(ctx, account, in["conversation_id"].(string), in["query"].(string), sender, cursor, limit, in["match_case"] == true)
	case "accounts.rename":
		return s.RenameAccount(ctx, account, in["label"].(string))
	case "accounts.diagnostics":
		return s.Diagnostics(ctx, account)
	case "conversations.search", "groups.list":
		query, _ := in["query"].(string)
		participant, _ := in["participant_id"].(string)
		return s.SearchConversations(ctx, account, query, participant, cursor, limit, name == "groups.list")
	case "deliveries.list":
		state, _ := in["state"].(string)
		return s.ListDeliveries(ctx, account, state, cursor, limit)
	case "accounts.session_status":
		return s.SessionStatus(ctx, account)
	case "accounts.list":
		return s.Accounts(ctx, cursor, limit)
	case "accounts.create":
		return s.CreateAccount(ctx, in["label"].(string))
	case "inbox.poll":
		budget := 3
		if n, ok := in["max_conversations"].(float64); ok {
			budget = int(n)
		}
		if _, ok := in["limit"]; !ok {
			limit = 40
		}
		return s.PollInbox(ctx, account, cursor, limit, budget)
	case "friends.search":
		return s.SearchFriends(ctx, account, in["query"].(string), cursor, limit)
	case "recipients.resolve":
		return s.ResolveRecipient(ctx, account, in["query"].(string))
	case "conversations.get", "groups.get":
		return s.GetConversation(ctx, account, in["conversation_id"].(string), name == "groups.get")
	case "exclusions.add", "exclusions.remove":
		return s.ChangeExclusion(ctx, account, in["recipient_id"].(string), name == "exclusions.add")
	case "deliveries.get":
		return s.GetDelivery(ctx, account, in["idempotency_key"].(string))
	case "media.upload_file":
		return s.UploadMediaFile(ctx, account, in["path"].(string), in["mime_type"].(string))
	case "requests.list":
		return s.IncomingRequests(ctx, account, cursor, limit)
	case "profiles.get":
		return s.PublicProfile(ctx, account, in["recipient_id"].(string))
	case "stories.status":
		return s.ActiveStoryStatus(ctx, account)
	case "stories.get":
		return s.StoryMetadata(ctx, account, in["content_id"].(string))
	case "friends.list":
		return s.SyncFriends(ctx, account, cursor, limit)
	case "media.upload":
		return s.UploadMedia(ctx, account, in["content_base64"].(string), in["mime_type"].(string))
	case "snaps.prepare":
		return s.prepareContent(ctx, account, in["conversation_id"].(string), "", "snap", in["media_id"].(string), "")
	case "stories.published":
		return s.PublishedStories(ctx, account, cursor, limit)
	case "stories.prepare":
		return s.prepareContent(ctx, account, myStoryID, "", "story", in["media_id"].(string), in["audience"].(string), in["ignore_exclusions"] == true)
	case "snaps.send":
		return s.SendSnap(ctx, account, in["conversation_id"].(string), in["media_id"].(string), in["idempotency_key"].(string))
	case "stories.publish":
		return s.PublishStory(ctx, account, in["media_id"].(string), in["audience"].(string), in["idempotency_key"].(string), in["ignore_exclusions"] == true)
	case "messages.prepare":
		return s.PrepareText(ctx, account, in["conversation_id"].(string), in["text"].(string))
	case "messages.send":
		reply, _ := in["reply_to"].(string)
		return s.SendText(ctx, account, in["conversation_id"].(string), in["text"].(string), reply, in["idempotency_key"].(string))
	case "messages.list":
		if _, ok := in["limit"]; !ok {
			limit = 40
		}
		if limit > 100 {
			return nil, fail("invalid_argument", "message limit must be 1–100", 400)
		}
		return s.NativeMessages(ctx, account, in["conversation_id"].(string), cursor, limit)
	case "conversations.list":
		return s.Conversations(ctx, account, cursor, limit)
	case "accounts.verify_session":
		return s.ProbeSession(ctx, account)
	case "accounts.verify_keys":
		return s.ProbeKeys(ctx, account)
	case "accounts.refresh":
		return s.Refresh(ctx, account)
	case "accounts.get":
		return s.Account(ctx, account)
	case "capabilities.list":
		s.mediaMu.RLock()
		fileUploadEnabled := len(s.mediaRoots) > 0
		s.mediaMu.RUnlock()
		return map[string]any{"backend": "standalone", "protocol_ready": false, "messaging_core_validated": true, "coverage_complete": false, "blocker": ProtocolBlocker, "operations": Operations(), "media_file_upload_enabled": fileUploadEnabled}, nil
	case "exclusions.list":
		return s.Exclusions(ctx, account)
	case "exclusions.replace":
		ids := []string{}
		for _, v := range in["recipients"].([]any) {
			ids = append(ids, v.(string))
		}
		if err := s.ReplaceExclusions(ctx, account, ids); err != nil {
			return nil, err
		}
		return s.Exclusions(ctx, account)
	case "events.list":
		var c int64
		if cursor != "" {
			var err error
			c, err = strconv.ParseInt(cursor, 10, 64)
			if err != nil || c < 0 {
				return nil, fail("invalid_argument", "cursor must be a nonnegative event ID", 400)
			}
		}
		return s.Events(ctx, account, c, limit)
	case "jobs.preview":
		excluded, err := s.Exclusions(ctx, account)
		if err != nil {
			return nil, err
		}
		blocked := map[string]bool{}
		for _, id := range excluded {
			blocked[id] = true
		}
		eligible, skipped := []string{}, []string{}
		seen := map[string]bool{}
		for _, v := range in["recipients"].([]any) {
			id := v.(string)
			if seen[id] {
				continue
			}
			seen[id] = true
			if blocked[id] {
				skipped = append(skipped, id)
			} else {
				eligible = append(eligible, id)
			}
		}
		return map[string]any{"eligible": eligible, "excluded": skipped, "friendship_verified": false, "can_submit": false, "blocker": ProtocolBlocker}, nil
	case "jobs.list":
		return s.Jobs(ctx, account, cursor, limit)
	case "jobs.get", "jobs.cancel":
		return s.Job(ctx, account, in["job_id"].(string), name == "jobs.cancel")
	}
	return nil, fmt.Errorf("unhandled operation %s", name)
}

// ResultSchema describes the shared HTTP/MCP response envelope.
func ResultSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"data":  map[string]any{},
			"error": map[string]any{"type": "object", "required": []string{"code", "message"}, "additionalProperties": false, "properties": map[string]any{"code": prop("string"), "message": prop("string"), "details": map[string]any{"type": "object"}}},
		},
		"oneOf": []any{map[string]any{"required": []string{"data"}}, map[string]any{"required": []string{"error"}}},
	}
}
