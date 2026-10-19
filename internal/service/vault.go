package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
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
func (s *Store) SaveSecret(ctx context.Context, v Vault, account string, plain []byte) error {
	if _, err := s.Account(ctx, account); err != nil {
		return err
	}
	sealed, err := v.Seal(account, plain)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO secrets VALUES(?,?) ON CONFLICT(account_id) DO UPDATE SET sealed=excluded.sealed", account, sealed)
	return err
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
