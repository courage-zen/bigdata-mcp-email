package main

import "net/http"

// multicaHeaderToField mirrors the header set injected by
// multica-stdio-mcp-proxy (proxy.go forwardMessage) and consumed by
// bigdata-mcp-pg's AuthKeyMiddleware.
var multicaHeaderToField = map[string]string{
	"X-Multica-Workspace-Id":   "workspace_id",
	"X-Multica-Agent-Id":       "agent_id",
	"X-Multica-Task-Id":        "task_id",
	"X-Multica-User-Id":        "user_id",
	"X-Multica-User-Name":      "user_name",
	"X-Multica-User-Email":     "user_email",
	"X-Multica-Agent-Name":     "agent_name",
	"X-Multica-Workspace-Name": "workspace_name",
}

// MulticaContextFromHeaders collects the X-Multica-* identity headers into
// the multica_context object echoed in tool results. Absent or empty values
// are omitted; a nil header yields an empty map (local dev).
func MulticaContextFromHeaders(h http.Header) map[string]string {
	ctx := map[string]string{}
	if h == nil {
		return ctx
	}
	for header, field := range multicaHeaderToField {
		if v := h.Get(header); v != "" {
			ctx[field] = v
		}
	}
	return ctx
}
