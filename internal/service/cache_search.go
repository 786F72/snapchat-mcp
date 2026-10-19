package service

import (
	"context"
	"encoding/json"
	"strings"
)

type CachedConversation struct {
	Conversation Conversation `json:"conversation"`
	SyncedAt     string       `json:"synced_at"`
	Stale        bool         `json:"stale"`
}

type ConversationSearch struct {
	Items      []CachedConversation `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
	Source     string               `json:"source"`
}

func (s *Store) SearchConversations(ctx context.Context, account, query, participant, cursor string, limit int, groupsOnly bool) (ConversationSearch, error) {
	result := ConversationSearch{Items: []CachedConversation{}, Source: "conversation_cache"}
	query = strings.ToLower(strings.TrimSpace(query))
	if len(query) > 128 || limit < 1 || limit > 500 {
		return result, fail("invalid_argument", "query must be at most 128 bytes and limit must be 1–500", 400)
	}
	if cursor != "" {
		if parseUUID(cursor) == nil {
			return result, fail("invalid_cursor", "use the conversation UUID returned as next_cursor", 400)
		}
		cursor = uuidString(parseUUID(cursor))
	}
	if participant != "" {
		if parseUUID(participant) == nil {
			return result, fail("invalid_argument", "participant_id must be a UUID", 400)
		}
		participant = uuidString(parseUUID(participant))
	}
	exclusions, err := s.Exclusions(ctx, account)
	if err != nil {
		return result, err
	}
	blocked := map[string]bool{}
	for _, id := range exclusions {
		blocked[id] = true
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,data,updated_at FROM conversations WHERE account_id=? AND id>? ORDER BY id", account, cursor)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, synced string
		var raw []byte
		if err := rows.Scan(&id, &raw, &synced); err != nil {
			return result, err
		}
		var c Conversation
		if json.Unmarshal(raw, &c) != nil || c.ID != id {
			return result, fail("internal_error", "invalid cached conversation", 500)
		}
		if groupsOnly && c.Type != "USER_CREATED_GROUP" {
			continue
		}
		visible, hasParticipant := true, participant == ""
		matches := query == "" || strings.Contains(strings.ToLower(c.Title), query) || strings.Contains(strings.ToLower(c.ID), query)
		for _, member := range c.Participants {
			if blocked[member] {
				visible = false
			}
			if member == participant {
				hasParticipant = true
			}
			if strings.Contains(strings.ToLower(member), query) {
				matches = true
			}
		}
		if !visible || !hasParticipant || !matches {
			continue
		}
		if len(result.Items) == limit {
			result.NextCursor = result.Items[len(result.Items)-1].Conversation.ID
			break
		}
		result.Items = append(result.Items, CachedConversation{c, synced, snapshotStale(synced)})
	}
	return result, rows.Err()
}

func (s *Store) ListDeliveries(ctx context.Context, account, state, cursor string, limit int) (Page, error) {
	if limit < 1 || limit > 500 || len(cursor) > 128 {
		return Page{}, fail("invalid_argument", "limit must be 1–500 and cursor at most 128 bytes", 400)
	}
	if state != "" && state != "pending" && state != "unknown" && state != "not_sent" && state != "sent" {
		return Page{}, fail("invalid_argument", "invalid delivery state", 400)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT idempotency_key,state,receipt,created_at FROM deliveries WHERE account_id=? AND idempotency_key>? AND (?='' OR state=?) ORDER BY idempotency_key LIMIT ?", account, cursor, state, state, limit+1)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	items := []any{}
	next := ""
	lastKey := ""
	for rows.Next() {
		var key, savedState, created string
		var raw []byte
		if err := rows.Scan(&key, &savedState, &raw, &created); err != nil {
			return Page{}, err
		}
		if len(items) == limit {
			next = lastKey
			break
		}
		item, err := deliverySummary(key, savedState, created, raw)
		if err != nil {
			return Page{}, err
		}
		items = append(items, item)
		lastKey = key
	}
	return Page{Items: items, NextCursor: next}, rows.Err()
}
