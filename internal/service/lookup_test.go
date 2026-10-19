package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

const fixtureSelf = "11111111-1111-4111-8111-111111111111"
const fixtureAlice = "22222222-2222-4222-8222-222222222222"
const fixtureBob = "33333333-3333-4333-8333-333333333333"
const fixtureExcluded = "44444444-4444-4444-8444-444444444444"
const lookupConversation = "55555555-5555-4555-8555-555555555555"
const lookupGroup = "66666666-6666-4666-8666-666666666666"

func lookupFixture(t *testing.T) (*Store, Account, Account) {
	t.Helper()
	s := testStore(t)
	a := account(t, s, "one")
	b := account(t, s, "two")
	s.vault = Vault{Key: func() (string, error) { return base64.StdEncoding.EncodeToString(make([]byte, 32)), nil }}
	friends, _ := json.Marshal([]Friend{{ID: fixtureSelf, Username: "self"}, {ID: fixtureAlice, Username: "alice", DisplayName: "Sam"}, {ID: fixtureBob, Username: "bob", DisplayName: "Sam"}, {ID: fixtureExcluded, Username: "private_contact", DisplayName: "Private"}})
	snapshot := SessionSnapshot{UserID: fixtureSelf, Friends: friends, FriendsSyncedAt: now()}
	raw, _ := json.Marshal(snapshot)
	if err := s.SaveSecret(context.Background(), s.vault, a.ID, raw); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceExclusions(context.Background(), a.ID, []string{fixtureExcluded}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Conversation{{ID: lookupConversation, Type: "ONE_ON_ONE", Participants: []string{fixtureSelf, fixtureAlice}, Version: 1}, {ID: lookupGroup, Type: "USER_CREATED_GROUP", Title: "Fixture group", Participants: []string{fixtureSelf, fixtureAlice, fixtureBob}, Version: 2}} {
		raw, _ := json.Marshal(c)
		if _, err := s.db.Exec("INSERT INTO conversations VALUES(?,?,?,?)", a.ID, c.ID, raw, now()); err != nil {
			t.Fatal(err)
		}
	}
	return s, a, b
}

func TestRecipientSearchAndResolution(t *testing.T) {
	s, a, b := lookupFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		query, status string
		count         int
	}{{"@ALICE", "resolved", 1}, {fixtureAlice, "resolved", 1}, {"Sam", "ambiguous", 2}, {"ali", "ambiguous", 1}, {"private_contact", "not_found", 0}, {"self", "not_found", 0}, {"missing", "not_found", 0}} {
		t.Run(tc.query, func(t *testing.T) {
			r, err := s.ResolveRecipient(ctx, a.ID, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			d := r.(map[string]any)
			if d["status"] != tc.status || d["total_candidates"] != tc.count || d["stale"] != false {
				t.Fatal(d)
			}
		})
	}
	r, err := s.SearchFriends(ctx, a.ID, "sam", "", 1)
	if err != nil || len(r.Items) != 1 || r.NextCursor != "" && r.NextCursor != r.Items[0].ID {
		t.Fatal(r, err)
	}
	if r.NextCursor == "" {
		t.Fatal("missing continuation")
	}
	next, err := s.SearchFriends(ctx, a.ID, "sam", r.NextCursor, 1)
	if err != nil || len(next.Items) != 1 || next.Items[0].ID == r.Items[0].ID || next.NextCursor != "" {
		t.Fatal(next, err)
	}
	if _, err = s.ResolveRecipient(ctx, b.ID, "alice"); err == nil {
		t.Fatal("cross-account snapshot access")
	}
	for _, q := range []string{" ", "@", strings.Repeat("x", 129)} {
		if _, err = s.ResolveRecipient(ctx, a.ID, q); err == nil {
			t.Fatal("accepted invalid query")
		}
	}
	if _, err = s.SearchFriends(ctx, a.ID, "sam", "bad", 1); err == nil {
		t.Fatal("accepted invalid cursor")
	}
	var snapshot SessionSnapshot
	raw, err := s.LoadSecret(ctx, s.vault, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &snapshot)
	snapshot.FriendsSyncedAt = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	raw, _ = json.Marshal(snapshot)
	if err = s.SaveSecret(ctx, s.vault, a.ID, raw); err != nil {
		t.Fatal(err)
	}
	result, err := s.ResolveRecipient(ctx, a.ID, "alice")
	if err != nil || result.(map[string]any)["stale"] != true {
		t.Fatal("stale snapshot not reported", err)
	}
}

func TestCachedConversationOwnershipAndExclusions(t *testing.T) {
	s, a, b := lookupFixture(t)
	ctx := context.Background()
	if _, err := s.GetConversation(ctx, a.ID, lookupGroup, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetConversation(ctx, b.ID, lookupGroup, true); err == nil {
		t.Fatal("cross-account group access")
	}
	if _, err := s.GetConversation(ctx, a.ID, lookupConversation, true); err == nil {
		t.Fatal("direct chat accepted as group")
	}
	if _, err := s.ChangeExclusion(ctx, a.ID, fixtureBob, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetConversation(ctx, a.ID, lookupGroup, true); publicError(err).Code != "excluded_recipient" {
		t.Fatal(err)
	}
	if _, err := s.GetConversation(ctx, a.ID, lookupConversation, false); err != nil {
		t.Fatal("unrelated exclusion blocked direct chat", err)
	}
}

func TestAtomicExclusionEdits(t *testing.T) {
	s, a, b := lookupFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for _, id := range []string{fixtureAlice, fixtureBob, fixtureAlice} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := s.ChangeExclusion(ctx, a.ID, id, true); err != nil {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	ids, err := s.Exclusions(ctx, a.ID)
	if err != nil || len(ids) != 3 {
		t.Fatal(ids, err)
	}
	r, err := s.ChangeExclusion(ctx, a.ID, fixtureAlice, true)
	if err != nil || r.(map[string]any)["changed"] != false {
		t.Fatal(r, err)
	}
	if _, err = s.ChangeExclusion(ctx, b.ID, fixtureAlice, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeExclusion(ctx, a.ID, fixtureAlice, false); err != nil {
		t.Fatal(err)
	}
	ids, err = s.Exclusions(ctx, a.ID)
	if err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
	var events int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM events WHERE account_id=? AND type='exclusions.added'", a.ID).Scan(&events); err != nil || events != 2 {
		t.Fatal(events, err)
	}
}

func TestDeliveryLookupRedactionAndOwnership(t *testing.T) {
	s, a, b := lookupFixture(t)
	ctx := context.Background()
	for _, state := range []string{"pending", "unknown", "not_sent", "sent"} {
		if _, err := s.db.Exec("INSERT INTO deliveries VALUES(?,?,?,?,?,?)", a.ID, state, "hash", state, []byte(`{"acknowledged":true,"server_message_id":"7","send_frame":"private","key":"secret"}`), now()); err != nil {
			t.Fatal(err)
		}
		r, err := s.GetDelivery(ctx, a.ID, state)
		if err != nil {
			t.Fatal(err)
		}
		d := r.(map[string]any)
		if d["state"] != state || d["can_retry_same_key"] != (state == "not_sent") {
			t.Fatal(d)
		}
		raw, _ := json.Marshal(d)
		if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "secret") {
			t.Fatal("receipt leaked internal data")
		}
	}
	if _, err := s.GetDelivery(ctx, b.ID, "sent"); publicError(err).Code != "not_found" {
		t.Fatal(err)
	}
}

func TestBootstrapFriendTimestampCompatibility(t *testing.T) {
	s, a, _ := lookupFixture(t)
	ctx := context.Background()
	raw, err := s.LoadSecret(ctx, s.vault, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot SessionSnapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.CapturedAt = now()
	snapshot.FriendsSyncedAt = ""
	raw, _ = json.Marshal(snapshot)
	if err = s.SaveSecret(ctx, s.vault, a.ID, raw); err != nil {
		t.Fatal(err)
	}
	result, err := s.SearchFriends(ctx, a.ID, "alice", "", 10)
	if err != nil || len(result.Items) != 1 || result.Stale || result.SyncedAt != snapshot.CapturedAt {
		t.Fatal("captured friend snapshot must be searchable", result, err)
	}
	snapshot.Friends = nil
	raw, _ = json.Marshal(snapshot)
	if err = s.SaveSecret(ctx, s.vault, a.ID, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SearchFriends(ctx, a.ID, "alice", "", 10); publicError(err).Code != "snapshot_required" {
		t.Fatal("missing friends must not use capture timestamp alone", err)
	}
}
