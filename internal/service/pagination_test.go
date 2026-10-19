package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/0xzer/snapper/protos"
)

func TestProtocolCursorIsEncryptedScopedAndExpires(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "a")
	b := account(t, s, "b")
	ctx := context.Background()
	v := Vault{Key: func() (string, error) { return strings.Repeat("A", 43) + "=", nil }}
	state := conversationCursor{Token: []byte("private-sync-token"), ConversationID: fixtureConversation, Timestamp: 123}
	id, err := s.saveCursor(ctx, v, a.ID, "conversations", state)
	if err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err = s.db.QueryRow("select state from protocol_cursors where id=?", id).Scan(&sealed); err != nil || strings.Contains(string(sealed), "private-sync-token") {
		t.Fatal(err, "plaintext cursor state")
	}
	var restored conversationCursor
	if err = s.loadCursor(ctx, v, a.ID, "conversations", id, &restored); err != nil || restored.ConversationID != fixtureConversation {
		t.Fatal(restored, err)
	}
	for _, tc := range []struct{ account, kind string }{{b.ID, "conversations"}, {a.ID, "stories"}} {
		if err = s.loadCursor(ctx, v, tc.account, tc.kind, id, &restored); err == nil {
			t.Fatal("cross scope cursor accepted")
		}
	}
	if _, err = s.db.Exec("update protocol_cursors set expires_at=? where id=?", time.Now().Add(-time.Minute).Unix(), id); err != nil {
		t.Fatal(err)
	}
	if err = s.loadCursor(ctx, v, a.ID, "conversations", id, &restored); err == nil {
		t.Fatal("expired cursor accepted")
	}
}
func TestPagedConversationMetadataRejectsAmbiguousIDs(t *testing.T) {
	entry := &protos.ConversationEntry{VersionInfo: &protos.ConversationVersionInfo{ConversationId: fixtureUUID(), ConversationVersion: 17}, Title: "group title", Participants: []*protos.UUID{fixtureUUID()}}
	decoded, err := decodeEntries([]*protos.ConversationEntry{entry})
	if err != nil || len(decoded) != 1 || decoded[0].Version != 17 || decoded[0].Title != "group title" {
		t.Fatal(decoded, err)
	}
	if _, err = decodeEntries([]*protos.ConversationEntry{entry, entry}); err == nil {
		t.Fatal("duplicate conversation accepted")
	}
	entry.Participants = []*protos.UUID{{EncodedId: []byte{1}}}
	if _, err = decodeEntries([]*protos.ConversationEntry{entry}); err == nil {
		t.Fatal("malformed participant accepted")
	}
}
