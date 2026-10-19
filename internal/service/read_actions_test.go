package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

const readFixtureID = "11111111-2222-3333-4444-555555555555"

func TestIncomingRequestsRequireCompleteUnambiguousSnapshot(t *testing.T) {
	friend := append(bytesField(1, rankingUUID(readFixtureID)), bytesField(2, []byte("fixture-user"))...)
	friend = append(friend, bytesField(3, []byte("Fixture"))...)
	friend = append(friend, numberField(5, 12345)...)
	full := append(bytesField(1, numberField(2, 2)), bytesField(2, friend)...)
	items, err := decodeIncomingRequests(full)
	if err != nil || len(items) != 1 || items[0].ID != readFixtureID || items[0].Username != "fixture-user" || items[0].AddedAt != 12345 {
		t.Fatal(items, err)
	}
	for name, payload := range map[string][]byte{
		"delta":              append(bytesField(1, numberField(2, 1)), bytesField(2, friend)...),
		"duplicate_metadata": append(full, bytesField(1, numberField(2, 2))...),
		"duplicate_friend":   append(full, bytesField(2, friend)...),
		"missing_id":         append(bytesField(1, numberField(2, 2)), bytesField(2, bytesField(2, []byte("unknown")))...),
		"truncated":          full[:len(full)-1],
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeIncomingRequests(payload); err == nil {
				t.Fatal("incomplete or ambiguous request response accepted")
			}
		})
	}
}

func TestPublicProfileMustMatchExactlyOneRequestedUser(t *testing.T) {
	record := append(bytesField(1, parseUUID(readFixtureID).EncodedId), bytesField(2, []byte("fixture"))...)
	record = append(record, numberField(4, 1)...)
	response := bytesField(1, record)
	profile, err := decodePublicProfile(response, readFixtureID)
	if err != nil || profile.ID != readFixtureID || !profile.Official {
		t.Fatal(profile, err)
	}
	if _, err := decodePublicProfile(response, "66666666-7777-8888-9999-aaaaaaaaaaaa"); err == nil {
		t.Fatal("wrong recipient accepted")
	}
	if _, err := decodePublicProfile(append(response, response...), readFixtureID); err == nil {
		t.Fatal("multiple profiles accepted")
	}
	if _, err := decodePublicProfile(nil, readFixtureID); publicError(err).Code != "not_found" {
		t.Fatal(err)
	}
	s := testStore(t)
	a := account(t, s, "one")
	if err := s.ReplaceExclusions(context.Background(), a.ID, []string{readFixtureID}); err != nil {
		t.Fatal(err)
	}
	// Exclusions are checked before credentials or any outbound request.
	result := s.Call(context.Background(), "profiles.get", map[string]any{"account_id": a.ID, "recipient_id": strings.ToUpper(readFixtureID)})
	if result.Error == nil || result.Error.Status != 403 {
		t.Fatal(result)
	}
}

func TestStoryStatusCountersDoNotProveDeletion(t *testing.T) {
	payload := bytesField(1, []byte{123, 0}) // packed timestamp fields, including zero
	count := append(bytesField(1, rankingUUID(readFixtureID)), numberField(2, 4)...)
	payload = append(payload, bytesField(2, count)...)
	timestamps, counts, err := decodeActiveStoryStatus(payload)
	if err != nil || len(timestamps) != 2 || timestamps[0] != 123 || len(counts) != 1 || counts[0]["story_id"] != readFixtureID || counts[0]["count"] != uint64(4) {
		t.Fatal(timestamps, counts, err)
	}
	if _, _, err := decodeActiveStoryStatus(bytesField(1, []byte{128})); err == nil {
		t.Fatal("truncated packed field accepted")
	}
	result, err := decodeOwnedStoryMetadata(nil, readFixtureID, "opaque")
	if err != nil || result["remote_status"] != "unknown" {
		t.Fatal(result, err)
	}
}

func TestOwnedStoryMetadataStripsMediaAndRejectsOtherOwners(t *testing.T) {
	// A fixture snap contains key-looking bytes. They must never reach the result.
	payload := bytesField(1, bytesField(1, []byte("PRIVATE-MEDIA-KEY")))
	payload = append(payload, bytesField(3, bytesField(1, []byte(readFixtureID)))...)
	result, err := decodeOwnedStoryMetadata(payload, readFixtureID, "opaque")
	raw, _ := json.Marshal(result)
	if err != nil || result["remote_status"] != "present" || strings.Contains(string(raw), "PRIVATE-MEDIA-KEY") || result["marked_viewed"] != false {
		t.Fatal(string(raw), err)
	}
	if _, err := decodeOwnedStoryMetadata(payload, "66666666-7777-8888-9999-aaaaaaaaaaaa", "opaque"); err == nil {
		t.Fatal("different publisher accepted")
	}
	s := testStore(t)
	a := account(t, s, "one")
	b := account(t, s, "two")
	contentID := base64.StdEncoding.EncodeToString([]byte("fixture-snap-id"))
	receipt, _ := json.Marshal(map[string]any{"acknowledged": true, "content_type": "story", "story_id": contentID})
	if _, err := s.db.Exec("INSERT INTO deliveries VALUES(?,?,?,?,?,?)", a.ID, "publish", "hash", "sent", receipt, now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoryMetadata(context.Background(), b.ID, contentID); publicError(err).Code != "not_found" {
		t.Fatal("cross-account receipt accepted", err)
	}
}

func TestIncomingRequestPaginationFiltersExcludedIgnoredAndSelf(t *testing.T) {
	items := []IncomingRequest{{ID: "a"}, {ID: "b"}, {ID: "c", Ignored: true}, {ID: "d"}, {ID: "e"}, {ID: "f"}}
	page, next := incomingRequestPage(items, []string{"b"}, "a", "", 1)
	if len(page) != 1 || page[0].ID != "d" || next != "d" {
		t.Fatal(page, next)
	}
	page, next = incomingRequestPage(items, []string{"b", "f"}, "a", next, 1)
	if len(page) != 1 || page[0].ID != "e" || next != "" {
		t.Fatal(page, next)
	}
}
