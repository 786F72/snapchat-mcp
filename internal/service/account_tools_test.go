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
