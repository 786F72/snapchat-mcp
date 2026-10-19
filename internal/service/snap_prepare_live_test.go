package service

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// Opt-in authenticated checks exercise the production packet guard. They stop
// before CreateContentMessage submission and never open incoming media.
func TestLiveSnapPreparation(t *testing.T) {
	dir, account, media := os.Getenv("QQ_SNAP_LIVE_DATA_DIR"), os.Getenv("QQ_SNAP_LIVE_ACCOUNT"), os.Getenv("QQ_SNAP_LIVE_MEDIA_ID")
	if dir == "" || account == "" || media == "" {
		t.Skip("requires explicit live account, data directory, and uploaded media handle")
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, test := range []struct{ name, env, encryption string }{{"direct", "QQ_SNAP_LIVE_DIRECT_CONVERSATION", "eel"}, {"group", "QQ_SNAP_LIVE_GROUP_CONVERSATION", "tls_only"}} {
		t.Run(test.name, func(t *testing.T) {
			conversation := os.Getenv(test.env)
			if conversation == "" {
				t.Skip("requires explicit conversation")
			}
			result := s.Call(context.Background(), "snaps.prepare", map[string]any{"account_id": account, "conversation_id": conversation, "media_id": media})
			if result.Error != nil {
				t.Fatal(result.Error)
			}
			raw, err := json.Marshal(result.Data)
			if err != nil {
				t.Fatal(err)
			}
			var prepared struct {
				Ready      bool   `json:"ready_to_submit"`
				Submitted  bool   `json:"submitted"`
				Encryption string `json:"encryption"`
				Kind       string `json:"content_kind"`
				Packet     struct {
					WireType int `json:"wire_content_type"`
				} `json:"packet"`
			}
			if json.Unmarshal(raw, &prepared) != nil || !prepared.Ready || prepared.Submitted || prepared.Encryption != test.encryption || prepared.Kind != "snap" || prepared.Packet.WireType != 0 {
				t.Fatal("snap did not produce a validated, unsubmitted packet with expected encryption")
			}
		})
	}
}
