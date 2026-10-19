package service

import (
	"context"
	"os"
	"testing"
	"time"
)

// Opt-in live send to one explicitly selected conversation, with a stable job key.
func TestLiveScheduledText(t *testing.T) {
	if os.Getenv("QQ_SNAP_LIVE_MUTATE") != "true" {
		t.Skip("explicit live mutation opt-in required")
	}
	account, dir, conversation := os.Getenv("QQ_SNAP_LIVE_ACCOUNT"), os.Getenv("QQ_SNAP_LIVE_DATA_DIR"), os.Getenv("QQ_SNAP_LIVE_CONVERSATION")
	if account == "" || dir == "" || conversation == "" {
		t.Fatal("explicit account, directory and conversation required")
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.SyncFriends(context.Background(), account, "", 100); err != nil {
		t.Fatal(err)
	}
	// A fixed schedule makes this test's retries resolve to the existing job.
	j, err := s.CreateTextJob(context.Background(), account, []string{conversation}, "Snapchat MCP scheduled-send validation.", "2026-10-19T00:00:00Z", "v070-scheduled-text-validation")
	if err != nil {
		t.Fatal(err)
	}
	if j.State == "completed" {
		t.Log("existing acknowledged job; no repeat send")
		return
	}
	if j.State != "queued" {
		t.Fatal("prior job requires inspection; no automatic retry")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.RunScheduler(ctx) }()
	for {
		saved, err := s.Job(ctx, account, j.ID, false)
		if err != nil {
			cancel()
			<-done
			t.Fatal(err)
		}
		if saved.State != "queued" && saved.State != "running" {
			cancel()
			<-done
			if saved.State != "completed" {
				t.Fatalf("job ended %s", saved.State)
			}
			t.Log("scheduled worker delivered one acknowledged text")
			return
		}
		select {
		case <-ctx.Done():
			<-done
			t.Fatal("job did not complete")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
