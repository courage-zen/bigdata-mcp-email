package main

import (
	"bytes"
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
	env := &ServerEnv{Config: cfg, Sender: fs}
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
	if resp.StatusCode != 200 || !strings.Contains(string(b), "ok") {
		t.Fatalf("health = %d %s", resp.StatusCode, b)
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
