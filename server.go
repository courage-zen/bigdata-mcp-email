package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewHandler builds the full HTTP handler: /health + /mcp (stateless,
// JSON-response streamable transport) behind bearer auth.
func NewHandler(env *ServerEnv, authKeys string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	server := mcp.NewServer(&mcp.Implementation{Name: "email-mcp", Version: "v0.1.0"}, nil)
	tool := &mcp.Tool{
		Name: "send-email",
		Description: fmt.Sprintf("Send an email via the configured SMTP relay. Attachments (max %d MiB each) come in two shapes: inline base64 ({\"filename\": \"x.xlsx\", \"content_b64\": \"...\"}) or an object-store reference ({\"s3_key\": \"workspace-42/reports/x.xlsx\"} — filename defaults to the key's base name; requires the s3 config section). If \"to\" is empty, the multica caller's user email is used; if that is also absent the call fails.",
			env.Config.MaxAttachmentMB),
		InputSchema: json.RawMessage(inputSchemaJSON),
	}
	mcp.AddTool(server, tool, func(ctx context.Context, req *mcp.CallToolRequest, in any) (*mcp.CallToolResult, any, error) {
		var hdr map[string][]string
		if req.Extra != nil && req.Extra.Header != nil {
			hdr = req.Extra.Header
		}
		mc := MulticaContextFromHeaders(hdr)
		// Normalize raw arguments (to/cc may be string or array).
		var args map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				// Defense in depth: the SDK's schema validation (In = any →
				// {"type":"object"}) rejects non-object arguments before the
				// handler runs, so this is normally unreachable — but keep it
				// correct rather than malformed.
				payload := errPayload(errCodeInvalidArguments, "invalid arguments: "+err.Error(), mc)
				return &mcp.CallToolResult{
					IsError:           true,
					Content:           []mcp.Content{&mcp.TextContent{Text: string(payload)}},
					StructuredContent: json.RawMessage(payload),
				}, nil, nil
			}
		}
		payload, err := env.SendEmail(hdr, args)
		if err != nil {
			return nil, nil, err
		}
		// The payload is surfaced twice, per the MCP structured-content
		// convention: raw JSON in structuredContent (machine-readable,
		// unescaped in the response) and the same bytes as a TextContent
		// fallback for pre-SEP-2106 clients.
		isErr := !jsonPayloadSuccess(payload)
		return &mcp.CallToolResult{
			IsError:           isErr,
			Content:           []mcp.Content{&mcp.TextContent{Text: string(payload)}},
			StructuredContent: json.RawMessage(payload),
		}, nil, nil
	})

	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
			// Base64 inflates attachments 4/3; without this the SDK's 4 MiB
			// default caps decoded attachments at ~3 MiB, silently defeating
			// max_attachment_mb. +2 MiB covers the JSON envelope and gives
			// slightly-over-limit attachments room to reach the tool's
			// friendly size error instead of a bare 413.
			MaxRequestBodyBytes: int64(env.Config.MaxAttachmentMB)*4*1024*1024/3 + 2<<20,
		},
	))

	return bearerAuth(authKeys, mux)
}

// jsonPayloadSuccess reports whether the tool's JSON payload says success.
func jsonPayloadSuccess(payload []byte) bool {
	var probe struct {
		Success bool `json:"success"`
	}
	return json.Unmarshal(payload, &probe) == nil && probe.Success
}

// inputSchemaJSON is the deliberately-lenient tool schema. Required-ness,
// content_b64/s3_key exclusivity and filename defaults are enforced by the
// handler (uniform error_code payloads), not by the schema — so those
// violations reach callers as readable tool errors rather than SDK schema
// rejections. The anyOf string forms let schema-less proxy frameworks
// pass array params as JSON strings through SDK pre-validation, where the
// handler's JSON.parse tolerance takes over.
const inputSchemaJSON = `{
  "type": "object",
  "properties": {
    "to": {
      "anyOf": [
        {"type": "string", "description": "recipient, or comma-separated recipients"},
        {"type": "array", "items": {"type": "string"}, "description": "recipient addresses"}
      ],
      "description": "recipients; empty falls back to the multica caller's email"
    },
    "cc": {
      "anyOf": [
        {"type": "string", "description": "carbon-copy address, or comma-separated addresses"},
        {"type": "array", "items": {"type": "string"}, "description": "carbon-copy addresses"}
      ],
      "description": "carbon-copy recipients; optional"
    },
    "subject": {"type": "string", "description": "email subject line (required)"},
    "body": {"type": "string", "description": "email body, plain text or HTML per body_type (required)"},
    "body_type": {"type": "string", "enum": ["text", "html"], "description": "body rendering; default \"text\""},
    "attachments": {
      "anyOf": [
        {
          "type": "array",
          "items": {
            "type": "object",
            "properties": {
              "filename": {"type": "string", "description": "attachment name shown to the recipient; defaults to the s3_key base name (required for content_b64)"},
              "content_b64": {"type": "string", "description": "base64-encoded attachment content (mutually exclusive with s3_key)"},
              "content_type": {"type": "string", "description": "MIME type; guessed from filename when empty"},
              "s3_key": {"type": "string", "description": "object key in the configured bucket; mutually exclusive with content_b64"}
            }
          }
        },
        {"type": "string", "description": "JSON-encoded array of attachment objects (tolerated for schema-less proxies)"}
      ],
      "description": "optional attachments, max size per attachment from config"
    }
  }
}`
