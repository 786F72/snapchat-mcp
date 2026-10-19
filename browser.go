package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

//go:embed bridge.js
var bridge string

type Friend struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}
type Browser struct {
	ctx         context.Context
	conn        net.Conn
	cancel      func()
	nextID      int
	evalTimeout time.Duration
}

func snapchatPage(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	if u.Hostname() == "web.snapchat.com" {
		return true
	}
	return (u.Hostname() == "www.snapchat.com" || u.Hostname() == "snapchat.com") && (u.Path == "/web" || strings.HasPrefix(u.Path, "/web/") || u.Path == "/add-friends" || u.Path == "/add-friends/")
}

func connectBrowser(parent context.Context, endpoint string) (*Browser, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
		return nil, errors.New("--cdp must be a local HTTP Chrome debugging endpoint")
	}
	listURL := *u
	listURL.Path = "/json/list"
	listURL.RawQuery = ""
	listURL.Fragment = ""
	req, err := http.NewRequestWithContext(parent, "GET", listURL.String(), nil)
	if err != nil {
		return nil, err
	}
	client := http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to Chrome: %w; see README.md", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("Chrome target listing failed")
	}
	var tabs []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
		WS   string `json:"webSocketDebuggerUrl"`
	}
	if err = json.NewDecoder(res.Body).Decode(&tabs); err != nil {
		return nil, err
	}
	var address string
	for _, tab := range tabs {
		if tab.Type == "page" && snapchatPage(tab.URL) {
			if address != "" {
				return nil, errors.New("multiple Snapchat tabs found; keep one open")
			}
			address = tab.WS
		}
	}
	if address == "" {
		return nil, errors.New("no Snapchat for Web tab found; open https://www.snapchat.com/web and sign in")
	}
	target, err := url.Parse(address)
	if err != nil || target.Scheme != "ws" || target.Host != u.Host {
		return nil, errors.New("Chrome supplied an unexpected debugger address")
	}
	dialCtx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	conn, reader, _, err := ws.DefaultDialer.Dial(dialCtx, address)
	if err != nil {
		return nil, fmt.Errorf("Chrome WebSocket connection failed: %w", err)
	}
	// There should be no frames before a command on a page debugger socket.
	if reader != nil && reader.Buffered() > 0 {
		conn.Close()
		return nil, errors.New("unexpected buffered data after debugger handshake")
	}
	closed := make(chan struct{})
	var once sync.Once
	b := &Browser{ctx: parent, conn: conn, cancel: func() { once.Do(func() { conn.Close(); close(closed) }) }}
	go func() {
		select {
		case <-parent.Done():
			b.cancel()
		case <-closed:
		}
	}()
	if err = b.eval(bridge, nil); err != nil {
		b.cancel()
		return nil, err
	}
	return b, nil
}

func (b *Browser) cdp(method string, params any, result any) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	timeout := b.evalTimeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if d, ok := b.ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := b.conn.SetDeadline(deadline); err != nil {
		return err
	}
	b.nextID++
	data, err := json.Marshal(map[string]any{"id": b.nextID, "method": method, "params": params})
	if err != nil {
		return err
	}
	if err = wsutil.WriteClientMessage(b.conn, ws.OpText, data); err != nil {
		return err
	}
	for {
		data, _, err = wsutil.ReadServerData(b.conn)
		if err != nil {
			return err
		}
		var response struct {
			ID    int `json:"id"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if err = json.Unmarshal(data, &response); err != nil {
			return err
		}
		if response.ID != b.nextID {
			continue
		}
		if response.Error != nil {
			return fmt.Errorf("Chrome command failed: %s", response.Error.Message)
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(response.Result, result)
	}
}
func (b *Browser) eval(expression string, result any) error {
	var response struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := b.cdp("Runtime.evaluate", map[string]any{"expression": expression, "awaitPromise": true, "returnByValue": true}, &response); err != nil {
		return err
	}
	if exception := response.Exception; exception != nil {
		message := exception.Text
		if exception.Exception != nil {
			message = strings.Split(exception.Exception.Description, "\n")[0]
		}
		return fmt.Errorf("Snapchat adapter: %s", message)
	}
	if result == nil {
		return nil
	}
	if len(response.Result.Value) == 0 {
		return errors.New("Snapchat adapter returned no result")
	}
	return json.Unmarshal(response.Result.Value, result)
}

func (b *Browser) Friends() ([]Friend, error) {
	var friends []Friend
	err := b.eval("globalThis.__qq_snap.friends()", &friends)
	return friends, err
}

func (b *Browser) Check(id string) (json.RawMessage, error) {
	arg, _ := json.Marshal(id)
	var response json.RawMessage
	err := b.eval("globalThis.__qq_snap.check("+string(arg)+")", &response)
	return response, err
}
func (b *Browser) Send(id, message string) error {
	args, _ := json.Marshal([]string{id, message})
	var response struct {
		OK bool `json:"ok"`
	}
	if err := b.eval("globalThis.__qq_snap.send(..."+string(args)+")", &response); err != nil {
		return err
	}
	if !response.OK {
		return errors.New("message was not acknowledged")
	}
	return nil
}
