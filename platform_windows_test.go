package main

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsTokenACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if _, err := tokenFile(path, true); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	defer protectTokenFile(path)
	if _, err = tokenFile(path, false); err == nil {
		t.Fatal("public token accepted")
	}
}
