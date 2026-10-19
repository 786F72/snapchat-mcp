package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"strings"

	"google.golang.org/protobuf/proto"
)

// readRPC deliberately has no arbitrary URL or mutation method support.
func readRPC(ctx context.Context, snapshot SessionSnapshot, method string, request proto.Message, response proto.Message) error {
	if method != "QueryConversations" {
		return fail("unsupported_capability", "RPC is not an allowed metadata read", 501)
	}
	payload, err := proto.Marshal(request)
	if err != nil {
		return err
	}
	payload, err = metadataRPC(ctx, snapshot, "/messagingcoreservice.MessagingCoreService/"+method, payload)
	if err != nil {
		return err
	}
	if proto.Unmarshal(payload, response) != nil {
		return fail("incompatible_protocol", "metadata response could not be decoded", 502)
	}
	return nil
}

func metadataRPC(ctx context.Context, snapshot SessionSnapshot, path string, payload []byte) ([]byte, error) {
	if path != "/messagingcoreservice.MessagingCoreService/QueryConversations" && path != "/snapchat.content.v2.MediaDeliveryService/getUploadLocations" && path != incomingRequestsPath && path != publicInfoPath && path != storyStatusPath && path != storyMetadataPath {
		return nil, fail("unsupported_capability", "RPC path is not allowed", 501)
	}
	client, err := sessionClient(snapshot)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 5)
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	frame = append(frame, payload...)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://web.snapchat.com"+path, bytes.NewReader(frame))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+snapshot.AuthToken)
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set("x-grpc-web", "1")
	if path == storyStatusPath || path == storyMetadataPath {
		req.Header.Set("X-Snap-Route-Tag", "mm")
	}
	req.Header.Set("x-user-agent", "grpc-web-javascript/0.1")
	req.Header.Set("Origin", "https://www.snapchat.com")
	req.Header.Set("Referer", "https://www.snapchat.com/")
	req.Header.Set("User-Agent", snapshot.UserAgent)
	os := "linux"
	if strings.Contains(snapshot.UserAgent, "Windows") {
		os = "windows"
	} else if strings.Contains(snapshot.UserAgent, "Macintosh") {
		os = "macos"
	}
	if version := chromeVersion.FindStringSubmatch(snapshot.UserAgent); len(version) == 2 {
		req.Header.Set("x-snap-client-user-agent", "SnapchatWeb/13.79.0 PROD ("+os+" 0.0.0; chrome "+version[1]+")")
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fail("network_error", "metadata request failed; no automatic retry", 502)
	}
	defer res.Body.Close()
	if res.StatusCode == 429 {
		return nil, fail("rate_limited", "metadata request throttled; no automatic retry", 429)
	}
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return nil, fail("reauth_required", "refresh this account session", 409)
	}
	if res.StatusCode != 200 {
		return nil, fail("sync_failed", "metadata request rejected", 502)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil || len(body) > 4<<20 {
		return nil, fail("sync_failed", "metadata response exceeded limit or was truncated", 502)
	}
	payload, err = grpcPayload(base64.StdEncoding.EncodeToString(body))
	if err != nil {
		return nil, fail("sync_failed", "metadata request did not contain a successful gRPC response", 502)
	}
	return payload, nil
}
