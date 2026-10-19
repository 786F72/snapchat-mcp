package service

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Store) Handler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			JSON(w, 401, Result{Error: fail("unauthorized", "bearer token required", 401)})
			return
		}
		if r.Header.Get("Origin") != "" {
			JSON(w, 403, Result{Error: fail("forbidden", "browser origin requests are disabled", 403)})
			return
		}
		if r.Method == "GET" && r.URL.Path == "/v1/health" {
			JSON(w, 200, Result{Data: map[string]any{"service": "ok", "protocol_ready": false}})
			return
		}
		if r.Method == "GET" && r.URL.Path == "/v1/openapi.json" {
			JSON(w, 200, OpenAPI())
			return
		}
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/events/stream") {
			s.stream(w, r)
			return
		}
		name, account := route(r.URL.Path, r.Method)
		if name == "" {
			JSON(w, 404, Result{Error: fail("not_found", "unknown route", 404)})
			return
		}
		in := map[string]any{}
		if r.Method == "GET" {
			for k, v := range r.URL.Query() {
				if len(v) != 1 {
					JSON(w, 400, Result{Error: fail("invalid_argument", "duplicate query parameter", 400)})
					return
				}
				if k == "limit" {
					n, err := strconv.Atoi(v[0])
					if err != nil {
						JSON(w, 400, Result{Error: fail("invalid_argument", "limit must be an integer", 400)})
						return
					}
					in[k] = float64(n)
				} else if k == "match_case" && name == "messages.search" {
					if v[0] != "true" && v[0] != "false" {
						JSON(w, 400, Result{Error: fail("invalid_argument", "match_case must be true or false", 400)})
						return
					}
					in[k] = v[0] == "true"
				} else {
					in[k] = v[0]
				}
			}
		} else {
			bodyLimit := int64(1 << 20)
			if name == "media.upload" {
				bodyLimit = 24 << 20
			}
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, bodyLimit))
			if err := d.Decode(&in); err != nil || in == nil {
				JSON(w, 400, Result{Error: fail("invalid_argument", "body must be a JSON object under 1 MiB", 400)})
				return
			}
			var extra any
			if err := d.Decode(&extra); err != io.EOF {
				JSON(w, 400, Result{Error: fail("invalid_argument", "unexpected trailing JSON", 400)})
				return
			}
		}
		if account != "" {
			if _, ok := in["account_id"]; ok {
				JSON(w, 400, Result{Error: fail("invalid_argument", "account_id belongs in the URL", 400)})
				return
			}
			in["account_id"] = account
		}
		result := s.Call(r.Context(), name, in)
		status := 200
		if result.Error != nil {
			status = result.Error.Status
		}
		JSON(w, status, result)
	})
}
func route(path, method string) (string, string) {
	if path == "/v1/accounts" {
		if method == "GET" {
			return "accounts.list", ""
		}
		if method == "POST" {
			return "accounts.create", ""
		}
		return "", ""
	}
	p := strings.Split(strings.Trim(path, "/"), "/")
	if len(p) < 3 || p[0] != "v1" || p[1] != "accounts" {
		return "", ""
	}
	if len(p) == 3 && method == "GET" {
		return "accounts.get", p[2]
	}
	if len(p) == 5 {
		n := p[3] + "." + p[4]
		for _, op := range Operations() {
			if op.Account && op.Name == n && method == op.Method() {
				return n, p[2]
			}
		}
	}
	return "", ""
}
func (s *Store) stream(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(p) != 5 || p[0] != "v1" || p[1] != "accounts" || p[3] != "events" || p[4] != "stream" {
		JSON(w, 404, Result{Error: fail("not_found", "unknown route", 404)})
		return
	}
	account := p[2]
	if _, err := s.Account(r.Context(), account); err != nil {
		e := publicError(err)
		JSON(w, e.Status, Result{Error: e})
		return
	}
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("cursor")
	}
	var cursor int64
	if raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			JSON(w, 400, Result{Error: fail("invalid_argument", "invalid event cursor", 400)})
			return
		}
		cursor = n
	}
	f, ok := w.(http.Flusher)
	if !ok {
		JSON(w, 500, Result{Error: fail("internal_error", "streaming unavailable", 500)})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	f.Flush()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		page, err := s.Events(r.Context(), account, cursor, 100)
		if err != nil {
			return
		}
		for _, e := range page.Items.([]Event) {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
			b, _ := json.Marshal(e)
			if _, err = io.WriteString(w, "id: "+strconv.FormatInt(e.Cursor, 10)+"\nevent: "+e.Type+"\ndata: "+string(b)+"\n\n"); err != nil {
				return
			}
			cursor = e.Cursor
		}
		f.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
func OpenAPI() map[string]any {
	paths := map[string]any{}
	for _, op := range Operations() {
		path := "/v1/accounts"
		method := strings.ToLower(op.Method())
		if op.Account {
			path += "/{account_id}"
			if op.Name != "accounts.get" {
				path += "/" + strings.ReplaceAll(op.Name, ".", "/")
			}
		}
		schema := op.Schema()
		props := schema["properties"].(map[string]any)
		delete(props, "account_id")
		required := []string{}
		for _, k := range schema["required"].([]string) {
			if k != "account_id" {
				required = append(required, k)
			}
		}
		schema["required"] = required
		params := []any{}
		if op.Account {
			params = append(params, map[string]any{"name": "account_id", "in": "path", "required": true, "schema": prop("string")})
		}
		entry := map[string]any{"operationId": op.Name, "description": op.Description, "parameters": params, "responses": map[string]any{"200": map[string]any{"description": "Structured operation result"}, "400": map[string]any{"description": "Invalid arguments"}, "401": map[string]any{"description": "Authentication required"}, "501": map[string]any{"description": "Standalone capability unavailable"}}}
		entry["x-standalone-available"] = op.Available
		if op.UnavailableReason != "" {
			entry["x-unavailable-reason"] = op.UnavailableReason
		}
		for code, description := range map[string]string{"403": "Recipient excluded or origin forbidden", "404": "Account or resource not found", "409": "Conflict, uncertain delivery, authentication, or audience verification required", "429": "Upstream rate limit", "502": "Upstream protocol or transport failure", "503": "Local vault or runtime unavailable"} {
			entry["responses"].(map[string]any)[code] = map[string]any{"description": description}
		}
		for _, response := range entry["responses"].(map[string]any) {
			response.(map[string]any)["content"] = map[string]any{"application/json": map[string]any{"schema": ResultSchema()}}
		}
		if method == "post" {
			entry["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}
		} else {
			keys := make([]string, 0, len(props))
			for k := range props {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				v := props[k]
				params = append(params, map[string]any{"name": k, "in": "query", "required": contains(required, k), "schema": v})
			}
			entry["parameters"] = params
		}
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		item[method] = entry
	}
	paths["/v1/health"] = map[string]any{"get": map[string]any{"responses": map[string]any{"200": map[string]any{"description": "Service health; not Snapchat readiness"}}}}
	paths["/v1/accounts/{account_id}/events/stream"] = map[string]any{"get": map[string]any{"parameters": []any{map[string]any{"name": "account_id", "in": "path", "required": true, "schema": prop("string")}, map[string]any{"name": "cursor", "in": "query", "schema": prop("string")}}, "responses": map[string]any{"200": map[string]any{"description": "Account-scoped local events; resume with Last-Event-ID", "content": map[string]any{"text/event-stream": map[string]any{}}}}}}
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "Snapchat MCP local API", "version": Version}, "paths": paths, "security": []any{map[string]any{"bearerAuth": []string{}}}, "components": map[string]any{"securitySchemes": map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer"}}}}
}
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
