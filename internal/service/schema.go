package service

// Keep agent-facing descriptions and HTTP/OpenAPI constraints in one registry.
func enrichOperation(op *Operation) {
	descriptions := map[string]string{
		"conversation_id":   "Snapchat conversation UUID returned by conversations.list; groups.get requires a user-created group.",
		"recipient_id":      "Snapchat recipient UUID. Resolve usernames using recipients.resolve first.",
		"media_id":          "Account-bound handle returned by media.upload or media.upload_file; never an external URL.",
		"idempotency_key":   "Stable caller-chosen key for one logical send. Reuse for retries/status; changing it can send another copy.",
		"max_conversations": "Maximum changed conversations whose recent history is fetched in this poll; 1–5, default 3. Unfinished work remains pending.",
		"query":             "Username (optional @), display name, or recipient UUID. Local friend snapshot only.",
		"path":              "Absolute path to a regular file inside a configured --media-root on the HTTP server host.",
		"mime_type":         "Actual media type; bytes are validated before encryption and upload.",
		"text":              "Message text, preserved verbatim; sending supports at most 16000 UTF-8 bytes.",
		"reply_to":          "Unavailable: quoted replies have not been validated; omit this field.",
		"cursor":            "Opaque continuation value from the same list operation. Friend search uses the last returned friend UUID.",
		"limit":             "Maximum page size; omitted values use the operation default.",
		"recipients":        "Explicit recipient identifiers; duplicates are ignored where applicable. This does not resolve usernames.",
		"label":             "Local account label; registering a label does not sign in to Snapchat.",
		"participant_id":    "Exact Snapchat participant UUID; resolve a username with recipients.resolve first.",
		"sender_id":         "Exact sender UUID used to filter message search results within this conversation.",
		"match_case":        "Match literal search text with case sensitivity; default false. Keep unchanged across cursor pages.",
		"state":             "Filter saved delivery attempts by pending, unknown, not_sent, or sent.",
		"content_base64":    "Base64-encoded PNG/JPEG/MP4 bytes, at most 16 MiB decoded. Prefer media.upload_file for local files.",
		"message_id":        "Stable server message ID returned by message history or a delivery receipt.",
		"content_id":        "Content identifier returned by the corresponding Snapchat operation.",
		"audience":          "Choose friends-only My Story or the account's saved custom audience; public story publishing is not supported.",
		"job_id":            "Account-owned local job ID returned by jobs.list.",
	}
	for name, value := range op.Properties {
		original := value.(map[string]any)
		schema := map[string]any{}
		for k, v := range original {
			schema[k] = v
		}
		if _, ok := schema["description"]; !ok {
			if d := descriptions[name]; d != "" {
				schema["description"] = d
			}
		}
		switch name {
		case "query", "idempotency_key", "recipient_id", "conversation_id", "media_id", "job_id", "participant_id", "sender_id":
			schema["minLength"] = 1
			schema["maxLength"] = 128
		case "path":
			schema["minLength"] = 1
			schema["maxLength"] = 4096
		case "cursor":
			schema["maxLength"] = 4096
		case "text":
			schema["minLength"] = 1
			schema["maxLength"] = 16000
		case "mime_type":
			schema["enum"] = []string{"image/png", "image/jpeg", "video/mp4"}
		case "state":
			schema["enum"] = []string{"pending", "unknown", "not_sent", "sent"}
		case "match_case":
			schema["default"] = false
		case "label":
			schema["minLength"] = 1
			schema["maxLength"] = 120
		case "max_conversations":
			schema["minimum"] = 1
			schema["maximum"] = 5
			schema["default"] = 3
		case "limit":
			schema["minimum"] = 1
			schema["maximum"] = 500
			schema["default"] = 100
			if op.Name == "messages.list" || op.Name == "messages.search" || op.Name == "inbox.poll" {
				schema["maximum"] = 100
				schema["default"] = 40
			}
		}
		if name == "query" {
			schema["examples"] = []string{"@sample_user", "Sample Name"}
			if op.Name == "conversations.search" || op.Name == "groups.list" {
				schema["description"] = "Case-insensitive substring of a cached conversation title, conversation UUID, or participant UUID; does not resolve usernames."
				schema["examples"] = []string{"Weekend plans"}
			}
			if op.Name == "messages.search" {
				schema["description"] = "Literal substring to match in text messages, 1–128 characters. Case-insensitive unless match_case is true."
				schema["examples"] = []string{"weekend"}
			}
		}
		if name == "idempotency_key" {
			schema["examples"] = []string{"photo-request-001"}
		}
		if name == "cursor" && (op.Name == "messages.list" || op.Name == "messages.search") {
			schema["description"] = "Opaque account/conversation-bound history cursor, expiring after one hour. Use next_cursor from the same operation with unchanged search filters."
		}
		if name == "cursor" && op.Name == "inbox.poll" {
			schema["description"] = "Opaque account-bound inbox checkpoint. Omit on first poll; always reuse next_cursor, including after empty pages. Survives service restarts."
		}
		if name == "cursor" && (op.Name == "conversations.search" || op.Name == "groups.list") {
			schema["description"] = "Last returned conversation UUID from next_cursor. Keep the same search and participant filters on subsequent pages."
		}
		if name == "cursor" && op.Name == "deliveries.list" {
			schema["description"] = "Last returned idempotency key from next_cursor; retain the same state filter."
			schema["maxLength"] = 128
		}
		op.Properties[name] = schema
	}
}
