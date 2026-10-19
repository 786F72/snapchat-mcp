package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/0xzer/snapper/protos"
	"google.golang.org/protobuf/proto"
)

func frame(flag byte, b []byte) []byte {
	o := make([]byte, 5+len(b))
	o[0] = flag
	binary.BigEndian.PutUint32(o[1:5], uint32(len(b)))
	copy(o[5:], b)
	return o
}
func TestHARBinaryAndFrames(t *testing.T) {
	b, err := harBytes("\u0000\u0080\u00ff", "")
	if err != nil || string(b) != string([]byte{0, 128, 255}) {
		t.Fatal("binary reconstruction corrupted", err)
	}
	if _, err = harBytes("\ufffd", ""); err == nil {
		t.Fatal("lossy body accepted")
	}
	for _, b := range [][]byte{{0}, {0, 0, 0, 0, 2, 1}, frame(1, []byte{1}), frame(128, []byte("grpc-status: 7\r\n"))} {
		if _, err = grpcFrames(b); err == nil {
			t.Fatal("bad frame accepted")
		}
	}
	f, err := grpcFrames(append(frame(0, []byte{128}), frame(128, []byte("grpc-status: 0\r\n"))...))
	if err != nil || len(f) != 1 || f[0][0] != 128 {
		t.Fatal(f, err)
	}
}
func TestInspectDeduplicatesAndDetectsEEL(t *testing.T) {
	id := &protos.UUID{EncodedId: make([]byte, 16)}
	id.EncodedId[15] = 1
	res := &protos.QueryConversationsResponse{Conversations: []*protos.ConversationEntry{{VersionInfo: &protos.ConversationVersionInfo{ConversationId: id, ConversationVersion: 3}, Participants: []*protos.UUID{id}}}, NoMore: true}
	b, err := proto.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var har HAR
	for i := 0; i < 2; i++ {
		var e HAREntry
		e.Request.URL = "https://web.snapchat.com/messagingcoreservice.MessagingCoreService/QueryConversations"
		e.Request.Method = "POST"
		e.Response.Status = 200
		e.Response.Content.Encoding = "base64"
		e.Response.Content.Text = base64.StdEncoding.EncodeToString(frame(0, b))
		har.Log.Entries = append(har.Log.Entries, e)
	}
	req := &protos.CreateContentMessageRequest{Content: &protos.ContentEnvelope{EnvelopeEncryption: &protos.EnvelopeEncryption{Method: &protos.EnvelopeEncryption_EelEncryption{EelEncryption: &protos.EelEncryption{}}}}}
	b, err = proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var send HAREntry
	send.Request.URL = "https://web.snapchat.com/messagingcoreservice.MessagingCoreService/CreateContentMessage"
	send.Request.Method = "POST"
	send.Response.Status = 200
	send.Request.PostData.Encoding = "base64"
	send.Request.PostData.Text = base64.StdEncoding.EncodeToString(frame(0, b))
	har.Log.Entries = append(har.Log.Entries, send)
	path := filepath.Join(t.TempDir(), "test.har")
	data, _ := json.Marshal(har)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := inspectHAR(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Conversations) != 1 || !r.PaginationCompleted || r.SendEncryption["EEL"] != 1 {
		t.Fatalf("wrong report: %+v", r)
	}
}
func TestRecipientsAndJournal(t *testing.T) {
	f := []Friend{{ID: "1", Username: "one"}, {ID: "2", Username: "two"}}
	selected, err := selectFriends(f, "two,2,one", 0)
	if err != nil || len(selected) != 2 || selected[0].ID != "2" {
		t.Fatal(selected, err)
	}
	if _, err = selectFriends(f, "unknown", 0); err == nil {
		t.Fatal("unknown recipient accepted")
	}
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := openJournal(path, "message")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = openJournal(path, "message"); err == nil {
		t.Fatal("concurrent journal use accepted")
	}
	if err = j.Record("1", "pending"); err != nil {
		t.Fatal(err)
	}
	j.Close()
	j, err = openJournal(path, "message")
	if err != nil {
		t.Fatal(err)
	}
	if j.previous["1"] != "pending" {
		t.Fatal("pending attempt lost")
	}
	j.Close()
	if _, err = openJournal(path, "different message"); err == nil {
		t.Fatal("different message accepted")
	}
}
