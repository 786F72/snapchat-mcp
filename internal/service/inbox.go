package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"time"
)

const inboxWindowLimit = 100

type InboxEvent struct {
	EventID        string  `json:"event_id"`
	ConversationID string  `json:"conversation_id"`
	MessageID      string  `json:"message_id"`
	SenderID       string  `json:"sender_id"`
	Type           string  `json:"type"`
	Text           *string `json:"text,omitempty"`
	CreatedAt      uint64  `json:"created_at"`
}
type inboxMessage struct {
	ID        string  `json:"id"`
	SenderID  string  `json:"sender_id"`
	Type      string  `json:"type"`
	Text      *string `json:"text,omitempty"`
	CreatedAt uint64  `json:"created_at"`
}
type inboxWindow struct {
	Items        []inboxMessage `json:"items"`
	Participants []string       `json:"participants"`
	HasMore      bool           `json:"has_more"`
	MediaOpened  bool           `json:"media_opened"`
	ReadReceipts bool           `json:"read_receipts_sent"`
}
type inboxMetadata struct {
	Items []Conversation
	Next  string
}
type inboxSource interface {
	metadata(context.Context, string, string) (inboxMetadata, error)
	messages(context.Context, string, string) (inboxWindow, error)
}
type nativeInboxSource struct{ s *Store }

func (n nativeInboxSource) metadata(ctx context.Context, account, cursor string) (inboxMetadata, error) {
	result, err := n.s.Conversations(ctx, account, cursor, 50)
	if err != nil {
		return inboxMetadata{}, err
	}
	d := result.(map[string]any)
	next, _ := d["next_cursor"].(string)
	return inboxMetadata{Items: d["items"].([]Conversation), Next: next}, nil
}
func (n nativeInboxSource) messages(ctx context.Context, account, id string) (inboxWindow, error) {
	var window inboxWindow
	result, err := n.s.nativeMessages(ctx, account, id, "", inboxWindowLimit, "inbox")
	if err != nil {
		return window, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return window, err
	}
	if json.Unmarshal(raw, &window) != nil {
		return window, fail("incompatible_protocol", "invalid inbox history window", 502)
	}
	return window, nil
}

type InboxSyncError struct {
	ConversationID string `json:"conversation_id,omitempty"`
	Code           string `json:"code"`
}
type InboxSync struct {
	MetadataSynced       bool             `json:"metadata_synced"`
	ConversationsFetched int              `json:"conversations_fetched"`
	PendingConversations int              `json:"pending_conversations"`
	MoreWork             bool             `json:"more_work"`
	HistoryGapPossible   []string         `json:"history_gap_possible"`
	Errors               []InboxSyncError `json:"errors"`
	WindowLimit          int              `json:"history_window_limit"`
}
type InboxPage struct {
	Items        []InboxEvent `json:"items"`
	NextCursor   string       `json:"next_cursor"`
	HasMore      bool         `json:"has_more"`
	InitialSync  bool         `json:"initial_sync"`
	Sync         InboxSync    `json:"sync"`
	MediaOpened  bool         `json:"media_opened"`
	ReadReceipts bool         `json:"read_receipts_sent"`
}
type inboxCursor struct {
	Version int   `json:"v"`
	After   int64 `json:"after"`
}

func (s *Store) inboxPosition(account, token string) (int64, error) {
	if token == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) > 1024 {
		return 0, fail("invalid_cursor", "invalid account-bound inbox cursor", 400)
	}
	plain, err := s.vault.Open(account+"/inbox/cursor", raw)
	if err != nil {
		return 0, fail("invalid_cursor", "inbox cursor is invalid or belongs to another account", 400)
	}
	var c inboxCursor
	if json.Unmarshal(plain, &c) != nil || c.Version != 1 || c.After < 0 {
		return 0, fail("invalid_cursor", "invalid inbox cursor state", 400)
	}
	return c.After, nil
}
func (s *Store) inboxToken(account string, after int64) (string, error) {
	raw, _ := json.Marshal(inboxCursor{Version: 1, After: after})
	sealed, err := s.vault.Seal(account+"/inbox/cursor", raw)
	if err != nil {
		return "", fail("vault_unavailable", "cannot seal inbox cursor", 503)
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}
func (s *Store) inboxBlocked(ctx context.Context, account string) (map[string]bool, error) {
	ids, err := s.Exclusions(ctx, account)
	if err != nil {
		return nil, err
	}
	blocked := map[string]bool{}
	for _, id := range ids {
		blocked[id] = true
	}
	return blocked, nil
}
func inboxConversationAllowed(c Conversation, self string, blocked map[string]bool) bool {
	if c.Type != "ONE_ON_ONE" && c.Type != "USER_CREATED_GROUP" {
		return false
	}
	hasSelf := false
	for _, p := range c.Participants {
		if blocked[p] {
			return false
		}
		if p == self {
			hasSelf = true
		}
	}
	return hasSelf && len(c.Participants) >= 2
}

// PollInbox synchronizes bounded metadata/history work and reads a durable log.
// A cursor acknowledges only returned/scanned log positions, never Snapchat reads.
func (s *Store) PollInbox(ctx context.Context, account, cursor string, limit, conversationLimit int) (InboxPage, error) {
	page := InboxPage{Items: []InboxEvent{}, InitialSync: cursor == "", Sync: InboxSync{Errors: []InboxSyncError{}, HistoryGapPossible: []string{}, WindowLimit: inboxWindowLimit}}
	if limit < 1 || limit > 100 || conversationLimit < 1 || conversationLimit > 5 {
		return page, fail("invalid_argument", "limit must be 1–100 and max_conversations 1–5", 400)
	}
	after, err := s.inboxPosition(account, cursor)
	if err != nil {
		return page, err
	}
	var maximum int64
	if err = s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM inbox_events WHERE account_id=?", account).Scan(&maximum); err != nil {
		return page, err
	}
	if after > maximum {
		return page, fail("invalid_cursor", "inbox cursor is ahead of this account's event log", 400)
	}
	snapshot, err := s.readSnapshot(ctx, account)
	if err != nil {
		return page, err
	}
	lock, _ := s.inboxLocks.LoadOrStore(account, make(chan struct{}, 1))
	ch := lock.(chan struct{})
	select {
	case ch <- struct{}{}:
		defer func() { <-ch }()
	case <-ctx.Done():
		return page, ctx.Err()
	}
	syncCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	source := s.inboxSource
	if source == nil {
		source = nativeInboxSource{s}
	}
	page.Sync = s.syncInbox(syncCtx, account, snapshot.UserID, source, conversationLimit)
	// Gap indications survive a lost response and are never cleared implicitly.
	rows, gapErr := s.db.QueryContext(ctx, "SELECT conversation_id FROM inbox_heads WHERE account_id=? AND gap_possible=1 ORDER BY conversation_id", account)
	if gapErr != nil {
		return page, gapErr
	}
	gaps := []string{}
	for rows.Next() {
		var id string
		if gapErr = rows.Scan(&id); gapErr != nil {
			break
		}
		gaps = append(gaps, id)
	}
	if gapErr == nil {
		gapErr = rows.Err()
	}
	rows.Close()
	if gapErr != nil {
		return page, gapErr
	}
	// Do not expose excluded group IDs through gap diagnostics.
	page.Sync.HistoryGapPossible = []string{}
	for _, id := range gaps {
		if _, e := s.GetConversation(ctx, account, id, false); e == nil {
			page.Sync.HistoryGapPossible = append(page.Sync.HistoryGapPossible, id)
		}
	}

	// Return already persisted data even when remote synchronization failed.
	page.Items, page.HasMore, after, err = s.inboxEvents(ctx, account, snapshot.UserID, after, limit)
	if err != nil {
		return page, err
	}
	page.NextCursor, err = s.inboxToken(account, after)
	return page, err
}

func (s *Store) syncInbox(ctx context.Context, account, self string, source inboxSource, budget int) (result InboxSync) {
	result = InboxSync{Errors: []InboxSyncError{}, HistoryGapPossible: []string{}, WindowLimit: inboxWindowLimit}
	defer func() {
		if len(result.Errors) > 0 {
			result.MoreWork = true
		}
	}()
	var scan string
	err := s.db.QueryRowContext(ctx, "SELECT cursor FROM inbox_scans WHERE account_id=?", account).Scan(&scan)
	if err != nil && err != sql.ErrNoRows {
		result.Errors = append(result.Errors, InboxSyncError{Code: "storage_error"})
		return result
	}
	head, err := source.metadata(ctx, account, "")
	if err != nil {
		result.Errors = append(result.Errors, InboxSyncError{Code: publicError(err).Code})
		return result
	}
	next := head.Next
	if scan != "" {
		tail, tailErr := source.metadata(ctx, account, scan)
		if tailErr != nil {
			result.Errors = append(result.Errors, InboxSyncError{Code: publicError(tailErr).Code})
			if publicError(tailErr).Code != "invalid_cursor" {
				next = scan
			}
		} else {
			next = tail.Next
		}
	}
	result.MetadataSynced = true
	if _, err = s.db.ExecContext(ctx, "INSERT INTO inbox_scans VALUES(?,?) ON CONFLICT(account_id) DO UPDATE SET cursor=excluded.cursor", account, next); err != nil {
		result.Errors = append(result.Errors, InboxSyncError{Code: "storage_error"})
		return result
	}
	blocked, err := s.inboxBlocked(ctx, account)
	if err != nil {
		result.Errors = append(result.Errors, InboxSyncError{Code: "storage_error"})
		return result
	}
	pending, err := s.pendingInbox(ctx, account, self, blocked)
	if err != nil {
		result.Errors = append(result.Errors, InboxSyncError{Code: "storage_error"})
		return result
	}
	if len(pending) > budget {
		pending = pending[:budget]
	}
	type fetched struct {
		c      Conversation
		window inboxWindow
		err    error
	}
	results := make(chan fetched, len(pending))
	for _, c := range pending {
		go func(c Conversation) {
			window, err := source.messages(ctx, account, c.ID)
			results <- fetched{c, window, err}
		}(c)
	}
	// Apply results in deterministic conversation order rather than completion order.
	ready := []fetched{}
	for range pending {
		ready = append(ready, <-results)
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].c.ID < ready[j].c.ID })
	for _, item := range ready {
		if item.err != nil {
			result.Errors = append(result.Errors, InboxSyncError{ConversationID: item.c.ID, Code: publicError(item.err).Code})
			continue
		}
		gap, err := s.persistInboxWindow(ctx, account, self, item.c, item.window)
		if err != nil {
			result.Errors = append(result.Errors, InboxSyncError{ConversationID: item.c.ID, Code: publicError(err).Code})
			continue
		}
		result.ConversationsFetched++
		if gap {
			result.HistoryGapPossible = append(result.HistoryGapPossible, item.c.ID)
		}
	}
	remaining, err := s.pendingInbox(ctx, account, self, blocked)
	if err != nil {
		result.Errors = append(result.Errors, InboxSyncError{Code: "storage_error"})
	} else {
		result.PendingConversations = len(remaining)
	}
	result.MoreWork = next != "" || result.PendingConversations > 0 || len(result.Errors) > 0
	return result
}

func (s *Store) pendingInbox(ctx context.Context, account, self string, blocked map[string]bool) ([]Conversation, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT c.data FROM conversations c LEFT JOIN inbox_heads h ON h.account_id=c.account_id AND h.conversation_id=c.id WHERE c.account_id=? AND (h.version IS NULL OR json_extract(c.data,'$.version')>h.version OR COALESCE(json_extract(c.data,'$.last_event_at'),0)>h.event_at OR (COALESCE(json_extract(c.data,'$.last_event_at'),0)=0 AND h.checked_at<?)) ORDER BY COALESCE(json_extract(c.data,'$.last_event_at'),0) DESC,c.updated_at DESC,c.id", account, time.Now().Add(-time.Minute).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		var raw []byte
		var c Conversation
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &c) != nil {
			return nil, fail("internal_error", "invalid inbox conversation metadata", 500)
		}
		if inboxConversationAllowed(c, self, blocked) {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

func (s *Store) persistInboxWindow(ctx context.Context, account, self string, c Conversation, window inboxWindow) (bool, error) {
	if window.MediaOpened || window.ReadReceipts {
		return false, fail("incompatible_protocol", "inbox backend reported a viewing side effect", 502)
	}
	c.Participants = window.Participants
	blocked, err := s.inboxBlocked(ctx, account)
	if err != nil {
		return false, err
	}
	if !inboxConversationAllowed(c, self, blocked) {
		return false, fail("excluded_recipient", "inbox membership is unavailable or contains an excluded recipient", 403)
	}
	type stored struct {
		message inboxMessage
		sealed  []byte
	}
	batch := []stored{}
	ids := map[string]bool{}
	var latest uint64
	for _, m := range window.Items {
		id, err := strconv.ParseUint(m.ID, 10, 64)
		if err != nil || id == 0 || strconv.FormatUint(id, 10) != m.ID || parseUUID(m.SenderID) == nil || (m.Type != "snap" && m.Type != "text") || (m.Type == "text" && m.Text == nil) || (m.Type == "snap" && m.Text != nil) {
			return false, fail("incompatible_protocol", "invalid inbox event projection", 502)
		}
		if ids[m.ID] {
			return false, fail("incompatible_protocol", "duplicate inbox message ID", 502)
		}
		ids[m.ID] = true
		if id > latest {
			latest = id
		}
		if m.SenderID == self || blocked[m.SenderID] {
			continue
		}
		event := InboxEvent{EventID: c.ID + "/" + m.ID, ConversationID: c.ID, MessageID: m.ID, SenderID: m.SenderID, Type: m.Type, Text: m.Text, CreatedAt: m.CreatedAt}
		raw, _ := json.Marshal(event)
		sealed, err := s.vault.Seal(account+"/inbox/"+c.ID+"/"+m.ID, raw)
		if err != nil {
			return false, fail("vault_unavailable", "cannot encrypt inbox event", 503)
		}
		batch = append(batch, stored{m, sealed})
	}
	sort.Slice(batch, func(i, j int) bool {
		a, _ := strconv.ParseUint(batch[i].message.ID, 10, 64)
		b, _ := strconv.ParseUint(batch[j].message.ID, 10, 64)
		return a < b
	})
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var anchor string
	err = tx.QueryRowContext(ctx, "SELECT anchor_id FROM inbox_heads WHERE account_id=? AND conversation_id=?", account, c.ID).Scan(&anchor)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	gap := window.HasMore && (anchor == "" || !ids[anchor])
	for _, row := range batch {
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO inbox_events(account_id,conversation_id,message_id,sealed) VALUES(?,?,?,?)", account, c.ID, row.message.ID, row.sealed); err != nil {
			return false, err
		}
	}
	if latest > 0 {
		anchor = strconv.FormatUint(latest, 10)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO inbox_heads VALUES(?,?,?,?,?,?,?) ON CONFLICT(account_id,conversation_id) DO UPDATE SET version=MAX(version,excluded.version),anchor_id=CASE WHEN excluded.event_at>=event_at THEN excluded.anchor_id ELSE anchor_id END,event_at=MAX(event_at,excluded.event_at),checked_at=MAX(checked_at,excluded.checked_at),gap_possible=MAX(gap_possible,excluded.gap_possible)", account, c.ID, c.Version, anchor, c.LastEventAt, time.Now().Unix(), gap); err != nil {
		return false, err
	}
	raw, _ := json.Marshal(c)
	if _, err = tx.ExecContext(ctx, "UPDATE conversations SET data=?,updated_at=? WHERE account_id=? AND id=? AND json_extract(data,'$.version')<=? AND COALESCE(json_extract(data,'$.last_event_at'),0)<=?", raw, now(), account, c.ID, c.Version, c.LastEventAt); err != nil {
		return false, err
	}
	return gap, tx.Commit()
}

func (s *Store) inboxEvents(ctx context.Context, account, self string, after int64, limit int) ([]InboxEvent, bool, int64, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT seq,conversation_id,message_id,sealed FROM inbox_events WHERE account_id=? AND seq>? ORDER BY seq LIMIT ?", account, after, limit+1)
	if err != nil {
		return nil, false, after, err
	}
	type row struct {
		seq              int64
		conversation, id string
		sealed           []byte
	}
	records := []row{}
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.seq, &r.conversation, &r.id, &r.sealed); err != nil {
			rows.Close()
			return nil, false, after, err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, after, err
	}
	more := len(records) > limit
	if more {
		records = records[:limit]
	}
	blocked, err := s.inboxBlocked(ctx, account)
	if err != nil {
		return nil, false, after, err
	}
	out := []InboxEvent{}
	for _, r := range records {
		var data []byte
		var c Conversation
		if err = s.db.QueryRowContext(ctx, "SELECT data FROM conversations WHERE account_id=? AND id=?", account, r.conversation).Scan(&data); err == sql.ErrNoRows {
			after = r.seq
			continue
		} else if err != nil {
			return nil, false, after, err
		}
		if json.Unmarshal(data, &c) != nil {
			return nil, false, after, fail("internal_error", "invalid cached inbox membership", 500)
		}
		if !inboxConversationAllowed(c, self, blocked) {
			after = r.seq
			continue
		}
		plain, err := s.vault.Open(account+"/inbox/"+r.conversation+"/"+r.id, r.sealed)
		if err != nil {
			return nil, false, after, fail("vault_unavailable", "cannot decrypt inbox event", 503)
		}
		var event InboxEvent
		if json.Unmarshal(plain, &event) != nil || event.ConversationID != r.conversation || event.MessageID != r.id {
			return nil, false, after, fail("internal_error", "invalid saved inbox event", 500)
		}
		if !blocked[event.SenderID] {
			out = append(out, event)
		}
		after = r.seq
	}
	return out, more, after, nil
}
