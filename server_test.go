package main

import (
	"bytes"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// newTestHTTPServer builds the full handler stack (auth + mcp) over a
// recorded sender, like production wiring but with a fake SMTP upstream.
func newTestHTTPServer(t *testing.T, authKeys string, fs MailSender) http.Handler {
	t.Helper()
	cfg := &Config{
		SMTP:            SMTPConfig{Host: "h", Port: 1, From: "noreply@example.com"},
		MaxAttachmentMB: 10,
	}
	env := &ServerEnv{Config: cfg, Sender: fs, Store: nil}
	return NewHandler(env, authKeys)
}

// mcpCall posts one JSON-RPC tools/call and returns the HTTP status and body.
func mcpCall(t *testing.T, base string, headers map[string]string, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", base+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

const callBody = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send-email","arguments":{"subject":"s","body":"hi","to":["a@x.com"]}}}`

func TestServer_NoAuthRequired_WhenKeyEmpty(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPServer(t, "", fs))
	defer ts.Close()

	status, body := mcpCall(t, ts.URL, nil, callBody)
	if status != 200 {
		t.Fatalf("status = %d: %s", status, body)
	}
	if !strings.Contains(body, `"success":true`) {
		t.Fatalf("no success: %s", body)
	}
}

func TestServer_AuthRequired(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPServer(t, "secret", fs))
	defer ts.Close()

	status, body := mcpCall(t, ts.URL, nil, callBody)
	if status != 401 {
		t.Fatalf("status = %d, want 401: %s", status, body)
	}
	if !strings.Contains(body, "unauthorized") {
		t.Fatalf("401 body shape: %s", body)
	}

	status, body = mcpCall(t, ts.URL, map[string]string{"Authorization": "Bearer wrong"}, callBody)
	if status != 401 {
		t.Fatalf("wrong key: status = %d", status)
	}

	status, body = mcpCall(t, ts.URL, map[string]string{"Authorization": "Bearer secret"}, callBody)
	if status != 200 {
		t.Fatalf("good key: status = %d: %s", status, body)
	}
	if !strings.Contains(body, `"success":true`) {
		t.Fatalf("no success: %s", body)
	}
}

// TestServer_WhitespaceKeyFailsClosed pins the whitespace-only MCP_AUTH_KEY
// behavior: " " splits to zero usable keys, and zero usable keys from a
// non-empty setting must NOT silently disable auth.
func TestServer_WhitespaceKeyFailsClosed(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPServer(t, "  ", fs))
	defer ts.Close()

	status, body := mcpCall(t, ts.URL, nil, callBody)
	if status != 401 {
		t.Fatalf("status = %d, want 401 (auth must fail closed): %s", status, body)
	}
}

func TestServer_MulticaHeadersReachTool(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPServer(t, "secret", fs))
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send-email","arguments":{"subject":"s","body":"hi"}}}`
	status, resp := mcpCall(t, ts.URL, map[string]string{
		"Authorization":        "Bearer secret",
		"X-Multica-User-Email": "user@example.com",
	}, body)
	if status != 200 {
		t.Fatalf("status = %d: %s", status, resp)
	}
	if !strings.Contains(resp, `"user_email":"user@example.com"`) {
		t.Fatalf("multica_context not echoed: %s", resp)
	}
	if !strings.Contains(resp, `"reply_to":"user@example.com"`) {
		t.Fatalf("fallback not applied: %s", resp)
	}
}

func TestServer_HealthEndpoint(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPHandlerForHealth(t, "secret", fs))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != `{"status":"ok","s3":"not-configured"}` {
		t.Fatalf("health = %d %s", resp.StatusCode, b)
	}

	// With a store configured (any ObjectStore), health flips to configured.
	env := &ServerEnv{
		Config: &Config{SMTP: SMTPConfig{Host: "h", Port: 1, From: "noreply@example.com"}, MaxAttachmentMB: 10},
		Sender: fs,
		Store:  &fakeObjectStore{},
	}
	ts2 := httptest.NewServer(NewHandler(env, ""))
	defer ts2.Close()
	resp2, err := http.Get(ts2.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	b2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != 200 || string(b2) != `{"status":"ok","s3":"configured"}` {
		t.Fatalf("health with store = %d %s", resp2.StatusCode, b2)
	}
}

// newTestHTTPHandlerForHealth wraps NewHandler but returns raw handler for GET.
func newTestHTTPHandlerForHealth(t *testing.T, authKeys string, fs MailSender) http.Handler {
	return newTestHTTPServer(t, authKeys, fs)
}

func TestServer_FullStackWithFakeSMTP(t *testing.T) {
	srv := newFakeSMTPServer(t)
	host, portStr, _ := net.SplitHostPort(srv.addr())
	port, _ := strconv.Atoi(portStr)

	cfg := &Config{
		SMTP:            SMTPConfig{Host: host, Port: port, From: "noreply@example.com"},
		MaxAttachmentMB: 10,
	}
	env := &ServerEnv{Config: cfg, Sender: &SMTPSender{Host: host, Port: port}}
	ts := httptest.NewServer(NewHandler(env, ""))
	defer ts.Close()

	status, body := mcpCall(t, ts.URL, nil, callBody)
	if status != 200 {
		t.Fatalf("status = %d: %s", status, body)
	}
	if !strings.Contains(body, `"success":true`) {
		t.Fatalf("no success: %s", body)
	}
	if srv.MailFrom != "noreply@example.com" || len(srv.RcptTo) != 1 {
		t.Errorf("envelope = %q %v", srv.MailFrom, srv.RcptTo)
	}
	if !bytes.Contains(srv.Data, []byte("Subject: s")) {
		t.Errorf("DATA:\n%s", srv.Data)
	}
}

func TestServer_LargeAttachmentUnderConfigLimit(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPServer(t, "", fs))
	defer ts.Close()

	// 5 MiB decoded ≈ 6.7 MiB base64 — over the SDK's 4 MiB default body
	// cap, under the 10 MiB config limit. Pins that the transport cap is
	// derived from max_attachment_mb.
	content := base64.StdEncoding.EncodeToString(make([]byte, 5<<20))
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send-email","arguments":{"subject":"s","body":"hi","to":["a@x.com"],"attachments":[{"filename":"big.bin","content_b64":"` + content + `"}]}}}`
	status, resp := mcpCall(t, ts.URL, nil, body)
	if status != 200 {
		t.Fatalf("status = %d (transport body cap not raised?): %.200s", status, resp)
	}
	if !strings.Contains(resp, `"success":true`) {
		t.Fatalf("no success: %.200s", resp)
	}
}

func TestServer_ToolSchemaExposed(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPServer(t, "", fs))
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	status, resp := mcpCall(t, ts.URL, nil, body)
	if status != 200 {
		t.Fatalf("status = %d: %s", status, resp)
	}
	for _, want := range []string{
		`"subject"`, `"body"`, `"body_type"`, `"to"`, `"cc"`,
		`"attachments"`, `"filename"`, `"content_b64"`, `"content_type"`, `"s3_key"`,
		`"anyOf"`,
	} {
		if !strings.Contains(resp, want) {
			t.Errorf("tools/list schema missing %s: %.500s", want, resp)
		}
	}
}

// TestServer_StringAttachmentsOverMCP pins the anyOf string branch for
// attachments: schema-less proxies pass the array param as a JSON string,
// which must clear SDK pre-validation and then hit the handler's JSON.parse
// tolerance.
func TestServer_StringAttachmentsOverMCP(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPServer(t, "", fs))
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send-email","arguments":{"subject":"s","body":"hi","to":["a@x.com"],"attachments":"[{\"filename\":\"x.txt\",\"content_b64\":\"aGVsbG8=\"}]"}}}`
	status, resp := mcpCall(t, ts.URL, nil, body)
	if status != 200 {
		t.Fatalf("status = %d: %s", status, resp)
	}
	if !strings.Contains(resp, `"success":true`) {
		t.Fatalf("string attachments must pass the SDK schema then parse: %.300s", resp)
	}
	if !strings.Contains(resp, `"attachment_count":1`) {
		t.Fatalf("attachment not sent: %.300s", resp)
	}
}

// TestServer_StringToOverMCP pins the anyOf string branch for "to": without
// it, every other test would still pass because they all send arrays, yet
// string recipients are core leniency for schema-less proxies.
func TestServer_StringToOverMCP(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPServer(t, "", fs))
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send-email","arguments":{"subject":"s","body":"hi","to":"a@x.com"}}}`
	status, resp := mcpCall(t, ts.URL, nil, body)
	if status != 200 {
		t.Fatalf("status = %d: %s", status, resp)
	}
	if !strings.Contains(resp, `"success":true`) {
		t.Fatalf("string to must pass the SDK schema then normalize: %.300s", resp)
	}
}

// TestServer_NullOptionalParamsOverMCP pins null tolerance end-to-end: the
// handler treats JSON null for the four optional params as absent (multica
// email fallback for "to"), and the schema must let null reach the handler
// rather than die at SDK pre-validation with a raw schema error.
func TestServer_NullOptionalParamsOverMCP(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPServer(t, "", fs))
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send-email","arguments":{"subject":"s","body":"hi","to":null,"cc":null,"body_type":null,"attachments":null}}}`
	status, resp := mcpCall(t, ts.URL, map[string]string{
		"X-Multica-User-Email": "user@example.com",
	}, body)
	if status != 200 {
		t.Fatalf("status = %d: %s", status, resp)
	}
	if !strings.Contains(resp, `"success":true`) {
		t.Fatalf("null optional params must pass schema and fall back to multica email: %.300s", resp)
	}
}

// TestServer_InvalidParamRejectedBySchema pins that SDK pre-validation is
// actually active: an integer "to" must be rejected as an isError result
// before the handler runs, proving the explicit schema has teeth.
func TestServer_InvalidParamRejectedBySchema(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPServer(t, "", fs))
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send-email","arguments":{"subject":"s","body":"hi","to":42}}}`
	status, resp := mcpCall(t, ts.URL, nil, body)
	if status != 200 {
		t.Fatalf("status = %d: %s", status, resp)
	}
	if !strings.Contains(resp, `"isError":true`) {
		t.Fatalf("schema rejection must surface as isError result: %.300s", resp)
	}
	if !strings.Contains(resp, "validating") {
		t.Fatalf("expected SDK validation error text: %.300s", resp)
	}
}
