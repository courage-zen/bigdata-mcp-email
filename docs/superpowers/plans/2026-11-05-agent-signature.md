# Agent Email Signature Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When `X-Multica-Agent-Name` and `X-Multica-Workspace-Name` headers are present, append a two-line agent signature block to the outgoing email body and report `signature_appended` in the tool result.

**Architecture:** Approach A from the spec — `identity.go`'s header→field map gains the two name headers (so they also appear in the echoed `multica_context`), a new pure function `appendAgentSignature(body, bodyType, mc)` in `signature.go` renders the block, and `tools.go`'s `SendEmail` calls it after body validation, before building the `Email` struct. No config surface changes.

**Tech Stack:** Go 1.22+, `net/http.Header`, table-driven tests with `testing`.

**Context:** Repo /Users/zhengyong/code/bigdata-mcp-email, branch `main` @ `0b2ab09` (spec commit). All existing tests pass (`make test` → 48 tests). The headers are injected by multica-stdio-mcp-proxy; both name headers are best-effort and may be absent — absence means "no signature, body unchanged". Body encoding is quoted-printable in `mime.go`, so signature text never touches the header section. Design spec: `docs/superpowers/specs/2026-11-05-agent-signature-design.md`.

**Conventions:** Every commit message ends with a `Co-Authored-By: Claude Code <noreply@anthropic.com>` trailer (blank line before it). Deviations from this plan get appended as numbered items to the amendment block at the end.

---

### Task 1: identity.go — collect the two name headers

**Files:**
- Modify: `identity.go:8-15` (map literal)
- Test: `identity_test.go`

- [ ] **Step 1: Write the failing test**

Append to `identity_test.go`:

```go
func TestMulticaContextFromHeaders_NameHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-Multica-Workspace-Name", "Acme")
	h.Set("X-Multica-Agent-Name", "Bob")

	ctx := MulticaContextFromHeaders(h)
	if ctx["workspace_name"] != "Acme" {
		t.Errorf("workspace_name = %q, want %q", ctx["workspace_name"], "Acme")
	}
	if ctx["agent_name"] != "Bob" {
		t.Errorf("agent_name = %q, want %q", ctx["agent_name"], "Bob")
	}
	if len(ctx) != 2 {
		t.Errorf("got %d fields, want 2: %v", len(ctx), ctx)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestMulticaContextFromHeaders_NameHeaders ./...`
Expected: FAIL — `workspace_name = ""` (map lacks the mappings).

- [ ] **Step 3: Write minimal implementation**

In `identity.go`, extend the map literal:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run 'TestMulticaContextFromHeaders' ./...`
Expected: PASS (all four identity tests — `AllPresent` asserts an exact field count of 6, and it sets only the original six headers, so it still passes).

- [ ] **Step 5: Commit**

```bash
git add identity.go identity_test.go
git commit -m "feat: collect X-Multica-Agent-Name and X-Multica-Workspace-Name into multica_context

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: signature.go — the append function

**Files:**
- Create: `signature.go`
- Test: `signature_test.go`

- [ ] **Step 1: Write the failing test**

Create `signature_test.go`:

```go
package main

import "testing"

func TestAppendAgentSignature(t *testing.T) {
	both := map[string]string{"agent_name": "Bob", "workspace_name": "Acme"}

	cases := []struct {
		name     string
		body     string
		bodyType string
		mc       map[string]string
		want     string
	}{
		{
			name:     "text body, both names",
			body:     "hi",
			bodyType: "text",
			mc:       both,
			want:     "hi\n\n--\nMultica 智能体：Bob\n工作区：Acme",
		},
		{
			name:     "html body, both names",
			body:     "<p>hi</p>",
			bodyType: "html",
			mc:       both,
			want:     "<p>hi</p>\n<br>--<br>\nMultica 智能体：Bob\n<br>工作区：Acme\n<br>",
		},
		{
			name:     "text body, missing workspace_name",
			body:     "hi",
			bodyType: "text",
			mc:       map[string]string{"agent_name": "Bob"},
			want:     "hi",
		},
		{
			name:     "text body, missing agent_name",
			body:     "hi",
			bodyType: "text",
			mc:       map[string]string{"workspace_name": "Acme"},
			want:     "hi",
		},
		{
			name:     "empty agent_name value",
			body:     "hi",
			bodyType: "text",
			mc:       map[string]string{"agent_name": "", "workspace_name": "Acme"},
			want:     "hi",
		},
		{
			name:     "no multica context at all",
			body:     "hi",
			bodyType: "text",
			mc:       nil,
			want:     "hi",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := appendAgentSignature(tc.body, tc.bodyType, tc.mc)
			if got != tc.want {
				t.Errorf("appendAgentSignature(%q, %q, %v)\n got %q\nwant %q", tc.body, tc.bodyType, tc.mc, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestAppendAgentSignature ./...`
Expected: FAIL to compile — `undefined: appendAgentSignature`.

- [ ] **Step 3: Write minimal implementation**

Create `signature.go`:

```go
package main

import "fmt"

// appendAgentSignature appends the two-line agent attribution block to the
// body when the multica context carries both an agent display name and a
// workspace display name. Either name missing or empty → body returned
// unchanged. html bodies get the same plain-text block with <br> breaks;
// text bodies get literal newlines.
func appendAgentSignature(body, bodyType string, mc map[string]string) string {
	if mc == nil {
		return body
	}
	agent := mc["agent_name"]
	workspace := mc["workspace_name"]
	if agent == "" || workspace == "" {
		return body
	}
	if bodyType == "html" {
		return fmt.Sprintf("%s\n<br>--<br>\nMultica 智能体：%s\n<br>工作区：%s\n<br>", body, agent, workspace)
	}
	return fmt.Sprintf("%s\n\n--\nMultica 智能体：%s\n工作区：%s", body, agent, workspace)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestAppendAgentSignature ./...`
Expected: PASS (all 6 subtests).

- [ ] **Step 5: Commit**

```bash
git add signature.go signature_test.go
git commit -m "feat: appendAgentSignature renders the agent attribution block

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: tools.go — call site + signature_appended result field

**Files:**
- Modify: `tools.go:48-59` (emailResult struct), `tools.go:207-217` (Email construction), `tools.go:227-237` (success result)
- Test: `tools_test.go`

- [ ] **Step 1: Write the failing test**

Append to `tools_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run 'TestSendEmail_Signature' ./...`
Expected: FAIL — `signature_appended` absent from payload → `out["signature_appended"]` is nil ≠ true; signature text not in message.

- [ ] **Step 3: Write minimal implementation**

Three edits in `tools.go`.

1. `emailResult` gains the field (after `AttachmentCount`):

```go
type emailResult struct {
	Success           bool              `json:"success"`
	MessageID         string            `json:"message_id,omitempty"`
	To                []string          `json:"to"`
	Cc                []string          `json:"cc,omitempty"`
	From              string            `json:"from"`
	ReplyTo           string            `json:"reply_to,omitempty"`
	AttachmentCount   int               `json:"attachment_count"`
	SignatureAppended bool              `json:"signature_appended"`
	DurationMs        float64           `json:"duration_ms"`
	Error             string            `json:"error,omitempty"`
	MulticaContext    map[string]string `json:"multica_context"`
}
```

2. In `SendEmail`, insert a new stanza immediately after the `// --- build & send ---` comment line, before the `email := Email{` literal:

```go
	// --- agent signature -----------------------------------------------------
	signedBody := appendAgentSignature(body, bodyType, mc)
	signatureAppended := signedBody != body
```

and change the `Email` construction to use the signed body:

```go
	email := Email{
		From:        env.Config.SMTP.From,
		To:          to,
		Cc:          cc,
		ReplyTo:     replyTo,
		Subject:     subject,
		MessageID:   newMessageID(env.Config.SMTP.From),
		Body:        signedBody,
		BodyType:    bodyType,
		Attachments: attachments,
	}
```

3. In the success `result := emailResult{...}` literal, add:

```go
		SignatureAppended: signatureAppended,
```

(also keep `MulticaContext: mc` as-is).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run 'TestSendEmail_Signature' ./...`
Expected: PASS (all three tests).

- [ ] **Step 5: Run the full suite for regressions**

Run: `go test ./...`
Expected: PASS — all existing tests plus the new ones. Note: `TestSendEmail_WithTo` and friends use headers without name fields, so no signature is appended and their `fs.Msg` assertions are unaffected.

- [ ] **Step 6: Commit**

```bash
git add tools.go tools_test.go
git commit -m "feat: append agent signature to outgoing email body

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 4: README — document the signature behavior

**Files:**
- Modify: `README.md` (tool result field list, line ~102-104)

- [ ] **Step 1: Update README**

In the `send-email tool` section, extend the result-field paragraph:

```markdown
Returns JSON: `success`, `message_id`, `to`, `cc`, `from`, `reply_to`,
`attachment_count`, `signature_appended`, `duration_ms`, `multica_context`;
`error` is present on failure (omitted on success).
```

And append a bullet to the bullet list near the top (after the
"`X-Multica-*` headers are echoed in the tool result as `multica_context`"
bullet):

```markdown
- Agent signature: when `X-Multica-Agent-Name` and `X-Multica-Workspace-Name`
  are both present, a two-line attribution block (`--` / agent / workspace,
  plain text, `<br>` in html bodies) is appended to the body; the tool result
  reports it via `signature_appended`
```

- [ ] **Step 2: Full suite + build still green**

Run: `make test && make build`
Expected: all tests PASS, binary builds.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: document the agent signature feature

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Amendment Log

(Deviations from this plan, appended as numbered items during execution.)
