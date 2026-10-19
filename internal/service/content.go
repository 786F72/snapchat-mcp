package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"
)

const myStoryID = "01010101-0101-0101-0101-010101010101"

func (s *Store) contentInput(ctx context.Context, account, conversationID, text, reply, operation, kind, mediaID, audience string, ignoreExclusions ...bool) ([]byte, error) {
	var material []byte
	var err error
	var input map[string]any
	if kind == "story" {
		if operation == "send" {
			if _, err = s.syncFriendsLocked(ctx, account, "", 1); err != nil {
				return nil, err
			}
		}
		excluded, err := s.Exclusions(ctx, account)
		if err != nil {
			return nil, err
		}

		material, err = s.LoadSecret(ctx, OSVault(), account)
		if err != nil {
			return nil, fail("reauth_required", "capture this account and unlock the OS keyring", 409)
		}
		if json.Unmarshal(material, &input) != nil {
			return nil, fail("invalid_session", "invalid captured session", 409)
		}
		input["operation"] = operation
		input["conversation_id"] = myStoryID
		input["conversation_type"] = "MY_STORY"
		input["attempt_id"] = newID()
		var snapshot SessionSnapshot
		if json.Unmarshal(material, &snapshot) != nil {
			return nil, fail("invalid_session", "invalid captured session", 409)
		}
		check := checkStoryAudience(snapshot, excluded, audience, ignoreExclusions...)
		if audience != "friends" && audience != "custom" {
			return nil, fail("invalid_argument", "audience must be friends or custom", 400)
		}
		if operation == "send" && !check.CanPublish {
			e := fail("audience_not_verified", "story audience is empty, stale, unverified, or includes an excluded recipient; publication blocked", 409)
			e.Details = map[string]any{"audience_check": check}
			return nil, e
		}
		input["can_publish"] = check.CanPublish
		input["audience_check"] = check
		// Current web client uses a sentinel messaging destination and an explicit
		// My Story metadata override. Never inherit a possibly public audience.
		privacy := uint64(1)
		if audience == "custom" {
			privacy = 2
		}
		metadata := append(bytesField(1, numberField(1, privacy)), bytesField(7, parseUUID(myStoryID).EncodedId)...)
		input["story_data"] = base64.StdEncoding.EncodeToString(metadata)
	} else {
		material, err = s.messageInput(ctx, account, conversationID, text, reply, operation)
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(material, &input) != nil {
			return nil, fail("invalid_session", "invalid captured session", 409)
		}
	}
	input["content_kind"] = kind
	if kind != "text" {
		media, err := s.loadMedia(ctx, account, mediaID)
		if err != nil {
			return nil, err
		}
		input["media"] = media
	}
	return json.Marshal(input)
}

type storyAudienceCheck struct {
	Audience           string `json:"audience"`
	Size               int    `json:"size"`
	Excluded           int    `json:"excluded_count"`
	ExclusionsEnforced bool   `json:"exclusions_enforced"`
	Verified           bool   `json:"verified"`
	CanPublish         bool   `json:"can_publish"`
}

func checkStoryAudience(snapshot SessionSnapshot, excluded []string, audience string, ignoreExclusions ...bool) storyAudienceCheck {
	check := storyAudienceCheck{Audience: audience, ExclusionsEnforced: len(ignoreExclusions) == 0 || !ignoreExclusions[0]}
	synced, err := time.Parse(time.RFC3339Nano, snapshot.FriendsSyncedAt)
	if err != nil || time.Since(synced) > time.Hour || time.Until(synced) > time.Minute {
		return check
	}
	var friends []Friend
	if json.Unmarshal(snapshot.Friends, &friends) != nil {
		return check
	}
	if audience != "friends" && audience != "custom" {
		return check
	}
	check.Verified = true
	blocked := map[string]bool{}
	for _, id := range excluded {
		blocked[id] = true
	}
	for _, friend := range friends {
		if audience == "custom" && !friend.CustomStoryAllowed {
			continue
		}
		if friend.ID == snapshot.UserID {
			continue
		}
		check.Size++
		if blocked[friend.ID] {
			check.Excluded++
		}
	}
	// Overrides apply only to this request; the stored exclusion list is unchanged.
	check.CanPublish = check.Size > 0 && (!check.ExclusionsEnforced || check.Excluded == 0)
	return check
}
