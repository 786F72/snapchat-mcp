package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func MCPServer(endpoint, token string) (*mcp.Server, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, fmt.Errorf("service URL must be a loopback HTTP origin")
	}
	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	server := mcp.NewServer(&mcp.Implementation{Name: "snapchat-mcp", Version: "0.1.0"}, nil)
	for _, op := range Operations() {
		openWorld := true
		switch op.Name {
		case "accounts.list", "accounts.create", "accounts.get", "accounts.verify_keys", "accounts.verify_session", "stories.published", "capabilities.list", "exclusions.list", "exclusions.replace", "events.list", "jobs.preview", "jobs.list", "jobs.get", "jobs.cancel":
			openWorld = false
		}
		description := op.Description
		if !op.Available && op.UnavailableReason != "" {
			description += " Unavailable: " + op.UnavailableReason
		}
		server.AddTool(&mcp.Tool{Name: "snapchat_" + strings.ReplaceAll(op.Name, ".", "_"), Description: description, InputSchema: op.Schema(), OutputSchema: ResultSchema(), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: !op.Mutation, OpenWorldHint: &openWorld}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			result := Result{}
			var in map[string]any
			if err := json.Unmarshal(request.Params.Arguments, &in); err != nil || in == nil {
				result.Error = fail("invalid_argument", "arguments must be an object", 400)
			} else if err = validate(op, in); err != nil {
				result.Error = publicError(err)
			} else {
				path := "/v1/accounts"
				method := op.Method()
				if op.Account {
					path += "/" + url.PathEscape(in["account_id"].(string))
					delete(in, "account_id")
					if op.Name != "accounts.get" {
						path += "/" + strings.ReplaceAll(op.Name, ".", "/")
					}
				}
				var body io.Reader
				if method == "GET" {
					q := url.Values{}
					for k, v := range in {
						q.Set(k, fmt.Sprint(v))
					}
					if len(q) > 0 {
						path += "?" + q.Encode()
					}
				} else {
					b, _ := json.Marshal(in)
					body = bytes.NewReader(b)
				}
				req, e := http.NewRequestWithContext(ctx, method, endpoint+path, body)
				if e != nil {
					result.Error = fail("service_unavailable", "cannot construct local request", 503)
				} else {
					req.Header.Set("Authorization", "Bearer "+token)
					req.Header.Set("Content-Type", "application/json")
					res, e := client.Do(req)
					if e != nil {
						result.Error = fail("service_unavailable", "local HTTP service unavailable", 503)
					} else {
						defer res.Body.Close()
						if e = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&result); e != nil {
							result.Error = fail("service_unavailable", "invalid service response", 503)
						}
					}
				}
			}
			b, _ := json.Marshal(result)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, StructuredContent: result, IsError: result.Error != nil}, nil
		})
	}
	return server, nil
}
