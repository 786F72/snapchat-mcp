package service

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// Explicitly opt in to read-only protocol validation. Never sends messages,
// opens snaps/stories, accepts requests, or changes account relationships.
func TestLiveReadActions(t *testing.T) {
	accountID := os.Getenv("QQ_SNAP_LIVE_ACCOUNT")
	dataDir := os.Getenv("QQ_SNAP_LIVE_DATA_DIR")
	if accountID == "" || dataDir == "" {
		t.Skip("requires explicit live read account and data directory")
	}
	s, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	snapshot, err := s.readSnapshot(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("incoming", func(t *testing.T) {
		result, err := s.IncomingRequests(ctx, accountID, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("incoming read succeeded, visible page size=%d", len(result.(map[string]any)["items"].([]IncomingRequest)))
	})
	t.Run("profile", func(t *testing.T) {
		result, err := s.PublicProfile(ctx, accountID, snapshot.UserID)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("own profile matched requested ID: %v", result.(PublicProfile).ID == snapshot.UserID)
	})
	t.Run("story_metadata", func(t *testing.T) {
		contentID := os.Getenv("QQ_SNAP_LIVE_STORY_ID")
		if contentID == "" {
			t.Skip("requires explicit owned story ID")
		}
		result, err := s.StoryMetadata(ctx, accountID, contentID)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		t.Log(string(raw))
	})
	t.Run("story_status", func(t *testing.T) {
		result, err := s.ActiveStoryStatus(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("own active story status: %+v", result)
	})
}
