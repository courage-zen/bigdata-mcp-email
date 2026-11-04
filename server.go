package main

import (
	"context"
	"encoding/json"
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

	server := mcp.NewServer(&mcp.Implementation{Name: "email-mcp", Version: "v0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "send-email",
		Description: "Send an email via the configured SMTP relay. If \"to\" is empty, the multica caller's user email is used; if that is also absent the call fails.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in any) (*mcp.CallToolResult, any, error) {
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
				payload := errPayload("invalid arguments: "+err.Error(), mc)
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
