package service

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPublishedStoriesAreScopedReceiptsNotRemoteVisibility(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "one")
	b := account(t, s, "two")
	for _, row := range []struct{ account, key, state, receipt string }{
		{a.ID, "first", "sent", `{"acknowledged":true,"content_type":"story","story_id":"story-one","verification":"rpc"}`},
		{b.ID, "other", "sent", `{"acknowledged":true,"content_type":"story","story_id":"private-other-account"}`},
		{a.ID, "text", "sent", `{"acknowledged":true,"content_type":"text"}`},
		{a.ID, "pending", "pending", `{"acknowledged":true,"content_type":"story","story_id":"unconfirmed"}`},
		{a.ID, "second", "sent", `{"acknowledged":true,"content_type":"story","story_id":"story-two","verification":"rpc"}`},
	} {
		if _, err := s.db.Exec("INSERT INTO deliveries VALUES(?,?,?,?,?,?)", row.account, row.key, "hash", row.state, []byte(row.receipt), now()); err != nil {
			t.Fatal(err)
		}
	}
	result := s.Call(context.Background(), "stories.published", map[string]any{"account_id": a.ID, "limit": float64(1)})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	data := result.Data.(map[string]any)
	items := data["items"].([]PublishedStory)
	if len(items) != 1 || items[0].StoryID != "story-one" || items[0].RemoteStatus != "unknown" || data["remote_state_verified"] != false || data["next_cursor"] == "" {
		t.Fatal(data)
	}
	result = s.Call(context.Background(), "stories.published", map[string]any{"account_id": a.ID, "limit": float64(1), "cursor": data["next_cursor"]})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	data = result.Data.(map[string]any)
	items = data["items"].([]PublishedStory)
	if len(items) != 1 || items[0].StoryID != "story-two" || data["next_cursor"] != "" {
		t.Fatal(data)
	}
	status, result := request(t, s.Handler("secret"), "POST", "/v1/accounts/"+a.ID+"/stories/delete", `{"content_id":"story-one"}`, "secret")
	if status != 501 || result.Error == nil || result.Error.Code != "unsupported_capability" {
		t.Fatal(status, result)
	}
	// A rejected delete must not erase the only record of the remote story.
	result = s.Call(context.Background(), "stories.published", map[string]any{"account_id": a.ID})
	raw, _ := json.Marshal(result.Data)
	if result.Error != nil || len(result.Data.(map[string]any)["items"].([]PublishedStory)) != 2 {
		t.Fatal(string(raw), result.Error)
	}
}
