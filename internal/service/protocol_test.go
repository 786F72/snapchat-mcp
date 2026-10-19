package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/0xzer/snapper/protos"
	"google.golang.org/protobuf/proto"
)

const fixtureConversation = "00112233-4455-6677-8899-aabbccddeeff"

func fixtureUUID() *protos.UUID {
	b, _ := hex.DecodeString(strings.ReplaceAll(fixtureConversation, "-", ""))
	return &protos.UUID{EncodedId: b}
}
func grpcFixture(t *testing.T, message proto.Message, status string) string {
	t.Helper()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 5)
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	frame = append(frame, payload...)
	trailer := []byte("grpc-status: " + status + "\r\n")
	header := make([]byte, 5)
	header[0] = 128
	binary.BigEndian.PutUint32(header[1:], uint32(len(trailer)))
	frame = append(frame, header...)
	frame = append(frame, trailer...)
	return base64.StdEncoding.EncodeToString(frame)
}
func TestAcknowledgmentMustMatchOneSuccessfulDestination(t *testing.T) {
	response := &protos.CreateContentMessageResponse{ClientResolutionId: 123, Result: []*protos.CreateContentMessageResult{{DestinationId: fixtureUUID(), Success: true, CreatedMessageId: 456}}}
	good := grpcFixture(t, response, "0")
	result, err := sendAcknowledgment(good, fixtureConversation, "123")
	if err != nil || result["server_message_id"] != "456" {
		t.Fatal(result, err)
	}
	for _, tc := range []struct{ conversation, resolution, status string }{{fixtureConversation, "124", "0"}, {"other", "123", "0"}, {fixtureConversation, "123", "7"}} {
		if _, err = sendAcknowledgment(grpcFixture(t, response, tc.status), tc.conversation, tc.resolution); err == nil {
			t.Fatal("accepted mismatched acknowledgment", tc)
		}
	}
	response.Result[0].Success = false
	if _, err = sendAcknowledgment(grpcFixture(t, response, "0"), fixtureConversation, "123"); err == nil {
		t.Fatal("accepted failed destination")
	}
}
func TestSyncFramingAndMetadata(t *testing.T) {
	message := &protos.SyncConversationsResponse{Conversations: []*protos.ConversationEntry{{VersionInfo: &protos.ConversationVersionInfo{ConversationId: fixtureUUID(), ConversationVersion: 8}, Participants: []*protos.UUID{fixtureUUID()}}}}
	encoded := grpcFixture(t, message, "0")
	items, err := syncMetadata(encoded)
	if err != nil || len(items) != 1 || items[0].ID != fixtureConversation {
		t.Fatal(items, err)
	}
	raw, _ := base64.StdEncoding.DecodeString(encoded)
	for _, bad := range [][]byte{raw[:len(raw)-1], append([]byte{1}, raw[1:]...), raw[:4]} {
		if _, err = syncMetadata(base64.StdEncoding.EncodeToString(bad)); err == nil {
			t.Fatal("accepted malformed frame")
		}
	}
	message.Conversations[0].VersionInfo.ConversationId.EncodedId = []byte{1}
	if _, err = syncMetadata(grpcFixture(t, message, "0")); err == nil {
		t.Fatal("accepted malformed UUID")
	}
}
func TestDeliveryReplayNeverRunsAnotherSend(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "one")
	ctx := context.Background()
	receipt := []byte(`{"acknowledged":true,"verification":"fixture"}`)
	for _, state := range []string{"sent", "unknown", "pending"} {
		key := "key-" + state
		_, err := s.db.Exec("INSERT INTO deliveries VALUES(?,?,?,?,?,?)", a.ID, key, sendHash("conversation", "text", ""), state, receipt, now())
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.SendText(ctx, a.ID, "conversation", "text", "", key)
		if state == "sent" {
			if err != nil || result.(map[string]any)["acknowledged"] != true {
				t.Fatal(result, err)
			}
		} else if err == nil || publicError(err).Code != "delivery_unknown" {
			t.Fatal(result, err)
		}
		if _, err = s.SendText(ctx, a.ID, "conversation", "different text", "", key); err == nil || publicError(err).Code != "conflict" {
			t.Fatal("changed payload accepted", err)
		}
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM deliveries").Scan(&count); err != nil || count != 3 {
		t.Fatal(count, err)
	}
}
func TestProgressProtocolRejectsRawDiagnostics(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "one")
	w := &progressWriter{store: s, account: a.ID, key: "test"}
	raw := []byte("untrusted native stderr containing secret material\n")
	_, _ = w.Write(raw)
	event := []byte(`{"event":"native_progress","phase":"submit","path":"/messagingcoreservice.MessagingCoreService/CreateContentMessage"}` + "\n")
	_, _ = w.Write(event[:15])
	_, _ = w.Write(event[15:])
	if !w.Submitted || !w.SawTrace {
		t.Fatal("fragmented progress not observed")
	}
	rows, err := s.Events(context.Background(), a.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(rows.Items)
	if bytes.Contains(encoded, []byte("secret material")) {
		t.Fatal("raw native stderr leaked")
	}
}

func TestStoryAcknowledgmentMustMatchPublishedDestination(t *testing.T) {
	response := &protos.CreateContentMessageResponse{ClientResolutionId: 123, Result: []*protos.CreateContentMessageResult{{Success: true, DestinationRes: &protos.CreateContentMessageResult_StoryDestinationResult{StoryDestinationResult: &protos.StoryDestinationResult{StoryDestinationId: parseUUID(myStoryID), StoryId: []byte("story-id")}}}}}
	receipt, err := contentAcknowledgment(grpcFixture(t, response, "0"), myStoryID, "123", "story")
	if err != nil || receipt["acknowledged"] != true {
		t.Fatal(receipt, err)
	}
	if _, err = contentAcknowledgment(grpcFixture(t, response, "0"), fixtureConversation, "123", "story"); err == nil {
		t.Fatal("wrong story destination accepted")
	}
	if _, err = contentAcknowledgment(grpcFixture(t, response, "0"), myStoryID, "123", "snap"); err == nil {
		t.Fatal("story acknowledgment accepted for a snap")
	}
}
