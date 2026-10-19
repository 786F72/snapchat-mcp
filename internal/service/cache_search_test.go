package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCachedConversationSearchFiltersPaginationAndExclusions(t *testing.T) {
	s, a, b := lookupFixture(t)
	ctx := context.Background()
	groups, err := s.SearchConversations(ctx, a.ID, "FIXTURE", fixtureBob, "", 1, true)
	if err != nil || len(groups.Items) != 1 || groups.Items[0].Conversation.ID != lookupGroup || groups.Items[0].Stale || groups.NextCursor != "" {
		t.Fatal(groups, err)
	}
	first, err := s.SearchConversations(ctx, a.ID, "", "", "", 1, false)
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	second, err := s.SearchConversations(ctx, a.ID, "", "", first.NextCursor, 1, false)
	if err != nil || len(second.Items) != 1 || second.NextCursor != "" || second.Items[0].Conversation.ID == first.Items[0].Conversation.ID {
		t.Fatal(second, err)
	}
	other, err := s.SearchConversations(ctx, b.ID, "", "", "", 10, false)
	if err != nil || len(other.Items) != 0 {
		t.Fatal(other, err)
	}
	if _, err := s.ChangeExclusion(ctx, a.ID, fixtureBob, true); err != nil {
		t.Fatal(err)
	}
	groups, err = s.SearchConversations(ctx, a.ID, "", "", "", 10, true)
	if err != nil || len(groups.Items) != 0 {
		t.Fatal("excluded group exposed", groups, err)
	}
	// An SQL wildcard is treated as literal search text.
	result, err := s.SearchConversations(ctx, a.ID, "%", "", "", 10, false)
	if err != nil || len(result.Items) != 0 {
		t.Fatal(result, err)
	}
	for _, tc := range []struct {
		participant, cursor string
		limit               int
	}{{"bad", "", 10}, {"", "bad", 10}, {"", "", 0}} {
		if _, err := s.SearchConversations(ctx, a.ID, "", tc.participant, tc.cursor, tc.limit, false); err == nil {
			t.Fatal("invalid search accepted", tc)
		}
	}
	if r := s.Call(ctx, "conversations.search", map[string]any{"account_id": a.ID, "query": " "}); r.Error == nil {
		t.Fatal("blank required query accepted")
	}
}

func TestDeliveryHistoryPaginationAndRedaction(t *testing.T) {
	s, a, b := lookupFixture(t)
	ctx := context.Background()
	for _, state := range []string{"not_sent", "pending", "sent", "unknown"} {
		if _, err := s.db.Exec("INSERT INTO deliveries VALUES(?,?,?,?,?,?)", a.ID, state, "hash", state, []byte(`{"server_message_id":"7","send_frame":"private-frame","key":"private-key"}`), now()); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListDeliveries(ctx, a.ID, "", "", 2)
	if err != nil || len(page.Items.([]any)) != 2 || page.NextCursor == "" {
		t.Fatal(page, err)
	}
	next, err := s.ListDeliveries(ctx, a.ID, "", page.NextCursor, 2)
	if err != nil || len(next.Items.([]any)) != 2 || next.NextCursor != "" {
		t.Fatal(next, err)
	}
	for _, p := range []Page{page, next} {
		raw, _ := json.Marshal(p)
		if strings.Contains(string(raw), "private-") {
			t.Fatal("delivery history leaked private material")
		}
		for _, value := range p.Items.([]any) {
			item := value.(map[string]any)
			if item["can_retry_same_key"] != (item["state"] == "not_sent") {
				t.Fatal(item)
			}
		}
	}
	filtered, err := s.ListDeliveries(ctx, a.ID, "unknown", "", 10)
	if err != nil || len(filtered.Items.([]any)) != 1 || filtered.Items.([]any)[0].(map[string]any)["state"] != "unknown" {
		t.Fatal(filtered, err)
	}
	other, err := s.ListDeliveries(ctx, b.ID, "", "", 10)
	if err != nil || len(other.Items.([]any)) != 0 {
		t.Fatal(other, err)
	}
	if r := s.Call(ctx, "deliveries.list", map[string]any{"account_id": a.ID, "state": "bogus"}); r.Error == nil || r.Error.Code != "invalid_argument" {
		t.Fatal(r)
	}
	if _, err := s.ListDeliveries(ctx, a.ID, "", strings.Repeat("x", 129), 10); err == nil {
		t.Fatal("long cursor accepted")
	}
	if _, err := s.db.Exec("UPDATE deliveries SET receipt=? WHERE account_id=? AND idempotency_key='sent'", []byte(`bad json`), a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListDeliveries(ctx, a.ID, "sent", "", 10); publicError(err).Code != "internal_error" {
		t.Fatal(err)
	}
}
