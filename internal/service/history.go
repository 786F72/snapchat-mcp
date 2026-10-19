package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

type HistoryMessage struct {
	ID        string `json:"id"`
	SenderID  string `json:"sender_id"`
	Text      string `json:"text"`
	CreatedAt uint64 `json:"created_at"`
}

type historyWindow struct {
	Items        []HistoryMessage `json:"items"`
	NextBefore   string           `json:"next_before"`
	NextNative   string           `json:"next_native"`
	More         bool             `json:"has_more"`
	Total        int              `json:"total_events"`
	MediaOpened  bool             `json:"media_opened"`
	ReadReceipts bool             `json:"read_receipts_sent"`
}

type HistoryPage struct {
	Items               []HistoryMessage `json:"items"`
	NextCursor          string           `json:"next_cursor,omitempty"`
	HasMore             bool             `json:"has_more"`
	PaginationSupported bool             `json:"pagination_supported"`
	TotalEvents         int              `json:"total_events"`
	TextOnly            bool             `json:"text_only"`
	MediaOpened         bool             `json:"media_opened"`
	ReadReceipts        bool             `json:"read_receipts_sent"`
	Backend             string           `json:"backend"`
}

type historyCursor struct {
	Conversation string `json:"conversation_id"`
	NativeBefore string `json:"native_before"`
	ServerBefore string `json:"server_before"`
	Filter       string `json:"filter"`
}

func serverMessageID(value string) (uint64, bool) {
	id, err := strconv.ParseUint(value, 10, 64)
	return id, err == nil && id > 0 && id <= math.MaxInt64 && strconv.FormatUint(id, 10) == value
}

func nativeMessageID(value string) bool {
	// Native handles may be zero or use the full unsigned 64-bit range; they
	// are not the strictly positive stable server IDs exposed on messages.
	if id, err := strconv.ParseUint(value, 10, 64); err == nil && strconv.FormatUint(id, 10) == value {
		return true
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && strconv.FormatInt(id, 10) == value
}

func (s *Store) fetchHistoryWindow(ctx context.Context, account, conversation, nativeBefore, serverBefore string, limit int) (historyWindow, error) {
	var window historyWindow
	result, err := s.nativeMessages(ctx, account, conversation, nativeBefore, limit, "messages", serverBefore)
	if err != nil {
		return window, err
	}
	raw, err := json.Marshal(result)
	if err != nil || json.Unmarshal(raw, &window) != nil {
		return window, fail("incompatible_protocol", "invalid text history window", 502)
	}
	return window, nil
}

func (s *Store) MessageHistory(ctx context.Context, account, conversation, cursor string, limit int) (HistoryPage, error) {
	return s.messageHistory(ctx, account, conversation, cursor, limit, "", "", false, false)
}

func (s *Store) SearchMessages(ctx context.Context, account, conversation, query, sender, cursor string, limit int, matchCase bool) (HistoryPage, error) {
	if strings.TrimSpace(query) == "" || utf8.RuneCountInString(query) > 128 {
		return HistoryPage{}, fail("invalid_argument", "query must contain 1–128 characters", 400)
	}
	if sender != "" {
		if parseUUID(sender) == nil {
			return HistoryPage{}, fail("invalid_argument", "sender_id must be a UUID", 400)
		}
		sender = uuidString(parseUUID(sender))
	}
	return s.messageHistory(ctx, account, conversation, cursor, limit, query, sender, matchCase, true)
}

func (s *Store) messageHistory(ctx context.Context, account, conversation, cursor string, limit int, query, sender string, matchCase, search bool) (HistoryPage, error) {
	page := HistoryPage{Items: []HistoryMessage{}, PaginationSupported: true, TextOnly: true, Backend: "standalone"}
	if limit < 1 || limit > 100 {
		return page, fail("invalid_argument", "history limit must be 1–100", 400)
	}
	if parseUUID(conversation) == nil {
		return page, fail("invalid_argument", "conversation_id must be a UUID", 400)
	}
	conversation = uuidString(parseUUID(conversation))
	if _, err := s.Account(ctx, account); err != nil {
		return page, err
	}
	if !matchCase {
		query = strings.ToLower(query)
	}
	filterRaw, _ := json.Marshal([]any{query, sender, matchCase})
	filterHash := sha256.Sum256(filterRaw)
	filter := hex.EncodeToString(filterHash[:])
	kind := "messages"
	if search {
		kind = "messages.search"
	}
	var position historyCursor
	if cursor != "" {
		if err := s.loadCursor(ctx, s.vault, account, kind, cursor, &position); err != nil {
			return page, err
		}
		if _, ok := serverMessageID(position.ServerBefore); !ok || !nativeMessageID(position.NativeBefore) || position.Conversation != conversation || position.Filter != filter {
			return page, fail("invalid_cursor", "history cursor belongs to another conversation or filter", 400)
		}
	}
	if _, err := s.GetConversation(ctx, account, conversation, false); err != nil {
		return page, err
	}
	fetch := s.historySource
	if fetch == nil {
		fetch = s.fetchHistoryWindow
	}
	window, err := fetch(ctx, account, conversation, position.NativeBefore, position.ServerBefore, limit)
	if err != nil {
		return page, err
	}
	if _, err := s.GetConversation(ctx, account, conversation, false); err != nil {
		return page, err
	}
	if window.MediaOpened || window.ReadReceipts || window.Total < 0 || window.Total > limit || len(window.Items) > window.Total {
		return page, fail("incompatible_protocol", "invalid or unsafe history response", 502)
	}
	prior, _ := serverMessageID(position.ServerBefore)
	seen := map[string]bool{}
	for _, item := range window.Items {
		id, ok := serverMessageID(item.ID)
		if !ok || seen[item.ID] || prior > 0 && id >= prior || parseUUID(item.SenderID) == nil || item.CreatedAt > 9007199254740991 || !utf8.ValidString(item.Text) || utf8.RuneCountInString(item.Text) > 65536 {
			return page, fail("incompatible_protocol", "invalid history message or nonadvancing page", 502)
		}
		seen[item.ID] = true
		text := item.Text
		if !matchCase {
			text = strings.ToLower(text)
		}
		if search && (!strings.Contains(text, query) || sender != "" && item.SenderID != sender) {
			continue
		}
		page.Items = append(page.Items, item)
	}
	page.TotalEvents = window.Total
	page.HasMore = window.More
	if window.More {
		next, ok := serverMessageID(window.NextBefore)
		if !ok {
			return page, fail("incompatible_protocol", "invalid server history boundary", 502)
		}
		if !nativeMessageID(window.NextNative) {
			return page, fail("incompatible_protocol", "invalid native history boundary", 502)
		}
		if window.Total == 0 || prior > 0 && next >= prior {
			return page, fail("incompatible_protocol", "history pagination did not advance", 502)
		}
		for _, item := range window.Items {
			id, _ := serverMessageID(item.ID)
			if id < next {
				return page, fail("incompatible_protocol", "invalid history pagination boundary", 502)
			}
		}
		page.NextCursor, err = s.saveCursor(ctx, s.vault, account, kind, historyCursor{conversation, window.NextNative, window.NextBefore, filter})
		if err != nil {
			return page, err
		}
	}
	return page, nil
}
