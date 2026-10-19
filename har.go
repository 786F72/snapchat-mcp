package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/0xzer/snapper/protos"
	"google.golang.org/protobuf/proto"
)

type HAR struct {
	Log struct {
		Entries []HAREntry `json:"entries"`
	} `json:"log"`
}
type HAREntry struct {
	Request struct {
		URL      string `json:"url"`
		Method   string `json:"method"`
		PostData struct {
			Text     string `json:"text"`
			Encoding string `json:"encoding"`
		} `json:"postData"`
	} `json:"request"`
	Response struct {
		Status  int `json:"status"`
		Content struct {
			Text     string `json:"text"`
			Encoding string `json:"encoding"`
		} `json:"content"`
	} `json:"response"`
}
type CaptureConversation struct {
	ID             string   `json:"id"`
	Version        int64    `json:"version"`
	ParticipantIDs []string `json:"participant_ids"`
	Deleted        bool     `json:"deleted"`
	Hidden         bool     `json:"hidden"`
	Type           string   `json:"type"`
}
type CaptureReport struct {
	Entries             int                   `json:"entries"`
	Endpoints           map[string]int        `json:"endpoints"`
	Conversations       []CaptureConversation `json:"conversations"`
	PaginationCompleted bool                  `json:"pagination_completed"`
	SendRequests        int                   `json:"send_requests"`
	SendEncryption      map[string]int        `json:"send_encryption"`
	Warnings            []string              `json:"warnings"`
}

// Chromium HAR represents request bytes as Unicode codepoints 0..255.
// UTF-8 encoding that string would silently corrupt binary protobuf payloads.
func harBytes(text, encoding string) ([]byte, error) {
	if encoding == "base64" {
		return base64.StdEncoding.DecodeString(text)
	}
	if encoding != "" {
		return nil, errors.New("unsupported HAR body encoding")
	}
	b := make([]byte, 0, len(text))
	for _, r := range text {
		if r > 255 {
			return nil, errors.New("HAR body contains non-byte codepoints; original bytes cannot be recovered")
		}
		b = append(b, byte(r))
	}
	return b, nil
}

func grpcFrames(body []byte) ([][]byte, error) {
	var frames [][]byte
	for len(body) > 0 {
		if len(body) < 5 {
			return nil, errors.New("truncated gRPC-web frame header")
		}
		flag := body[0]
		n := uint64(binary.BigEndian.Uint32(body[1:5]))
		body = body[5:]
		if n > uint64(len(body)) {
			return nil, errors.New("truncated gRPC-web frame payload")
		}
		frame := body[:int(n)]
		body = body[int(n):]
		switch flag {
		case 0:
			frames = append(frames, frame)
		case 128:
			for _, line := range strings.Split(string(frame), "\r\n") {
				name, value, ok := strings.Cut(line, ":")
				if ok && strings.EqualFold(strings.TrimSpace(name), "grpc-status") {
					status, err := strconv.Atoi(strings.TrimSpace(value))
					if err != nil || status != 0 {
						return nil, errors.New("non-success gRPC-web trailer")
					}
				}
			}
		default:
			return nil, errors.New("unsupported compressed gRPC-web frame")
		}
	}
	return frames, nil
}

func uuidText(u *protos.UUID) string {
	if u == nil || len(u.EncodedId) != 16 {
		return ""
	}
	b := u.EncodedId
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func inspectHAR(path string) (*CaptureReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var har HAR
	if err = json.Unmarshal(data, &har); err != nil {
		return nil, err
	}
	r := &CaptureReport{Entries: len(har.Log.Entries), Endpoints: map[string]int{}, SendEncryption: map[string]int{}, Warnings: []string{"Conversation participants are not a verified current friends list."}}
	conversations := map[string]CaptureConversation{}
	for i, e := range har.Log.Entries {
		u, err := url.Parse(e.Request.URL)
		if err != nil {
			return nil, fmt.Errorf("entry %d has an invalid URL", i)
		}
		if u.Hostname() != "web.snapchat.com" || !strings.HasPrefix(u.Path, "/messagingcoreservice.MessagingCoreService/") {
			continue
		}
		method := strings.TrimPrefix(u.Path, "/messagingcoreservice.MessagingCoreService/")
		r.Endpoints[method]++
		if e.Request.Method != "POST" || e.Response.Status != 200 {
			continue
		}
		if method == "CreateContentMessage" {
			b, err := harBytes(e.Request.PostData.Text, e.Request.PostData.Encoding)
			if err != nil {
				return nil, fmt.Errorf("entry %d: %w", i, err)
			}
			frames, err := grpcFrames(b)
			if err != nil {
				return nil, fmt.Errorf("entry %d: %w", i, err)
			}
			for _, frame := range frames {
				var req protos.CreateContentMessageRequest
				if err = proto.Unmarshal(frame, &req); err != nil {
					return nil, err
				}
				r.SendRequests++
				enc := req.GetContent().GetEnvelopeEncryption()
				mode := "unknown"
				switch enc.GetMethod().(type) {
				case *protos.EnvelopeEncryption_EelEncryption:
					mode = "EEL"
				case *protos.EnvelopeEncryption_None:
					mode = "none"
				case *protos.EnvelopeEncryption_FideliusEncryption:
					mode = "Fidelius"
				case *protos.EnvelopeEncryption_ConversationV1:
					mode = "conversation_v1"
				}
				r.SendEncryption[mode]++
			}
			continue
		}
		if method != "SyncConversations" && method != "QueryConversations" {
			continue
		}
		b, err := harBytes(e.Response.Content.Text, e.Response.Content.Encoding)
		if err != nil {
			return nil, err
		}
		frames, err := grpcFrames(b)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
		for _, frame := range frames {
			var entries []*protos.ConversationEntry
			if method == "SyncConversations" {
				var res protos.SyncConversationsResponse
				err = proto.Unmarshal(frame, &res)
				entries = res.GetConversations()
			} else {
				var res protos.QueryConversationsResponse
				err = proto.Unmarshal(frame, &res)
				entries = res.GetConversations()
				if res.GetNoMore() {
					r.PaginationCompleted = true
				}
			}
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				id := uuidText(entry.GetVersionInfo().GetConversationId())
				if id == "" {
					return nil, errors.New("conversation has an invalid UUID")
				}
				c := CaptureConversation{ID: id, Version: entry.GetVersionInfo().GetConversationVersion(), Deleted: entry.GetIsDeleted(), Hidden: entry.GetHideFromSendtoAndSearch(), Type: entry.GetType().String(), ParticipantIDs: []string{}}
				for _, p := range entry.GetParticipants() {
					pid := uuidText(p)
					if pid != "" {
						c.ParticipantIDs = append(c.ParticipantIDs, pid)
					}
				}
				sort.Strings(c.ParticipantIDs)
				old, ok := conversations[id]
				if !ok || c.Version >= old.Version {
					conversations[id] = c
				}
			}
		}
	}
	for _, c := range conversations {
		r.Conversations = append(r.Conversations, c)
	}
	sort.Slice(r.Conversations, func(i, j int) bool {
		return bytes.Compare([]byte(r.Conversations[i].ID), []byte(r.Conversations[j].ID)) < 0
	})
	return r, nil
}
