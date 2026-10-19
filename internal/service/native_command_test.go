package service

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestNativeCommandAvoidsWindowsArgumentLimitAndCredentialArgs(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("requires Node.js")
	}
	source := "//" + strings.Repeat("padding", 6000) + "\nconsole.log(JSON.stringify({valid:globalThis.__qqSnapshot.test==='private-fixture'}));"
	cmd := nativeCommand(context.Background(), source, t.TempDir(), []byte(`{"test":"private-fixture"}`))
	args := strings.Join(cmd.Args, " ")
	if len(args) > 4096 || strings.Contains(args, "private-fixture") || strings.Contains(args, "padding") {
		t.Fatal("native source or credentials were placed on argv")
	}
	output, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(output)) != `{"valid":true}` {
		t.Fatal("large native source did not execute through stdin", err)
	}
	cmd = nativeCommand(context.Background(), source, t.TempDir(), []byte(`bad private-fixture`))
	output, err = cmd.Output()
	if err == nil || strings.Contains(string(output), "private-fixture") {
		t.Fatal("invalid snapshot failed to produce a redacted failure")
	}
}
