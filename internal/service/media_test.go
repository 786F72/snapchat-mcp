package service

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestMediaCBCMatchesNativeWebCryptoFormat(t *testing.T) {
	key := make([]byte, 32)
	iv := make([]byte, 16)
	for i := range key {
		key[i] = byte(i)
	}
	for i := range iv {
		iv[i] = byte(i)
	}
	encrypted, err := encryptMedia([]byte("known media fixture"), key, iv)
	if err != nil || hex.EncodeToString(encrypted) != "0c26d0bcd4caa33dc79b61629aa42874b34fa62d6561d9e6dcd5a8ecd5137cd0" {
		t.Fatal("native-compatible AES-CBC fixture failed", err)
	}
}
func TestUploadURLNeverForwardsToArbitraryHosts(t *testing.T) {
	for _, url := range []string{"https://bolt.sc-cdn.net/path?signature=abc", "https://storage.googleapis.com/bucket/path"} {
		if _, err := uploadURL(url); err != nil {
			t.Fatal(err)
		}
	}
	for _, url := range []string{"http://bolt.sc-cdn.net/x", "https://localhost/x", "https://127.0.0.1/x", "https://bolt.sc-cdn.net.evil.example/x", "https://evil.example/?next=storage.googleapis.com", "https://user@bolt.sc-cdn.net/x", "https://bolt.sc-cdn.net:444/x"} {
		if _, err := uploadURL(url); err == nil {
			t.Fatal("accepted unsafe upload host", url)
		}
	}
}
func TestMediaFormatAndReferenceValidation(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	m, err := mediaMetadata(context.Background(), png, "image/png")
	if err != nil || m.Width != 1 || m.Height != 1 {
		t.Fatal(m, err)
	}
	if _, err = mediaMetadata(context.Background(), png, "image/jpeg"); err == nil {
		t.Fatal("mismatched MIME accepted")
	}
	if _, err = mediaMetadata(context.Background(), png, "application/octet-stream"); err == nil {
		t.Fatal("unsupported MIME accepted")
	}
	ref := bytesField(3, []byte("content-object"))
	location := append(bytesField(1, []byte("https://bolt.sc-cdn.net/x")), bytesField(4, ref)...)
	response := bytesField(1, location)
	target, object, err := decodeUploadLocation(response)
	if err != nil || target == "" || string(object) != "content-object" {
		t.Fatal(err)
	}
	if _, _, err = decodeUploadLocation(append(response, response...)); err == nil {
		t.Fatal("multiple upload locations accepted")
	}
	s := testStore(t)
	a := account(t, s, "a")
	b := account(t, s, "b")
	if _, err = s.db.Exec("INSERT INTO media VALUES(?,?,?,?)", a.ID, "handle", []byte("sealed"), now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.loadMedia(context.Background(), b.ID, "handle"); err == nil || publicError(err).Code != "not_found" {
		t.Fatal("cross account media accepted", err)
	}
}
func TestStoryAudienceRequiresExclusionsUnlessExplicitlyOverridden(t *testing.T) {
	friends, _ := json.Marshal([]Friend{{ID: "allowed", CustomStoryAllowed: true}, {ID: "excluded", CustomStoryAllowed: false}})
	snapshot := SessionSnapshot{Friends: friends, FriendsSyncedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if check := checkStoryAudience(snapshot, []string{"excluded"}, "friends"); check.CanPublish || check.Excluded != 1 || !check.ExclusionsEnforced {
		t.Fatal(check)
	}
	if check := checkStoryAudience(snapshot, []string{"excluded"}, "custom"); !check.CanPublish || check.Size != 1 {
		t.Fatal(check)
	}
	if check := checkStoryAudience(snapshot, []string{"allowed"}, "custom"); check.CanPublish || check.Excluded != 1 || !check.ExclusionsEnforced {
		t.Fatal(check)
	}
	if check := checkStoryAudience(snapshot, []string{"excluded"}, "friends", true); !check.CanPublish || check.ExclusionsEnforced || check.Excluded != 1 {
		t.Fatal("explicit override did not allow verified audience", check)
	}
	if check := checkStoryAudience(snapshot, []string{"allowed"}, "custom", false); check.CanPublish || !check.ExclusionsEnforced {
		t.Fatal("false override bypassed exclusions", check)
	}
	if check := checkStoryAudience(snapshot, nil, "public"); check.CanPublish || check.Verified {
		t.Fatal("invalid audience accepted", check)
	}
	empty := snapshot
	empty.Friends = json.RawMessage(`[]`)
	if check := checkStoryAudience(empty, nil, "friends", true); check.CanPublish {
		t.Fatal("empty audience accepted", check)
	}
	snapshot.FriendsSyncedAt = time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	if check := checkStoryAudience(snapshot, nil, "friends", true); check.CanPublish || check.Verified {
		t.Fatal("stale audience accepted", check)
	}
}

func TestMediaUploadBodyLimitAllowsLargerImagesWithoutNetwork(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "a")
	raw, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	raw = append(raw, make([]byte, 1<<20)...)
	body, _ := json.Marshal(map[string]any{"content_base64": base64.StdEncoding.EncodeToString(raw), "mime_type": "image/png"})
	status, result := request(t, s.Handler("secret"), "POST", "/v1/accounts/"+a.ID+"/media/upload", string(body), "secret")
	if status != 409 || result.Error == nil || result.Error.Code != "reauth_required" {
		t.Fatal("large media request did not reach the credential gate", status, result.Error)
	}
}
