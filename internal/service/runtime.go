package service

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

//go:embed probe.mjs
var probeScript string

//go:embed client.mjs
var clientScript string

//go:embed packet.mjs
var packetScript string

//go:embed media_upload.mjs
var mediaUploadScript string

//go:embed inbox_message.mjs
var inboxMessageScript string

//go:embed history_message.mjs
var historyMessageScript string

func nativeClientSource() string {
	return packetScript + "\n" + mediaUploadScript + "\n" + inboxMessageScript + "\n" + historyMessageScript + "\n" + clientScript
}

// Keep native source and credentials off argv. In particular, Windows limits
// command lines to roughly 32 KiB, below the combined embedded adapter size.
func nativeCommand(ctx context.Context, source, dir string, material []byte) *exec.Cmd {
	const loader = `let raw='';try{for await(const chunk of process.stdin){raw+=chunk;if(raw.length>4*1024*1024)throw Error()}const input=JSON.parse(raw);globalThis.__qqSnapshot=JSON.parse(input.snapshot);await import('data:text/javascript;base64,'+Buffer.from(input.source).toString('base64'))}catch{console.log(JSON.stringify({ready:false,stage:'bootstrap',error:'Native bootstrap failed.'}));process.exit(1)}`
	payload, _ := json.Marshal(struct {
		Source   string `json:"source"`
		Snapshot string `json:"snapshot"`
	}{source, string(material)})
	cmd := exec.CommandContext(ctx, "node", "--input-type=module", "--eval", loader, dir)
	cmd.Stdin = bytes.NewReader(payload)
	return cmd
}

func (s *Store) ProbeKeys(ctx context.Context, account string) (any, error) {
	material, err := s.LoadSecret(ctx, s.vault, account)
	if err != nil {
		return nil, fail("reauth_required", "capture this account with snapchat-mcp bootstrap and unlock the OS keyring", 409)
	}
	dir := s.runtimeDir
	for _, name := range []string{"runtime.js", "messaging.wasm"} {
		if _, err = os.Stat(filepath.Join(dir, name)); err != nil {
			return nil, fail("runtime_unavailable", "install pinned WASM assets with snapchat-mcp protocol-assets", 409)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--input-type=module", "--eval", probeScript, dir)
	cmd.Stdin = bytes.NewReader(material)
	// Native diagnostics must never be returned: they can contain session state.
	var output bytes.Buffer
	cmd.Stdout = &cappedOutput{buffer: &output, remaining: 65536}
	err = cmd.Run()
	var result map[string]any
	if json.Unmarshal(output.Bytes(), &result) != nil {
		return nil, fail("runtime_unavailable", "Node runtime failed to produce a probe result", 503)
	}
	if err != nil {
		e := fail("key_restore_failed", "offline key restoration failed without enabling network or registration", 409)
		e.Details = result
		return nil, e
	}
	return result, nil
}

type cappedOutput struct {
	buffer    *bytes.Buffer
	remaining int
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, fmt.Errorf("runtime output exceeded limit")
	}
	n, err := w.buffer.Write(p)
	w.remaining -= n
	return n, err
}

// InstallRuntime downloads only immutable, pinned public assets; it reads no
// account credentials. Node fetch transparently decompresses CDN Brotli bodies.
func InstallRuntime(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	const script = `import fs from 'node:fs';import path from 'node:path';import{createHash}from'node:crypto';const root=process.argv[1];const assets=[['worker.js','https://cf-st.sc-cdn.net/dw/7b29a483d11eef27ff35.chunk.js','1a4d9ae058b3dc7c8dd0661c8c120c791196ac7f4c78bc48fc1a6781c8365cb9'],['runtime.js','https://cf-st.sc-cdn.net/dw/10edf76a4d87e4231fe2.chunk.js','b2f2d21f6e1e4214f2a47acb65a21a3ca950c8a7ab22a94ec16934db8cff131c'],['messaging.wasm','https://cf-st.sc-cdn.net/dw/903641c0ba985b2dcd13.wasm','2ce913a96d256605ea3b9998e71a65ee93b4f736fa4289d27490ed7fa5a95cd5']];for(const[name,url,pin]of assets){const res=await fetch(url);if(!res.ok)throw Error('asset download failed');const reader=res.body.getReader(),chunks=[];let size=0;for(;;){const{done,value}=await reader.read();if(done)break;size+=value.length;if(size>32*1024*1024){await reader.cancel();throw Error('asset exceeds size limit')}chunks.push(value)}const data=Buffer.concat(chunks);if(createHash('sha256').update(data).digest('hex')!==pin)throw Error('asset hash mismatch');const target=path.join(root,name),temp=target+'.tmp-'+process.pid;try{fs.writeFileSync(temp,data,{mode:0o600,flag:'wx'});fs.renameSync(temp,target)}finally{try{fs.unlinkSync(temp)}catch{}}}`
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--input-type=module", "--eval", script, dir)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pinned runtime installation failed; Node 24+ and network access are required")
	}
	return nil
}

func (s *Store) ProbeSession(ctx context.Context, account string) (any, error) {
	material, err := s.LoadSecret(ctx, s.vault, account)
	if err != nil {
		return nil, fail("reauth_required", "capture this account with snapchat-mcp bootstrap", 409)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := nativeCommand(ctx, nativeClientSource(), s.runtimeDir, material)
	var output bytes.Buffer
	cmd.Stdout = &cappedOutput{buffer: &output, remaining: 65536}
	err = cmd.Run()
	var result map[string]any
	if json.Unmarshal(output.Bytes(), &result) != nil {
		return nil, fail("runtime_unavailable", "native runtime produced no result", 503)
	}
	if err != nil {
		e := fail("session_init_failed", "native session initialization failed", 409)
		e.Details = result
		return nil, e
	}
	return result, nil
}

func (s *Store) NativeFeed(ctx context.Context, account string) (any, error) {
	material, err := s.LoadSecret(ctx, s.vault, account)
	if err != nil {
		return nil, fail("reauth_required", "capture this account with snapchat-mcp bootstrap", 409)
	}
	var input map[string]any
	if json.Unmarshal(material, &input) != nil {
		return nil, fail("invalid_session", "invalid session", 409)
	}
	input["operation"] = "feed"
	material, _ = json.Marshal(input)
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := nativeCommand(ctx, nativeClientSource(), s.runtimeDir, material)
	var output bytes.Buffer
	cmd.Stdout = &cappedOutput{buffer: &output, remaining: 6 << 20}
	err = cmd.Run()
	var result map[string]any
	if json.Unmarshal(output.Bytes(), &result) != nil {
		return nil, fail("runtime_unavailable", "native sync produced no result", 503)
	}
	if encoded, ok := result["sync_frame"].(string); ok {
		delete(result, "sync_frame")
		items, decodeErr := syncMetadata(encoded)
		if decodeErr != nil {
			return nil, fail("incompatible_protocol", "could not decode native conversation metadata", 502)
		}
		for _, c := range items {
			data, _ := json.Marshal(c)
			if _, err = s.db.ExecContext(ctx, "INSERT INTO conversations VALUES(?,?,?,?) ON CONFLICT(account_id,id) DO UPDATE SET data=excluded.data,updated_at=excluded.updated_at", account, c.ID, data, now()); err != nil {
				return nil, err
			}
		}
		excluded, readErr := s.Exclusions(ctx, account)
		if readErr != nil {
			return nil, readErr
		}
		blocked := map[string]bool{}
		for _, id := range excluded {
			blocked[id] = true
		}
		filtered := []Conversation{}
		for _, c := range items {
			skip := false
			for _, id := range c.Participants {
				if blocked[id] {
					skip = true
				}
			}
			if !skip {
				filtered = append(filtered, c)
			}
		}
		return map[string]any{"items": filtered, "partial": true, "backend": "standalone", "rpc_agent": result["rpc_agent"]}, nil
	}
	if err != nil {
		e := fail("sync_failed", "native feed synchronization failed", 502)
		e.Details = result
		return nil, e
	}
	return result, nil
}

func (s *Store) NativeMessages(ctx context.Context, account, conversationID, cursor string, limit int) (any, error) {
	return s.MessageHistory(ctx, account, conversationID, cursor, limit)
}

func (s *Store) nativeMessages(ctx context.Context, account, conversationID, cursor string, limit int, operation string, serverBefore ...string) (any, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM conversations WHERE account_id=? AND id=?", account, conversationID).Scan(&raw)
	if err != nil {
		return nil, fail("not_found", "synchronize this conversation before fetching its messages", 404)
	}
	var conversation Conversation
	if json.Unmarshal(raw, &conversation) != nil {
		return nil, fail("internal_error", "invalid cached conversation", 500)
	}
	excluded, err := s.Exclusions(ctx, account)
	if err != nil {
		return nil, err
	}
	for _, id := range excluded {
		for _, p := range conversation.Participants {
			if p == id {
				return nil, fail("excluded_recipient", "conversation includes an excluded recipient", 403)
			}
		}
	}
	material, err := s.LoadSecret(ctx, s.vault, account)
	if err != nil {
		return nil, fail("reauth_required", "capture this account and unlock the OS keyring", 409)
	}
	var input map[string]any
	if json.Unmarshal(material, &input) != nil {
		return nil, fail("invalid_session", "invalid captured session", 409)
	}
	input["excluded_ids"] = excluded
	input["operation"] = operation
	input["conversation_type"] = conversation.Type
	input["conversation_version"] = conversation.Version
	input["conversation_id"] = conversationID
	input["cursor"] = cursor
	if len(serverBefore) > 0 {
		input["before_server"] = serverBefore[0]
	}
	input["limit"] = limit
	material, _ = json.Marshal(input)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := nativeCommand(ctx, nativeClientSource(), s.runtimeDir, material)
	var output bytes.Buffer
	cmd.Stdout = &cappedOutput{buffer: &output, remaining: 1 << 20}
	err = cmd.Run()
	var result map[string]any
	if json.Unmarshal(output.Bytes(), &result) != nil {
		return nil, fail("runtime_unavailable", "native message fetch produced no result", 503)
	}
	if err != nil {
		e := fail("sync_failed", "native message fetch failed", 502)
		e.Details = result
		return nil, e
	}
	return result, nil
}
