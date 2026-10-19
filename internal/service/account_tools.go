package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func (s *Store) RenameAccount(ctx context.Context, account, label string) (Account, error) {
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 120 {
		return Account{}, fail("invalid_argument", "label must contain 1–120 bytes", 400)
	}
	unlock, err := s.lockAccount(ctx, account)
	if err != nil {
		return Account{}, err
	}
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	a, err := scanAccount(tx.QueryRowContext(ctx, "SELECT * FROM accounts WHERE id=?", account))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, fail("not_found", "account not found", 404)
		}
		return Account{}, err
	}
	if a.Label == label {
		return a, nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE accounts SET label=? WHERE id=?", label, account); err != nil {
		return a, err
	}
	if err := appendEvent(ctx, tx, account, "account.renamed", map[string]any{"label": label}); err != nil {
		return a, err
	}
	a.Label = label
	return a, tx.Commit()
}

type DiagnosticCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type AccountDiagnostics struct {
	AccountID            string            `json:"account_id"`
	Version              string            `json:"version"`
	ReadyForVerification bool              `json:"ready_for_verification"`
	RemoteVerified       bool              `json:"remote_verified"`
	Checks               []DiagnosticCheck `json:"checks"`
	Counts               map[string]int    `json:"counts"`
}

func nodeRuntimeCheck(ctx context.Context) DiagnosticCheck {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "--version").Output()
	if err != nil {
		return DiagnosticCheck{"node", "error", "Node.js 24 or later is required on the service host"}
	}
	version := strings.TrimSpace(string(output))
	major, err := strconv.Atoi(strings.Split(strings.TrimPrefix(version, "v"), ".")[0])
	if err != nil || major < 24 {
		return DiagnosticCheck{"node", "error", "Node.js 24 or later is required on the service host"}
	}
	return DiagnosticCheck{"node", "ok", version}
}

func (s *Store) Diagnostics(ctx context.Context, account string) (AccountDiagnostics, error) {
	d := AccountDiagnostics{AccountID: account, Version: Version, Checks: []DiagnosticCheck{}, Counts: map[string]int{}}
	if _, err := s.Account(ctx, account); err != nil {
		return d, err
	}
	node := nodeRuntimeCheck(ctx)
	d.Checks = append(d.Checks, node)
	status, err := s.SessionStatus(ctx, account)
	if err != nil {
		d.Checks = append(d.Checks, DiagnosticCheck{"session", "error", publicError(err).Code})
	} else {
		state := "error"
		if status.State == "captured" {
			state = "ok"
		}
		d.Checks = append(d.Checks, DiagnosticCheck{"session", state, status.State})
		runtimeStatus := "error"
		if status.RuntimeInstalled {
			runtimeStatus = "ok"
		}
		d.Checks = append(d.Checks, DiagnosticCheck{"runtime_assets", runtimeStatus, "Pinned runtime files must be installed; use protocol-assets if missing"})
		d.ReadyForVerification = node.Status == "ok" && status.State == "captured" && status.RuntimeInstalled
	}
	for _, table := range []string{"conversations", "exclusions", "deliveries", "inbox_events"} {
		var count int
		// Table names are a fixed internal allowlist, never caller input.
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE account_id=?", account).Scan(&count); err != nil {
			return d, err
		}
		d.Counts[table] = count
	}
	return d, nil
}

// DisconnectAccount forgets this service's credentials. Browser/mobile sessions are unaffected.
func (s *Store) DisconnectAccount(ctx context.Context, account string) (any, error) {
	unlock, err := s.lockAccount(ctx, account)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err = s.Account(ctx, account); err != nil {
		return nil, err
	}
	// Legacy installations predate persistent identity bindings. Preserve identity
	// before deleting credentials so reconnect cannot mix two users' history.
	var bound string
	err = s.db.QueryRowContext(ctx, "SELECT user_id FROM account_identities WHERE account_id=?", account).Scan(&bound)
	if err == sql.ErrNoRows {
		raw, readErr := s.LoadSecret(ctx, s.vault, account)
		if readErr != nil && readErr != sql.ErrNoRows {
			return nil, fail("vault_unavailable", "unlock credential storage once to preserve this account identity before disconnecting", 409)
		}
		if readErr == nil {
			var identity struct {
				UserID string `json:"user_id"`
			}
			if json.Unmarshal(raw, &identity) != nil || identity.UserID == "" {
				return nil, fail("invalid_session", "cannot preserve account identity", 409)
			}
			if _, err = s.db.ExecContext(ctx, "INSERT OR IGNORE INTO account_identities VALUES(?,?)", account, identity.UserID); err != nil {
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	removed, err := tx.ExecContext(ctx, "DELETE FROM secrets WHERE account_id=?", account)
	if err != nil {
		return nil, err
	}
	n, err := removed.RowsAffected()
	if err != nil {
		return nil, err
	}
	cancelled, err := tx.ExecContext(ctx, "UPDATE jobs SET state='cancelled' WHERE account_id=? AND state='queued'", account)
	if err != nil {
		return nil, err
	}
	count, err := cancelled.RowsAffected()
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE accounts SET status='disconnected' WHERE id=?", account); err != nil {
		return nil, err
	}
	if n > 0 || count > 0 {
		if err = appendEvent(ctx, tx, account, "account.disconnected", map[string]any{"cancelled_jobs": count}); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"account_id": account, "session_removed": n > 0, "cancelled_jobs": count, "status": "disconnected", "browser_session_unchanged": true}, nil
}
