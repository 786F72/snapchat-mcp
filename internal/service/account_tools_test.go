package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRenameAccountIsLocalAtomicAndIdempotent(t *testing.T) {
	s, a, b := lookupFixture(t)
	ctx := context.Background()
	before, err := s.LoadSecret(ctx, s.vault, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r := s.Call(ctx, "accounts.rename", map[string]any{"account_id": a.ID, "label": " Personal "})
		if r.Error != nil || r.Data.(Account).Label != "Personal" {
			t.Fatal(r)
		}
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM events WHERE account_id=? AND type='account.renamed'", a.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	other, err := s.Account(ctx, b.ID)
	if err != nil || other.Label != b.Label {
		t.Fatal(other, err)
	}
	after, err := s.LoadSecret(ctx, s.vault, a.ID)
	if err != nil || string(before) != string(after) {
		t.Fatal("rename modified credentials", err)
	}
	for _, label := range []string{" ", strings.Repeat("x", 121)} {
		if r := s.Call(ctx, "accounts.rename", map[string]any{"account_id": a.ID, "label": label}); r.Error == nil || r.Error.Code != "invalid_argument" {
			t.Fatal(r)
		}
	}
	if r := s.Call(ctx, "accounts.rename", map[string]any{"account_id": "missing", "label": "x"}); r.Error == nil || r.Error.Code != "not_found" {
		t.Fatal(r)
	}
}

func TestDiagnosticsAreLocalIsolatedAndRedacted(t *testing.T) {
	s, a, b := lookupFixture(t)
	ctx := context.Background()
	d, err := s.Diagnostics(ctx, a.ID)
	if err != nil || d.Counts["conversations"] != 2 || d.Counts["exclusions"] != 1 || d.Version != Version || d.RemoteVerified || d.ReadyForVerification {
		t.Fatal(d, err)
	}
	other, err := s.Diagnostics(ctx, b.ID)
	if err != nil || other.Counts["conversations"] != 0 || other.Counts["exclusions"] != 0 {
		t.Fatal(other, err)
	}
	s.vault = Vault{Key: func() (string, error) { return "", fmt.Errorf("private-key-error") }}
	d, err = s.Diagnostics(ctx, a.ID)
	if err != nil || d.ReadyForVerification {
		t.Fatal(d, err)
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "private-key-error") || strings.Contains(string(raw), fixtureSelf) || strings.Contains(string(raw), "private_contact") {
		t.Fatal("diagnostics leaked private data")
	}
	if r := s.Call(ctx, "accounts.diagnostics", map[string]any{"account_id": "missing"}); r.Error == nil || r.Error.Code != "not_found" {
		t.Fatal(r)
	}
}

func TestDisconnectAccountRemovesSessionAndCancelsOnlyOwnedQueuedJobs(t *testing.T) {
	s, a, b := lookupFixture(t)
	ctx := context.Background()
	for _, tc := range []struct{ id, account, state string }{{"queued-a", a.ID, "queued"}, {"running-a", a.ID, "running"}, {"queued-b", b.ID, "queued"}} {
		if _, err := s.db.Exec("INSERT INTO jobs VALUES(?,?,?,?,?)", tc.id, tc.account, tc.state, []byte(`{}`), now()); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := s.LoadSecret(ctx, s.vault, a.ID)
	if len(before) == 0 {
		t.Fatal("missing fixture session")
	}
	r, err := s.DisconnectAccount(ctx, a.ID)
	if err != nil || r.(map[string]any)["session_removed"] != true {
		t.Fatal(r, err)
	}
	if _, err = s.LoadSecret(ctx, s.vault, a.ID); err == nil {
		t.Fatal("session retained")
	}
	for _, tc := range []struct{ id, account, state string }{{"queued-a", a.ID, "cancelled"}, {"running-a", a.ID, "running"}, {"queued-b", b.ID, "queued"}} {
		j, err := s.Job(ctx, tc.account, tc.id, false)
		if err != nil || j.State != tc.state {
			t.Fatal(j, err)
		}
	}
	r, err = s.DisconnectAccount(ctx, a.ID)
	if err != nil || r.(map[string]any)["session_removed"] != false {
		t.Fatal(r, err)
	}
	status, err := s.SessionStatus(ctx, a.ID)
	if err != nil || status.CredentialsStored || status.NextAction != "connect" {
		t.Fatal(status, err)
	}
	excluded, _ := s.Exclusions(ctx, a.ID)
	if len(excluded) != 1 || excluded[0] != fixtureExcluded {
		t.Fatal("exclusions changed")
	}
}

func TestDisconnectedAccountCannotBeReboundOrResurrectedByRefresh(t *testing.T) {
	s, a, _ := lookupFixture(t)
	ctx := context.Background()
	old, _ := s.LoadSecret(ctx, s.vault, a.ID)
	if _, err := s.DisconnectAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSecret(ctx, s.vault, a.ID, old); publicError(err).Code != "reauth_required" {
		t.Fatal("late refresh resurrected session", err)
	}
	var snapshot SessionSnapshot
	json.Unmarshal(old, &snapshot)
	snapshot.UserID = fixtureBob
	different, _ := json.Marshal(snapshot)
	if err := s.CaptureSession(ctx, s.vault, a.ID, different); publicError(err).Code != "identity_mismatch" {
		t.Fatal("account rebound after logout", err)
	}
	if err := s.CaptureSession(ctx, s.vault, a.ID, old); err != nil {
		t.Fatal("same user could not reconnect", err)
	}
	if _, err := s.LoadSecret(ctx, s.vault, a.ID); err != nil {
		t.Fatal(err)
	}
}
