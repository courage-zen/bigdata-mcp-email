package main

import (
	"net/http"
	"testing"
)

func TestMulticaContextFromHeaders_AllPresent(t *testing.T) {
	h := http.Header{}
	h.Set("X-Multica-Workspace-Id", "ws-1")
	h.Set("X-Multica-Agent-Id", "ag-1")
	h.Set("X-Multica-Task-Id", "tk-1")
	h.Set("X-Multica-User-Id", "u-1")
	h.Set("X-Multica-User-Name", "zhengyong.js")
	h.Set("X-Multica-User-Email", "zhengyong.js@example.com")

	ctx := MulticaContextFromHeaders(h)
	want := map[string]string{
		"workspace_id": "ws-1",
		"agent_id":     "ag-1",
		"task_id":      "tk-1",
		"user_id":      "u-1",
		"user_name":    "zhengyong.js",
		"user_email":   "zhengyong.js@example.com",
	}
	if len(ctx) != len(want) {
		t.Fatalf("got %d fields, want %d: %v", len(ctx), len(want), ctx)
	}
	for k, v := range want {
		if ctx[k] != v {
			t.Errorf("%s = %q, want %q", k, ctx[k], v)
		}
	}
}

func TestMulticaContextFromHeaders_EmptyAndMissing(t *testing.T) {
	h := http.Header{}
	h.Set("X-Multica-Workspace-Id", "ws-1")
	h.Set("X-Multica-User-Email", "") // set-but-empty must be omitted

	ctx := MulticaContextFromHeaders(h)
	if _, ok := ctx["user_email"]; ok {
		t.Error("empty header must be omitted")
	}
	if ctx["workspace_id"] != "ws-1" {
		t.Errorf("workspace_id = %q", ctx["workspace_id"])
	}
	if len(ctx) != 1 {
		t.Errorf("got %v", ctx)
	}
}

func TestMulticaContextFromHeaders_NilSafe(t *testing.T) {
	if ctx := MulticaContextFromHeaders(nil); len(ctx) != 0 {
		t.Errorf("nil headers must yield empty map, got %v", ctx)
	}
}
