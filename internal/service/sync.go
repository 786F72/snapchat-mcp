package service

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/0xzer/snapper/protos"
	"google.golang.org/protobuf/proto"
)

type Conversation struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Participants []string `json:"participants"`
	Title        string   `json:"title,omitempty"`
	Version      int64    `json:"version"`
	LastEventAt  int64    `json:"last_event_at,omitempty"`
}

func uuidString(v *protos.UUID) string {
	if v == nil || len(v.EncodedId) != 16 {
		return ""
	}
	s := hex.EncodeToString(v.EncodedId)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func grpcPayload(encoded string) ([]byte, error) {
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(body) > 4<<20 {
		return nil, fmt.Errorf("invalid sync frame")
	}
	frames := [][]byte{}
	trailerSeen := false
	for len(body) > 0 {
		if len(body) < 5 {
			return nil, fmt.Errorf("truncated gRPC frame")
		}
		flag := body[0]
		size := int(binary.BigEndian.Uint32(body[1:5]))
		body = body[5:]
		if size > len(body) {
			return nil, fmt.Errorf("truncated gRPC payload")
		}
		payload := body[:size]
		body = body[size:]
		switch flag {
		case 0:
			if trailerSeen {
				return nil, fmt.Errorf("data after gRPC trailers")
			}
			frames = append(frames, payload)
		case 128:
			trailerSeen = true
			code := -1
			for _, line := range strings.Split(string(payload), "\r\n") {
				k, v, ok := strings.Cut(line, ":")
				if ok && strings.EqualFold(strings.TrimSpace(k), "grpc-status") {
					code, err = strconv.Atoi(strings.TrimSpace(v))
					if err != nil {
						return nil, fmt.Errorf("invalid gRPC status")
					}
				}
			}
			if code != 0 {
				return nil, fmt.Errorf("gRPC sync rejected")
			}
		default:
			return nil, fmt.Errorf("unsupported gRPC frame")
		}
	}
	if !trailerSeen || len(frames) != 1 {
		return nil, fmt.Errorf("expected one successful gRPC response")
	}
	return frames[0], nil
}

func syncMetadata(encoded string) ([]Conversation, error) {
	payload, err := grpcPayload(encoded)
	if err != nil {
		return nil, err
	}

	var response protos.SyncConversationsResponse
	if err = proto.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("incompatible sync protobuf")
	}
	out := []Conversation{}
	for _, entry := range response.GetConversations() {
		if entry.GetIsDeleted() || entry.GetHideFromSendtoAndSearch() {
			continue
		}
		c := Conversation{LastEventAt: entry.GetLastEventTimestamp(), ID: uuidString(entry.GetVersionInfo().GetConversationId()), Type: entry.GetType().String(), Version: entry.GetVersionInfo().GetConversationVersion(), Participants: []string{}}
		if c.ID == "" {
			return nil, fmt.Errorf("incompatible conversation identifier")
		}
		for _, p := range entry.GetParticipants() {
			id := uuidString(p)
			if id == "" {
				return nil, fmt.Errorf("incompatible participant identifier")
			}
			c.Participants = append(c.Participants, id)
		}
		out = append(out, c)
	}
	return out, nil
}
