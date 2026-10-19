package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/786F72/snapchat-mcp/internal/service"
)

// A browser bootstrap is a credential capture, not proof of standalone login.
// It never initializes or replaces any Snapchat device keys.
func runBootstrap(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("snapchat-mcp bootstrap", flag.ContinueOnError)
	f.SetOutput(out)
	dir := f.String("data-dir", serviceDir(), "private local service directory")
	account := f.String("account", "", "existing local account ID")
	endpoint := f.String("cdp", "http://127.0.0.1:9222", "logged-in local Chromium debugging endpoint")
	initKeyring := f.Bool("init-keyring", false, "explicitly provision OS keyring master key")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *account == "" || f.NArg() != 0 {
		return fmt.Errorf("bootstrap requires --account ACCOUNT_ID")
	}
	store, err := service.Open(*dir)
	if err != nil {
		return err
	}
	defer store.Close()
	if _, err = store.Account(ctx, *account); err != nil {
		return err
	}
	if *initKeyring {
		if err = service.ProvisionMasterKey(); err != nil {
			return fmt.Errorf("cannot provision credential vault: %w", err)
		}
	}
	vault := service.OSVault()
	if _, err = vault.Seal(*account, nil); err != nil {
		return fmt.Errorf("credential vault unavailable; unlock the OS keyring and use --init-keyring if needed: %w", err)
	}
	b, err := connectBrowser(ctx, *endpoint)
	if err != nil {
		return err
	}
	defer b.cancel()
	var snapshot struct {
		Version         int             `json:"version"`
		UserID          string          `json:"user_id"`
		UserAgent       string          `json:"user_agent"`
		AuthToken       string          `json:"auth_token"`
		IdentityStorage string          `json:"identity_storage"`
		RootWrappingKey json.RawMessage `json:"root_wrapping_key,omitempty"`
		CapturedAt      string          `json:"captured_at"`
		FriendsSyncedAt string          `json:"friends_synced_at,omitempty"`
		Cookies         json.RawMessage `json:"cookies"`
		Friends         []Friend        `json:"friends"`
	}
	const capture = `(async()=>{let r;webpackChunk_snapchat_web_calling_app.push([[crypto.randomUUID()],{},x=>r=x]);const s=r(68047).M.getState();const k=await r(38369).Qs().itemForUser(r(74918).Bh(s.auth.userId).id).get();const b=r(67526).fromByteArray;return {user_id:s.auth.userId,user_agent:navigator.userAgent,auth_token:s.auth.authToken?.token,identity_storage:localStorage.getItem('uds.e2eeIdentityKey.shared'),root_wrapping_key:k?{data:b(k.rwk.data),identity_key_id:b(k.keyIdentifier.data)}:undefined}})()`
	// No captured material is logged or returned on stdout.
	if err = b.eval(capture, &snapshot); err != nil {
		return fmt.Errorf("session capture failed; verify the browser is signed in")
	}
	if snapshot.UserID == "" || snapshot.AuthToken == "" || snapshot.IdentityStorage == "" {
		return fmt.Errorf("browser session is missing authentication or identity material")
	}
	if snapshot.Friends, err = b.Friends(); err != nil {
		return fmt.Errorf("could not capture confirmed friend metadata")
	}
	if err = b.cdp("Network.getCookies", map[string]any{"urls": []string{"https://www.snapchat.com/", "https://web.snapchat.com/", "https://accounts.snapchat.com/"}}, &snapshot.Cookies); err != nil {
		return fmt.Errorf("browser cookie capture failed")
	}
	previous, err := store.LoadSecret(ctx, vault, *account)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("cannot read previous account bootstrap")
	}
	if err == nil {
		var old struct {
			UserID string `json:"user_id"`
		}
		if json.Unmarshal(previous, &old) != nil || old.UserID != snapshot.UserID {
			return fmt.Errorf("this local account is already bound to a different Snapchat identity")
		}
	}
	snapshot.Version = 1
	snapshot.CapturedAt = time.Now().UTC().Format(time.RFC3339Nano)
	snapshot.FriendsSyncedAt = snapshot.CapturedAt
	material, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("cannot encode bootstrap")
	}
	if err = store.CaptureSession(ctx, vault, *account, material); err != nil {
		return fmt.Errorf("cannot persist encrypted bootstrap")
	}
	return json.NewEncoder(out).Encode(map[string]any{"account_id": *account, "bootstrap_captured": true, "root_wrapping_key_captured": len(snapshot.RootWrappingKey) > 0, "protocol_ready": false, "status": "protocol_blocked", "blocker": service.ProtocolBlocker})
}
