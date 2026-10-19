// Package service implements the local, account-scoped Snapchat service.
package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db           *sql.DB
	accountLocks sync.Map
	vault        Vault
	mediaMu      sync.RWMutex
	mediaRoots   []string
	runtimeDir   string
}
type Account struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	TextOnly  bool   `json:"text_only"`
	CreatedAt string `json:"created_at"`
}
type Event struct {
	Cursor    int64           `json:"cursor"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	CreatedAt string          `json:"created_at"`
}
type Page struct {
	Items      any    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "service.db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY,label TEXT NOT NULL,status TEXT NOT NULL,text_only INTEGER NOT NULL,created_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS exclusions(account_id TEXT REFERENCES accounts(id) ON DELETE CASCADE,recipient TEXT NOT NULL,PRIMARY KEY(account_id,recipient));
 CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY AUTOINCREMENT,account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,type TEXT NOT NULL,data BLOB NOT NULL,created_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,state TEXT NOT NULL,data BLOB NOT NULL,created_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS deliveries(account_id TEXT REFERENCES accounts(id) ON DELETE CASCADE,idempotency_key TEXT NOT NULL,payload_hash TEXT NOT NULL,state TEXT NOT NULL,receipt BLOB NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(account_id,idempotency_key));
 CREATE TABLE IF NOT EXISTS conversations(account_id TEXT REFERENCES accounts(id) ON DELETE CASCADE,id TEXT NOT NULL,data BLOB NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(account_id,id));
 CREATE TABLE IF NOT EXISTS secrets(account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,sealed BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS protocol_cursors(account_id TEXT REFERENCES accounts(id) ON DELETE CASCADE,id TEXT NOT NULL,kind TEXT NOT NULL,state BLOB NOT NULL,expires_at INTEGER NOT NULL,PRIMARY KEY(account_id,id));
 CREATE TABLE IF NOT EXISTS media(account_id TEXT REFERENCES accounts(id) ON DELETE CASCADE,id TEXT NOT NULL,sealed BLOB NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(account_id,id));
 PRAGMA user_version=3;`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, vault: OSVault(), runtimeDir: filepath.Join(dir, "runtime")}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func scanAccount(row interface{ Scan(...any) error }) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Label, &a.Status, &a.TextOnly, &a.CreatedAt)
	return a, err
}
func (s *Store) Account(ctx context.Context, id string) (Account, error) {
	a, err := scanAccount(s.db.QueryRowContext(ctx, "SELECT * FROM accounts WHERE id=?", id))
	if err == sql.ErrNoRows {
		return a, fail("not_found", "account not found", 404)
	}
	return a, err
}
func (s *Store) CreateAccount(ctx context.Context, label string) (Account, error) {
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 120 {
		return Account{}, fail("invalid_argument", "label must contain 1–120 bytes", 400)
	}
	a := Account{newID(), label, "protocol_blocked", true, now()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return a, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO accounts VALUES(?,?,?,?,?)", a.ID, a.Label, a.Status, a.TextOnly, a.CreatedAt); err != nil {
		return a, err
	}
	if err = appendEvent(ctx, tx, a.ID, "account.created", map[string]any{"status": a.Status}); err != nil {
		return a, err
	}
	return a, tx.Commit()
}
func (s *Store) Accounts(ctx context.Context, cursor string, limit int) (Page, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT * FROM accounts WHERE id>? ORDER BY id LIMIT ?", cursor, limit+1)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	items := []Account{}
	for rows.Next() {
		a, e := scanAccount(rows)
		if e != nil {
			return Page{}, e
		}
		items = append(items, a)
	}
	p := Page{Items: items}
	if len(items) > limit {
		p.Items = items[:limit]
		p.NextCursor = items[limit-1].ID
	}
	return p, rows.Err()
}
func appendEvent(ctx context.Context, tx *sql.Tx, account, kind string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO events(account_id,type,data,created_at) VALUES(?,?,?,?)", account, kind, b, now())
	return err
}
func (s *Store) Exclusions(ctx context.Context, account string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT recipient FROM exclusions WHERE account_id=? ORDER BY recipient", account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func (s *Store) ReplaceExclusions(ctx context.Context, account string, ids []string) error {
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || len(id) > 128 {
			return fail("invalid_argument", "invalid recipient identifier", 400)
		}
	}
	unlock, err := s.lockAccount(ctx, account)
	if err != nil {
		return err
	}
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM exclusions WHERE account_id=?", account); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO exclusions VALUES(?,?)", account, id); err != nil {
			return err
		}
	}
	if err = appendEvent(ctx, tx, account, "exclusions.replaced", map[string]int{"count": len(ids)}); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Events(ctx context.Context, account string, cursor int64, limit int) (Page, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,type,data,created_at FROM events WHERE account_id=? AND id>? ORDER BY id LIMIT ?", account, cursor, limit+1)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	items := []Event{}
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.Cursor, &e.Type, &e.Data, &e.CreatedAt); err != nil {
			return Page{}, err
		}
		items = append(items, e)
	}
	p := Page{Items: items}
	if len(items) > limit {
		p.Items = items[:limit]
		p.NextCursor = fmt.Sprint(items[limit-1].Cursor)
	}
	return p, rows.Err()
}

// Share mutation ordering with the native sender's per-account lock.
func (s *Store) lockAccount(ctx context.Context, account string) (func(), error) {
	lock, _ := s.accountLocks.LoadOrStore(account, make(chan struct{}, 1))
	ch := lock.(chan struct{})
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
