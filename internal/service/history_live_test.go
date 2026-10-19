package service

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Explicit opt-in. Logs contain aggregate validation results only. This check
// reads text, never opens media, marks chats read, or submits content.
func TestLiveHistoryPagination(t *testing.T) {
	account, dir := os.Getenv("QQ_SNAP_LIVE_ACCOUNT"), os.Getenv("QQ_SNAP_LIVE_DATA_DIR")
	if account == "" || dir == "" {
		t.Skip("requires explicit live account and data directory")
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	metadata, err := s.Conversations(ctx, account, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	conversations := metadata.(map[string]any)["items"].([]Conversation)
	sort.SliceStable(conversations, func(i, j int) bool {
		return conversations[i].Type == "ONE_ON_ONE" && conversations[j].Type != "ONE_ON_ONE"
	})
	kinds := map[string]bool{}
	prepared := false
	for _, c := range conversations {
		if kinds[c.Type] || c.Type != "ONE_ON_ONE" && c.Type != "USER_CREATED_GROUP" {
			continue
		}
		first, err := s.MessageHistory(ctx, account, c.ID, "", 2)
		if err != nil {
			t.Logf("history unavailable: %s", publicError(err).Code)
			continue
		}
		if !first.HasMore || first.NextCursor == "" {
			continue
		}
		second, err := s.MessageHistory(ctx, account, c.ID, first.NextCursor, 2)
		if err != nil {
			t.Fatalf("continuation failed: %s: %s", publicError(err).Code, publicError(err).Message)
		}
		seen := map[string]bool{}
		for _, page := range []HistoryPage{first, second} {
			for _, item := range page.Items {
				if seen[item.ID] {
					t.Fatal("duplicate history ID across pages")
				}
				seen[item.ID] = true
			}
		}
		if len(first.Items) > 0 && len(second.Items) > 0 {
			boundary, _ := strconv.ParseUint(first.Items[len(first.Items)-1].ID, 10, 64)
			for _, item := range second.Items {
				id, _ := strconv.ParseUint(item.ID, 10, 64)
				if id >= boundary {
					t.Fatal("older history did not advance")
				}
			}
		}
		if second.HasMore {
			if _, err := s.MessageHistory(ctx, account, c.ID, second.NextCursor, 2); err != nil {
				t.Fatalf("third page failed: %s", publicError(err).Code)
			}
		}
		search, err := s.SearchMessages(ctx, account, c.ID, "test", "", "", 2, false)
		if err != nil {
			t.Fatal(publicError(err).Code)
		}
		for _, item := range search.Items {
			if !strings.Contains(strings.ToLower(item.Text), "test") {
				t.Fatal("search returned a nonmatching message")
			}
		}
		if search.HasMore {
			if _, err := s.SearchMessages(ctx, account, c.ID, "test", "", search.NextCursor, 2, false); err != nil {
				t.Fatal(publicError(err).Code)
			}
		}
		if !prepared && c.Type == "ONE_ON_ONE" {
			result, err := s.PrepareText(ctx, account, c.ID, "[snapchat-mcp test] packet preparation only")
			if err != nil {
				t.Fatalf("native launcher preparation failed: %s", publicError(err).Code)
			}
			packet := result.(map[string]any)
			if packet["ready_to_submit"] != true || packet["submitted"] != false {
				t.Fatal("preparation did not stop before submission")
			}
			prepared = true
		}
		t.Logf("type=%s paginated=true first_events=%d next_events=%d search_validated=true", c.Type, first.TotalEvents, second.TotalEvents)
		kinds[c.Type] = true
		if len(kinds) == 2 {
			break
		}
	}
	if len(kinds) == 0 {
		t.Fatal("no multi-page history was validated")
	}
}
