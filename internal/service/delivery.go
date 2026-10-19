package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os/exec"
	"strings"
	"time"
)

func sendHash(conversation, text, reply string) string {
	b, _ := json.Marshal([]string{conversation, text, reply})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *Store) SendText(ctx context.Context, account, conversationID, text, reply, key string) (any, error) {
	return s.sendContent(ctx, account, conversationID, text, reply, key, "text", "", "")
}

func (s *Store) SendSnap(ctx context.Context, account, conversationID, mediaID, key string) (any, error) {
	return s.sendContent(ctx, account, conversationID, "", "", key, "snap", mediaID, "")
}
func (s *Store) PublishStory(ctx context.Context, account, mediaID, audience, key string, ignoreExclusions ...bool) (any, error) {
	return s.sendContent(ctx, account, myStoryID, "", "", key, "story", mediaID, audience, ignoreExclusions...)
}

func (s *Store) sendContent(ctx context.Context, account, conversationID, text, reply, key, kind, mediaID, audience string, ignoreExclusions ...bool) (any, error) {
	if reply != "" {
		return nil, fail("unsupported_capability", "quoted replies have not been validated", 501)
	}
	if strings.TrimSpace(key) == "" || kind == "text" && (strings.TrimSpace(text) == "" || len(text) > 16000) || len(key) > 128 {
		return nil, fail("invalid_argument", "text must be nonempty and under 16000 bytes; idempotency key must be under 128 bytes", 400)
	}
	lock, _ := s.accountLocks.LoadOrStore(account, make(chan struct{}, 1))
	ch := lock.(chan struct{})
	select {
	case ch <- struct{}{}:
		defer func() { <-ch }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	hash := sendHash(conversationID, text, reply)
	if kind != "text" {
		if mediaID == "" {
			return nil, fail("invalid_argument", "media_id is required", 400)
		}
		hashAudience := audience
		if kind == "story" && len(ignoreExclusions) > 0 && ignoreExclusions[0] {
			hashAudience += ":ignore_exclusions"
		}
		hash = sendHash(conversationID, mediaID, kind+":"+hashAudience)
	}
	var previousHash, state string
	var receipt []byte
	err := s.db.QueryRowContext(ctx, "SELECT payload_hash,state,receipt FROM deliveries WHERE account_id=? AND idempotency_key=?", account, key).Scan(&previousHash, &state, &receipt)
	if err == nil {
		if previousHash != hash {
			return nil, fail("conflict", "idempotency key is bound to a different message", 409)
		}
		if state == "sent" {
			var result any
			if json.Unmarshal(receipt, &result) != nil {
				return nil, fail("internal_error", "invalid delivery receipt", 500)
			}
			return result, nil
		}
		if state != "not_sent" {
			return nil, fail("delivery_unknown", "this key has a prior attempt; no automatic resubmission is allowed", 409)
		}
	}
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	material, err := s.contentInput(ctx, account, conversationID, text, reply, "send", kind, mediaID, audience, ignoreExclusions...)
	if err != nil {
		return nil, err
	}

	// A durable pre-submission record makes crashes/timeouts conservatively
	// uncertain. The same key can never cause an automatic second send.
	insertResult, insertErr := s.db.ExecContext(ctx, "INSERT INTO deliveries VALUES(?,?,?,?,?,?) ON CONFLICT(account_id,idempotency_key) DO UPDATE SET state=excluded.state,receipt=excluded.receipt WHERE deliveries.state='not_sent' AND deliveries.payload_hash=excluded.payload_hash", account, key, hash, "pending", []byte(`null`), now())
	if insertErr != nil {
		return nil, insertErr
	}
	affected, err := insertResult.RowsAffected()
	if err != nil || affected != 1 {
		return nil, fail("delivery_unknown", "another process owns this attempt; no message submitted", 409)
	}
	runCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "--input-type=module", "--eval", nativeClientSource(), s.runtimeDir)
	cmd.Stdin = bytes.NewReader(material)
	var output bytes.Buffer
	cmd.Stdout = &cappedOutput{buffer: &output, remaining: 1 << 20}
	progress := &progressWriter{store: s, account: account, key: key}
	cmd.Stderr = progress
	runErr := cmd.Run()
	var result map[string]any
	parseErr := json.Unmarshal(output.Bytes(), &result)
	if parseErr == nil {
		if encoded, ok := result["send_frame"].(string); ok {
			delete(result, "send_frame")
			if result["http_status"] == float64(200) {
				resolutionID, _ := result["client_resolution_id"].(string)
				receipt, decodeErr := contentAcknowledgment(encoded, conversationID, resolutionID, kind)
				if decodeErr == nil {
					receipt["encryption"] = result["encryption"]
					result = receipt
				} else {
					result["acknowledged"] = false
					result["ack_error"] = "server response did not contain a matching successful destination acknowledgment"
				}
			}
		}
	}
	state = "unknown"
	if runErr == nil && parseErr == nil && result["acknowledged"] == true {
		state = "sent"
	} else if (parseErr == nil && result["submitted"] == false) || (!progress.Submitted && progress.SawTrace && !progress.Truncated) {
		state = "not_sent"
	}
	// Persist completion even when the caller has disconnected.
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer persistCancel()
	if parseErr != nil {
		result = map[string]any{"acknowledged": false, "submitted": progress.Submitted, "delivery": state, "progress": progress.Events}
	}
	receipt, _ = json.Marshal(result)
	if _, err = s.db.ExecContext(persistCtx, "UPDATE deliveries SET state=?,receipt=? WHERE account_id=? AND idempotency_key=?", state, receipt, account, key); err != nil {
		return nil, fail("delivery_unknown", "delivery result could not be persisted; do not resubmit", 409)
	}
	if state != "sent" {
		e := fail("delivery_unknown", "message was not acknowledged; this key will not be resubmitted", 409)
		if state == "not_sent" {
			e.Code = "not_sent"
			e.Message = "native client failed before submitting any message"
		}
		e.Details = result
		return nil, e
	}
	return result, nil
}

// TestSend chooses and persists one eligible conversation for a stable validation
// key. Re-running the command cannot silently choose a second recipient.
func (s *Store) TestSend(ctx context.Context, account, text, key string) (any, error) {
	var selection struct {
		ConversationID string `json:"conversation_id"`
		RecipientID    string `json:"recipient_id"`
		Username       string `json:"username"`
		Key            string `json:"idempotency_key"`
	}
	var previous []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM events WHERE account_id=? AND type='protocol.test_selected' AND json_extract(data,'$.idempotency_key')=? ORDER BY id LIMIT 1", account, key).Scan(&previous)
	if err == nil {
		if json.Unmarshal(previous, &selection) != nil {
			return nil, fail("internal_error", "invalid test selection", 500)
		}
	} else if err == sql.ErrNoRows {
		if _, err = s.NativeFeed(ctx, account); err != nil {
			return nil, err
		}
		material, err := s.LoadSecret(ctx, s.vault, account)
		if err != nil {
			return nil, fail("reauth_required", "capture the account before testing", 409)
		}
		var snapshot SessionSnapshot
		if json.Unmarshal(material, &snapshot) != nil {
			return nil, fail("invalid_session", "invalid session", 409)
		}
		var friends []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		}
		if json.Unmarshal(snapshot.Friends, &friends) != nil {
			return nil, fail("invalid_session", "missing friend metadata", 409)
		}
		confirmed := map[string]string{}
		for _, f := range friends {
			confirmed[f.ID] = f.Username
		}
		excluded, err := s.Exclusions(ctx, account)
		if err != nil {
			return nil, err
		}
		blocked := map[string]bool{}
		for _, id := range excluded {
			blocked[id] = true
		}
		rows, err := s.db.QueryContext(ctx, "SELECT data FROM conversations WHERE account_id=? ORDER BY id", account)
		if err != nil {
			return nil, err
		}
		candidates := []struct {
			ConversationID string
			RecipientID    string
			Username       string
		}{}
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, err
			}
			var c Conversation
			if json.Unmarshal(raw, &c) != nil || c.Type != "ONE_ON_ONE" || len(c.Participants) != 2 {
				continue
			}
			for _, id := range c.Participants {
				if id != snapshot.UserID && !blocked[id] {
					if username, ok := confirmed[id]; ok {
						candidates = append(candidates, struct {
							ConversationID string
							RecipientID    string
							Username       string
						}{c.ID, id, username})
					}
				}
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		if len(candidates) == 0 {
			return nil, fail("not_found", "no synchronized confirmed friend outside exclusions", 404)
		}
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(candidates))))
		if err != nil {
			return nil, err
		}
		chosen := candidates[index.Int64()]
		selection.ConversationID = chosen.ConversationID
		selection.RecipientID = chosen.RecipientID
		selection.Username = chosen.Username
		selection.Key = key
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		if err = appendEvent(ctx, tx, account, "protocol.test_selected", selection); err != nil {
			tx.Rollback()
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	result, err := s.SendText(ctx, account, selection.ConversationID, text, "", key)
	if err != nil {
		return nil, err
	}
	return map[string]any{"receipt": result, "test_recipient": selection.Username, "conversation_id": selection.ConversationID}, nil
}

func (s *Store) messageInput(ctx context.Context, account, conversationID, text, reply, operation string) ([]byte, error) {
	var raw []byte
	if err := s.db.QueryRowContext(ctx, "SELECT data FROM conversations WHERE account_id=? AND id=?", account, conversationID).Scan(&raw); err != nil {
		return nil, fail("not_found", "synchronize the conversation before sending", 404)
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
		for _, participant := range conversation.Participants {
			if participant == id {
				return nil, fail("excluded_recipient", "conversation includes an excluded recipient", 403)
			}
		}
	}
	material, err := s.LoadSecret(ctx, s.vault, account)
	if err != nil {
		return nil, fail("reauth_required", "capture the account and unlock the OS keyring", 409)
	}
	var input map[string]any
	var snapshot SessionSnapshot
	if json.Unmarshal(material, &input) != nil || json.Unmarshal(material, &snapshot) != nil {
		return nil, fail("invalid_session", "invalid captured session", 409)
	}
	friendTime := snapshot.FriendsSyncedAt
	if friendTime == "" {
		friendTime = snapshot.CapturedAt
	}
	captured, err := time.Parse(time.RFC3339Nano, friendTime)
	if conversation.Type == "ONE_ON_ONE" && (err != nil || time.Since(captured) > time.Hour) {
		return nil, fail("reauth_required", "confirmed friend metadata is stale; synchronize friends before sending", 409)
	}
	var friends []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(snapshot.Friends, &friends) != nil {
		return nil, fail("invalid_session", "missing confirmed friend metadata", 409)
	}
	confirmed := map[string]bool{}
	for _, f := range friends {
		confirmed[f.ID] = true
	}
	if conversation.Type != "ONE_ON_ONE" && conversation.Type != "USER_CREATED_GROUP" {
		return nil, fail("unsupported_capability", "unsupported conversation type", 501)
	}
	selfPresent := false
	recipients := 0
	for _, id := range conversation.Participants {
		if id == snapshot.UserID {
			selfPresent = true
		} else {
			if conversation.Type == "ONE_ON_ONE" && !confirmed[id] {
				return nil, fail("not_a_friend", "recipient is not in the captured confirmed friend list", 409)
			}
			recipients++
		}
	}
	if !selfPresent || recipients < 1 || conversation.Type == "ONE_ON_ONE" && recipients != 1 {
		return nil, fail("invalid_argument", "conversation must include this account and valid recipients", 400)
	}
	input["excluded_ids"] = excluded
	input["conversation_participants"] = conversation.Participants
	input["operation"] = operation
	input["conversation_id"] = conversationID
	input["conversation_type"] = conversation.Type
	input["conversation_version"] = conversation.Version
	input["text"] = text
	input["reply_to"] = reply
	input["attempt_id"] = newID()
	material, _ = json.Marshal(input)
	return material, nil
}

func (s *Store) PrepareText(ctx context.Context, account, conversationID, text string) (any, error) {
	if strings.TrimSpace(text) == "" || len(text) > 16000 {
		return nil, fail("invalid_argument", "text must be nonempty and at most 16000 bytes", 400)
	}
	return s.prepareContent(ctx, account, conversationID, text, "text", "", "")
}

func (s *Store) prepareContent(ctx context.Context, account, conversationID, text, kind, mediaID, audience string, ignoreExclusions ...bool) (any, error) {
	material, err := s.contentInput(ctx, account, conversationID, text, "", "prepare_send", kind, mediaID, audience, ignoreExclusions...)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--input-type=module", "--eval", nativeClientSource(), s.runtimeDir)
	cmd.Stdin = bytes.NewReader(material)
	var output bytes.Buffer
	cmd.Stdout = &cappedOutput{buffer: &output, remaining: 1 << 20}
	progress := &progressWriter{store: s, account: account, key: "prepare-only"}
	cmd.Stderr = progress
	err = cmd.Run()
	var result map[string]any
	if json.Unmarshal(output.Bytes(), &result) != nil {
		result = map[string]any{"ready_to_submit": false, "submitted": false, "progress": progress.Events}
	}
	if err != nil {
		e := fail("prepare_failed", "native packet preparation did not complete; no message submitted", 409)
		e.Details = result
		return nil, e
	}
	return result, nil
}
