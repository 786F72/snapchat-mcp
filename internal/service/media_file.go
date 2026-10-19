package service

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ConfigureMediaRoots is a local operator setting, never an HTTP/MCP argument.
func (s *Store) ConfigureMediaRoots(roots []string) error {
	configured := []string{}
	for _, name := range roots {
		if !filepath.IsAbs(name) {
			return fail("invalid_argument", "media roots must be absolute directory paths", 400)
		}
		root, err := os.OpenRoot(name)
		if err != nil {
			return fail("invalid_argument", "media root must be an accessible directory", 400)
		}
		root.Close()
		configured = append(configured, filepath.Clean(name))
	}
	s.mediaMu.Lock()
	s.mediaRoots = configured
	s.mediaMu.Unlock()
	return nil
}

func (s *Store) readMediaFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fail("invalid_argument", "path must be an absolute file path on the HTTP server host", 400)
	}
	s.mediaMu.RLock()
	roots := append([]string(nil), s.mediaRoots...)
	s.mediaMu.RUnlock()
	if len(roots) == 0 {
		return nil, fail("media_root_required", "start serve with --media-root pointing to a dedicated media directory", 409)
	}
	for _, name := range roots {
		relative, err := filepath.Rel(name, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			continue
		}
		root, err := os.OpenRoot(name)
		if err != nil {
			return nil, fail("file_unavailable", "media root cannot be opened", 400)
		}
		before, err := root.Stat(relative)
		if err != nil || !before.Mode().IsRegular() {
			root.Close()
			return nil, fail("invalid_argument", "media must be an accessible regular file within the root", 400)
		}
		f, err := root.Open(relative)
		root.Close()
		if err != nil {
			return nil, fail("file_unavailable", "file cannot be opened within the configured media root", 400)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return nil, fail("invalid_argument", "media must be a regular file", 400)
		}
		if info.Size() < 1 || info.Size() > maxMediaBytes {
			return nil, fail("invalid_argument", "media must contain 1 byte to 16 MiB", 400)
		}
		raw, err := io.ReadAll(io.LimitReader(f, maxMediaBytes+1))
		if err != nil {
			return nil, fail("file_unavailable", "media file could not be read", 400)
		}
		if len(raw) < 1 || len(raw) > maxMediaBytes {
			return nil, fail("invalid_argument", "media must contain 1 byte to 16 MiB", 400)
		}
		return raw, nil
	}
	return nil, fail("file_not_allowed", "path is outside configured media roots", 403)
}

func (s *Store) UploadMediaFile(ctx context.Context, account, path, mime string) (any, error) {
	raw, err := s.readMediaFile(path)
	if err != nil {
		return nil, err
	}
	return s.uploadMediaBytes(ctx, account, raw, mime)
}
