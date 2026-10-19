package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// SessionStatus reports stored material, never inferred remote authentication.
type SessionStatus struct {
	AccountID         string `json:"account_id"`
	State             string `json:"state"`
	CredentialsStored bool   `json:"credentials_stored"`
	SnapchatUserID    string `json:"snapchat_user_id,omitempty"`
	CapturedAt        string `json:"captured_at,omitempty"`
	RefreshedAt       string `json:"refreshed_at,omitempty"`
	FriendsSyncedAt   string `json:"friends_synced_at,omitempty"`
	RuntimeInstalled  bool   `json:"runtime_installed"`
	RemoteVerified    bool   `json:"remote_verified"`
	NextAction        string `json:"next_action"`
}

func safeSessionTime(value string) string {
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return ""
	}
	return timestamp.UTC().Format(time.RFC3339Nano)
}

func (s *Store) SessionStatus(ctx context.Context, account string) (SessionStatus, error) {
	status := SessionStatus{AccountID: account, State: "not_connected", NextAction: "connect"}
	if _, err := s.Account(ctx, account); err != nil {
		return status, err
	}
	status.RuntimeInstalled = true
	for _, name := range []string{"worker.js", "runtime.js", "messaging.wasm"} {
		info, err := os.Stat(filepath.Join(s.runtimeDir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			status.RuntimeInstalled = false
		}
	}
	raw, err := s.LoadSecret(ctx, s.vault, account)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return status, fail("vault_unavailable", "unlock the OS keyring to inspect this account's stored session", 503)
	}
	status.CredentialsStored = true
	var snapshot SessionSnapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Version != 1 || parseUUID(snapshot.UserID) == nil || snapshot.AuthToken == "" || snapshot.IdentityStorage == "" {
		status.State = "incomplete"
		return status, nil
	}
	status.State = "captured"
	status.SnapchatUserID = snapshot.UserID
	status.CapturedAt = safeSessionTime(snapshot.CapturedAt)
	status.RefreshedAt = safeSessionTime(snapshot.RefreshedAt)
	status.FriendsSyncedAt = safeSessionTime(snapshot.FriendsSyncedAt)
	status.NextAction = "verify_session"
	if !status.RuntimeInstalled {
		status.NextAction = "install_runtime"
	}
	return status, nil
}
