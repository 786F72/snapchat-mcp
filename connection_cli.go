package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/786F72/snapchat-mcp/internal/service"
)

func connectionAccount(ctx context.Context, store *service.Store, id, label string, create bool) (service.Account, error) {
	if id != "" {
		if label != "" {
			return service.Account{}, fmt.Errorf("use --account or --label, not both")
		}
		return store.Account(ctx, id)
	}
	page, err := store.Accounts(ctx, "", 500)
	if err != nil {
		return service.Account{}, err
	}
	var matches []service.Account
	for _, account := range page.Items.([]service.Account) {
		if label == "" || account.Label == label {
			matches = append(matches, account)
		}
	}
	if len(matches) == 1 && page.NextCursor == "" {
		return matches[0], nil
	}
	if len(matches) > 1 || page.NextCursor != "" {
		return service.Account{}, fmt.Errorf("multiple local accounts; select one with --account ACCOUNT_ID")
	}
	if !create {
		return service.Account{}, fmt.Errorf("no matching local account; run snapchat-mcp connect first")
	}
	if label == "" {
		label = "personal"
	}
	return store.CreateAccount(ctx, label)
}

func runConnection(ctx context.Context, command string, args []string, out io.Writer) error {
	f := flag.NewFlagSet("snapchat-mcp "+command, flag.ContinueOnError)
	f.SetOutput(out)
	dir := f.String("data-dir", serviceDir(), "private local service directory")
	id := f.String("account", "", "local account ID; optional when only one account exists")
	label := f.String("label", "", "select an exact local account label; connect creates it if missing")
	verify := f.Bool("verify", command == "connect" || command == "refresh", "verify the captured session against Snapchat without sending messages")
	var cdp *string
	var initKeyring *bool
	if command == "connect" {
		cdp = f.String("cdp", "http://127.0.0.1:9222", "signed-in local Chromium debugging endpoint")
		initKeyring = f.Bool("init-keyring", true, "provision an OS keyring master key if missing")
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	store, err := service.Open(*dir)
	if err != nil {
		return err
	}
	defer store.Close()
	// Diagnose browser access before creating an account or provisioning a vault.
	if command == "connect" {
		browser, err := connectBrowser(ctx, *cdp)
		if err != nil {
			return fmt.Errorf("open a dedicated Chrome profile with --remote-debugging-port=9222, sign in to Snapchat, and keep one Snapchat tab open: %w", err)
		}
		browser.cancel()
	}
	account, err := connectionAccount(ctx, store, *id, *label, command == "connect")
	if err != nil {
		return err
	}
	if command == "doctor" {
		if *verify {
			return fmt.Errorf("doctor is local-only; use status --verify for live verification")
		}
		diagnostics, err := store.Diagnostics(ctx, account.ID)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(out).Encode(service.Result{Data: diagnostics}); err != nil {
			return err
		}
		if !diagnostics.ReadyForVerification {
			return fmt.Errorf("local connection prerequisites are incomplete; inspect checks")
		}
		return nil
	}
	if command == "refresh" {
		if _, err := store.Refresh(ctx, account.ID); err != nil {
			status, statusErr := store.SessionStatus(ctx, account.ID)
			if statusErr != nil {
				return err
			}
			return writeConnectionResult(out, status, err)
		}
	}
	if command == "connect" {
		bootstrapArgs := []string{"--data-dir", *dir, "--account", account.ID, "--cdp", *cdp}
		if *initKeyring {
			bootstrapArgs = append(bootstrapArgs, "--init-keyring")
		}
		if err = runBootstrap(ctx, bootstrapArgs, &bytes.Buffer{}); err != nil {
			return err
		}
	}
	status, err := store.SessionStatus(ctx, account.ID)
	if err != nil {
		return err
	}
	if *verify && command == "connect" && !status.RuntimeInstalled {
		if err = service.InstallRuntime(ctx, filepath.Join(*dir, "runtime")); err != nil {
			return writeConnectionResult(out, status, err)
		}
		status, err = store.SessionStatus(ctx, account.ID)
		if err != nil {
			return err
		}
	}
	if *verify {
		var result any
		result, err = store.ProbeSession(ctx, account.ID)
		if err == nil {
			ready, ok := result.(map[string]any)
			if !ok || ready["ready"] != true {
				err = fmt.Errorf("session verification did not confirm readiness")
			}
		}
		if err == nil {
			status.RemoteVerified = true
			status.State = "connected"
			status.NextAction = "none"
		}
	}
	return writeConnectionResult(out, status, err)
}

func writeConnectionResult(out io.Writer, status service.SessionStatus, operationErr error) error {
	result := service.Result{Data: status}
	if operationErr != nil {
		var public *service.Error
		if errors.As(operationErr, &public) {
			result.Error = public
			switch public.Code {
			case "reauth_required", "invalid_session", "identity_mismatch":
				status.NextAction = "connect"
			case "runtime_unavailable":
				status.NextAction = "install_runtime"
			}
		} else {
			result.Error = &service.Error{Code: "connection_failed", Message: operationErr.Error(), Status: 503}
		}
	}
	result.Data = status
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return err
	}
	return operationErr
}
