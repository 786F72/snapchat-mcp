package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/zalando/go-keyring"
)

// Vault is internal-only: credential material never crosses HTTP or MCP.
// The master key must already exist or be provisioned explicitly. Never fall
// back to a plaintext file when the user's keyring is unavailable.
type Vault struct{ Key func() (string, error) }

func OSVault() Vault {
	return Vault{Key: func() (string, error) { return keyring.Get("qq_snap", "master-key-v1") }}
}
func ProvisionMasterKey() error {
	if _, err := keyring.Get("qq_snap", "master-key-v1"); err == nil {
		return nil
	} else if err != keyring.ErrNotFound {
		return fmt.Errorf("OS keyring unavailable: %w", err)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return err
	}
	return keyring.Set("qq_snap", "master-key-v1", base64.StdEncoding.EncodeToString(key))
}
func (v Vault) aead() (cipher.AEAD, error) {
	raw, err := v.Key()
	if err != nil {
		return nil, fmt.Errorf("OS keyring unavailable")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("invalid vault key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (v Vault) Seal(account string, plain []byte) ([]byte, error) {
	a, err := v.aead()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, plain, []byte("qq_snap/v1/"+account)), nil
}
func (v Vault) Open(account string, sealed []byte) ([]byte, error) {
	a, err := v.aead()
	if err != nil {
		return nil, err
	}
	if len(sealed) < a.NonceSize() {
		return nil, fmt.Errorf("invalid sealed session")
	}
	return a.Open(nil, sealed[:a.NonceSize()], sealed[a.NonceSize():], []byte("qq_snap/v1/"+account))
}

// CaptureSession explicitly reconnects an account after a browser sign-in.
func (s *Store) CaptureSession(ctx context.Context, v Vault, account string, plain []byte) error {
	return s.saveSession(ctx, v, account, plain, true)
}
func (s *Store) SaveSecret(ctx context.Context, v Vault, account string, plain []byte) error {
	return s.saveSession(ctx, v, account, plain, false)
}
func (s *Store) saveSession(ctx context.Context, v Vault, account string, plain []byte, reconnect bool) error {
	sealed, err := v.Seal(account, plain)
	if err != nil {
		return err
	}
	var identity struct {
		UserID string `json:"user_id"`
	}
	_ = json.Unmarshal(plain, &identity)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(ctx, "SELECT status FROM accounts WHERE id=?", account).Scan(&status); err == sql.ErrNoRows {
		return fail("not_found", "account not found", 404)
	} else if err != nil {
		return err
	}
	if status == "disconnected" && !reconnect {
		return fail("reauth_required", "account was disconnected; reconnect with a browser sign-in", 409)
	}
	if identity.UserID != "" {
		var bound string
		err = tx.QueryRowContext(ctx, "SELECT user_id FROM account_identities WHERE account_id=?", account).Scan(&bound)
		if err == nil && bound != identity.UserID {
			return fail("identity_mismatch", "this account is bound to another Snapchat identity", 409)
		}
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if parseUUID(identity.UserID) != nil {
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO account_identities VALUES(?,?)", account, identity.UserID); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO secrets VALUES(?,?) ON CONFLICT(account_id) DO UPDATE SET sealed=excluded.sealed", account, sealed); err != nil {
		return err
	}
	if reconnect && status == "disconnected" {
		if _, err = tx.ExecContext(ctx, "UPDATE accounts SET status='protocol_blocked' WHERE id=?", account); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LoadSecret(ctx context.Context, v Vault, account string) ([]byte, error) {
	if _, err := s.Account(ctx, account); err != nil {
		return nil, err
	}
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, "SELECT sealed FROM secrets WHERE account_id=?", account).Scan(&sealed); err != nil {
		return nil, err
	}
	return v.Open(account, sealed)
}
