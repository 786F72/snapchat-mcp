package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

const maxMediaBytes = 16 << 20

type Media struct {
	ID            string `json:"id"`
	MIME          string `json:"mime_type"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	DurationMS    int64  `json:"duration_ms,omitempty"`
	HasAudio      bool   `json:"has_audio"`
	SHA256        string `json:"sha256"`
	Size          int    `json:"size"`
	Key           []byte `json:"key"`
	IV            []byte `json:"iv"`
	ContentObject []byte `json:"content_object"`
	ExpiresAt     int64  `json:"expires_at"`
}

func bytesField(n protowire.Number, b []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, n, protowire.BytesType), b)
}
func numberField(n protowire.Number, v uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, n, protowire.VarintType), v)
}
func mediaMetadata(ctx context.Context, raw []byte, mime string) (Media, error) {
	m := Media{MIME: mime, Size: len(raw)}
	if len(raw) == 0 || len(raw) > maxMediaBytes {
		return m, fail("invalid_argument", "media must be 1 byte–16 MiB", 400)
	}
	switch mime {
	case "image/png", "image/jpeg":
		config, format, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil || format == "png" && mime != "image/png" || format == "jpeg" && mime != "image/jpeg" {
			return m, fail("invalid_argument", "image bytes do not match the declared PNG/JPEG type", 400)
		}
		m.Width = config.Width
		m.Height = config.Height
	case "video/mp4":
		if len(raw) < 12 || string(raw[4:8]) != "ftyp" {
			return m, fail("invalid_argument", "expected an MP4 container", 400)
		}
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(probeCtx, "ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", "-i", "pipe:0")
		cmd.Stdin = bytes.NewReader(raw)
		var output bytes.Buffer
		cmd.Stdout = &cappedOutput{buffer: &output, remaining: 1 << 20}
		if cmd.Run() != nil {
			return m, fail("invalid_argument", "MP4 metadata could not be read; ffprobe is required", 400)
		}
		var probe struct {
			Streams []struct {
				CodecType     string `json:"codec_type"`
				CodecName     string `json:"codec_name"`
				Width, Height int
			}
			Format struct{ Duration string }
		}
		if json.Unmarshal(output.Bytes(), &probe) != nil {
			return m, fail("invalid_argument", "invalid MP4 metadata", 400)
		}
		videoCount := 0
		for _, stream := range probe.Streams {
			switch stream.CodecType {
			case "video":
				videoCount++
				if stream.CodecName != "h264" {
					return m, fail("unsupported_media", "MP4 requires H.264 video", 400)
				}
				m.Width = stream.Width
				m.Height = stream.Height
			case "audio":
				if stream.CodecName != "aac" {
					return m, fail("unsupported_media", "MP4 requires AAC audio", 400)
				}
				m.HasAudio = true
			}
		}
		duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
		if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > 60 || videoCount != 1 {
			return m, fail("invalid_argument", "expected one video stream lasting at most 60 seconds", 400)
		}
		m.DurationMS = int64(duration * 1000)
	default:
		return m, fail("unsupported_media", "supported media: PNG, JPEG, and H.264/AAC MP4", 400)
	}
	if m.Width < 1 || m.Height < 1 || m.Width > 8192 || m.Height > 8192 || int64(m.Width)*int64(m.Height) > 40_000_000 {
		return m, fail("invalid_argument", "media dimensions exceed supported limits", 400)
	}
	sum := sha256.Sum256(raw)
	m.SHA256 = hex.EncodeToString(sum[:])
	return m, nil
}
func encryptMedia(raw, key, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padding := aes.BlockSize - len(raw)%aes.BlockSize
	plain := append(append([]byte(nil), raw...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	encrypted := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, plain)
	return encrypted, nil
}
func uploadURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Port() != "" && u.Port() != "443" {
		return nil, fail("invalid_upload_url", "unexpected upload destination", 502)
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil || !(strings.HasSuffix(host, ".sc-cdn.net") || strings.HasSuffix(host, ".sc-prod.net") || host == "storage.googleapis.com" || strings.HasSuffix(host, ".storage.googleapis.com")) {
		return nil, fail("invalid_upload_url", "upload destination is outside the supported Snapchat storage hosts", 502)
	}
	return u, nil
}
func decodeUploadLocation(payload []byte) (string, []byte, error) {
	var location []byte
	count := 0
	err := walkFields(payload, func(f wireField) error {
		if f.number == 1 && f.kind == protowire.BytesType {
			count++
			location = f.data
		}
		return nil
	})
	if err != nil || count != 1 {
		return "", nil, fail("incompatible_protocol", "expected one upload location", 502)
	}
	var target string
	var content []byte
	err = walkFields(location, func(f wireField) error {
		if f.number == 1 && f.kind == protowire.BytesType {
			target = string(f.data)
		}
		if f.number == 4 && f.kind == protowire.BytesType {
			return walkFields(f.data, func(v wireField) error {
				if v.number == 3 && v.kind == protowire.BytesType {
					content = append([]byte(nil), v.data...)
				}
				if v.number == 1 && v.value != 0 {
					return fail("upload_failed", "storage did not return a successful content reference", 502)
				}
				return nil
			})
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	if target == "" || len(content) == 0 {
		return "", nil, fail("incompatible_protocol", "missing upload URL or content object", 502)
	}
	return target, content, nil
}
func (s *Store) UploadMedia(ctx context.Context, account, encoded, mime string) (any, error) {
	if len(encoded) > base64.StdEncoding.EncodedLen(maxMediaBytes) {
		return nil, fail("invalid_argument", "media exceeds 16 MiB", 400)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fail("invalid_argument", "content_base64 is invalid", 400)
	}
	return s.uploadMediaBytes(ctx, account, raw, mime)
}

func (s *Store) uploadMediaBytes(ctx context.Context, account string, raw []byte, mime string) (any, error) {
	media, err := mediaMetadata(ctx, raw, mime)
	if err != nil {
		return nil, err
	}
	material, err := s.LoadSecret(ctx, s.vault, account)
	if err != nil {
		return nil, fail("reauth_required", "capture this account and unlock the OS keyring", 409)
	}
	var snapshot SessionSnapshot
	if json.Unmarshal(material, &snapshot) != nil {
		return nil, fail("invalid_session", "invalid captured session", 409)
	}
	media.Key = make([]byte, 32)
	media.IV = make([]byte, 16)
	if _, err = io.ReadFull(rand.Reader, media.Key); err != nil {
		return nil, err
	}
	if _, err = io.ReadFull(rand.Reader, media.IV); err != nil {
		return nil, err
	}
	encrypted, err := encryptMedia(raw, media.Key, media.IV)
	if err != nil {
		return nil, err
	}
	request := append(numberField(2, 1), numberField(4, 1)...)
	request = append(request, bytesField(7, []byte{0})...)
	request = append(request, numberField(16, 1)...)
	payload, err := metadataRPC(ctx, snapshot, "/snapchat.content.v2.MediaDeliveryService/getUploadLocations", request)
	if err != nil {
		return nil, err
	}
	target, object, err := decodeUploadLocation(payload)
	if err != nil {
		return nil, err
	}
	u, err := uploadURL(target)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", u.String(), bytes.NewReader(encrypted))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	// Signed storage receives ciphertext only. No Snapchat authorization or
	// cookies are forwarded, and redirects are never followed.
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, fail("upload_unknown", "encrypted upload did not complete; no snap was submitted", 502)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fail("upload_failed", "encrypted storage upload was rejected", 502)
	}
	media.ID = newID()
	media.ContentObject = object
	media.ExpiresAt = time.Now().Add(24 * time.Hour).Unix()
	plain, _ := json.Marshal(media)
	sealed, err := s.vault.Seal(account+"/media/"+media.ID, plain)
	if err != nil {
		return nil, err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO media VALUES(?,?,?,?)", account, media.ID, sealed, now()); err != nil {
		return nil, err
	}
	return map[string]any{"media_id": media.ID, "mime_type": media.MIME, "width": media.Width, "height": media.Height, "size": media.Size, "sha256": media.SHA256, "expires_at": media.ExpiresAt, "encryption": "aes_256_cbc", "uploaded": true}, nil
}
func (s *Store) loadMedia(ctx context.Context, account, id string) (Media, error) {
	var media Media
	var sealed []byte
	if s.db.QueryRowContext(ctx, "SELECT sealed FROM media WHERE account_id=? AND id=?", account, id).Scan(&sealed) != nil {
		return media, fail("not_found", "media does not exist in this account", 404)
	}
	plain, err := s.vault.Open(account+"/media/"+id, sealed)
	if err != nil || json.Unmarshal(plain, &media) != nil {
		return media, fail("vault_unavailable", "media record could not be decrypted", 503)
	}
	if media.ExpiresAt <= time.Now().Unix() {
		return media, fail("media_expired", "upload media again before sending", 409)
	}
	return media, nil
}
