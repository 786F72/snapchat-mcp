package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/786F72/snapchat-mcp/internal/service"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "snapchat-mcp:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version") {
		if len(args) != 1 {
			return fmt.Errorf("version takes no arguments")
		}
		fmt.Fprintln(out, "snapchat-mcp", service.Version)
		return nil
	}
	if len(args) > 0 && (args[0] == "connect" || args[0] == "status" || args[0] == "doctor" || args[0] == "refresh") {
		return runConnection(ctx, args[0], args[1:], out)
	}
	if len(args) > 0 && args[0] == "bootstrap" {
		return runBootstrap(ctx, args[1:], out)
	}
	if len(args) > 0 && (args[0] == "serve" || args[0] == "mcp" || args[0] == "import-exclusions" || args[0] == "api-spec" || args[0] == "protocol-assets" || args[0] == "protocol-check" || args[0] == "protocol-test-send" || args[0] == "protocol-prepare-send") {
		return runService(ctx, args[0], args[1:], out)
	}
	command := "send"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	if command != "inspect" && command != "friends" && command != "send" && command != "check" && command != "eval" {
		return errors.New("use connect, status, doctor, refresh, version, serve, mcp, bootstrap, api-spec, import-exclusions, inspect, friends, check, eval, or send; see README.md")
	}
	f := flag.NewFlagSet("snapchat-mcp "+command, flag.ContinueOnError)
	f.SetOutput(out)
	cdp := f.String("cdp", "http://127.0.0.1:9222", "local Chrome debugging endpoint")
	har := f.String("har", "", "HAR file to inspect offline")
	output := f.String("output", "", "write JSON to this file (mode 0600)")
	script := f.String("script", "", "JavaScript file for the eval diagnostic command")
	message := f.String("message", "", "message text, including with -message")
	send := f.Bool("send", false, "send; default only previews recipients")
	continueOnError := f.Bool("continue-on-error", false, "skip unresolved recipients and continue after individual failures; never retry uncertain sends")
	to := f.String("to", "", "comma-separated exact usernames or friend UUIDs; default all confirmed friends")
	limit := f.Int("limit", 0, "maximum recipients; zero means all")
	delay := f.Duration("delay", 2*time.Second, "interval between sends")
	journalPath := f.String("journal", "qq_snap-journal.jsonl", "resumption journal; do not share it")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if command == "inspect" {
		if *har == "" {
			return errors.New("inspect requires --har")
		}
		r, err := inspectHAR(*har)
		if err != nil {
			return err
		}
		if *output != "" {
			if err = saveJSON(*output, r); err != nil {
				return err
			}
		}
		fmt.Fprintf(out, "Capture: %d requests; %d distinct conversations; %d sends.\n", r.Entries, len(r.Conversations), r.SendRequests)
		fmt.Fprintf(out, "Final conversation page captured: %t. Encryption modes: %v.\n", r.PaginationCompleted, r.SendEncryption)
		fmt.Fprintln(out, "These are conversation records, not a verified friends list.")
		return nil
	}
	if *limit < 0 || *delay < 0 {
		return errors.New("limit and delay must not be negative")
	}
	if command == "send" && strings.TrimSpace(*message) == "" {
		return errors.New("provide -message \"Hello World!\"")
	}
	b, err := connectBrowser(ctx, *cdp)
	if err != nil {
		return err
	}
	defer b.cancel()
	if command == "eval" {
		b.evalTimeout = 5 * time.Minute
		if *script == "" {
			return errors.New("eval requires --script")
		}
		code, err := os.ReadFile(*script)
		if err != nil {
			return err
		}
		var result json.RawMessage
		if err = b.eval(string(code), &result); err != nil {
			return err
		}
		if *output != "" {
			return saveJSON(*output, result)
		}
		fmt.Fprintln(out, string(result))
		return nil
	}
	friends, err := b.Friends()
	if err != nil {
		return err
	}
	selected, err := selectFriends(friends, *to, *limit)
	if err != nil {
		return err
	}
	if *output != "" {
		if err = saveJSON(*output, selected); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "%d confirmed friends; %d selected.\n", len(friends), len(selected))
	if command == "friends" || (command == "send" && !*send) {
		for _, p := range selected {
			fmt.Fprintf(out, "  %s  %s  %s\n", p.ID, p.Username, p.DisplayName)
		}
	}
	if command == "friends" {
		return nil
	}
	if command == "check" {
		for _, p := range selected {
			response, err := b.Check(p.ID)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, string(response))
		}
		return nil
	}
	if !*send {
		fmt.Fprintln(out, "Preview only. Add --send to send the specified message.")
		return nil
	}
	j, err := openJournal(*journalPath, *message)
	if err != nil {
		return err
	}
	defer j.Close()
	failed := 0
	for i, p := range selected {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if status, ok := j.previous[p.ID]; ok && status != "not_sent" {
			if status == "sent" {
				fmt.Fprintf(out, "Skipped previously sent recipient %s.\n", p.ID)
				continue
			}
			if *continueOnError {
				failed++
				fmt.Fprintf(out, "Skipped unresolved recipient %s.\n", p.ID)
				continue
			}
			return fmt.Errorf("recipient %s has an unresolved previous attempt; verify delivery before choosing a new journal", p.ID)
		}
		// Persist before the network operation. A timeout/crash is ambiguous, so never retry it automatically.
		if err = j.Record(p.ID, "pending"); err != nil {
			return err
		}
		if err = b.Send(p.ID, *message); err != nil {
			if logErr := j.Record(p.ID, "uncertain"); logErr != nil {
				return fmt.Errorf("send failed and journal update failed: %w", logErr)
			}
			if *continueOnError {
				failed++
				fmt.Fprintf(out, "Recipient %s failed; delivery may be uncertain: %v\n", p.ID, err)
				continue
			}
			return fmt.Errorf("send stopped for %s; delivery may be uncertain: %w", p.ID, err)
		}
		if err = j.Record(p.ID, "sent"); err != nil {
			return fmt.Errorf("message acknowledged but journal update failed: %w", err)
		}
		fmt.Fprintf(out, "Sent %d/%d.\n", i+1, len(selected))
		if i+1 < len(selected) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(*delay):
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("run finished with %d unresolved recipients; do not retry without verifying delivery", failed)
	}
	return nil
}

func saveJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".qq-snap-*.json")
	if err != nil {
		return err
	}
	defer f.Close()
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func selectFriends(friends []Friend, to string, limit int) ([]Friend, error) {
	selected := friends
	if to != "" {
		selected = nil
		seen := map[string]bool{}
		for _, requested := range strings.Split(to, ",") {
			requested = strings.TrimSpace(requested)
			if requested == "" {
				return nil, errors.New("empty --to recipient")
			}
			var matches []Friend
			for _, p := range friends {
				if p.ID == requested || (p.Username != "" && p.Username == requested) {
					matches = append(matches, p)
				}
			}
			if len(matches) != 1 {
				return nil, fmt.Errorf("recipient %q is missing or ambiguous in the confirmed friends list", requested)
			}
			if !seen[matches[0].ID] {
				selected = append(selected, matches[0])
				seen[matches[0].ID] = true
			}
		}
	}
	if limit > 0 && len(selected) > limit {
		selected = selected[:limit]
	}
	return selected, nil
}

type JournalRecord struct {
	Recipient   string `json:"recipient"`
	MessageHash string `json:"message_hash"`
	Status      string `json:"status"`
	Time        string `json:"time"`
}
type Journal struct {
	file     *os.File
	hash     string
	previous map[string]string
}

func openJournal(path, message string) (*Journal, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	if err = f.Chmod(0600); err != nil {
		return nil, err
	}
	if err = lockJournalFile(f); err != nil {
		return nil, errors.New("another sender is using this journal")
	}
	h := sha256.Sum256([]byte(message))
	j := &Journal{file: f, hash: hex.EncodeToString(h[:]), previous: map[string]string{}}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var rec JournalRecord
		if err = json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			return nil, errors.New("journal is damaged; verify prior delivery before continuing")
		}
		if rec.MessageHash != j.hash {
			return nil, errors.New("journal belongs to a different message; choose a different --journal")
		}
		if rec.Recipient == "" || (rec.Status != "pending" && rec.Status != "uncertain" && rec.Status != "sent" && rec.Status != "not_sent") {
			return nil, errors.New("invalid journal record")
		}
		j.previous[rec.Recipient] = rec.Status
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	if _, err = f.Seek(0, io.SeekEnd); err != nil {
		return nil, err
	}
	ok = true
	return j, nil
}
func (j *Journal) Record(id, status string) error {
	b, err := json.Marshal(JournalRecord{id, j.hash, status, time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	if _, err = j.file.Write(append(b, '\n')); err != nil {
		return err
	}
	if err = j.file.Sync(); err != nil {
		return err
	}
	j.previous[id] = status
	return nil
}
func (j *Journal) Close() error { return j.file.Close() }
