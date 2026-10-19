package service

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduledJobEncryptionIdentityAndRestart(t *testing.T) {
	s, a, b := lookupFixture(t)
	ctx := context.Background()
	j, err := s.CreateTextJob(ctx, a.ID, []string{lookupGroup, lookupConversation, lookupGroup}, "private scheduled fixture", "", "batch-key")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateTextJob(ctx, a.ID, []string{lookupConversation, lookupGroup}, "private scheduled fixture", "", "batch-key")
	if err != nil || again.ID != j.ID {
		t.Fatal("duplicate creation", err)
	}
	for _, change := range []struct{ text, at string }{{"different", ""}, {"private scheduled fixture", "2030-01-01T00:00:00Z"}} {
		if _, err = s.CreateTextJob(ctx, a.ID, []string{lookupConversation, lookupGroup}, change.text, change.at, "batch-key"); publicError(err).Code != "conflict" {
			t.Fatal(err)
		}
	}
	if _, err = s.RunDueJob(ctx, b.ID, j.ID); publicError(err).Code != "not_found" {
		t.Fatal("cross-account run", err)
	}
	var sealed []byte
	s.db.QueryRow("SELECT sealed FROM scheduled_jobs WHERE job_id=?", j.ID).Scan(&sealed)
	if bytes.Contains(sealed, []byte("private scheduled fixture")) || bytes.Contains(j.Data, []byte("private scheduled fixture")) {
		t.Fatal("plaintext content leaked")
	}
	if _, err = s.vault.Open(b.ID+"/job/"+j.ID, sealed); err == nil {
		t.Fatal("cross-account decryption")
	}
	var path string
	s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path)
	reopened, err := Open(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.vault = s.vault
	calls := 0
	reopened.jobSend = func(_ context.Context, account, conversation, text, key string) (any, error) {
		calls++
		if account != a.ID || text != "private scheduled fixture" || !strings.HasPrefix(key, "job:"+j.ID+":") {
			t.Fatal("wrong job destination or payload")
		}
		return map[string]any{"acknowledged": true}, nil
	}
	finished, err := reopened.RunDueJob(ctx, a.ID, j.ID)
	if err != nil || finished.State != "completed" || calls != 2 {
		t.Fatal(finished, err, calls)
	}
	if _, err = reopened.RunDueJob(ctx, a.ID, j.ID); publicError(err).Code != "conflict" || calls != 2 {
		t.Fatal("completed job replayed", err)
	}
	var summary jobSummary
	json.Unmarshal(finished.Data, &summary)
	if len(summary.Results) != 2 || summary.CompletedAt == "" {
		t.Fatal(summary)
	}
}
func TestScheduledJobDueCancellationAndEligibility(t *testing.T) {
	s, a, _ := lookupFixture(t)
	ctx := context.Background()
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	j, err := s.CreateTextJob(ctx, a.ID, []string{lookupConversation}, "future", future, "future-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RunDueJob(ctx, a.ID, j.ID); publicError(err).Code != "not_due" {
		t.Fatal(err)
	}
	if _, err = s.Job(ctx, a.ID, j.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RunDueJob(ctx, a.ID, j.ID); publicError(err).Code != "conflict" {
		t.Fatal(err)
	}
	j, err = s.CreateTextJob(ctx, a.ID, []string{lookupConversation}, "blocked before execution", "", "excluded-key")
	if err != nil {
		t.Fatal(err)
	}
	s.ReplaceExclusions(ctx, a.ID, []string{fixtureAlice})
	s.jobSend = func(context.Context, string, string, string, string) (any, error) {
		t.Fatal("excluded send invoked")
		return nil, nil
	}
	j, err = s.RunDueJob(ctx, a.ID, j.ID)
	if err != nil || j.State != "failed" {
		t.Fatal(j, err)
	}
	var summary jobSummary
	json.Unmarshal(j.Data, &summary)
	if summary.Results[0].ErrorCode != "excluded_recipient" || summary.Results[0].State != "not_sent" {
		t.Fatal(summary)
	}
}
func TestScheduledJobConcurrentClaimsAndUnknownOutcome(t *testing.T) {
	s, a, _ := lookupFixture(t)
	ctx := context.Background()
	j, err := s.CreateTextJob(ctx, a.ID, []string{lookupConversation}, "one attempt", "", "claim-key")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.jobSend = func(_ context.Context, account, conversation, text, key string) (any, error) {
		calls.Add(1)
		close(started)
		<-release
		s.db.Exec("INSERT INTO deliveries VALUES(?,?,?,?,?,?)", account, key, "fixture", "pending", []byte(`null`), now())
		return nil, fail("delivery_unknown", "fixture timeout", 409)
	}
	done := make(chan error, 1)
	go func() { _, err := s.RunDueJob(ctx, a.ID, j.ID); done <- err }()
	<-started
	if _, err = s.RunDueJob(ctx, a.ID, j.ID); publicError(err).Code != "conflict" {
		t.Fatal("second worker claimed job", err)
	}
	if _, err = s.Job(ctx, a.ID, j.ID, true); publicError(err).Code != "conflict" {
		t.Fatal("running cancellation accepted", err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	saved, _ := s.Job(ctx, a.ID, j.ID, false)
	if saved.State != "unknown" || calls.Load() != 1 {
		t.Fatal(saved, calls.Load())
	}
	if _, err = s.RunDueJob(ctx, a.ID, j.ID); publicError(err).Code != "conflict" {
		t.Fatal(err)
	}
}
func TestSchedulerProcessesDueJobsAndStops(t *testing.T) {
	s, a, _ := lookupFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j, err := s.CreateTextJob(ctx, a.ID, []string{lookupConversation}, "worker", "", "worker-key")
	if err != nil {
		t.Fatal(err)
	}
	s.jobSend = func(context.Context, string, string, string, string) (any, error) {
		return map[string]any{"acknowledged": true}, nil
	}
	done := make(chan struct{})
	go func() { defer close(done); s.RunScheduler(ctx) }()
	deadline := time.After(3 * time.Second)
	for {
		saved, _ := s.Job(context.Background(), a.ID, j.ID, false)
		if saved.State == "completed" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("scheduler did not execute due job")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}
func TestJobSchemaAndInputValidation(t *testing.T) {
	s, a, _ := lookupFixture(t)
	ctx := context.Background()
	for _, in := range []map[string]any{
		{"account_id": a.ID, "conversation_ids": []any{}, "text": "hello", "idempotency_key": "k"},
		{"account_id": a.ID, "conversation_ids": []any{lookupConversation}, "text": "hello", "idempotency_key": "k", "run_at": "tomorrow"},
		{"account_id": a.ID, "conversation_ids": []any{"invalid"}, "text": "hello", "idempotency_key": "k"},
	} {
		if r := s.Call(ctx, "jobs.create", in); r.Error == nil || r.Error.Code != "invalid_argument" {
			t.Fatal(r)
		}
	}
}
