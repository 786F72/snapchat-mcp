package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixtureInboxSource struct {
	s           *Store
	mu          sync.Mutex
	pages       map[string]inboxMetadata
	windows     map[string]inboxWindow
	failures    map[string]error
	calls       map[string]int
	metadataErr error
}

func (f *fixtureInboxSource) metadata(ctx context.Context, account, cursor string) (inboxMetadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.metadataErr != nil {
		return inboxMetadata{}, f.metadataErr
	}
	page, ok := f.pages[cursor]
	if !ok {
		return inboxMetadata{}, fail("invalid_cursor", "fixture cursor expired", 400)
	}
	for _, c := range page.Items {
		raw, _ := json.Marshal(c)
		if _, err := f.s.db.ExecContext(ctx, "INSERT INTO conversations VALUES(?,?,?,?) ON CONFLICT(account_id,id) DO UPDATE SET data=excluded.data,updated_at=excluded.updated_at", account, c.ID, raw, now()); err != nil {
			return inboxMetadata{}, err
		}
	}
	return page, nil
}
func (f *fixtureInboxSource) messages(_ context.Context, _ string, id string) (inboxWindow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[id]++
	if err := f.failures[id]; err != nil {
		return inboxWindow{}, err
	}
	return f.windows[id], nil
}
func fixtureInbox(t *testing.T) (*Store, Account, Account, *fixtureInboxSource) {
	t.Helper()
	s, a, b := lookupFixture(t)
	selfText, incoming := "outgoing", "incoming fixture text"
	direct := Conversation{ID: lookupConversation, Type: "ONE_ON_ONE", Participants: []string{fixtureSelf, fixtureAlice}, Version: 1, LastEventAt: 100}
	group := Conversation{ID: lookupGroup, Type: "USER_CREATED_GROUP", Participants: []string{fixtureSelf, fixtureAlice, fixtureBob}, Version: 2, LastEventAt: 200}
	source := &fixtureInboxSource{s: s, pages: map[string]inboxMetadata{"": {Items: []Conversation{direct, group}}}, windows: map[string]inboxWindow{
		lookupConversation: {Participants: direct.Participants, Items: []inboxMessage{{ID: "1", SenderID: fixtureSelf, Type: "text", Text: &selfText, CreatedAt: 1}, {ID: "2", SenderID: fixtureAlice, Type: "text", Text: &incoming, CreatedAt: 2}, {ID: "3", SenderID: fixtureAlice, Type: "snap", CreatedAt: 3}}},
		lookupGroup:        {Participants: group.Participants, Items: []inboxMessage{{ID: "7", SenderID: fixtureBob, Type: "text", Text: &incoming, CreatedAt: 7}}},
	}, calls: map[string]int{}, failures: map[string]error{}}
	s.inboxSource = source
	return s, a, b, source
}
func pollFixture(t *testing.T, s *Store, account, cursor string, limit, budget int) InboxPage {
	t.Helper()
	p, err := s.PollInbox(context.Background(), account, cursor, limit, budget)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestInboxCursorDeduplicationAndRestart(t *testing.T) {
	s, a, b, source := fixtureInbox(t)
	first := pollFixture(t, s, a.ID, "", 1, 3)
	if len(first.Items) != 1 || !first.HasMore || first.Items[0].MessageID != "2" || first.NextCursor == "" || !first.InitialSync || first.MediaOpened || first.ReadReceipts {
		t.Fatal(first)
	}
	second := pollFixture(t, s, a.ID, first.NextCursor, 100, 3)
	if len(second.Items) != 2 || second.HasMore || source.calls[lookupConversation] != 1 || source.calls[lookupGroup] != 1 {
		t.Fatal(second, source.calls)
	}
	again := pollFixture(t, s, a.ID, first.NextCursor, 100, 3)
	if len(again.Items) != len(second.Items) || again.Items[0].EventID != second.Items[0].EventID {
		t.Fatal("old cursor did not replay stable events")
	}
	empty := pollFixture(t, s, a.ID, second.NextCursor, 100, 3)
	if len(empty.Items) != 0 || empty.NextCursor == "" {
		t.Fatal(empty)
	}
	if _, err := s.PollInbox(context.Background(), b.ID, first.NextCursor, 100, 3); publicError(err).Code != "invalid_cursor" {
		t.Fatal("cross-account cursor accepted", err)
	}
	if _, err := s.PollInbox(context.Background(), a.ID, first.NextCursor+"corrupt", 100, 3); publicError(err).Code != "invalid_cursor" {
		t.Fatal(err)
	}
	var sealed []byte
	if err := s.db.QueryRow("SELECT sealed FROM inbox_events WHERE account_id=? LIMIT 1", a.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "incoming fixture text") {
		t.Fatal("inbox text stored in plaintext")
	}
	// Reopen the actual SQLite file with the same injected vault, simulating restart.
	var path string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(strings.TrimSuffix(path, "/service.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.vault = s.vault
	reopened.inboxSource = source
	recovered := pollFixture(t, reopened, a.ID, first.NextCursor, 100, 3)
	if len(recovered.Items) != len(second.Items) {
		t.Fatal("restart lost the cursor or events", recovered)
	}
	source.pages[""].Items[0].Version++
	window := source.windows[lookupConversation]
	next := "next incoming"
	window.Items = append(window.Items, inboxMessage{ID: "4", SenderID: fixtureAlice, Type: "text", Text: &next, CreatedAt: 4})
	source.windows[lookupConversation] = window
	updated := pollFixture(t, s, a.ID, empty.NextCursor, 100, 3)
	if len(updated.Items) != 1 || updated.Items[0].MessageID != "4" {
		t.Fatal(updated)
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM inbox_events WHERE account_id=?", a.ID).Scan(&count)
	if count != 4 {
		t.Fatal("duplicate events inserted", count)
	}
}
func TestInboxExclusionsApplyToSavedEvents(t *testing.T) {
	s, a, _, _ := fixtureInbox(t)
	page := pollFixture(t, s, a.ID, "", 100, 3)
	if len(page.Items) != 3 {
		t.Fatal(page)
	}
	if _, err := s.ChangeExclusion(context.Background(), a.ID, fixtureBob, true); err != nil {
		t.Fatal(err)
	}
	replay := pollFixture(t, s, a.ID, "", 100, 3)
	if len(replay.Items) != 2 {
		t.Fatal("excluded group leaked saved messages", replay)
	}
	for _, event := range replay.Items {
		if event.ConversationID == lookupGroup {
			t.Fatal("excluded group visible")
		}
	}
}
func TestInboxPartialFailureRetriesAndPersistentGap(t *testing.T) {
	s, a, _, source := fixtureInbox(t)
	source.failures[lookupGroup] = errors.New("private diagnostic")
	page := pollFixture(t, s, a.ID, "", 100, 3)
	if len(page.Items) != 2 || len(page.Sync.Errors) != 1 || !page.Sync.MoreWork {
		t.Fatal(page)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "private diagnostic") {
		t.Fatal("raw error leaked")
	}
	delete(source.failures, lookupGroup)
	window := source.windows[lookupGroup]
	window.HasMore = true
	source.windows[lookupGroup] = window
	recovered := pollFixture(t, s, a.ID, page.NextCursor, 100, 3)
	if len(recovered.Items) != 1 || len(recovered.Sync.HistoryGapPossible) != 1 {
		t.Fatal(recovered)
	}
	next := pollFixture(t, s, a.ID, recovered.NextCursor, 100, 3)
	if len(next.Items) != 0 || len(next.Sync.HistoryGapPossible) != 1 {
		t.Fatal("gap forgotten after response loss", next)
	}
	source.metadataErr = fail("reauth_required", "private auth detail", 409)
	offline := pollFixture(t, s, a.ID, page.NextCursor, 100, 3)
	if len(offline.Items) != 1 || offline.Sync.MetadataSynced || !offline.Sync.MoreWork || offline.Sync.Errors[0].Code != "reauth_required" {
		t.Fatal(offline)
	}
}
func TestInboxWorkBudgetAndMetadataContinuation(t *testing.T) {
	s, a, _, source := fixtureInbox(t)
	head := source.pages[""]
	source.pages[""] = inboxMetadata{Items: head.Items[:1], Next: "older"}
	source.pages["older"] = inboxMetadata{Items: head.Items[1:]}
	page := pollFixture(t, s, a.ID, "", 100, 1)
	if page.Sync.ConversationsFetched != 1 || !page.Sync.MoreWork {
		t.Fatal(page)
	}
	second := pollFixture(t, s, a.ID, page.NextCursor, 100, 1)
	if second.Sync.ConversationsFetched != 1 {
		t.Fatal(second)
	}
	if source.calls[lookupConversation] != 1 || source.calls[lookupGroup] != 1 {
		t.Fatal("unchanged history fetched again", source.calls)
	}
}
func TestInboxRejectsUnsafeProjectionWithoutAdvancing(t *testing.T) {
	s, a, _, source := fixtureInbox(t)
	w := source.windows[lookupConversation]
	w.MediaOpened = true
	source.windows[lookupConversation] = w
	page := pollFixture(t, s, a.ID, "", 100, 3)
	if len(page.Items) != 1 || page.Sync.PendingConversations != 1 || len(page.Sync.Errors) != 1 {
		t.Fatal(page)
	}
	w.MediaOpened = false
	w.Participants = append(w.Participants, fixtureExcluded)
	source.windows[lookupConversation] = w
	blocked := pollFixture(t, s, a.ID, page.NextCursor, 100, 3)
	if len(blocked.Items) != 0 || len(blocked.Sync.Errors) != 1 {
		t.Fatal("newly excluded member did not block ingestion", blocked)
	}
}

func TestInboxDetectsActivityWithoutVersionChange(t *testing.T) {
	s, a, _, source := fixtureInbox(t)
	first := pollFixture(t, s, a.ID, "", 100, 3)
	source.pages[""].Items[0].LastEventAt++
	w := source.windows[lookupConversation]
	text := "timestamp-only update"
	w.Items = append(w.Items, inboxMessage{ID: "4", SenderID: fixtureAlice, Type: "text", Text: &text, CreatedAt: 4})
	source.windows[lookupConversation] = w
	changed := pollFixture(t, s, a.ID, first.NextCursor, 100, 3)
	if len(changed.Items) != 1 || changed.Items[0].MessageID != "4" {
		t.Fatal("missed activity at unchanged conversation version", changed)
	}
}

func TestInboxRechecksMissingActivityTimestampAndFormerMembers(t *testing.T) {
	s, a, _, source := fixtureInbox(t)
	source.pages[""].Items[0].LastEventAt = 0
	first := pollFixture(t, s, a.ID, "", 100, 3)
	if _, err := s.db.Exec("UPDATE inbox_heads SET checked_at=0 WHERE account_id=? AND conversation_id=?", a.ID, lookupConversation); err != nil {
		t.Fatal(err)
	}
	w := source.windows[lookupConversation]
	text := "former member text"
	w.Items = append(w.Items, inboxMessage{ID: "4", SenderID: "77777777-7777-4777-8777-777777777777", Type: "text", Text: &text, CreatedAt: 4})
	source.windows[lookupConversation] = w
	changed := pollFixture(t, s, a.ID, first.NextCursor, 100, 3)
	if len(changed.Items) != 1 || changed.Items[0].MessageID != "4" {
		t.Fatal("periodic metadata fallback missed a former member event", changed)
	}
}

func TestInboxHTTPMCPParity(t *testing.T) {
	s, a, _, _ := fixtureInbox(t)
	pollFixture(t, s, a.ID, "", 100, 3)
	cursor, err := s.inboxToken(a.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler("secret")
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	server, err := MCPServer(httpServer.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "inbox-fixture", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	raw, _ := json.Marshal(map[string]any{"cursor": cursor, "limit": 100, "max_conversations": 3})
	status, httpResult := request(t, handler, "POST", "/v1/accounts/"+a.ID+"/inbox/poll", string(raw), "secret")
	if status != 200 || httpResult.Error != nil {
		t.Fatal(status, httpResult)
	}
	tool, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "snapchat_inbox_poll", Arguments: map[string]any{"account_id": a.ID, "cursor": cursor, "limit": 100, "max_conversations": 3}})
	if err != nil || tool.IsError {
		t.Fatal(tool, err)
	}
	var mcpResult Result
	raw, _ = json.Marshal(tool.StructuredContent)
	if err = json.Unmarshal(raw, &mcpResult); err != nil {
		t.Fatal(err)
	}
	h, m := httpResult.Data.(map[string]any), mcpResult.Data.(map[string]any)
	hp, err := s.inboxPosition(a.ID, h["next_cursor"].(string))
	if err != nil {
		t.Fatal(err)
	}
	mp, err := s.inboxPosition(a.ID, m["next_cursor"].(string))
	if err != nil || hp != mp {
		t.Fatal("checkpoint positions differ", err)
	}
	delete(h, "next_cursor")
	delete(m, "next_cursor")
	left, _ := json.Marshal(h)
	right, _ := json.Marshal(m)
	if string(left) != string(right) {
		t.Fatalf("inbox HTTP/MCP mismatch: %s != %s", left, right)
	}
}

func TestInboxPrioritizesLatestActivity(t *testing.T) {
	s, a, _, source := fixtureInbox(t)
	source.pages[""].Items[0].LastEventAt = 900
	source.pages[""].Items[1].LastEventAt = 100
	page := pollFixture(t, s, a.ID, "", 100, 1)
	if page.Sync.ConversationsFetched != 1 || source.calls[lookupConversation] != 1 || source.calls[lookupGroup] != 0 {
		t.Fatal("cache write order overrode activity priority", page, source.calls)
	}
}

func TestConcurrentInboxPollsDoNotDuplicateIngestion(t *testing.T) {
	s, a, _, source := fixtureInbox(t)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			page, err := s.PollInbox(context.Background(), a.ID, "", 100, 3)
			if err != nil || len(page.Items) != 3 {
				t.Errorf("concurrent poll failed: %v", err)
			}
		}()
	}
	wg.Wait()
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM inbox_events WHERE account_id=?", a.ID).Scan(&count); err != nil || count != 3 {
		t.Fatal("duplicate ingestions", count, err)
	}
	if source.calls[lookupConversation] != 1 || source.calls[lookupGroup] != 1 {
		t.Fatal("unchanged history fetched by concurrent poll", source.calls)
	}
}

func TestExcludedInboxPagesStillAdvance(t *testing.T) {
	s, a, _, _ := fixtureInbox(t)
	pollFixture(t, s, a.ID, "", 100, 3)
	if _, err := s.ChangeExclusion(context.Background(), a.ID, fixtureAlice, true); err != nil {
		t.Fatal(err)
	}
	cursor := ""
	for i := 0; i < 3; i++ {
		page := pollFixture(t, s, a.ID, cursor, 1, 3)
		if len(page.Items) != 0 || page.NextCursor == "" || page.HasMore != (i < 2) {
			t.Fatal("hidden records prevented continuation", page)
		}
		cursor = page.NextCursor
	}
}
