package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/0xzer/snapper/protos"
)

type conversationCursor struct {
	Token          []byte `json:"token"`
	Timestamp      int64  `json:"timestamp"`
	ConversationID string `json:"conversation_id"`
}

func parseUUID(id string) *protos.UUID {
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	if err != nil || len(raw) != 16 {
		return nil
	}
	return &protos.UUID{EncodedId: raw}
}
func (s *Store) saveCursor(ctx context.Context, v Vault, account, kind string, data any) (string, error) {
	id := newID()
	raw, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	sealed, err := v.Seal(account+"/cursor/"+kind+"/"+id, raw)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO protocol_cursors VALUES(?,?,?,?,?)", account, id, kind, sealed, time.Now().Add(time.Hour).Unix())
	return id, err
}
func (s *Store) loadCursor(ctx context.Context, v Vault, account, kind, id string, data any) error {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, "SELECT state FROM protocol_cursors WHERE account_id=? AND id=? AND kind=? AND expires_at>?", account, id, kind, time.Now().Unix()).Scan(&sealed)
	if err != nil {
		return fail("invalid_cursor", "cursor is invalid, expired, or belongs to another account", 400)
	}
	raw, err := v.Open(account+"/cursor/"+kind+"/"+id, sealed)
	if err != nil {
		return fail("invalid_cursor", "cursor could not be authenticated", 400)
	}
	if json.Unmarshal(raw, data) != nil {
		return fail("invalid_cursor", "invalid cursor state", 400)
	}
	return nil
}
func decodeEntries(entries []*protos.ConversationEntry) ([]Conversation, error) {
	out := []Conversation{}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.GetIsDeleted() || entry.GetHideFromSendtoAndSearch() {
			continue
		}
		c := Conversation{ID: uuidString(entry.GetVersionInfo().GetConversationId()), Type: entry.GetType().String(), Version: entry.GetVersionInfo().GetConversationVersion(), Title: entry.GetTitle(), Participants: []string{}}
		if c.ID == "" || seen[c.ID] {
			return nil, fail("incompatible_protocol", "missing or duplicate conversation identifier", 502)
		}
		seen[c.ID] = true
		for _, p := range entry.GetParticipants() {
			id := uuidString(p)
			if id == "" {
				return nil, fail("incompatible_protocol", "invalid conversation participant", 502)
			}
			c.Participants = append(c.Participants, id)
		}
		out = append(out, c)
	}
	return out, nil
}
func (s *Store) Conversations(ctx context.Context, account, cursor string, limit int) (any, error) {
	var position conversationCursor
	vault := s.vault
	if cursor != "" {
		if err := s.loadCursor(ctx, vault, account, "conversations", cursor, &position); err != nil {
			return nil, err
		}
	}
	material, err := s.LoadSecret(ctx, vault, account)
	if err != nil {
		return nil, fail("reauth_required", "capture this account and unlock the OS keyring", 409)
	}
	var snapshot SessionSnapshot
	if json.Unmarshal(material, &snapshot) != nil || parseUUID(snapshot.UserID) == nil {
		return nil, fail("invalid_session", "invalid captured session", 409)
	}
	request := &protos.QueryConversationsRequest{SelfUserId: parseUUID(snapshot.UserID), RequestedPageSize: int32(limit), SyncToken: position.Token}
	if cursor != "" {
		request.PaginationInfo = &protos.QueryConversationsRequest_PaginationInfo{OldestConversationOrderTimestamp: position.Timestamp, OldestConversationId: parseUUID(position.ConversationID)}
	}
	response := &protos.QueryConversationsResponse{}
	if err = readRPC(ctx, snapshot, "QueryConversations", request, response); err != nil {
		return nil, err
	}
	items, err := decodeEntries(response.GetConversations())
	if err != nil {
		return nil, err
	}
	next := ""
	if !response.GetNoMore() {
		last := response.GetLastConversation()
		id := uuidString(last.GetOldestConversationId())
		timestamp := last.GetOldestConversationOrderTimestamp()
		if id == "" || timestamp <= 0 || id == position.ConversationID && timestamp == position.Timestamp {
			return nil, fail("incompatible_protocol", "server pagination did not advance", 502)
		}
		next, err = s.saveCursor(ctx, vault, account, "conversations", conversationCursor{Token: response.GetSyncToken(), Timestamp: timestamp, ConversationID: id})
		if err != nil {
			return nil, err
		}
	}
	excluded, err := s.Exclusions(ctx, account)
	if err != nil {
		return nil, err
	}
	blocked := map[string]bool{}
	for _, id := range excluded {
		blocked[id] = true
	}
	filtered := []Conversation{}
	for _, c := range items {
		raw, _ := json.Marshal(c)
		if _, err = s.db.ExecContext(ctx, "INSERT INTO conversations VALUES(?,?,?,?) ON CONFLICT(account_id,id) DO UPDATE SET data=excluded.data,updated_at=excluded.updated_at", account, c.ID, raw, now()); err != nil {
			return nil, err
		}
		skip := false
		for _, id := range c.Participants {
			if blocked[id] {
				skip = true
			}
		}
		if !skip {
			filtered = append(filtered, c)
		}
	}
	return map[string]any{"items": filtered, "has_more": !response.GetNoMore(), "next_cursor": next, "partial": !response.GetNoMore(), "backend": "standalone"}, nil
}
