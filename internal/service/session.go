package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// SessionSnapshot is private credential material. Never expose it through a tool.
type SessionSnapshot struct {
	Version         int             `json:"version"`
	UserID          string          `json:"user_id"`
	UserAgent       string          `json:"user_agent"`
	FriendsSyncedAt string          `json:"friends_synced_at,omitempty"`
	Friends         json.RawMessage `json:"friends,omitempty"`
	AuthToken       string          `json:"auth_token"`
	IdentityStorage string          `json:"identity_storage"`
	RootWrappingKey json.RawMessage `json:"root_wrapping_key,omitempty"`
	CapturedAt      string          `json:"captured_at"`
	RefreshedAt     string          `json:"refreshed_at,omitempty"`
	Cookies         struct {
		Cookies []SessionCookie `json:"cookies"`
	} `json:"cookies"`
}
type SessionCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	Secure   bool    `json:"secure"`
	HTTPOnly bool    `json:"httpOnly"`
}

const ssoEndpoint = "https://accounts.snapchat.com/accounts/sso?client_id=web-calling-corp--prod"

func snapDomain(domain string) bool {
	d := strings.TrimPrefix(domain, ".")
	return d == "snapchat.com" || d == "accounts.snapchat.com" || d == "www.snapchat.com" || d == "web.snapchat.com"
}
func sessionClient(snapshot SessionSnapshot) (*http.Client, error) {
	jar, _ := cookiejar.New(nil)
	for _, c := range snapshot.Cookies.Cookies {
		if !snapDomain(c.Domain) {
			return nil, fail("invalid_session", "snapshot contains an unexpected cookie domain", 409)
		}
		u, _ := url.Parse("https://" + strings.TrimPrefix(c.Domain, "."))
		cookie := &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HttpOnly: c.HTTPOnly}
		if c.Expires > 0 {
			cookie.Expires = time.Unix(int64(c.Expires), 0)
		}
		jar.SetCookies(u, []*http.Cookie{cookie})
	}
	return &http.Client{Jar: jar, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func (s *Store) Refresh(ctx context.Context, account string) (any, error) {
	lock, _ := s.accountLocks.LoadOrStore(account, make(chan struct{}, 1))
	ch := lock.(chan struct{})
	select {
	case ch <- struct{}{}:
		defer func() { <-ch }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	vault := OSVault()
	material, err := s.LoadSecret(ctx, vault, account)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fail("reauth_required", "capture an interactive login with snapchat-mcp bootstrap first", 409)
	}
	if err != nil {
		return nil, fail("vault_unavailable", "unlock the OS keyring to access this account", 503)
	}
	var snapshot SessionSnapshot
	if json.Unmarshal(material, &snapshot) != nil || snapshot.Version != 1 || snapshot.UserID == "" || len(snapshot.Cookies.Cookies) == 0 {
		return nil, fail("invalid_session", "invalid stored session snapshot", 409)
	}
	client, err := sessionClient(snapshot)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", ssoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("scuid", "1")
	req.Header.Set("Origin", "https://www.snapchat.com")
	req.Header.Set("Referer", "https://www.snapchat.com/")
	res, err := client.Do(req)
	if err != nil {
		return nil, fail("network_error", "Snapchat session refresh failed; no messages were submitted", 502)
	}
	defer res.Body.Close()
	if res.StatusCode == 429 {
		return nil, fail("rate_limited", "Snapchat throttled session refresh; no automatic retry", 429)
	}
	if res.StatusCode >= 300 && res.StatusCode < 500 {
		return nil, fail("reauth_required", "Snapchat requires interactive login or device approval", 409)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fail("upstream_error", "Snapchat session refresh was unsuccessful", 502)
	}
	if res.Header.Get("scuid") != snapshot.UserID {
		return nil, fail("identity_mismatch", "refresh identity was absent or differs from the captured account", 409)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	token := strings.TrimSpace(string(body))
	if err != nil || len(body) > 8192 || len(token) < 16 || strings.ContainsAny(token, " \t\r\n<>") {
		return nil, fail("invalid_session", "unexpected session refresh response", 502)
	}
	snapshot.AuthToken = token
	snapshot.RefreshedAt = now()
	// Preserve browser cookie attributes and update values explicitly returned by
	// this endpoint. Other session cookies remain unchanged.
	for _, cookie := range res.Cookies() {
		domain := cookie.Domain
		if domain == "" {
			domain = "accounts.snapchat.com"
		}
		if !snapDomain(domain) {
			return nil, fail("invalid_session", "unexpected refreshed cookie domain", 502)
		}
		path := cookie.Path
		if path == "" {
			path = "/accounts"
		}
		item := SessionCookie{Name: cookie.Name, Value: cookie.Value, Domain: domain, Path: path, Secure: cookie.Secure, HTTPOnly: cookie.HttpOnly}
		if !cookie.Expires.IsZero() {
			item.Expires = float64(cookie.Expires.Unix())
		}
		if cookie.MaxAge > 0 {
			item.Expires = float64(time.Now().Unix() + int64(cookie.MaxAge))
		}
		if cookie.MaxAge < 0 {
			item.Expires = 1
		}
		replaced := false
		for i, old := range snapshot.Cookies.Cookies {
			if old.Name == item.Name && old.Domain == item.Domain && old.Path == item.Path {
				snapshot.Cookies.Cookies[i] = item
				replaced = true
			}
		}
		if !replaced {
			snapshot.Cookies.Cookies = append(snapshot.Cookies.Cookies, item)
		}
	}
	material, err = json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if err = s.SaveSecret(ctx, vault, account, material); err != nil {
		return nil, fail("vault_unavailable", "could not persist refreshed session", 503)
	}
	return map[string]any{"account_id": account, "session_refreshed": true, "refreshed_at": snapshot.RefreshedAt, "backend": "standalone", "protocol_ready": false}, nil
}
