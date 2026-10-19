package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// Atlas schema follows the pinned web app's SyncFriendData and OutgoingFriend
// messages. It is distinct from the messaging-core UUID encoding.
type Friend struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	DisplayName        string `json:"display_name"`
	CustomStoryAllowed bool   `json:"custom_story_allowed"`
}
type wireField struct {
	number protowire.Number
	kind   protowire.Type
	data   []byte
	value  uint64
}

func walkFields(data []byte, visit func(wireField) error) error {
	for len(data) > 0 {
		number, kind, n := protowire.ConsumeTag(data)
		if n < 0 || number < 1 {
			return fmt.Errorf("invalid protobuf tag")
		}
		data = data[n:]
		f := wireField{number: number, kind: kind}
		var size int
		switch kind {
		case protowire.BytesType:
			f.data, size = protowire.ConsumeBytes(data)
		case protowire.VarintType:
			f.value, size = protowire.ConsumeVarint(data)
		case protowire.Fixed64Type:
			f.value, size = protowire.ConsumeFixed64(data)
		case protowire.Fixed32Type:
			var value uint32
			value, size = protowire.ConsumeFixed32(data)
			f.value = uint64(value)
		default:
			return fmt.Errorf("unsupported protobuf field")
		}
		if size < 0 {
			return fmt.Errorf("truncated protobuf field")
		}
		data = data[size:]
		if err := visit(f); err != nil {
			return err
		}
	}
	return nil
}
func atlasUUID(data []byte) (string, error) {
	var high, low uint64
	err := walkFields(data, func(f wireField) error {
		if f.number == 1 || f.number == 2 {
			if f.kind != protowire.Fixed64Type {
				return fmt.Errorf("invalid Atlas UUID")
			}
			if f.number == 1 {
				high = f.value
			} else {
				low = f.value
			}
		}
		return nil
	})
	if err != nil || high == 0 && low == 0 {
		return "", fmt.Errorf("invalid Atlas UUID")
	}
	raw := make([]byte, 16)
	binary.BigEndian.PutUint64(raw, high)
	binary.BigEndian.PutUint64(raw[8:], low)
	s := hex.EncodeToString(raw)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func decodeFriends(payload []byte) ([]Friend, error) {
	var outgoing []byte
	seen := false
	err := walkFields(payload, func(f wireField) error {
		if f.number == 1 {
			if seen || f.kind != protowire.BytesType {
				return fmt.Errorf("invalid outgoing sync")
			}
			seen = true
			outgoing = f.data
		}
		return nil
	})
	if err != nil || !seen {
		return nil, fmt.Errorf("missing outgoing sync")
	}
	full := false
	friends := []Friend{}
	ids := map[string]bool{}
	err = walkFields(outgoing, func(f wireField) error {
		switch f.number {
		case 1:
			if f.kind != protowire.BytesType {
				return fmt.Errorf("invalid sync metadata")
			}
			return walkFields(f.data, func(m wireField) error {
				if m.number == 2 {
					if m.kind != protowire.VarintType {
						return fmt.Errorf("invalid sync type")
					}
					full = m.value == 2
				}
				return nil
			})
		case 2:
			if f.kind != protowire.BytesType {
				return fmt.Errorf("invalid friend")
			}
			var friend Friend
			var relation uint64
			err := walkFields(f.data, func(v wireField) error {
				switch v.number {
				case 1:
					if v.kind != protowire.BytesType {
						return fmt.Errorf("invalid friend UUID")
					}
					var err error
					friend.ID, err = atlasUUID(v.data)
					return err
				case 2, 3:
					if v.kind != protowire.BytesType {
						return fmt.Errorf("invalid friend name")
					}
					if v.number == 2 {
						friend.Username = string(v.data)
					} else {
						friend.DisplayName = string(v.data)
					}
				case 8:
					if v.kind != protowire.VarintType {
						return fmt.Errorf("invalid custom story permission")
					}
					friend.CustomStoryAllowed = v.value != 0
				case 4:
					if v.kind != protowire.VarintType {
						return fmt.Errorf("invalid friend relation")
					}
					relation = v.value
				}
				return nil
			})
			if err != nil {
				return err
			}
			// FRIEND=2 is mutual; pending, following, blocked, deleted and unknown
			// relations must never enter the confirmed recipient snapshot.
			if relation == 2 {
				if friend.ID == "" || ids[friend.ID] {
					return fmt.Errorf("missing or duplicate confirmed friend")
				}
				ids[friend.ID] = true
				friends = append(friends, friend)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !full {
		return nil, fmt.Errorf("expected full friendship synchronization")
	}
	sort.Slice(friends, func(i, j int) bool { return friends[i].ID < friends[j].ID })
	return friends, nil
}

var chromeVersion = regexp.MustCompile(`(?:Chrome|Chromium)/([0-9.]+)`)

func (s *Store) SyncFriends(ctx context.Context, account, cursor string, limit int) (any, error) {
	lock, _ := s.accountLocks.LoadOrStore(account, make(chan struct{}, 1))
	ch := lock.(chan struct{})
	select {
	case ch <- struct{}{}:
		defer func() { <-ch }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.syncFriendsLocked(ctx, account, cursor, limit)
}

func (s *Store) syncFriendsLocked(ctx context.Context, account, cursor string, limit int) (any, error) {
	material, err := s.LoadSecret(ctx, OSVault(), account)
	if err != nil {
		return nil, fail("reauth_required", "capture this account and unlock the OS keyring", 409)
	}
	var snapshot SessionSnapshot
	if json.Unmarshal(material, &snapshot) != nil {
		return nil, fail("invalid_session", "invalid captured session", 409)
	}
	client, err := sessionClient(snapshot)
	if err != nil {
		return nil, err
	}
	// Full outgoing snapshot: outgoingSyncRequest.requestType.token="". The
	// operation reads relationships only; it never accepts/adds/removes friends.
	request := []byte{0, 0, 0, 0, 4, 0x2a, 2, 0x0a, 0}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://web.snapchat.com/com.snapchat.atlas.gw.AtlasGw/SyncFriendData", bytes.NewReader(request))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+snapshot.AuthToken)
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set("x-grpc-web", "1")
	req.Header.Set("x-user-agent", "grpc-web-javascript/0.1")
	req.Header.Set("Origin", "https://www.snapchat.com")
	req.Header.Set("Referer", "https://www.snapchat.com/")
	if snapshot.UserAgent != "" {
		req.Header.Set("User-Agent", snapshot.UserAgent)
	}
	if version := chromeVersion.FindStringSubmatch(snapshot.UserAgent); len(version) == 2 {
		os := "linux"
		if strings.Contains(snapshot.UserAgent, "Windows") {
			os = "windows"
		} else if strings.Contains(snapshot.UserAgent, "Macintosh") {
			os = "macos"
		}
		req.Header.Set("x-snap-client-user-agent", "SnapchatWeb/13.79.0 PROD ("+os+" 0.0.0; chrome "+version[1]+")")
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fail("network_error", "friend synchronization failed; no friendships changed", 502)
	}
	defer response.Body.Close()
	if response.StatusCode == 429 {
		return nil, fail("rate_limited", "friend synchronization throttled; no automatic retry", 429)
	}
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return nil, fail("reauth_required", "refresh this account session before synchronizing friends", 409)
	}
	if response.StatusCode != 200 {
		return nil, fail("sync_failed", "friend synchronization was rejected", 502)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(body) > 4<<20 {
		return nil, fail("sync_failed", "friend response exceeded limit or was truncated", 502)
	}
	payload, err := grpcPayload(base64.StdEncoding.EncodeToString(body))
	if err != nil {
		return nil, fail("sync_failed", "friend synchronization did not contain a successful response", 502)
	}
	friends, err := decodeFriends(payload)
	if err != nil {
		return nil, fail("sync_failed", "friend response was incompatible or incomplete", 502)
	}
	snapshot.Friends, _ = json.Marshal(friends)
	snapshot.FriendsSyncedAt = time.Now().UTC().Format(time.RFC3339Nano)
	material, _ = json.Marshal(snapshot)
	if err = s.SaveSecret(ctx, OSVault(), account, material); err != nil {
		return nil, err
	}
	excluded, err := s.Exclusions(ctx, account)
	if err != nil {
		return nil, err
	}
	blocked := map[string]bool{}
	for _, id := range excluded {
		blocked[id] = true
	}
	items := []Friend{}
	hasMore := false
	for _, friend := range friends {
		if blocked[friend.ID] || friend.ID == snapshot.UserID || friend.ID <= cursor {
			continue
		}
		if len(items) == limit {
			hasMore = true
			break
		}
		items = append(items, friend)
	}
	next := ""
	if hasMore {
		next = items[len(items)-1].ID
	}
	return map[string]any{"items": items, "next_cursor": next, "synced_at": snapshot.FriendsSyncedAt, "source": "standalone", "confirmed_only": true, "total_confirmed": len(friends)}, nil
}
