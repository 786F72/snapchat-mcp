package service

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestMediaRootConfinement(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	outside := t.TempDir()
	good := filepath.Join(root, "photo.png")
	secret := filepath.Join(outside, "private.png")
	if err := os.WriteFile(good, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readMediaFile(good); publicError(err).Code != "media_root_required" {
		t.Fatal(err)
	}
	if err := s.ConfigureMediaRoots([]string{root}); err != nil {
		t.Fatal(err)
	}
	raw, err := s.readMediaFile(good)
	if err != nil || string(raw) != "image" {
		t.Fatal(err)
	}
	if _, err = s.readMediaFile(secret); publicError(err).Code != "file_not_allowed" {
		t.Fatal(err)
	}
	t.Run("escaping symlink", func(t *testing.T) {
		escape := filepath.Join(root, "escape.png")
		if err := os.Symlink(secret, escape); err != nil {
			if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) {
				t.Skip("Windows requires symlink privilege")
			}
			t.Fatal(err)
		}
		if raw, err := s.readMediaFile(escape); err == nil || len(raw) != 0 {
			t.Fatal("symlink escaped the root")
		}
	})
	if _, err = s.readMediaFile(root); err == nil {
		t.Fatal("read a directory")
	}
	if _, err = s.readMediaFile("photo.png"); err == nil {
		t.Fatal("accepted relative path")
	}
	empty := filepath.Join(root, "empty.png")
	os.WriteFile(empty, nil, 0600)
	if _, err = s.readMediaFile(empty); err == nil {
		t.Fatal("accepted empty media")
	}
	huge := filepath.Join(root, "huge.mp4")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(maxMediaBytes + 1)
	f.Close()
	if _, err = s.readMediaFile(huge); err == nil {
		t.Fatal("accepted oversized media")
	}
	if err = s.ConfigureMediaRoots(nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.readMediaFile(good); publicError(err).Code != "media_root_required" {
		t.Fatal("disable did not revoke roots", err)
	}
}

func TestFileUploadValidatesMediaBeforeCredentials(t *testing.T) {
	s := testStore(t)
	a := account(t, s, "test")
	root := t.TempDir()
	if err := s.ConfigureMediaRoots([]string{root}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "photo.png")
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a5WQAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, png, 0600)
	_, err = s.UploadMediaFile(context.Background(), a.ID, path, "image/png")
	if publicError(err).Code != "reauth_required" {
		t.Fatal("valid file did not reach the shared upload pipeline", err)
	}
	os.WriteFile(path, []byte("not media"), 0600)
	_, err = s.UploadMediaFile(context.Background(), a.ID, path, "image/png")
	if publicError(err).Code != "invalid_argument" {
		t.Fatal("invalid bytes were not rejected before credentials", err)
	}
}
