package service

import (
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/0xzer/snapper/protos"
	"google.golang.org/protobuf/proto"
)

func sendAcknowledgment(encoded, conversationID, resolutionID string) (map[string]any, error) {
	return contentAcknowledgment(encoded, conversationID, resolutionID, "text")
}

func contentAcknowledgment(encoded, conversationID, resolutionID, kind string) (map[string]any, error) {
	payload, err := grpcPayload(encoded)
	if err != nil {
		return nil, err
	}
	var response protos.CreateContentMessageResponse
	if proto.Unmarshal(payload, &response) != nil {
		return nil, fmt.Errorf("incompatible acknowledgment")
	}
	if strconv.FormatUint(response.GetClientResolutionId(), 10) != resolutionID || len(response.GetResult()) != 1 {
		return nil, fmt.Errorf("acknowledgment does not match this submission")
	}
	result := response.GetResult()[0]
	if !result.GetSuccess() || result.GetFailureReason() != nil {
		return nil, fmt.Errorf("destination was not acknowledged")
	}
	if kind == "story" {
		story := result.GetStoryDestinationResult()
		if story == nil || uuidString(story.GetStoryDestinationId()) != conversationID || len(story.GetStoryId()) == 0 {
			return nil, fmt.Errorf("incompatible story acknowledgment")
		}
		return map[string]any{"acknowledged": true, "submitted": true, "backend": "standalone", "content_type": "story", "story_id": base64.StdEncoding.EncodeToString(story.GetStoryId()), "client_resolution_id": resolutionID, "verification": "rpc"}, nil
	}
	destination := uuidString(result.GetDestinationId())
	messageID := result.GetCreatedMessageId()
	if nested := result.GetConversationDestinationResult(); nested != nil {
		destination = uuidString(nested.GetConversationDestinationId())
		messageID = nested.GetCreatedMessageId()
	}
	if destination != conversationID || messageID == 0 {
		return nil, fmt.Errorf("incompatible destination acknowledgment")
	}
	return map[string]any{"acknowledged": true, "submitted": true, "backend": "standalone", "encryption": "eel", "conversation_id": conversationID, "server_message_id": strconv.FormatUint(messageID, 10), "client_resolution_id": resolutionID, "verification": "rpc", "content_type": kind}, nil
}
