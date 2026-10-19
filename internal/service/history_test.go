package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func historyFixture(t *testing.T) (*Store, Account, Account) {
	s, a, b := lookupFixture(t)
	s.historySource = func(_ context.Context, _, _, nativeBefore, serverBefore string, _ int) (historyWindow, error) {
		if nativeBefore == "" {
			return historyWindow{Items: []HistoryMessage{{"10", fixtureAlice, "Weekend TEST", 100}, {"9", fixtureSelf, "test response", 99}}, NextBefore: "8", NextNative: "0", More: true, Total: 3}, nil
		}
		if nativeBefore != "0" || serverBefore != "8" {
			t.Fatal("wrong native/server boundary")
		}
		return historyWindow{Items: []HistoryMessage{{"7", fixtureBob, "older TEST", 90}}, Total: 1}, nil
	}
	return s, a, b
}

func TestMessageHistoryCursorsIsolationTamperingAndRestart(t *testing.T) {
	s, a, b := historyFixture(t)
	ctx := context.Background()
	first, err := s.MessageHistory(ctx, a.ID, lookupConversation, "", 3)
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" || !first.HasMore || !first.PaginationSupported {
		t.Fatal("initial page failed", err)
	}
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), "next_native") || strings.Contains(string(raw), "next_before") {
		t.Fatal("internal pagination handles leaked")
	}
	second, err := s.MessageHistory(ctx, a.ID, lookupConversation, first.NextCursor, 3)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != "7" || second.HasMore || second.NextCursor != "" {
		t.Fatal(second, err)
	}
	if _, err := s.MessageHistory(ctx, a.ID, lookupGroup, first.NextCursor, 3); publicError(err).Code != "invalid_cursor" {
		t.Fatal("cross-conversation cursor accepted", err)
	}
	if _, err := s.MessageHistory(ctx, b.ID, lookupConversation, first.NextCursor, 3); publicError(err).Code != "invalid_cursor" {
		t.Fatal("cross-account cache accepted", err)
	}
	for _, c := range []string{first.NextCursor + "corrupt", "invalid"} {
		if _, err := s.MessageHistory(ctx, a.ID, lookupConversation, c, 3); publicError(err).Code != "invalid_cursor" {
			t.Fatal(err)
		}
	}
	// Copy a cursor record into another account: encrypted associated data still
	// binds it to the original account, independent of database ownership.
	var sealed []byte
	if err := s.db.QueryRow("SELECT state FROM protocol_cursors WHERE id=?", first.NextCursor).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "native_before") {
		t.Fatal("plaintext cursor stored")
	}
	if _, err := s.db.Exec("INSERT INTO protocol_cursors SELECT ?,?,kind,state,expires_at FROM protocol_cursors WHERE id=?", b.ID, "copied", first.NextCursor); err != nil {
		t.Fatal(err)
	}
	var copied historyCursor
	if err := s.loadCursor(ctx, s.vault, b.ID, "messages", "copied", &copied); publicError(err).Code != "invalid_cursor" {
		t.Fatal("copied cursor authenticated", err)
	}
	var dbPath string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(filepath.Dir(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.vault = s.vault
	reopened.historySource = s.historySource
	if page, err := reopened.MessageHistory(ctx, a.ID, lookupConversation, first.NextCursor, 3); err != nil || len(page.Items) != 1 {
		t.Fatal("restart lost continuation", err)
	}
	if _, err := s.db.Exec("UPDATE protocol_cursors SET expires_at=0 WHERE id=?", first.NextCursor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MessageHistory(ctx, a.ID, lookupConversation, first.NextCursor, 3); publicError(err).Code != "invalid_cursor" {
		t.Fatal("expired cursor accepted", err)
	}
}

func TestMessageSearchFiltersAndCursorBinding(t *testing.T) {
	s, a, _ := historyFixture(t)
	ctx := context.Background()
	first, err := s.SearchMessages(ctx, a.ID, lookupConversation, "TEST", fixtureAlice, "", 3, false)
	if err != nil || len(first.Items) != 1 || first.Items[0].SenderID != fixtureAlice || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	second, err := s.SearchMessages(ctx, a.ID, lookupConversation, "test", fixtureAlice, first.NextCursor, 3, false)
	if err != nil || len(second.Items) != 0 || second.HasMore {
		t.Fatal(second, err)
	}
	for _, tc := range []struct {
		query, sender string
		matchCase     bool
	}{{"different", fixtureAlice, false}, {"test", fixtureBob, false}, {"TEST", fixtureAlice, true}} {
		if _, err := s.SearchMessages(ctx, a.ID, lookupConversation, tc.query, tc.sender, first.NextCursor, 3, tc.matchCase); publicError(err).Code != "invalid_cursor" {
			t.Fatal("changed filters accepted", err)
		}
	}
	if _, err := s.MessageHistory(ctx, a.ID, lookupConversation, first.NextCursor, 3); publicError(err).Code != "invalid_cursor" {
		t.Fatal("search cursor accepted for history", err)
	}
	caseSensitive, err := s.SearchMessages(ctx, a.ID, lookupConversation, "TEST", "", "", 3, true)
	if err != nil || len(caseSensitive.Items) != 1 {
		t.Fatal(caseSensitive, err)
	}
	noMatch, err := s.SearchMessages(ctx, a.ID, lookupConversation, "unmatched", "", "", 3, false)
	if err != nil || len(noMatch.Items) != 0 || !noMatch.HasMore || noMatch.NextCursor == "" {
		t.Fatal("empty filtered page lost continuation", err)
	}
	for _, query := range []string{" ", strings.Repeat("x", 129)} {
		if _, err := s.SearchMessages(ctx, a.ID, lookupConversation, query, "", "", 3, false); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	if _, err := s.SearchMessages(ctx, a.ID, lookupConversation, "test", "bad", "", 3, false); err == nil {
		t.Fatal("invalid sender accepted")
	}
}

func TestHistoryHTTPBooleanValidation(t *testing.T) {
	s, a, _ := historyFixture(t)
	for _, tc := range []struct {
		value  string
		status int
	}{{"true", 200}, {"false", 200}, {"1", 400}, {"yes", 400}, {"true&match_case=false", 400}} {
		path := "/v1/accounts/" + a.ID + "/messages/search?conversation_id=" + lookupConversation + "&query=TEST&limit=3&match_case=" + tc.value
		status, _ := request(t, s.Handler("secret"), "GET", path, "", "secret")
		if status != tc.status {
			t.Fatalf("match_case %s: got %d", tc.value, status)
		}
	}
}

func TestMessageHistoryRejectsUnsafeAndNonadvancingWindows(t *testing.T) {
	s, a, _ := historyFixture(t)
	ctx := context.Background()
	valid := historyWindow{Items: []HistoryMessage{{"10", fixtureAlice, "fixture", 100}}, Total: 1}
	for _, mutate := range []func(*historyWindow){
		func(w *historyWindow) { w.MediaOpened = true }, func(w *historyWindow) { w.ReadReceipts = true }, func(w *historyWindow) { w.Total = 101 },
		func(w *historyWindow) { w.Items[0].ID = "0" }, func(w *historyWindow) { w.Items[0].SenderID = "bad" }, func(w *historyWindow) { w.Items[0].CreatedAt = 9007199254740992 },
		func(w *historyWindow) { w.Items = append(w.Items, w.Items[0]); w.Total = 2 },
		func(w *historyWindow) { w.More = true; w.NextBefore = "11"; w.NextNative = "0" },
		func(w *historyWindow) { w.More = true; w.NextBefore = "10"; w.NextNative = "bad" },
	} {
		window := valid
		window.Items = append([]HistoryMessage{}, valid.Items...)
		mutate(&window)
		s.historySource = func(context.Context, string, string, string, string, int) (historyWindow, error) { return window, nil }
		if _, err := s.MessageHistory(ctx, a.ID, lookupConversation, "", 100); publicError(err).Code != "incompatible_protocol" {
			t.Fatal("unsafe window accepted", err)
		}
	}
	if _, err := s.MessageHistory(ctx, a.ID, lookupConversation, "", 101); publicError(err).Code != "invalid_argument" {
		t.Fatal(err)
	}
	if _, err := s.MessageHistory(ctx, a.ID, "bad", "", 3); publicError(err).Code != "invalid_argument" {
		t.Fatal(err)
	}
	// An exclusion added during the fetch must suppress the returned page.
	s.historySource = func(context.Context, string, string, string, string, int) (historyWindow, error) {
		_, err := s.ChangeExclusion(ctx, a.ID, fixtureAlice, true)
		return valid, err
	}
	if _, err := s.MessageHistory(ctx, a.ID, lookupConversation, "", 3); publicError(err).Code != "excluded_recipient" {
		t.Fatal("new exclusion ignored", err)
	}
}
