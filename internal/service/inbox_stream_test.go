package service

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func readInboxPage(t *testing.T, body io.Reader) (string, InboxPage) {
	t.Helper()
	reader := bufio.NewReader(body)
	var cursor string
	var page InboxPage
	found := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "id: ") {
			cursor = strings.TrimPrefix(line, "id: ")
		}
		if strings.HasPrefix(line, "data: ") {
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &page) != nil {
				t.Fatal("invalid SSE page")
			}
			found = true
		}
	}
	if !found || cursor == "" || cursor != page.NextCursor {
		t.Fatal("missing page checkpoint")
	}
	return cursor, page
}
func TestInboxStreamResumeAndAccountIsolation(t *testing.T) {
	s, a, b, _ := fixtureInbox(t)
	server := httptest.NewServer(s.Handler("secret"))
	defer server.Close()
	fetch := func(account, cursor string) *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), "GET", server.URL+"/v1/accounts/"+account+"/inbox/stream", nil)
		req.Header.Set("Authorization", "Bearer secret")
		if cursor != "" {
			req.Header.Set("Last-Event-ID", cursor)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	first := fetch(a.ID, "")
	if first.StatusCode != 200 || first.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(first.Status)
	}
	cursor, page := readInboxPage(t, first.Body)
	first.Body.Close()
	if len(page.Items) != 3 || page.MediaOpened || page.ReadReceipts {
		t.Fatal(page)
	}
	resumed := fetch(a.ID, cursor)
	_, page = readInboxPage(t, resumed.Body)
	resumed.Body.Close()
	if len(page.Items) != 0 {
		t.Fatal("checkpoint replayed acknowledged page")
	}
	for _, tc := range []struct{ account, cursor string }{{b.ID, cursor}, {a.ID, "invalid"}} {
		res := fetch(tc.account, tc.cursor)
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatal("invalid checkpoint accepted", res.Status)
		}
	}
	status, _ := request(t, s.Handler("secret"), "GET", "/v1/accounts/"+a.ID+"/inbox/stream", "", "wrong")
	if status != 401 {
		t.Fatal(status)
	}
}
