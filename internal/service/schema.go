package service

// Keep agent-facing descriptions and HTTP/OpenAPI constraints in one registry.
func enrichOperation(op *Operation) {
	descriptions := map[string]string{
		"conversation_id": "Snapchat conversation UUID returned by conversations.list; groups.get requires a user-created group.",
		"recipient_id":    "Snapchat recipient UUID. Resolve usernames using recipients.resolve first.",
		"media_id":        "Account-bound handle returned by media.upload or media.upload_file; never an external URL.",
		"idempotency_key": "Stable caller-chosen key for one logical send. Reuse for retries/status; changing it can send another copy.",
		"query":           "Username (optional @), display name, or recipient UUID. Local friend snapshot only.",
		"path":            "Absolute path to a regular file inside a configured --media-root on the HTTP server host.",
		"mime_type":       "Actual media type; bytes are validated before encryption and upload.",
		"text":            "Message text, preserved verbatim; sending supports at most 16000 UTF-8 bytes.",
		"reply_to":        "Unavailable: quoted replies have not been validated; omit this field.",
		"cursor":          "Opaque continuation value from the same list operation. Friend search uses the last returned friend UUID.",
		"limit":           "Maximum page size; omitted values use the operation default.",
		"recipients":      "Explicit recipient identifiers; duplicates are ignored where applicable. This does not resolve usernames.",
		"label":           "Local account label; registering a label does not sign in to Snapchat.",
		"content_base64":  "Base64-encoded PNG/JPEG/MP4 bytes, at most 16 MiB decoded. Prefer media.upload_file for local files.",
		"message_id":      "Stable server message ID returned by message history or a delivery receipt.",
		"content_id":      "Content identifier returned by the corresponding Snapchat operation.",
		"audience":        "Choose friends-only My Story or the account's saved custom audience; public story publishing is not supported.",
		"job_id":          "Account-owned local job ID returned by jobs.list.",
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
		case "query", "idempotency_key", "recipient_id", "conversation_id", "media_id", "job_id":
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
		case "limit":
			schema["minimum"] = 1
			schema["maximum"] = 500
			schema["default"] = 100
			if op.Name == "messages.list" {
				schema["maximum"] = 100
				schema["default"] = 40
			}
		}
		if name == "query" {
			schema["examples"] = []string{"@sample_user", "Sample Name"}
		}
		if name == "idempotency_key" {
			schema["examples"] = []string{"photo-request-001"}
		}
		if name == "cursor" && op.Name == "messages.list" {
			schema["description"] = "Unavailable: message history pagination is not yet implemented; omit cursor."
		}
		op.Properties[name] = schema
	}
}
