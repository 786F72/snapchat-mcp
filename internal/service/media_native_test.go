package service

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// This opt-in contract test restores local native keys but replaces ALL network
// access and stops at packet preparation. An expired auth token is sufficient.
// It validates the upload callback through the story path; conversation snap
// delivery still requires a separate authenticated validation.
func TestOfflinePreuploadedMediaNativeContract(t *testing.T) {
	dir, account, mediaID := os.Getenv("QQ_SNAP_OFFLINE_DATA_DIR"), os.Getenv("QQ_SNAP_OFFLINE_ACCOUNT"), os.Getenv("QQ_SNAP_OFFLINE_MEDIA_ID")
	if dir == "" || account == "" || mediaID == "" {
		t.Skip("requires explicit offline account directory and uploaded media handle")
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	material, err := s.contentInput(context.Background(), account, myStoryID, "", "", "prepare_send", "story", mediaID, "friends")
	if err != nil {
		t.Fatal(err)
	}
	source := nativeClientSource()
	source = strings.Replace(source, "const root=process.argv[1],networkFetch=globalThis.fetch;", "const root=process.argv[1],networkFetch=()=>{throw Error('offline contract test refuses network')};", 1)
	if strings.Contains(source, "networkFetch=globalThis.fetch") {
		t.Fatal("network replacement failed")
	}
	// Exercise the new delegate through a supported native destination. Normal
	// story sends use no local refs; conversation snaps use one local handle.
	needle := "const destinations={phoneNumbers:"
	if strings.Count(source, needle) != 1 {
		t.Fatal("native destination construction changed")
	}
	source = strings.Replace(source, needle, "content.localMediaReferences=[{id:new TextEncoder().encode('1')}];"+needle, 1)
	// Djinni caches a native interface ID on each JS delegate. A shared Proxy
	// target aliases these IDs and can dispatch a conversation callback through
	// the wrong native interface (the original snap preparation memory fault).
	source = strings.Replace(source, "const conversationManager=await session.getConversationManager()", "if(conversation._djinni_js_proxy_id==null||feed._djinni_js_proxy_id==null||conversation._djinni_js_proxy_id===feed._djinni_js_proxy_id)throw Error('native delegate identity collision');const conversationManager=await session.getConversationManager()", 1)
	source = strings.Replace(source, "packet:packetInfo,conversation_id:snapshot.conversation_id", "packet:packetInfo,events,conversation_id:snapshot.conversation_id", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--input-type=module", "--eval", source, s.runtimeDir)
	cmd.Stdin = bytes.NewReader(material)
	var stdout bytes.Buffer
	cmd.Stdout = &cappedOutput{buffer: &stdout, remaining: 1 << 20}
	var result struct {
		Ready     bool   `json:"ready_to_submit"`
		Submitted bool   `json:"submitted"`
		Kind      string `json:"content_kind"`
		Events    struct {
			Resolved bool `json:"preuploaded_media_resolved"`
		} `json:"events"`
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("offline native contract failed: %v", err)
	}
	if json.Unmarshal(stdout.Bytes(), &result) != nil || !result.Ready || result.Submitted || !result.Events.Resolved || result.Kind != "story" {
		t.Fatal("delegate did not produce a validated packet without submission")
	}
	t.Log("native upload delegate returned the exact media object and produced a validated packet; no network or submission")
}
