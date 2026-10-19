package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func account(t *testing.T, s *Store, label string) Account {
	t.Helper()
	a, err := s.CreateAccount(context.Background(), label)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func request(t *testing.T, handler http.Handler, method, path, body, token string) (int, Result) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var result Result
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(w.Body.String(), err)
	}
	return w.Code, result
}
func TestIsolationAndRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := account(t, s, "one")
	b := account(t, s, "two")
	if err = s.ReplaceExclusions(ctx, a.ID, []string{"excluded"}); err != nil {
		t.Fatal(err)
	}
	ids, err := s.Exclusions(ctx, b.ID)
	if err != nil || len(ids) != 0 {
		t.Fatal(ids, err)
	}
	events, err := s.Events(ctx, b.ID, 0, 100)
	if err != nil || len(events.Items.([]Event)) != 1 {
		t.Fatal(events, err)
	}
	if _, err = s.db.Exec("INSERT INTO jobs VALUES(?,?,?,?,?)", "job", a.ID, "queued", []byte(`{}`), now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Job(ctx, b.ID, "job", true); err == nil {
		t.Fatal("cross-account cancellation succeeded")
	}
	if _, err = s.Job(ctx, a.ID, "job", true); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ids, err = s.Exclusions(ctx, a.ID)
	if err != nil || !reflect.DeepEqual(ids, []string{"excluded"}) {
		t.Fatal(ids, err)
	}
	job, err := s.Job(ctx, a.ID, "job", false)
	if err != nil || job.State != "cancelled" {
		t.Fatal(job, err)
	}
	result := s.Call(ctx, "jobs.create", map[string]any{"account_id": a.ID, "recipients": []any{"friend"}, "idempotency_key": "key"})
	if result.Error == nil || result.Error.Code != "unsupported_capability" {
		t.Fatal(result)
	}
	jobs, err := s.Jobs(ctx, a.ID, "", 100)
	if err != nil || len(jobs.Items.([]Job)) != 1 {
		t.Fatal(jobs, err)
	}
}
func TestHTTPValidationAndPreview(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "one")
	h := s.Handler("secret")
	for _, tc := range []struct {
		method, path, body, token string
		status                    int
		code                      string
	}{
		{"GET", "/v1/accounts", "", "wrong", 401, "unauthorized"},
		{"POST", "/v1/accounts", `{"label":"x","password":"bad"}`, "secret", 400, "invalid_argument"},
		{"POST", "/v1/accounts", `null`, "secret", 400, "invalid_argument"},
		{"POST", "/v1/accounts", `{"label":"x"} {}`, "secret", 400, "invalid_argument"},
		{"GET", "/v1/accounts/unknown/friends/list", "", "secret", 404, "not_found"},
		{"GET", "/v1/accounts/" + a.ID + "/requests/list", "", "secret", 409, "reauth_required"},
		{"GET", "/v1/accounts?limit=-1", "", "secret", 400, "invalid_argument"},
		{"GET", "/v1/accounts/" + a.ID + "/events/list?cursor=garbage", "", "secret", 400, "invalid_argument"},
		{"POST", "/v1/accounts/" + a.ID + "/exclusions/replace", `{"recipients":[3]}`, "secret", 400, "invalid_argument"},
		{"POST", "/v1/accounts/" + a.ID + "/exclusions/replace", `{"recipients":[],"account_id":"other"}`, "secret", 400, "invalid_argument"},
	} {
		status, r := request(t, h, tc.method, tc.path, tc.body, tc.token)
		if status != tc.status || r.Error == nil || r.Error.Code != tc.code {
			t.Fatalf("%s %s: %d %+v", tc.method, tc.path, status, r)
		}
	}
	status, r := request(t, h, "POST", "/v1/accounts/"+a.ID+"/exclusions/replace", `{"recipients":["skip"]}`, "secret")
	if status != 200 {
		t.Fatal(r)
	}
	status, r = request(t, h, "POST", "/v1/accounts/"+a.ID+"/jobs/preview", `{"recipients":["skip","keep","keep"]}`, "secret")
	if status != 200 {
		t.Fatal(r)
	}
	data := r.Data.(map[string]any)
	if data["can_submit"] != false || !reflect.DeepEqual(data["eligible"], []any{"keep"}) {
		t.Fatal(data)
	}
	req := httptest.NewRequest("GET", "/v1/accounts", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestVaultAccountBinding(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
	v := Vault{Key: func() (string, error) { return key, nil }}
	sealed, err := v.Seal("one", []byte("credential"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("credential")) {
		t.Fatal("plaintext in sealed credential")
	}
	if _, err = v.Open("two", sealed); err == nil {
		t.Fatal("cross-account credential decrypted")
	}
	plain, err := v.Open("one", sealed)
	if err != nil || string(plain) != "credential" {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err = v.Open("one", sealed); err == nil {
		t.Fatal("tampering undetected")
	}
}
func TestMCPHTTPParity(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "one")
	if _, err := s.db.Exec("INSERT INTO deliveries VALUES(?,?,?,?,?,?)", a.ID, "replay", sendHash("conversation", "text", ""), "sent", []byte(`{"acknowledged":true}`), now()); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"snap", "story"} {
		conversation := "conversation"
		audience := ""
		if kind == "story" {
			conversation = myStoryID
			audience = "friends"
		}
		if _, err := s.db.Exec("INSERT INTO deliveries VALUES(?,?,?,?,?,?)", a.ID, "replay-"+kind, sendHash(conversation, "fixture-media", kind+":"+audience), "sent", []byte(`{"acknowledged":true}`), now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("INSERT INTO deliveries VALUES(?,?,?,?,?,?)", a.ID, "replay-story-override", sendHash(myStoryID, "fixture-media", "story:friends:ignore_exclusions"), "sent", []byte(`{"acknowledged":true}`), now()); err != nil {
		t.Fatal(err)
	}
	h := s.Handler("secret")
	httpServer := httptest.NewServer(h)
	defer httpServer.Close()
	server, err := MCPServer(httpServer.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != len(Operations()) {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		switch tool.Name {
		case "snapchat_snaps_send":
			if strings.HasPrefix(tool.Description, "Unavailable:") || !strings.Contains(tool.Description, "group TLS-only JPEG delivery are live-validated") || !strings.Contains(tool.Description, "Video snap delivery has not been live-tested") {
				t.Fatal("snap sending must describe validated direct/group images and untested video delivery")
			}
		case "snapchat_stories_publish":
			if !strings.Contains(tool.Description, "audience_not_verified") {
				t.Fatal("story publishing must document its per-request audience check")
			}
		}
	}
	for _, test := range []struct {
		name, method, path, body string
		args                     map[string]any
	}{
		{"snaps.send", "POST", "/v1/accounts/" + a.ID + "/snaps/send", `{"conversation_id":"conversation","media_id":"fixture-media","idempotency_key":"replay-snap"}`, map[string]any{"account_id": a.ID, "conversation_id": "conversation", "media_id": "fixture-media", "idempotency_key": "replay-snap"}},
		{"stories.publish", "POST", "/v1/accounts/" + a.ID + "/stories/publish", `{"media_id":"fixture-media","audience":"friends","idempotency_key":"replay-story"}`, map[string]any{"account_id": a.ID, "media_id": "fixture-media", "audience": "friends", "idempotency_key": "replay-story"}},
		{"stories.publish", "POST", "/v1/accounts/" + a.ID + "/stories/publish", `{"media_id":"fixture-media","audience":"friends","ignore_exclusions":true,"idempotency_key":"replay-story-override"}`, map[string]any{"account_id": a.ID, "media_id": "fixture-media", "audience": "friends", "ignore_exclusions": true, "idempotency_key": "replay-story-override"}},
		{"messages.send", "POST", "/v1/accounts/" + a.ID + "/messages/send", `{"conversation_id":"conversation","text":"text","idempotency_key":"replay"}`, map[string]any{"account_id": a.ID, "conversation_id": "conversation", "text": "text", "idempotency_key": "replay"}},
		{"messages.send", "POST", "/v1/accounts/" + a.ID + "/messages/send", `{"conversation_id":"conversation","text":"text","reply_to":"1","idempotency_key":"reply"}`, map[string]any{"account_id": a.ID, "conversation_id": "conversation", "text": "text", "reply_to": "1", "idempotency_key": "reply"}},
		{"profiles.get", "GET", "/v1/accounts/" + a.ID + "/profiles/get?recipient_id=11111111-1111-1111-1111-111111111111", "", map[string]any{"account_id": a.ID, "recipient_id": "11111111-1111-1111-1111-111111111111"}},
		{"stories.status", "GET", "/v1/accounts/" + a.ID + "/stories/status", "", map[string]any{"account_id": a.ID}},
		{"stories.get", "GET", "/v1/accounts/" + a.ID + "/stories/get?content_id=fixture", "", map[string]any{"account_id": a.ID, "content_id": "fixture"}},
		{"stories.published", "GET", "/v1/accounts/" + a.ID + "/stories/published", "", map[string]any{"account_id": a.ID}},
		{"stories.delete", "POST", "/v1/accounts/" + a.ID + "/stories/delete", `{"content_id":"owned-test-story"}`, map[string]any{"account_id": a.ID, "content_id": "owned-test-story"}},
		{"accounts.get", "GET", "/v1/accounts/" + a.ID, "", map[string]any{"account_id": a.ID}},
		{"requests.list", "GET", "/v1/accounts/" + a.ID + "/requests/list", "", map[string]any{"account_id": a.ID}},
		{"jobs.preview", "POST", "/v1/accounts/" + a.ID + "/jobs/preview", `{"recipients":["friend"]}`, map[string]any{"account_id": a.ID, "recipients": []string{"friend"}}},
	} {
		_, expected := request(t, h, test.method, test.path, test.body, "secret")
		got, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "snapchat_" + strings.ReplaceAll(test.name, ".", "_"), Arguments: test.args})
		if err != nil {
			t.Fatal(err)
		}
		var actual Result
		b, _ := json.Marshal(got.StructuredContent)
		if err = json.Unmarshal(b, &actual); err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(expected)
		if string(b) != string(want) {
			t.Fatalf("%s: %s != %s", test.name, b, want)
		}
		if test.name == "snaps.send" && actual.Error != nil {
			t.Fatal("acknowledged snap must replay through HTTP and MCP without sending")
		}
		if got.IsError != (actual.Error != nil) {
			t.Fatal("incorrect MCP error flag")
		}
	}
}
func TestEventPaginationAndCatalog(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "one")
	ctx := context.Background()
	if err := s.ReplaceExclusions(ctx, a.ID, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	page, err := s.Events(ctx, a.ID, 0, 1)
	if err != nil || page.NextCursor == "" {
		t.Fatal(page, err)
	}
	result := s.Call(ctx, "events.list", map[string]any{"account_id": a.ID, "cursor": page.NextCursor})
	if result.Error != nil {
		t.Fatal(result)
	}
	if len(result.Data.(Page).Items.([]Event)) != 1 {
		t.Fatal(result)
	}
	spec := OpenAPI()
	paths := spec["paths"].(map[string]any)
	for _, test := range []struct {
		name      string
		available bool
	}{
		{"stories/publish", true},
		{"stories/get", false},
		{"stories/status", true},
		{"profiles/get", true},
		{"requests/list", true},
		{"snaps/send", true},
	} {
		method := "get"
		if test.name == "stories/publish" || test.name == "snaps/send" {
			method = "post"
		}
		entry := paths["/v1/accounts/{account_id}/"+test.name].(map[string]any)[method].(map[string]any)
		if entry["x-standalone-available"] != test.available {
			t.Fatalf("incorrect availability for %s", test.name)
		}
		if !test.available && !strings.HasPrefix(entry["description"].(string), "Unavailable") {
			t.Fatalf("OpenAPI must clearly label %s unavailable", test.name)
		}
	}
	for _, o := range Operations() {
		if o.Account && o.Name != "accounts.get" {
			path := "/v1/accounts/{account_id}/" + strings.ReplaceAll(o.Name, ".", "/")
			if paths[path] == nil {
				t.Fatal(fmt.Sprint("missing ", o.Name))
			}
		}
	}
}
