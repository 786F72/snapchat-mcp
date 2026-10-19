package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionStatusIsolationAndRedaction(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "connected")
	b := account(t, s, "empty")
	s.vault = Vault{Key: func() (string, error) { return base64.StdEncoding.EncodeToString(make([]byte, 32)), nil }}
	ctx := context.Background()
	snapshot := SessionSnapshot{Version: 1, UserID: fixtureSelf, AuthToken: "secret-token", IdentityStorage: "secret-identity", CapturedAt: "2026-10-19T21:00:00Z", RefreshedAt: "secret-invalid-time", FriendsSyncedAt: "2026-10-19T21:01:00Z"}
	raw, _ := json.Marshal(snapshot)
	if err := s.SaveSecret(ctx, s.vault, a.ID, raw); err != nil {
		t.Fatal(err)
	}
	r := s.Call(ctx, "accounts.session_status", map[string]any{"account_id": a.ID})
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	status := r.Data.(SessionStatus)
	if status.State != "captured" || !status.CredentialsStored || status.RemoteVerified || status.RuntimeInstalled || status.NextAction != "install_runtime" || status.RefreshedAt != "" || status.CapturedAt != snapshot.CapturedAt {
		t.Fatal(status)
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), "secret-") {
		t.Fatal("session status leaked private material")
	}
	other, err := s.SessionStatus(ctx, b.ID)
	if err != nil || other.State != "not_connected" || other.CredentialsStored || other.SnapchatUserID != "" {
		t.Fatal(other, err)
	}
	if r := s.Call(ctx, "accounts.session_status", map[string]any{"account_id": "missing"}); r.Error == nil || r.Error.Code != "not_found" {
		t.Fatal(r)
	}
	if err := os.MkdirAll(s.runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"worker.js", "runtime.js", "messaging.wasm"} {
		if err := os.WriteFile(filepath.Join(s.runtimeDir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	status, err = s.SessionStatus(ctx, a.ID)
	if err != nil || !status.RuntimeInstalled || status.NextAction != "verify_session" || status.RemoteVerified {
		t.Fatal(status, err)
	}
	s.vault = Vault{Key: func() (string, error) { return "", fmt.Errorf("locked secret-key") }}
	if _, err := s.SessionStatus(ctx, a.ID); publicError(err).Code != "vault_unavailable" || strings.Contains(err.Error(), "secret-key") {
		t.Fatal(err)
	}
}

func TestSessionStatusRejectsIncompleteSnapshots(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "one")
	s.vault = Vault{Key: func() (string, error) { return base64.StdEncoding.EncodeToString(make([]byte, 32)), nil }}
	for _, raw := range []string{`bad json`, `{"version":1,"user_id":"bad","auth_token":"secret","identity_storage":"secret"}`, `{"version":1,"user_id":"` + fixtureSelf + `"}`} {
		if err := s.SaveSecret(context.Background(), s.vault, a.ID, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		status, err := s.SessionStatus(context.Background(), a.ID)
		if err != nil || status.State != "incomplete" || !status.CredentialsStored || status.SnapchatUserID != "" || status.RemoteVerified || status.NextAction != "connect" {
			t.Fatal(status, err)
		}
	}
}
