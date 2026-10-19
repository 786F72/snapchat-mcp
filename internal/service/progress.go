package service

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"time"
)

// progressWriter accepts only our fixed diagnostic protocol. Native stderr is
// never copied into logs or API responses.
type progressWriter struct {
	store                          *Store
	account, key                   string
	pending                        []byte
	Submitted, SawTrace, Truncated bool
	Events                         []map[string]any
}

var rpcPath = regexp.MustCompile(`^/[A-Za-z0-9_./-]{1,255}$`)

func (w *progressWriter) Write(p []byte) (int, error) {
	size := len(p)
	if len(w.pending)+size > 65536 {
		w.Truncated = true
		w.pending = nil
		return size, nil
	}
	w.pending = append(w.pending, p...)
	for {
		index := bytes.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		line := w.pending[:index]
		w.pending = w.pending[index+1:]
		var value struct {
			Event string `json:"event"`
			Phase string `json:"phase"`
			Path  string `json:"path"`
		}
		if json.Unmarshal(line, &value) != nil || value.Event != "native_progress" {
			continue
		}
		switch value.Phase {
		case "runtime_loaded", "request", "send_start", "queued", "prepared", "submit":
		default:
			continue
		}
		if value.Path != "" && !rpcPath.MatchString(value.Path) {
			continue
		}
		w.SawTrace = true
		if value.Phase == "submit" {
			w.Submitted = true
		}
		data := map[string]any{"phase": value.Phase, "path": value.Path, "idempotency_key": w.key}
		w.Events = append(w.Events, data)
		body, _ := json.Marshal(data)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _ = w.store.db.ExecContext(ctx, "INSERT INTO events(account_id,type,data,created_at) VALUES(?,?,?,?)", w.account, "native.progress", body, now())
		cancel()
	}
	return size, nil
}
