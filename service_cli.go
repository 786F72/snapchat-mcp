package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/786F72/snapchat-mcp/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func serviceDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ".qq_snap-service"
	}
	return filepath.Join(dir, "qq_snap")
}
func tokenFile(path string, create bool) (string, error) {
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, err = f.WriteString(hex.EncodeToString(b[:]) + "\n")
			closeErr := f.Close()
			if err != nil {
				return "", err
			}
			if closeErr != nil {
				return "", closeErr
			}
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("token must be a regular file accessible only by its owner")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if len(token) != 64 {
		return "", fmt.Errorf("token file must contain a 32-byte hexadecimal token")
	}
	if _, err = hex.DecodeString(token); err != nil {
		return "", fmt.Errorf("invalid token encoding")
	}
	return token, nil
}
func runService(ctx context.Context, command string, args []string, out io.Writer) error {
	if command == "api-spec" {
		if len(args) != 0 {
			return fmt.Errorf("api-spec takes no arguments")
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(service.OpenAPI())
	}
	f := flag.NewFlagSet("snapchat-mcp "+command, flag.ContinueOnError)
	f.SetOutput(out)
	dir := f.String("data-dir", serviceDir(), "private local service directory")
	addr := f.String("listen", "127.0.0.1:8787", "loopback HTTP address (serve)")
	endpoint := f.String("url", "http://127.0.0.1:8787", "local service origin (mcp)")
	tokenPath := f.String("token-file", "", "private bearer token file; default DATA_DIR/token")
	provision := f.Bool("init-keyring", false, "explicitly provision OS keyring master key (serve)")
	accountID := f.String("account", "", "target account ID (import-exclusions)")
	testText := f.String("text", "hey, testing my Snapchat connection 👋", "single validation message (protocol-test-send)")
	testKey := f.String("idempotency-key", "standalone-validation-2026-10-19", "stable validation key (protocol-test-send)")
	includeUnavailable := f.Bool("include-unavailable", false, "advertise unimplemented MCP tools as well as available tools (mcp)")
	mediaRoots := []string{}
	f.Func("media-root", "absolute allowed local media directory; repeat for multiple roots (serve)", func(value string) error { mediaRoots = append(mediaRoots, value); return nil })
	probeKind := f.String("probe", "keys", "protocol probe: keys, session, feed, native-feed, or messages")
	prepareMediaID := f.String("media-id", "", "uploaded media handle (protocol-prepare-send)")
	prepareAudience := f.String("audience", "friends", "story audience: friends or saved custom audience")
	prepareStory := f.Bool("story", false, "prepare a My Story publication without publishing")
	probeCursor := f.String("cursor", "", "pagination cursor (protocol-check)")
	probeLimit := f.Int("limit", 40, "page size (protocol-check)")
	importFile := f.String("file", "", "JSON array of excluded recipient IDs (import-exclusions)")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if command == "import-exclusions" {
		if *accountID == "" || *importFile == "" {
			return fmt.Errorf("import-exclusions requires --account and --file")
		}
		store, err := service.Open(*dir)
		if err != nil {
			return err
		}
		defer store.Close()
		if _, err = store.Account(ctx, *accountID); err != nil {
			return err
		}
		f, err := os.Open(*importFile)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		if err != nil {
			return err
		}
		if len(b) > 1<<20 {
			return fmt.Errorf("import exceeds 1 MiB")
		}
		var ids []string
		if err = json.Unmarshal(b, &ids); err != nil || ids == nil {
			return fmt.Errorf("file must contain a JSON array of recipient IDs")
		}
		existing, err := store.Exclusions(ctx, *accountID)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, id := range append(existing, ids...) {
			seen[id] = true
		}
		merged := make([]string, 0, len(seen))
		for id := range seen {
			merged = append(merged, id)
		}
		sort.Strings(merged)
		if err = store.ReplaceExclusions(ctx, *accountID, merged); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"account_id": *accountID, "excluded": len(merged)})
	}
	if *tokenPath == "" {
		*tokenPath = filepath.Join(*dir, "token")
	}
	if command == "protocol-prepare-send" {
		if *accountID == "" || (!*prepareStory && *importFile == "") {
			return fmt.Errorf("protocol-prepare-send requires --account and --file CONVERSATION_ID (or --story)")
		}
		store, err := service.Open(*dir)
		if err != nil {
			return err
		}
		defer store.Close()
		var data any
		if *prepareStory || *prepareMediaID != "" {
			op := "snaps.prepare"
			args := map[string]any{"account_id": *accountID, "media_id": *prepareMediaID}
			if *prepareStory {
				op = "stories.prepare"
				args["audience"] = *prepareAudience
			} else {
				args["conversation_id"] = *importFile
			}
			r := store.Call(ctx, op, args)
			data = r.Data
			if r.Error != nil {
				err = r.Error
			}
		} else {
			data, err = store.PrepareText(ctx, *accountID, *importFile, *testText)
		}
		result := service.Result{Data: data}
		if err != nil {
			if e, ok := err.(*service.Error); ok {
				result.Error = e
			} else {
				return err
			}
		}
		if e := json.NewEncoder(out).Encode(result); e != nil {
			return e
		}
		return err
	}
	if command == "protocol-test-send" {
		if *accountID == "" {
			return fmt.Errorf("protocol-test-send requires --account")
		}
		store, err := service.Open(*dir)
		if err != nil {
			return err
		}
		defer store.Close()
		data, err := store.TestSend(ctx, *accountID, *testText, *testKey)
		result := service.Result{Data: data}
		if err != nil {
			if e, ok := err.(*service.Error); ok {
				result.Error = e
			} else {
				return err
			}
		}
		if e := json.NewEncoder(out).Encode(result); e != nil {
			return e
		}
		return err
	}
	if command == "protocol-check" {
		if *accountID == "" {
			return fmt.Errorf("protocol-check requires --account ACCOUNT_ID")
		}
		store, err := service.Open(*dir)
		if err != nil {
			return err
		}
		defer store.Close()
		op := "accounts.verify_keys"
		if *probeKind == "messages" {
			op = "messages.list"
		}
		if *probeKind == "feed" {
			op = "conversations.list"
		}
		if *probeKind == "session" {
			op = "accounts.verify_session"
		}
		input := map[string]any{"account_id": *accountID}
		if op == "conversations.list" {
			input["limit"] = float64(*probeLimit)
			input["cursor"] = *probeCursor
		}
		if op == "messages.list" {
			input["conversation_id"] = *importFile
			input["limit"] = float64(*probeLimit)
		}
		var result service.Result
		if *probeKind == "native-feed" {
			result.Data, err = store.NativeFeed(ctx, *accountID)
			if e, ok := err.(*service.Error); ok {
				result.Error = e
			} else if err != nil {
				return err
			}
		} else {
			result = store.Call(ctx, op, input)
		}
		if err = json.NewEncoder(out).Encode(result); err != nil {
			return err
		}
		if result.Error != nil {
			return result.Error
		}
		return nil
	}
	if command == "protocol-assets" {
		if err := service.InstallRuntime(ctx, filepath.Join(*dir, "runtime")); err != nil {
			return err
		}
		fmt.Fprintln(out, "Pinned messaging runtime installed; consult account capabilities before use.")
		return nil
	}
	if command == "mcp" {
		token, err := tokenFile(*tokenPath, false)
		if err != nil {
			return err
		}
		server, err := service.MCPServer(*endpoint, token, service.MCPOptions{IncludeUnavailable: *includeUnavailable})
		if err != nil {
			return err
		}
		return server.Run(ctx, &mcp.StdioTransport{})
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--listen must use a literal loopback IP")
	}
	store, err := service.Open(*dir)
	if err != nil {
		return err
	}
	defer store.Close()
	token, err := tokenFile(*tokenPath, true)
	if err != nil {
		return err
	}
	if *provision {
		if err = service.ProvisionMasterKey(); err != nil {
			return err
		}
	}
	if err = store.ConfigureMediaRoots(mediaRoots); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: store.Handler(token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-done:
		}
	}()
	fmt.Fprintf(out, "snapchat-mcp service listening at http://%s; standalone API (see capabilities); token file: %s\n", listener.Addr(), *tokenPath)
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
