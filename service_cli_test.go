package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/786F72/snapchat-mcp/internal/service"
)

func TestServiceTokenPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	first, err := tokenFile(path, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tokenFile(path, true)
	if err != nil || first != second {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Windows ACL rejection is covered by TestWindowsTokenACL.
		return
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = tokenFile(path, false); err == nil {
		t.Fatal("public token accepted")
	}
}
func TestExplicitExclusionImportMerges(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := service.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := s.CreateAccount(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReplaceExclusions(ctx, a.ID, []string{"existing"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "import.json")
	if err = os.WriteFile(path, []byte(`["new","existing"]`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = run(ctx, []string{"import-exclusions", "--data-dir", dir, "--account", a.ID, "--file", path}, &out); err != nil {
		t.Fatal(err)
	}
	ids, err := s.Exclusions(ctx, a.ID)
	if err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
	out.Reset()
	if err = run(ctx, []string{"api-spec"}, &out); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out.Bytes()) {
		t.Fatal("invalid OpenAPI JSON")
	}
}
