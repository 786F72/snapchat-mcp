package service

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

const incomingRequestsPath = "/snapchat.friending.server.FriendRequests/IncomingFriendSync"
const publicInfoPath = "/com.snapchat.atlas.gw.AtlasGw/GetSnapchatterPublicInfo"
const storyStatusPath = "/ranking.storymanagement.StoryManagementGatewayService/GetActiveStoryStatus"

func (s *Store) readSnapshot(ctx context.Context, account string) (SessionSnapshot, error) {
	var snapshot SessionSnapshot
	raw, err := s.LoadSecret(ctx, s.vault, account)
	if err != nil {
		return snapshot, fail("reauth_required", "capture this account and unlock the OS keyring", 409)
	}
	if json.Unmarshal(raw, &snapshot) != nil || parseUUID(snapshot.UserID) == nil {
		return snapshot, fail("invalid_session", "invalid captured session", 409)
	}
	return snapshot, nil
}

type IncomingRequest struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	AddedAt     uint64 `json:"added_at"`
	Viewed      bool   `json:"viewed"`
	Ignored     bool   `json:"ignored"`
	LinkType    uint64 `json:"link_type"`
}

func decodeIncomingRequests(payload []byte) ([]IncomingRequest, error) {
	full := false
	metadataSeen := false
	items := []IncomingRequest{}
	ids := map[string]bool{}
	err := walkFields(payload, func(f wireField) error {
		if f.number == 1 {
			if metadataSeen || f.kind != protowire.BytesType {
				return fmt.Errorf("invalid incoming metadata")
			}
			metadataSeen = true
			return walkFields(f.data, func(v wireField) error {
				if v.number == 2 {
					if v.kind != protowire.VarintType {
						return fmt.Errorf("invalid incoming mode")
					}
					full = v.value == 2 || v.value == 3
				}
				return nil
			})
		}
		if f.number != 2 {
			return nil
		}
		if f.kind != protowire.BytesType {
			return fmt.Errorf("invalid incoming friend")
		}
		var item IncomingRequest
		if err := walkFields(f.data, func(v wireField) error {
			switch v.number {
			case 1:
				if v.kind != protowire.BytesType {
					return fmt.Errorf("invalid incoming UUID")
				}
				var err error
				item.ID, err = atlasUUID(v.data)
				return err
			case 2, 3:
				if v.kind != protowire.BytesType {
					return fmt.Errorf("invalid incoming name")
				}
				if v.number == 2 {
					item.Username = string(v.data)
				} else {
					item.DisplayName = string(v.data)
				}
			case 4, 5, 7, 13:
				if v.kind != protowire.VarintType {
					return fmt.Errorf("invalid incoming flag")
				}
				switch v.number {
				case 4:
					item.LinkType = v.value
				case 5:
					item.AddedAt = v.value
				case 7:
					item.Viewed = v.value != 0
				case 13:
					item.Ignored = v.value != 0
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if item.ID == "" || ids[item.ID] {
			return fmt.Errorf("missing or duplicate incoming UUID")
		}
		ids[item.ID] = true
		items = append(items, item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !full {
		return nil, fmt.Errorf("expected full incoming snapshot")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (s *Store) IncomingRequests(ctx context.Context, account, cursor string, limit int) (any, error) {
	if limit < 1 || limit > 500 {
		return nil, fail("invalid_argument", "limit must be 1–500", 400)
	}
	if cursor != "" {
		id := parseUUID(cursor)
		if id == nil {
			return nil, fail("invalid_cursor", "incoming request cursor must be a UUID", 400)
		}
		cursor = rawUUID(id.EncodedId)
	}
	snapshot, err := s.readSnapshot(ctx, account)
	if err != nil {
		return nil, err
	}
	payload, err := metadataRPC(ctx, snapshot, incomingRequestsPath, nil)
	if err != nil {
		return nil, err
	}
	items, err := decodeIncomingRequests(payload)
	if err != nil {
		return nil, fail("incompatible_protocol", "incoming response was not a complete compatible snapshot", 502)
	}
	excluded, err := s.Exclusions(ctx, account)
	if err != nil {
		return nil, err
	}
	page, next := incomingRequestPage(items, excluded, snapshot.UserID, cursor, limit)
	return map[string]any{"items": page, "next_cursor": next, "source": "standalone", "synced_at": now(), "marked_viewed": false, "friendships_changed": false}, nil
}

func incomingRequestPage(items []IncomingRequest, excluded []string, selfID, cursor string, limit int) ([]IncomingRequest, string) {
	blocked := map[string]bool{}
	for _, id := range excluded {
		blocked[id] = true
	}
	page := []IncomingRequest{}
	next := ""
	for _, item := range items {
		if item.ID <= cursor || blocked[item.ID] || item.ID == selfID || item.Ignored {
			continue
		}
		if len(page) == limit {
			next = page[len(page)-1].ID
			break
		}
		page = append(page, item)
	}
	return page, next
}

type PublicProfile struct {
	ID              string `json:"id"`
	Username        string `json:"username"`
	MutableUsername string `json:"mutable_username"`
	DisplayName     string `json:"display_name"`
	Official        bool   `json:"official"`
	Popular         bool   `json:"popular"`
	ChatBot         bool   `json:"chat_bot"`
}

func rawUUID(raw []byte) string {
	if len(raw) != 16 {
		return ""
	}
	s := hex.EncodeToString(raw)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func decodePublicProfile(payload []byte, expected string) (PublicProfile, error) {
	var out PublicProfile
	seen := false
	err := walkFields(payload, func(f wireField) error {
		if f.number != 1 {
			return nil
		}
		if seen || f.kind != protowire.BytesType {
			return fmt.Errorf("ambiguous profile response")
		}
		seen = true
		return walkFields(f.data, func(v wireField) error {
			switch v.number {
			case 1:
				if v.kind != protowire.BytesType {
					return fmt.Errorf("invalid profile ID")
				}
				out.ID = rawUUID(v.data)
			case 2, 3, 7:
				if v.kind != protowire.BytesType {
					return fmt.Errorf("invalid profile name")
				}
				switch v.number {
				case 2:
					out.Username = string(v.data)
				case 3:
					out.DisplayName = string(v.data)
				case 7:
					out.MutableUsername = string(v.data)
				}
			case 4, 5, 12:
				if v.kind != protowire.VarintType {
					return fmt.Errorf("invalid profile flag")
				}
				switch v.number {
				case 4:
					out.Official = v.value != 0
				case 5:
					out.Popular = v.value != 0
				case 12:
					out.ChatBot = v.value != 0
				}
			}
			return nil
		})
	})
	if err != nil {
		return out, err
	}
	if !seen {
		return out, fail("not_found", "public profile was not returned", 404)
	}
	if out.ID != expected {
		return out, fmt.Errorf("profile ID mismatch")
	}
	return out, nil
}
func (s *Store) PublicProfile(ctx context.Context, account, recipient string) (any, error) {
	id := parseUUID(recipient)
	if id == nil {
		return nil, fail("invalid_argument", "recipient_id must be a UUID", 400)
	}
	recipient = rawUUID(id.EncodedId)
	excluded, err := s.Exclusions(ctx, account)
	if err != nil {
		return nil, err
	}
	for _, blocked := range excluded {
		if blocked == recipient {
			return nil, fail("recipient_excluded", "recipient is excluded", 403)
		}
	}
	snapshot, err := s.readSnapshot(ctx, account)
	if err != nil {
		return nil, err
	}
	request := append(bytesField(1, id.EncodedId), numberField(2, 1)...)
	response, err := metadataRPC(ctx, snapshot, publicInfoPath, request)
	if err != nil {
		return nil, err
	}
	profile, err := decodePublicProfile(response, recipient)
	if err != nil {
		if e, ok := err.(*Error); ok {
			return nil, e
		}
		return nil, fail("incompatible_protocol", "public profile response did not match requested recipient", 502)
	}
	return profile, nil
}

func rankingUUID(id string) []byte {
	raw := parseUUID(id)
	if raw == nil {
		return nil
	}
	out := protowire.AppendTag(nil, 1, protowire.Fixed64Type)
	out = protowire.AppendFixed64(out, binary.BigEndian.Uint64(raw.EncodedId))
	out = protowire.AppendTag(out, 2, protowire.Fixed64Type)
	return protowire.AppendFixed64(out, binary.BigEndian.Uint64(raw.EncodedId[8:]))
}
func (s *Store) ActiveStoryStatus(ctx context.Context, account string) (any, error) {
	snapshot, err := s.readSnapshot(ctx, account)
	if err != nil {
		return nil, err
	}
	request := append(bytesField(1, storyRequestMetadata(snapshot.UserID)), bytesField(2, rankingUUID(snapshot.UserID))...)
	response, err := metadataRPC(ctx, snapshot, storyStatusPath, request)
	if err != nil {
		return nil, err
	}
	// Preserve only the fixed schema's counters and timestamps, never media or keys.
	timestamps, counts, err := decodeActiveStoryStatus(response)
	if err != nil {
		return nil, fail("incompatible_protocol", "invalid active story status response", 502)
	}
	return map[string]any{"user_id": snapshot.UserID, "latest_post_timestamps_ms": timestamps, "active_story_counts": counts, "source": "standalone", "checked_at": now(), "note": "Status counters only; does not open stories or identify an individual publication."}, nil
}

func decodeActiveStoryStatus(response []byte) ([]uint64, []map[string]any, error) {
	timestamps := []uint64{}
	counts := []map[string]any{}
	err := walkFields(response, func(f wireField) error {
		switch f.number {
		case 1:
			if f.kind == protowire.VarintType {
				timestamps = append(timestamps, f.value)
			} else if f.kind == protowire.BytesType {
				data := f.data
				for len(data) > 0 {
					value, n := protowire.ConsumeVarint(data)
					if n < 0 {
						return fmt.Errorf("invalid packed timestamp")
					}
					timestamps = append(timestamps, value)
					data = data[n:]
				}
			} else {
				return fmt.Errorf("invalid timestamp")
			}
		case 2:
			if f.kind != protowire.BytesType {
				return fmt.Errorf("invalid active count")
			}
			fields := map[string]any{}
			if err := walkFields(f.data, func(v wireField) error {
				switch v.number {
				case 1:
					if v.kind != protowire.BytesType {
						return fmt.Errorf("invalid status story ID")
					}
					id, err := atlasUUID(v.data)
					if err != nil {
						return err
					}
					fields["story_id"] = id
				case 2:
					if v.kind != protowire.VarintType {
						return fmt.Errorf("invalid status count")
					}
					fields["count"] = v.value
				}
				return nil
			}); err != nil {
				return err
			}
			counts = append(counts, fields)
		}
		return nil
	})
	return timestamps, counts, err
}

func storyRequestMetadata(userID string) []byte {
	metadata := append(bytesField(1, []byte("dweb-"+newID()[:8])), numberField(2, uint64(time.Now().UnixMilli()))...)
	metadata = append(metadata, numberField(3, 1)...)
	return append(metadata, bytesField(4, bytesField(1, rankingUUID(userID)))...)
}
