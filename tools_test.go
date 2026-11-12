package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// fakeSender records what the tool asked it to send.
type fakeSender struct {
	From string
	To   []string
	Msg  []byte
	Err  error
}

func (f *fakeSender) Send(from string, to []string, msg []byte) error {
	f.From, f.To, f.Msg = from, to, msg
	return f.Err
}

// fakeObjectStore implements ObjectStore with canned data per key.
type fakeObjectStore struct {
	objects map[string][]byte
	err     error // when non-nil, returned for every key
}

func (f *fakeObjectStore) Get(key string, limit int64) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	data, ok := f.objects[key]
	if !ok {
		return nil, ErrObjectNotFound
	}
	if int64(len(data)) > limit {
		return nil, ErrAttachmentTooLarge
	}
	return data, nil
}

func newToolEnv(t *testing.T) (*ServerEnv, *fakeSender) {
	t.Helper()
	fs := &fakeSender{}
	env := &ServerEnv{
		Config: &Config{
			SMTP:            SMTPConfig{Host: "h", Port: 1, From: "noreply@example.com"},
			MaxAttachmentMB: 10,
		},
		Sender: fs,
	}
	return env, fs
}

// callSendEmail runs the tool logic directly (no MCP layer) and decodes the
// JSON payload from the result map.
func callSendEmail(t *testing.T, env *ServerEnv, hdr http.Header, args map[string]any) map[string]any {
	t.Helper()
	raw, sendErr := env.SendEmail(hdr, args)
	if sendErr != nil {
		t.Fatalf("SendEmail returned Go error %q (tool errors must be in payload)", sendErr)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("payload not JSON: %v\n%s", err, raw)
	}
	return out
}

func TestSendEmail_WithTo(t *testing.T) {
	env, fs := newToolEnv(t)
	hdr := http.Header{}
	hdr.Set("X-Multica-User-Email", "user@example.com")

	out := callSendEmail(t, env, hdr, map[string]any{
		"to":      []any{"a@x.com", "b@x.com"},
		"subject": "报告",
		"body":    "hi",
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if fs.From != "noreply@example.com" {
		t.Errorf("envelope from = %q", fs.From)
	}
	if len(fs.To) != 2 || fs.To[0] != "a@x.com" {
		t.Errorf("envelope to = %v", fs.To)
	}
	if !bytes.Contains(fs.Msg, []byte("Subject: =?utf-8?B?")) {
		t.Errorf("subject not RFC2047 encoded:\n%s", fs.Msg)
	}
	if bytes.Contains(fs.Msg, []byte("Reply-To:")) {
		t.Error("Reply-To must NOT appear when explicit to given")
	}
	mc, _ := out["multica_context"].(map[string]any)
	if mc["user_email"] != "user@example.com" {
		t.Errorf("multica_context = %v", out["multica_context"])
	}
	if got, _ := out["to"].([]any); len(got) != 2 {
		t.Errorf("result.to = %v", out["to"])
	}
}

func TestSendEmail_FallbackToUserEmail(t *testing.T) {
	env, fs := newToolEnv(t)
	hdr := http.Header{}
	hdr.Set("X-Multica-User-Email", "user@example.com")

	out := callSendEmail(t, env, hdr, map[string]any{
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if len(fs.To) != 1 || fs.To[0] != "user@example.com" {
		t.Errorf("envelope to = %v, want fallback user_email", fs.To)
	}
	if !bytes.Contains(fs.Msg, []byte("Reply-To: user@example.com")) {
		t.Error("Reply-To must be set on fallback")
	}
}

func TestSendEmail_FallbackCcOnly(t *testing.T) {
	// Cc recipients ride in the SMTP envelope (RFC 5321) alongside the fallback to.
	env, fs := newToolEnv(t)
	hdr := http.Header{}
	hdr.Set("X-Multica-User-Email", "user@example.com")

	out := callSendEmail(t, env, hdr, map[string]any{
		"cc":      []any{"b@x.com"},
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if len(fs.To) != 2 || fs.To[0] != "user@example.com" || fs.To[1] != "b@x.com" {
		t.Errorf("envelope to = %v, want fallback user_email + cc in RCPT TO", fs.To)
	}
	if !bytes.Contains(fs.Msg, []byte("Cc: b@x.com")) {
		t.Errorf("Cc header missing: %s", fs.Msg)
	}
}

func TestSendEmail_NoRecipient(t *testing.T) {
	env, _ := newToolEnv(t)
	hdr := http.Header{} // no multica headers at all

	out := callSendEmail(t, env, hdr, map[string]any{
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != false {
		t.Fatalf("success = %v, want false", out["success"])
	}
	msg, _ := out["error"].(string)
	if msg == "" {
		t.Error("error message must explain both sources were empty")
	}
	if !strings.Contains(msg, "user_email") {
		t.Errorf("error should mention user_email: %q", msg)
	}
	if code, _ := out["error_code"].(string); code != "MISSING_PARAM" {
		t.Errorf("error_code = %q, want %q", code, "MISSING_PARAM")
	}
}

func TestSendEmail_StringToNormalizes(t *testing.T) {
	env, fs := newToolEnv(t)
	hdr := http.Header{}

	out := callSendEmail(t, env, hdr, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if len(fs.To) != 1 || fs.To[0] != "a@x.com" {
		t.Errorf("string to not normalized: %v", fs.To)
	}
}

func TestSendEmail_StringToCommaSplit(t *testing.T) {
	env, fs := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com, b@x.com",
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if len(fs.To) != 2 || fs.To[0] != "a@x.com" || fs.To[1] != "b@x.com" {
		t.Errorf("envelope to = %v, want [a@x.com b@x.com]", fs.To)
	}
}

func TestSendEmail_StringCcCommaSplit(t *testing.T) {
	env, fs := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"cc":      "b@x.com, c@x.com",
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if len(fs.To) != 3 || fs.To[0] != "a@x.com" || fs.To[1] != "b@x.com" || fs.To[2] != "c@x.com" {
		t.Errorf("envelope to = %v, want [a@x.com b@x.com c@x.com]", fs.To)
	}
	if !bytes.Contains(fs.Msg, []byte("Cc: b@x.com, c@x.com")) {
		t.Errorf("Cc header missing or wrong: %s", fs.Msg)
	}
}

func TestSendEmail_MissingSubject(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":   "a@x.com",
		"body": "hi",
	})
	if out["success"] != false {
		t.Fatal("success must be false without subject")
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "subject") {
		t.Errorf("error should name subject: %q", msg)
	}
	if code, _ := out["error_code"].(string); code != "MISSING_PARAM" {
		t.Errorf("error_code = %q, want %q", code, "MISSING_PARAM")
	}
}

func TestSendEmail_MissingBody(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
	})
	if out["success"] != false {
		t.Fatal("success must be false without body")
	}
	if code, _ := out["error_code"].(string); code != "MISSING_PARAM" {
		t.Errorf("error_code = %q, want %q", code, "MISSING_PARAM")
	}
}

func TestSendEmail_InvalidEmail(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "not-an-email",
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != false {
		t.Fatal("success must be false for invalid to")
	}
	if code, _ := out["error_code"].(string); code != "INVALID_PARAM_VALUE" {
		t.Errorf("error_code = %q, want %q", code, "INVALID_PARAM_VALUE")
	}
}

func TestSendEmail_BadBase64Attachment(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"filename":    "x.txt",
			"content_b64": "!!!not-base64!!!",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for undecodable base64")
	}
	if code, _ := out["error_code"].(string); code != "INVALID_BASE64" {
		t.Errorf("error_code = %q, want %q", code, "INVALID_BASE64")
	}
}

func TestSendEmail_AttachmentsWrongType(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":          "a@x.com",
		"subject":     "s",
		"body":        "hi",
		"attachments": "not-a-list",
	})
	if out["success"] != false {
		t.Fatal("success must be false for non-array attachments")
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "attachments") {
		t.Errorf("error should name attachments: %q", msg)
	}
	if code, _ := out["error_code"].(string); code != "INVALID_PARAM_TYPE" {
		t.Errorf("error_code = %q, want %q", code, "INVALID_PARAM_TYPE")
	}
}

func TestSendEmail_BodyTypeWrongType(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":        "a@x.com",
		"subject":   "s",
		"body":      "hi",
		"body_type": 7,
	})
	if out["success"] != false {
		t.Fatal("success must be false for non-string body_type")
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "body_type") {
		t.Errorf("error should name body_type: %q", msg)
	}
	if code, _ := out["error_code"].(string); code != "INVALID_PARAM_TYPE" {
		t.Errorf("error_code = %q, want %q", code, "INVALID_PARAM_TYPE")
	}
}

func TestSendEmail_AttachmentTooBig(t *testing.T) {
	env, _ := newToolEnv(t)
	env.Config.MaxAttachmentMB = 1
	big := make([]byte, 2*1024*1024)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"filename":    "big.bin",
			"content_b64": base64.StdEncoding.EncodeToString(big),
		}},
	})
	if out["success"] != false {
		t.Fatal("2MB attachment must fail 1MB cap")
	}
	if code, _ := out["error_code"].(string); code != "ATTACHMENT_TOO_LARGE" {
		t.Errorf("error_code = %q, want %q", code, "ATTACHMENT_TOO_LARGE")
	}
}

func TestSendEmail_SenderFailure(t *testing.T) {
	env, fs := newToolEnv(t)
	fs.Err = fmt.Errorf("smtp: upstream refused")
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != false {
		t.Fatal("success must be false when sender errors")
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "smtp") {
		t.Errorf("error should mention smtp: %q", msg)
	}
	if code, _ := out["error_code"].(string); code != "SMTP_FAILED" {
		t.Errorf("error_code = %q, want %q", code, "SMTP_FAILED")
	}
}

func TestSendEmail_MessageIDInResultAndMessage(t *testing.T) {
	env, fs := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	mid, _ := out["message_id"].(string)
	if mid == "" || !strings.HasPrefix(mid, "<") || !strings.HasSuffix(mid, ">") {
		t.Fatalf("message_id = %q, want <...> form", mid)
	}
	if !bytes.Contains(fs.Msg, []byte("Message-ID: "+mid)) {
		t.Errorf("Message-ID header and result must match: %q vs\n%s", mid, fs.Msg)
	}
}

func TestSendEmail_SignatureAppended(t *testing.T) {
	env, fs := newToolEnv(t)
	hdr := http.Header{}
	hdr.Set("X-Multica-User-Email", "user@example.com")
	hdr.Set("X-Multica-Agent-Name", "Bob")
	hdr.Set("X-Multica-Workspace-Name", "Acme")

	out := callSendEmail(t, env, hdr, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if out["signature_appended"] != true {
		t.Errorf("signature_appended = %v, want true", out["signature_appended"])
	}
	// QP leaves hard line breaks as literal CRLF and ASCII as literal; assert
	// on the signature separator line and the two names, which survive the
	// QP stream unchanged.
	sep := []byte("--\r\n")
	if !bytes.Contains(fs.Msg, sep) {
		t.Errorf("signature separator not found in message:\n%s", fs.Msg)
	}
	if !bytes.Contains(fs.Msg, []byte("Bob")) || !bytes.Contains(fs.Msg, []byte("Acme")) {
		t.Errorf("signature names not found in message:\n%s", fs.Msg)
	}
}

func TestSendEmail_SignatureNotAppended(t *testing.T) {
	env, fs := newToolEnv(t)
	hdr := http.Header{}
	hdr.Set("X-Multica-User-Email", "user@example.com")
	hdr.Set("X-Multica-Agent-Name", "Bob") // workspace_name missing

	out := callSendEmail(t, env, hdr, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if out["signature_appended"] != false {
		t.Errorf("signature_appended = %v, want false", out["signature_appended"])
	}
	if bytes.Contains(fs.Msg, []byte("--\r\n")) {
		t.Errorf("signature must not appear:\n%s", fs.Msg)
	}
	if bytes.Contains(fs.Msg, []byte("Bob")) {
		t.Errorf("agent name must not leak into unsigned body:\n%s", fs.Msg)
	}
}

func TestSendEmail_SignatureHtmlBody(t *testing.T) {
	env, fs := newToolEnv(t)
	hdr := http.Header{}
	hdr.Set("X-Multica-User-Email", "user@example.com")
	hdr.Set("X-Multica-Agent-Name", "Bob")
	hdr.Set("X-Multica-Workspace-Name", "Acme")

	out := callSendEmail(t, env, hdr, map[string]any{
		"to":        "a@x.com",
		"subject":   "s",
		"body":      "<p>hi</p>",
		"body_type": "html",
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if out["signature_appended"] != true {
		t.Errorf("signature_appended = %v, want true", out["signature_appended"])
	}
	if !bytes.Contains(fs.Msg, []byte("<br>--<br>")) {
		t.Errorf("html signature separator not found:\n%s", fs.Msg)
	}
}

// allowlistEnv returns a tool env with the allowlist active.
func allowlistEnv(t *testing.T, domains ...string) (*ServerEnv, *fakeSender) {
	t.Helper()
	env, fs := newToolEnv(t)
	env.Config.AllowedDomains = domains
	set := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		set[d] = struct{}{}
	}
	env.Config.allowedDomainSet = set
	return env, fs
}

func TestSendEmail_DomainAllowlist_BlockedTo(t *testing.T) {
	env, fs := allowlistEnv(t, "example.com", "acme.org")
	hdr := http.Header{}
	hdr.Set("X-Multica-User-Email", "user@example.com")

	out := callSendEmail(t, env, hdr, map[string]any{
		"to":      []any{"a@evil.com"},
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != false {
		t.Fatalf("success = %v, want false (out-of-list to must be rejected)", out["success"])
	}
	errMsg, _ := out["error"].(string)
	if !strings.Contains(errMsg, `"a@evil.com"`) {
		t.Errorf("error must name the rejected address (quoted), got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "[example.com, acme.org]") {
		t.Errorf("error must list configured domains comma-separated, got: %q", errMsg)
	}
	if fs.To != nil || fs.Msg != nil {
		t.Errorf("nothing must be sent when rejected (to=%v, msg set=%v)", fs.To, fs.Msg != nil)
	}
	if code, _ := out["error_code"].(string); code != "DOMAIN_NOT_ALLOWED" {
		t.Errorf("error_code = %q, want %q", code, "DOMAIN_NOT_ALLOWED")
	}
}

func TestSendEmail_DomainAllowlist_BlockedCc(t *testing.T) {
	env, _ := allowlistEnv(t, "example.com")
	hdr := http.Header{}
	hdr.Set("X-Multica-User-Email", "user@example.com")

	out := callSendEmail(t, env, hdr, map[string]any{
		"to":      []any{"a@example.com"},
		"cc":      []any{"b@evil.com"},
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != false {
		t.Fatalf("success = %v, want false (out-of-list cc must be rejected)", out["success"])
	}
	errMsg, _ := out["error"].(string)
	if !strings.Contains(errMsg, `"b@evil.com"`) {
		t.Errorf("error must name the rejected cc address (quoted), got: %q", errMsg)
	}
	if code, _ := out["error_code"].(string); code != "DOMAIN_NOT_ALLOWED" {
		t.Errorf("error_code = %q, want %q", code, "DOMAIN_NOT_ALLOWED")
	}
}

func TestSendEmail_DomainAllowlist_BlockedFallback(t *testing.T) {
	env, _ := allowlistEnv(t, "example.com")
	hdr := http.Header{}
	hdr.Set("X-Multica-User-Email", "user@outside.com") // to empty → fallback blocked

	out := callSendEmail(t, env, hdr, map[string]any{
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != false {
		t.Fatalf("success = %v, want false (out-of-list fallback user_email must be rejected)", out["success"])
	}
	errMsg, _ := out["error"].(string)
	if !strings.Contains(errMsg, `"user@outside.com"`) {
		t.Errorf("error must name the rejected fallback address (quoted), got: %q", errMsg)
	}
	if code, _ := out["error_code"].(string); code != "DOMAIN_NOT_ALLOWED" {
		t.Errorf("error_code = %q, want %q", code, "DOMAIN_NOT_ALLOWED")
	}
}

func TestSendEmail_DomainAllowlist_MixedBlocked(t *testing.T) {
	env, fs := allowlistEnv(t, "example.com")
	hdr := http.Header{}
	hdr.Set("X-Multica-User-Email", "user@example.com")

	out := callSendEmail(t, env, hdr, map[string]any{
		"to":      []any{"a@example.com", "b@evil.com", "c@evil.com"},
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != false {
		t.Fatalf("success = %v, want false (any out-of-list recipient rejects the whole call)", out["success"])
	}
	errMsg, _ := out["error"].(string)
	if !strings.Contains(errMsg, `"b@evil.com", "c@evil.com"`) {
		t.Errorf("error must list both rejected addresses quoted and comma-joined, got: %q", errMsg)
	}
	if fs.To != nil || fs.Msg != nil {
		t.Errorf("nothing must be sent when rejected (to=%v, msg set=%v)", fs.To, fs.Msg != nil)
	}
	if code, _ := out["error_code"].(string); code != "DOMAIN_NOT_ALLOWED" {
		t.Errorf("error_code = %q, want %q", code, "DOMAIN_NOT_ALLOWED")
	}
}

func TestSendEmail_DomainAllowlist_AllAllowed(t *testing.T) {
	env, fs := allowlistEnv(t, "example.com", "acme.org")
	hdr := http.Header{}
	hdr.Set("X-Multica-User-Email", "user@example.com")

	out := callSendEmail(t, env, hdr, map[string]any{
		"to":      []any{"a@example.com", "b@ACME.org"},
		"cc":      []any{"c@acme.org"},
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	// fakeSender.Send receives the combined envelope recipient slice (to+cc
	// merged by the tool before Send); there is no separate Cc field.
	if len(fs.To) != 3 || fs.To[0] != "a@example.com" || fs.To[1] != "b@ACME.org" || fs.To[2] != "c@acme.org" {
		t.Errorf("envelope recipients = %v", fs.To)
	}
}

func TestSendEmail_DomainAllowlist_Inactive(t *testing.T) {
	env, fs := newToolEnv(t) // no AllowedDomains configured → inactive
	hdr := http.Header{}
	hdr.Set("X-Multica-User-Email", "user@example.com")

	out := callSendEmail(t, env, hdr, map[string]any{
		"to":      []any{"a@anywhere.com"},
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != true {
		t.Fatalf("success = %v (inactive allowlist must not restrict), err: %v", out["success"], out["error"])
	}
	if len(fs.To) != 1 || fs.To[0] != "a@anywhere.com" {
		t.Errorf("envelope to = %v", fs.To)
	}
}

func TestSendEmail_AttachmentsStringParses(t *testing.T) {
	env, fs := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":          "a@x.com",
		"subject":     "s",
		"body":        "hi",
		"attachments": `[{"filename":"x.txt","content_b64":"aGVsbG8="}]`,
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if got, _ := out["attachment_count"].(float64); got != 1 {
		t.Errorf("attachment_count = %v, want 1", out["attachment_count"])
	}
	if !strings.Contains(string(fs.Msg), "x.txt") {
		t.Errorf("attachment not sent:\n%s", fs.Msg)
	}
}

func TestSendEmail_AttachmentsStringInvalid(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":          "a@x.com",
		"subject":     "s",
		"body":        "hi",
		"attachments": "not-json",
	})
	if out["success"] != false {
		t.Fatal("success must be false for unparseable attachments string")
	}
	if code, _ := out["error_code"].(string); code != "INVALID_PARAM_TYPE" {
		t.Errorf("error_code = %q, want INVALID_PARAM_TYPE", code)
	}
}

func TestSendEmail_S3Attachment(t *testing.T) {
	env, fs := newToolEnv(t)
	env.Store = &fakeObjectStore{objects: map[string][]byte{
		"workspace-42/reports/销量.xlsx": []byte("xlsx-bytes"),
	}}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key": "workspace-42/reports/销量.xlsx",
		}},
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if !strings.Contains(string(fs.Msg), "销量.xlsx") {
		t.Errorf("basename not used as filename:\n%s", fs.Msg)
	}
}

func TestSendEmail_S3AttachmentExplicitFilename(t *testing.T) {
	env, fs := newToolEnv(t)
	env.Store = &fakeObjectStore{objects: map[string][]byte{"k": []byte("v")}}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key":   "k",
			"filename": "report.txt",
		}},
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if !strings.Contains(string(fs.Msg), "filename=\"report.txt\"") {
		t.Errorf("explicit filename not used:\n%s", fs.Msg)
	}
}

func TestSendEmail_S3Unavailable(t *testing.T) {
	env, _ := newToolEnv(t) // Store nil
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key": "k",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false when s3 is unconfigured")
	}
	if code, _ := out["error_code"].(string); code != "S3_UNAVAILABLE" {
		t.Errorf("error_code = %q, want S3_UNAVAILABLE", code)
	}
}

func TestSendEmail_S3ObjectNotFound(t *testing.T) {
	env, _ := newToolEnv(t)
	env.Store = &fakeObjectStore{} // empty map
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key": "missing",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for missing object")
	}
	if code, _ := out["error_code"].(string); code != "S3_OBJECT_NOT_FOUND" {
		t.Errorf("error_code = %q, want S3_OBJECT_NOT_FOUND", code)
	}
	// Sanitization: the message must not echo the requested key.
	if msg, _ := out["error"].(string); strings.Contains(msg, "missing") {
		t.Errorf("error message echoes the key: %s", msg)
	}
}

func TestSendEmail_S3TooLarge(t *testing.T) {
	env, _ := newToolEnv(t)
	env.Config.MaxAttachmentMB = 1
	env.Store = &fakeObjectStore{objects: map[string][]byte{"k": make([]byte, 2<<20)}}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key": "k",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for oversized object")
	}
	if code, _ := out["error_code"].(string); code != "ATTACHMENT_TOO_LARGE" {
		t.Errorf("error_code = %q, want ATTACHMENT_TOO_LARGE", code)
	}
}

func TestSendEmail_S3FetchFailed(t *testing.T) {
	env, _ := newToolEnv(t)
	env.Store = &fakeObjectStore{err: fmt.Errorf("dial: connection refused")}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key": "k",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for store failure")
	}
	if code, _ := out["error_code"].(string); code != "S3_FETCH_FAILED" {
		t.Errorf("error_code = %q, want S3_FETCH_FAILED", code)
	}
}

func TestSendEmail_S3InvalidKey(t *testing.T) {
	env, _ := newToolEnv(t)
	env.Store = &fakeObjectStore{objects: map[string][]byte{}}
	for _, key := range []string{
		"/abs/x.xlsx", "a/../b.xlsx", "a//b.xlsx", "payload.exe",
	} {
		out := callSendEmail(t, env, http.Header{}, map[string]any{
			"to": "a@x.com", "subject": "s", "body": "hi",
			"attachments": []any{map[string]any{"s3_key": key}},
		})
		if out["success"] != false {
			t.Fatalf("key %q must be rejected", key)
		}
		if code, _ := out["error_code"].(string); code != "S3_INVALID_KEY" {
			t.Errorf("key %q: error_code = %q, want S3_INVALID_KEY", key, code)
		}
		if msg, _ := out["error"].(string); strings.Contains(msg, key) {
			t.Errorf("key %q leaked in message: %s", key, msg)
		}
	}
}

func TestSendEmail_S3PresignedURLKey(t *testing.T) {
	env, _ := newToolEnv(t)
	env.Store = &fakeObjectStore{objects: map[string][]byte{"workspaces/a/b.xlsx": []byte("v")}}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to": "a@x.com", "subject": "s", "body": "hi",
		"attachments": []any{map[string]any{
			"s3_key": "http://minio:9000/bucket/workspaces/a/b.xlsx?X-Amz-Signature=x",
		}},
	})
	// No bucket match (fake env has no S3Config bucket): rejected as
	// invalid, success=false.
	if out["success"] != false {
		t.Fatal("URL with unknown bucket must fail")
	}
	if code, _ := out["error_code"].(string); code != "S3_INVALID_KEY" {
		t.Errorf("error_code = %q, want S3_INVALID_KEY", code)
	}
}

func TestSendEmail_S3ObjectTooLarge(t *testing.T) {
	env, _ := newToolEnv(t)
	env.Store = &fakeObjectStore{err: ErrObjectTooLarge}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to": "a@x.com", "subject": "s", "body": "hi",
		"attachments": []any{map[string]any{"s3_key": "k"}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for S3-layer oversize")
	}
	if code, _ := out["error_code"].(string); code != "S3_OBJECT_TOO_LARGE" {
		t.Errorf("error_code = %q, want S3_OBJECT_TOO_LARGE", code)
	}
}

func TestSendEmail_S3UnavailableBeforeKeyValidation(t *testing.T) {
	env, _ := newToolEnv(t) // Store nil, Config.S3 nil
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to": "a@x.com", "subject": "s", "body": "hi",
		"attachments": []any{map[string]any{"s3_key": "payload.exe"}},
	})
	// Config problem outranks input problem: unavailable, not invalid key.
	if code, _ := out["error_code"].(string); code != "S3_UNAVAILABLE" {
		t.Errorf("error_code = %q, want S3_UNAVAILABLE (config check first)", code)
	}
}

func TestSendEmail_FilenameDangerousExtension(t *testing.T) {
	env, _ := newToolEnv(t)
	// s3_key benign, explicit filename smuggles the extension.
	env.Store = &fakeObjectStore{objects: map[string][]byte{"report.xlsx": []byte("v")}}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to": "a@x.com", "subject": "s", "body": "hi",
		"attachments": []any{map[string]any{
			"s3_key": "report.xlsx", "filename": "evil.exe",
		}},
	})
	if out["success"] != false {
		t.Fatal("explicit filename must not smuggle a dangerous extension")
	}
	if code, _ := out["error_code"].(string); code != "INVALID_PARAM_VALUE" {
		t.Errorf("error_code = %q, want INVALID_PARAM_VALUE", code)
	}

	// Same rule for content_b64 (caller-supplied name, same channel).
	out2 := callSendEmail(t, env, http.Header{}, map[string]any{
		"to": "a@x.com", "subject": "s", "body": "hi",
		"attachments": []any{map[string]any{
			"content_b64": "aGVsbG8=", "filename": "evil.CMD.",
		}},
	})
	if out2["success"] != false {
		t.Fatal("content_b64 filename must not smuggle a dangerous extension")
	}
	if code, _ := out2["error_code"].(string); code != "INVALID_PARAM_VALUE" {
		t.Errorf("error_code = %q, want INVALID_PARAM_VALUE", code)
	}

	// Positive control: a benign explicit filename over s3_key must
	// succeed, guarding against the convergence check rejecting
	// everything.
	out3 := callSendEmail(t, env, http.Header{}, map[string]any{
		"to": "a@x.com", "subject": "s", "body": "hi",
		"attachments": []any{map[string]any{
			"s3_key": "report.xlsx", "filename": "report.pdf",
		}},
	})
	if out3["success"] != true {
		t.Fatalf("benign explicit filename must be accepted, err: %v", out3["error"])
	}
}

func TestSendEmail_FilenameTrailingDotBypass(t *testing.T) {
	env, _ := newToolEnv(t)
	env.Store = &fakeObjectStore{objects: map[string][]byte{"k": []byte("v")}}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to": "a@x.com", "subject": "s", "body": "hi",
		"attachments": []any{map[string]any{
			"s3_key": "k", "filename": "evil.exe.",
		}},
	})
	if out["success"] != false {
		t.Fatal("trailing-dot filename must not bypass the blocklist")
	}
}

func TestSendEmail_AttachmentBothSources(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key":      "k",
			"content_b64": "aGVsbG8=",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for both sources given")
	}
	if code, _ := out["error_code"].(string); code != "INVALID_PARAM_VALUE" {
		t.Errorf("error_code = %q, want INVALID_PARAM_VALUE", code)
	}
}

func TestSendEmail_AttachmentNoSource(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"filename": "x.txt",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for no source given")
	}
	if code, _ := out["error_code"].(string); code != "INVALID_PARAM_VALUE" {
		t.Errorf("error_code = %q, want INVALID_PARAM_VALUE", code)
	}
}

func TestSendEmail_S3KeyTrailingSlash(t *testing.T) {
	env, _ := newToolEnv(t)
	env.Store = &fakeObjectStore{objects: map[string][]byte{"a/b": []byte("v")}}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key": "workspace-42/reports/",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for trailing-slash key")
	}
	if code, _ := out["error_code"].(string); code != "S3_INVALID_KEY" {
		t.Errorf("error_code = %q, want S3_INVALID_KEY", code)
	}
	// Sanitization: the message must not echo the key.
	if msg, _ := out["error"].(string); strings.Contains(msg, "workspace-42") {
		t.Errorf("error message echoes the key: %s", msg)
	}
}

func TestSendEmail_ContentB64MissingFilename(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"content_b64": "aGVsbG8=",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for missing filename")
	}
	if code, _ := out["error_code"].(string); code != "MISSING_FILENAME" {
		t.Errorf("error_code = %q, want MISSING_FILENAME", code)
	}
}
