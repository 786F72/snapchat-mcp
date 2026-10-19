package service

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDefaultMCPDiscoveryOnlyWorkingTools(t *testing.T) {
	s := testStore(t)
	httpServer := httptest.NewServer(s.Handler("secret"))
	defer httpServer.Close()
	server, err := MCPServer(httpServer.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{}
	for _, op := range Operations() {
		if op.Available {
			expected["snapchat_"+strings.ReplaceAll(op.Name, ".", "_")] = true
		}
	}
	if len(tools.Tools) != len(expected) {
		t.Fatalf("advertised %d tools, expected %d available tools", len(tools.Tools), len(expected))
	}
	for _, tool := range tools.Tools {
		if !expected[tool.Name] {
			t.Fatalf("advertised unavailable tool %s", tool.Name)
		}
	}
}

func TestAgentSchemaConstraints(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "fixture")
	ctx := context.Background()
	for _, in := range []map[string]any{{"account_id": a.ID, "query": "x", "limit": float64(0)}, {"account_id": a.ID, "query": "x", "limit": float64(501)}} {
		if r := s.Call(ctx, "friends.search", in); r.Error == nil || r.Error.Code != "invalid_argument" {
			t.Fatal("schema bounds not enforced", r)
		}
	}
	r := s.Call(ctx, "media.upload_file", map[string]any{"account_id": a.ID, "path": "/media/a", "mime_type": "text/plain"})
	if r.Error == nil || r.Error.Code != "invalid_argument" {
		t.Fatal("MIME enum not enforced")
	}
	for _, op := range Operations() {
		if !op.Available {
			continue
		}
		for name, value := range op.Schema()["properties"].(map[string]any) {
			if value.(map[string]any)["description"] == nil {
				t.Fatalf("missing agent-facing description: %s.%s", op.Name, name)
			}
		}
	}
}
