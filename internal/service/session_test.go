package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"
	"time"
)

func TestEncryptedSessionPersistence(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "one")
	b := account(t, s, "two")
	ctx := context.Background()
	v := Vault{Key: func() (string, error) { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), nil }}
	material := []byte(`{"private_test_material":"never store me in plaintext"}`)
	if err := s.SaveSecret(ctx, v, a.ID, material); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := s.db.QueryRow("SELECT sealed FROM secrets WHERE account_id=?", a.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("private_test_material")) {
		t.Fatal("plaintext credential stored")
	}
	decoded, err := s.LoadSecret(ctx, v, a.ID)
	if err != nil || !bytes.Equal(decoded, material) {
		t.Fatal(err)
	}
	if _, err = s.LoadSecret(ctx, v, b.ID); err == nil {
		t.Fatal("another account loaded this credential")
	}
}
func TestSessionCookieScopeAndExpiry(t *testing.T) {
	snapshot := SessionSnapshot{Version: 1, UserID: "test"}
	snapshot.Cookies.Cookies = []SessionCookie{{Name: "shared", Value: "value", Domain: ".snapchat.com", Path: "/", Secure: true}, {Name: "account", Value: "value", Domain: "accounts.snapchat.com", Path: "/accounts", Secure: true}, {Name: "expired", Value: "value", Domain: ".snapchat.com", Path: "/", Expires: float64(time.Now().Add(-time.Hour).Unix())}}
	client, err := sessionClient(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	web, _ := url.Parse("https://web.snapchat.com/")
	cookies := client.Jar.Cookies(web)
	if len(cookies) != 1 || cookies[0].Name != "shared" {
		t.Fatal(cookies)
	}
	snapshot.Cookies.Cookies = append(snapshot.Cookies.Cookies, SessionCookie{Domain: "evil.example", Name: "unexpected", Value: "secret"})
	if _, err = sessionClient(snapshot); err == nil {
		t.Fatal("foreign cookie domain accepted")
	}
}
func TestRefreshPreservesBootstrapFields(t *testing.T) {
	raw := []byte(`{"version":1,"user_id":"test","user_agent":"browser-agent","auth_token":"opaque-token","identity_storage":"[]","root_wrapping_key":{"data":"AA==","identity_key_id":"AA=="},"friends":[{"id":"friend"}],"cookies":{"cookies":[]}}`)
	var snapshot SessionSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"root_wrapping_key", "identity_key_id", "friends", "browser-agent"} {
		if !bytes.Contains(b, []byte(required)) {
			t.Fatal("lost bootstrap field", required)
		}
	}
}
func TestRefreshMissingSessionDoesNotContactSnapchat(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "one")
	r := s.Call(context.Background(), "accounts.refresh", map[string]any{"account_id": a.ID})
	if r.Error == nil || r.Error.Code != "reauth_required" {
		t.Fatal(r)
	}
}
