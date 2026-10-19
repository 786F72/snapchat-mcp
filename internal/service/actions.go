package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"
)

// PrepareGroupAction intercepts the complete native request before any mutation is submitted.
func (s *Store) PrepareGroupAction(ctx context.Context, account, conversation, action, title string) (map[string]any, error) {
	if action != "rename" && action != "leave" {
		return nil, fail("invalid_argument", "action must be rename or leave", 400)
	}
	if action == "rename" && (strings.TrimSpace(title) == "" || len(title) > 256) {
		return nil, fail("invalid_argument", "title must contain 1–256 bytes for rename", 400)
	}
	if action == "leave" && title != "" {
		return nil, fail("invalid_argument", "title is only used for rename", 400)
	}
	if _, err := s.GetConversation(ctx, account, conversation, true); err != nil {
		return nil, err
	}
	material, err := s.contentInput(ctx, account, conversation, "", "", "action_prepare", "text", "", "")
	if err != nil {
		return nil, err
	}
	var input map[string]any
	if json.Unmarshal(material, &input) != nil {
		return nil, fail("invalid_session", "invalid session", 409)
	}
	input["action"] = "groups." + action
	input["title"] = title
	material, _ = json.Marshal(input)
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := nativeCommand(ctx, nativeClientSource(), s.runtimeDir, material)
	var output bytes.Buffer
	cmd.Stdout = &cappedOutput{buffer: &output, remaining: 1 << 20}
	progress := &progressWriter{store: s, account: account, key: "group-prepare"}
	cmd.Stderr = progress
	runErr := cmd.Run()
	var result map[string]any
	if json.Unmarshal(output.Bytes(), &result) != nil || runErr != nil || result["prepared"] != true || result["submitted"] != false {
		return nil, fail("prepare_failed", "group request preparation failed; no change submitted", 409)
	}
	return result, nil
}
