package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/786F72/snapchat-mcp/internal/service"
)

func TestConnectionAccountSelection(t *testing.T) {
	s, err := service.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := connectionAccount(ctx, s, "", "", false); err == nil {
		t.Fatal("status created an account")
	}
	a, err := connectionAccount(ctx, s, "", "", true)
	if err != nil || a.Label != "personal" {
		t.Fatal(a, err)
	}
	selected, err := connectionAccount(ctx, s, "", "", true)
	if err != nil || selected.ID != a.ID {
		t.Fatal("single account was not reused", selected, err)
	}
	b, err := connectionAccount(ctx, s, "", "work", true)
	if err != nil || b.ID == a.ID {
		t.Fatal(b, err)
	}
	if _, err := connectionAccount(ctx, s, "", "", true); err == nil {
		t.Fatal("ambiguous account chosen")
	}
	if selected, err := connectionAccount(ctx, s, "", "work", false); err != nil || selected.ID != b.ID {
		t.Fatal(selected, err)
	}
	if _, err := connectionAccount(ctx, s, a.ID, "work", true); err == nil {
		t.Fatal("conflicting selectors accepted")
	}
	if _, err := connectionAccount(ctx, s, "missing", "", true); err == nil {
		t.Fatal("unknown account accepted")
	}
	if _, err := s.CreateAccount(ctx, "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := connectionAccount(ctx, s, "", "work", true); err == nil {
		t.Fatal("ambiguous label chosen")
	}
}

func TestVersionDoctorAndRefreshCommands(t *testing.T) {
	var out bytes.Buffer
	for _, command := range []string{"version", "--version"} {
		out.Reset()
		if err := run(context.Background(), []string{command}, &out); err != nil || strings.TrimSpace(out.String()) != "snapchat-mcp "+service.Version {
			t.Fatal(out.String(), err)
		}
	}
	if err := run(context.Background(), []string{"version", "extra"}, &out); err == nil {
		t.Fatal("extra version arguments accepted")
	}
	dir := t.TempDir()
	s, err := service.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccount(context.Background(), "empty"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	out.Reset()
	if err := run(context.Background(), []string{"doctor", "--data-dir", dir}, &out); err == nil {
		t.Fatal("doctor did not flag missing prerequisites")
	}
	var diagnostics struct {
		Data service.AccountDiagnostics `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &diagnostics); err != nil || diagnostics.Data.ReadyForVerification || diagnostics.Data.Version != service.Version {
		t.Fatal(out.String(), err)
	}
	out.Reset()
	if err := run(context.Background(), []string{"refresh", "--data-dir", dir}, &out); err == nil {
		t.Fatal("refresh succeeded without credentials")
	}
	var failed struct {
		Data  service.SessionStatus `json:"data"`
		Error *service.Error        `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &failed); err != nil || failed.Error == nil || failed.Error.Code != "reauth_required" || failed.Data.RemoteVerified || failed.Data.NextAction != "connect" {
		t.Fatal(out.String(), err)
	}
}

func TestConnectionStatusAndBrowserFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := service.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAccount(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	var out bytes.Buffer
	if err := run(context.Background(), []string{"status", "--data-dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Data service.SessionStatus `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.AccountID != a.ID || result.Data.State != "not_connected" || result.Data.RemoteVerified {
		t.Fatal(result)
	}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) }))
	defer fake.Close()
	if err := run(context.Background(), []string{"connect", "--data-dir", dir, "--label", "new", "--cdp", fake.URL}, &out); err == nil {
		t.Fatal("connected without a signed-in tab")
	}
	s, err = service.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err := s.Accounts(context.Background(), "", 100)
	if err != nil || len(page.Items.([]service.Account)) != 1 {
		t.Fatal("failed browser check created an account", page, err)
	}
}
