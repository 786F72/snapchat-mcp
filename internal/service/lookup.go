package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// These tools use account-scoped snapshots; they never infer remote freshness.
type FriendSearch struct {
	Items      []Friend `json:"items"`
	NextCursor string   `json:"next_cursor,omitempty"`
	SyncedAt   string   `json:"synced_at"`
	Stale      bool     `json:"stale"`
	Source     string   `json:"source"`
}

func snapshotStale(timestamp string) bool {
	t, err := time.Parse(time.RFC3339Nano, timestamp)
	return err != nil || time.Since(t) > time.Hour || time.Until(t) > time.Minute
}

func (s *Store) cachedFriends(ctx context.Context, account string) (SessionSnapshot, []Friend, error) {
	snapshot, err := s.readSnapshot(ctx, account)
	if err != nil {
		return snapshot, nil, err
	}
	var friends []Friend
	if snapshot.FriendsSyncedAt == "" {
		snapshot.FriendsSyncedAt = snapshot.CapturedAt
	}
	if snapshot.FriendsSyncedAt == "" || json.Unmarshal(snapshot.Friends, &friends) != nil || friends == nil {
		return snapshot, nil, fail("snapshot_required", "synchronize friends.list before searching or resolving recipients", 409)
	}
	excluded, err := s.Exclusions(ctx, account)
	if err != nil {
		return snapshot, nil, err
	}
	blocked := map[string]bool{snapshot.UserID: true}
	for _, id := range excluded {
		blocked[id] = true
	}
	visible := []Friend{}
	seen := map[string]bool{}
	for _, f := range friends {
		if f.ID != "" && !blocked[f.ID] && !seen[f.ID] {
			visible = append(visible, f)
			seen[f.ID] = true
		}
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].ID < visible[j].ID })
	return snapshot, visible, nil
}

func normalizedQuery(query string) (string, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" || len(query) > 128 {
		return "", fail("invalid_argument", "query must contain 1–128 bytes", 400)
	}
	query = strings.TrimPrefix(query, "@")
	if id := parseUUID(query); id != nil {
		query = uuidString(id)
	}
	return query, nil
}

func friendMatches(f Friend, q string) bool {
	return strings.Contains(strings.ToLower(f.Username), q) || strings.Contains(strings.ToLower(f.DisplayName), q) || strings.EqualFold(f.ID, q)
}

func (s *Store) SearchFriends(ctx context.Context, account, query, cursor string, limit int) (FriendSearch, error) {
	var result FriendSearch
	if limit < 1 || limit > 500 {
		return result, fail("invalid_argument", "limit must be 1–500", 400)
	}
	q, err := normalizedQuery(query)
	if err != nil {
		return result, err
	}
	if q == "" {
		return result, fail("invalid_argument", "query must contain a username, name, or UUID", 400)
	}
	if cursor != "" && parseUUID(cursor) == nil {
		return result, fail("invalid_cursor", "use the last friend UUID returned as next_cursor", 400)
	}
	if cursor != "" {
		cursor = uuidString(parseUUID(cursor))
	}
	snapshot, friends, err := s.cachedFriends(ctx, account)
	if err != nil {
		return result, err
	}
	result = FriendSearch{Items: []Friend{}, SyncedAt: snapshot.FriendsSyncedAt, Stale: snapshotStale(snapshot.FriendsSyncedAt), Source: "friend_snapshot"}
	for _, f := range friends {
		if f.ID > cursor && friendMatches(f, q) {
			result.Items = append(result.Items, f)
			if len(result.Items) > limit {
				result.Items = result.Items[:limit]
				result.NextCursor = result.Items[limit-1].ID
				break
			}
		}
	}
	return result, nil
}

func (s *Store) ResolveRecipient(ctx context.Context, account, query string) (any, error) {
	q, err := normalizedQuery(query)
	if err != nil {
		return nil, err
	}
	if q == "" {
		return nil, fail("invalid_argument", "query must contain a username, name, or UUID", 400)
	}
	snapshot, friends, err := s.cachedFriends(ctx, account)
	if err != nil {
		return nil, err
	}
	exact, display, partial := []Friend{}, []Friend{}, []Friend{}
	for _, f := range friends {
		if strings.EqualFold(f.ID, q) || strings.EqualFold(f.Username, q) {
			exact = append(exact, f)
		}
		if strings.EqualFold(strings.TrimSpace(f.DisplayName), q) {
			display = append(display, f)
		}
		if friendMatches(f, q) {
			partial = append(partial, f)
		}
	}
	candidates := partial
	matchedBy := "partial"
	status := "ambiguous"
	if len(exact) > 0 {
		candidates = exact
		matchedBy = "username_or_id"
	} else if len(display) > 0 {
		candidates = display
		matchedBy = "display_name"
	}
	result := map[string]any{"status": status, "matched_by": matchedBy, "source": "friend_snapshot", "synced_at": snapshot.FriendsSyncedAt, "stale": snapshotStale(snapshot.FriendsSyncedAt)}
	if len(candidates) == 0 {
		result["status"] = "not_found"
	} else if len(candidates) == 1 && matchedBy != "partial" {
		result["status"] = "resolved"
		result["recipient"] = candidates[0]
	}
	result["total_candidates"] = len(candidates)
	if len(candidates) > 100 {
		candidates = candidates[:100]
		result["truncated"] = true
	}
	result["candidates"] = candidates
	return result, nil
}

func (s *Store) GetConversation(ctx context.Context, account, id string, groupOnly bool) (any, error) {
	if parseUUID(id) == nil {
		return nil, fail("invalid_argument", "conversation_id must be a UUID", 400)
	}
	id = uuidString(parseUUID(id))
	var raw []byte
	var synced string
	err := s.db.QueryRowContext(ctx, "SELECT data,updated_at FROM conversations WHERE account_id=? AND id=?", account, id).Scan(&raw, &synced)
	if err == sql.ErrNoRows {
		return nil, fail("not_found", "conversation is not cached in this account; synchronize conversations.list", 404)
	}
	if err != nil {
		return nil, err
	}
	var c Conversation
	if json.Unmarshal(raw, &c) != nil || c.ID != id {
		return nil, fail("internal_error", "invalid cached conversation", 500)
	}
	excluded, err := s.Exclusions(ctx, account)
	if err != nil {
		return nil, err
	}
	for _, blocked := range excluded {
		for _, member := range c.Participants {
			if member == blocked {
				return nil, fail("excluded_recipient", "conversation includes an excluded recipient", 403)
			}
		}
	}
	if groupOnly && c.Type != "USER_CREATED_GROUP" {
		return nil, fail("invalid_argument", "conversation is not a user-created group", 400)
	}
	return map[string]any{"conversation": c, "synced_at": synced, "stale": snapshotStale(synced), "source": "conversation_cache"}, nil
}

func (s *Store) ChangeExclusion(ctx context.Context, account, id string, add bool) (any, error) {
	if parseUUID(id) == nil {
		return nil, fail("invalid_argument", "recipient_id must be a UUID; use recipients.resolve to find it", 400)
	}
	id = uuidString(parseUUID(id))
	unlock, err := s.lockAccount(ctx, account)
	if err != nil {
		return nil, err
	}
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var changed sql.Result
	if add {
		changed, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO exclusions VALUES(?,?)", account, id)
	} else {
		changed, err = tx.ExecContext(ctx, "DELETE FROM exclusions WHERE account_id=? AND recipient=?", account, id)
	}
	if err != nil {
		return nil, err
	}
	n, err := changed.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n > 0 {
		kind := "exclusions.removed"
		if add {
			kind = "exclusions.added"
		}
		if err = appendEvent(ctx, tx, account, kind, map[string]any{"recipient_id": id}); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"recipient_id": id, "excluded": add, "changed": n > 0}, nil
}

func (s *Store) GetDelivery(ctx context.Context, account, key string) (any, error) {
	var state, created string
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT state,receipt,created_at FROM deliveries WHERE account_id=? AND idempotency_key=?", account, key).Scan(&state, &raw, &created)
	if err == sql.ErrNoRows {
		return nil, fail("not_found", "no delivery attempt with this idempotency key in this account", 404)
	}
	if err != nil {
		return nil, err
	}
	return deliverySummary(key, state, created, raw)
}

func deliverySummary(key, state, created string, raw []byte) (any, error) {
	var stored map[string]any
	if json.Unmarshal(raw, &stored) != nil {
		return nil, fail("internal_error", "invalid saved receipt", 500)
	}
	receipt := map[string]any{}
	for _, field := range []string{"acknowledged", "submitted", "conversation_id", "content_type", "server_message_id", "story_id", "encryption", "verification", "http_status", "ack_error"} {
		if value, ok := stored[field]; ok {
			receipt[field] = value
		}
	}
	return map[string]any{"idempotency_key": key, "state": state, "created_at": created, "can_retry_same_key": state == "not_sent", "receipt": receipt}, nil
}
