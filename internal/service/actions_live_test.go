package service

import (
	"context"
	"os"
	"testing"
)

func TestLiveGroupActionPreparation(t *testing.T) {
	account, dir, group := os.Getenv("QQ_SNAP_LIVE_ACCOUNT"), os.Getenv("QQ_SNAP_LIVE_DATA_DIR"), os.Getenv("QQ_SNAP_LIVE_GROUP_CONVERSATION")
	if account == "" || dir == "" || group == "" {
		t.Skip("requires explicit account, directory and group")
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, action := range []string{"rename", "leave"} {
		title := ""
		if action == "rename" {
			title = "Snapchat MCP validation"
		}
		r, err := s.PrepareGroupAction(context.Background(), account, group, action, title)
		if err != nil {
			t.Fatal(err)
		}
		if r["prepared"] != true || r["submitted"] != false {
			t.Fatal("group preparation did not confirm zero submissions")
		}
		t.Logf("%s validated without submission", action)
	}
}
