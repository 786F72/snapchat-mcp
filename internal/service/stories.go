package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"google.golang.org/protobuf/encoding/protowire"
	"strconv"
	"unicode/utf8"
)

// PublishedStory is a local publication receipt, not evidence of current remote
// visibility. Stories may have expired or been removed through another client.
type PublishedStory struct {
	StoryID        string `json:"story_id"`
	IdempotencyKey string `json:"idempotency_key"`
	CreatedAt      string `json:"created_at"`
	Verification   string `json:"verification"`
	RemoteStatus   string `json:"remote_status"`
}

func (s *Store) PublishedStories(ctx context.Context, account, cursor string, limit int) (any, error) {
	var after int64
	if cursor != "" {
		var err error
		after, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || after < 0 {
			return nil, fail("invalid_argument", "invalid publication history cursor", 400)
		}
	}
	if limit < 1 || limit > 500 {
		return nil, fail("invalid_argument", "limit must be 1–500", 400)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT rowid,idempotency_key,receipt,created_at FROM deliveries WHERE account_id=? AND state='sent' AND rowid>? ORDER BY rowid`, account, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []PublishedStory{}
	next := ""
	var last int64
	for rows.Next() {
		var id int64
		var key, created string
		var raw []byte
		if err := rows.Scan(&id, &key, &raw, &created); err != nil {
			return nil, err
		}
		var receipt struct {
			StoryID      string `json:"story_id"`
			Kind         string `json:"content_type"`
			Acknowledged bool   `json:"acknowledged"`
			Verification string `json:"verification"`
		}
		if json.Unmarshal(raw, &receipt) != nil || receipt.Kind != "story" || !receipt.Acknowledged || receipt.StoryID == "" {
			continue
		}
		if len(items) == limit {
			next = strconv.FormatInt(last, 10)
			break
		}
		items = append(items, PublishedStory{StoryID: receipt.StoryID, IdempotencyKey: key, CreatedAt: created, Verification: receipt.Verification, RemoteStatus: "unknown"})
		last = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"source": "local_publication_receipts", "remote_state_verified": false, "items": items, "next_cursor": next}, nil
}

const storyMetadataPath = "/ranking.storymanagement.StoryManagementGatewayService/GetSnapElement"

// StoryMetadata fetches only the exact service-published owned story. It neither
// downloads media nor uploads read receipts. It never returns encryption keys.
func (s *Store) StoryMetadata(ctx context.Context, account, contentID string) (any, error) {
	rawID, err := base64.StdEncoding.Strict().DecodeString(contentID)
	if err != nil || len(rawID) == 0 || len(rawID) > 1024 || !utf8.Valid(rawID) {
		return nil, fail("invalid_argument", "content_id must be a publication story_id", 400)
	}
	var owned int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deliveries WHERE account_id=? AND state='sent' AND json_valid(receipt) AND json_extract(receipt,'$.content_type')='story' AND json_extract(receipt,'$.acknowledged')=1 AND json_extract(receipt,'$.story_id')=?`, account, contentID).Scan(&owned)
	if err != nil {
		return nil, err
	}
	if owned == 0 {
		return nil, fail("not_found", "no acknowledged story publication for this account and content_id", 404)
	}
	snapshot, err := s.readSnapshot(ctx, account)
	if err != nil {
		return nil, err
	}
	request := append(bytesField(1, storyRequestMetadata(snapshot.UserID)), bytesField(2, rawID)...)
	response, err := metadataRPC(ctx, snapshot, storyMetadataPath, request)
	if err != nil {
		return nil, err
	}
	result, err := decodeOwnedStoryMetadata(response, snapshot.UserID, contentID)
	if err != nil {
		return nil, fail("incompatible_protocol", "story metadata did not match the owned publication", 502)
	}
	return result, nil
}

func decodeOwnedStoryMetadata(payload []byte, owner, contentID string) (map[string]any, error) {
	var publisher string
	hasSnap := false
	var status, storyType uint64
	err := walkFields(payload, func(f wireField) error {
		switch f.number {
		case 1:
			if f.kind != protowire.BytesType {
				return fmt.Errorf("invalid story snap")
			}
			hasSnap = len(f.data) > 0
		case 2, 4:
			if f.kind != protowire.VarintType {
				return fmt.Errorf("invalid story status")
			}
			if f.number == 2 {
				status = f.value
			} else {
				storyType = f.value
			}
		case 3:
			if f.kind != protowire.BytesType {
				return fmt.Errorf("invalid story publisher")
			}
			return walkFields(f.data, func(v wireField) error {
				if v.number == 1 {
					if v.kind != protowire.BytesType {
						return fmt.Errorf("invalid publisher ID")
					}
					publisher = string(v.data)
				}
				return nil
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if publisher != "" && publisher != owner {
		return nil, fmt.Errorf("publisher mismatch")
	}
	if hasSnap && publisher != owner {
		return nil, fmt.Errorf("publisher missing")
	}
	remote := "unknown"
	if hasSnap && publisher == owner {
		remote = "present"
	}
	return map[string]any{"story_id": contentID, "owner_id": owner, "status_code": status, "story_type": storyType, "remote_status": remote, "metadata_received": hasSnap, "media_downloaded": false, "marked_viewed": false, "source": "standalone", "checked_at": now()}, nil
}
