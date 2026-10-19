package service

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// Stream whole pages so a reconnect cannot checkpoint past a partially delivered page.
func (s *Store) streamInbox(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(p) != 5 || p[0] != "v1" || p[1] != "accounts" || p[3] != "inbox" || p[4] != "stream" {
		JSON(w, 404, Result{Error: fail("not_found", "unknown route", 404)})
		return
	}
	for name, values := range r.URL.Query() {
		if name != "cursor" || len(values) != 1 {
			JSON(w, 400, Result{Error: fail("invalid_argument", "only one cursor query parameter is supported", 400)})
			return
		}
	}
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("cursor")
	}
	if len(cursor) > 4096 {
		JSON(w, 400, Result{Error: fail("invalid_cursor", "invalid inbox checkpoint", 400)})
		return
	}
	if _, err := s.Account(r.Context(), p[2]); err != nil {
		e := publicError(err)
		JSON(w, e.Status, Result{Error: e})
		return
	}
	f, ok := w.(http.Flusher)
	if !ok {
		JSON(w, 500, Result{Error: fail("internal_error", "streaming unavailable", 500)})
		return
	}
	page, err := s.PollInbox(r.Context(), p[2], cursor, 40, 3)
	if err != nil {
		e := publicError(err)
		JSON(w, e.Status, Result{Error: e})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	for {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
		body, _ := json.Marshal(page)
		if _, err = io.WriteString(w, "id: "+page.NextCursor+"\nevent: inbox.page\ndata: "+string(body)+"\n\n"); err != nil {
			return
		}
		f.Flush()
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		cursor = page.NextCursor
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		page, err = s.PollInbox(r.Context(), p[2], cursor, 40, 3)
		if err != nil {
			body, _ = json.Marshal(map[string]string{"code": publicError(err).Code})
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
			_, _ = io.WriteString(w, "event: inbox.error\ndata: "+string(body)+"\n\n")
			f.Flush()
			return
		}
	}
}
