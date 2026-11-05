# Recipient Domain Allowlist Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When `allowed_domains` (yaml list) or `ALLOWED_DOMAINS` (env, comma-separated) is configured, `send-email` rejects requests whose recipient addresses (to + cc + fallback multica user email) fall outside those domains; empty configuration means no restriction.

**Architecture:** Config-time normalization — `config.go` parses and normalizes (trim, lowercase, drop empties, collapse duplicates) the domain list into both a display slice `AllowedDomains []string` and an O(1) membership map `allowedDomainSet map[string]struct{}` on `Config`. `tools.go`'s `SendEmail` checks the allowlist in one new stanza right after the recipient fallback / `validEmail` loop; the whole call fails with rejected addresses + configured domains in the error.

**Tech Stack:** Go 1.22+, `strings`, `gopkg.in/yaml.v3`, table-driven tests with `testing`.

**Context:** Repo /Users/zhengyong/code/bigdata-mcp-email, branch `main`. All existing tests pass (53 test functions). Current unpushed commits on main: `d290dda` (signature label) and `8c8cd7b` (this feature's spec) — they get pushed together with the feature commits at the end of the release flow. Design spec: `docs/superpowers/specs/2026-11-05-domain-allowlist-design.md`. Key existing code: `LoadConfig` in config.go (yaml + env override pattern, lines 42-103); `SendEmail` in tools.go line 115, its `fail` helper line 119, recipient fallback stanza lines 156-165, `validEmail` loop lines 167-171; `newToolEnv`/`callSendEmail`/`fakeSender` test helpers in tools_test.go lines 13-52 (note: `newToolEnv` builds `Config` WITHOUT `AllowedDomains` — nil set = inactive, so existing tests keep passing).

**Conventions:** Every commit message ends with a `Co-Authored-By: Claude Code <noreply@anthropic.com>` trailer (blank line before it). Deviations from this plan get appended as numbered items to the amendment log at the end.

---

### Task 1: config.go — allowed_domains parsing + normalization

**Files:**
- Modify: `config.go` (structs + LoadConfig)
- Test: `config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `config_test.go`:

```go
func TestLoadConfig_AllowedDomains(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
allowed_domains:
  - "Example.com"
  - " acme.org "
  - ""
  - "acme.org"
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.com", "acme.org"}
	if len(cfg.AllowedDomains) != 2 || cfg.AllowedDomains[0] != want[0] || cfg.AllowedDomains[1] != want[1] {
		t.Fatalf("AllowedDomains = %v, want %v", cfg.AllowedDomains, want)
	}
	if _, ok := cfg.allowedDomainSet["example.com"]; !ok {
		t.Errorf("allowedDomainSet missing example.com: %v", cfg.allowedDomainSet)
	}
	if _, ok := cfg.allowedDomainSet["acme.org"]; !ok {
		t.Errorf("allowedDomainSet missing acme.org: %v", cfg.allowedDomainSet)
	}
	if len(cfg.allowedDomainSet) != 2 {
		t.Errorf("allowedDomainSet size = %d, want 2", len(cfg.allowedDomainSet))
	}
}

func TestLoadConfig_AllowedDomainsEnvOverride(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
allowed_domains:
  - "file.com"
`)
	t.Setenv("ALLOWED_DOMAINS", " EnvA.com , envb.com, ,enva.com ")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// env replaces the file list entirely; same normalization rules.
	want := []string{"enva.com", "envb.com"}
	if len(cfg.AllowedDomains) != 2 || cfg.AllowedDomains[0] != want[0] || cfg.AllowedDomains[1] != want[1] {
		t.Fatalf("AllowedDomains = %v, want %v (env must replace file list)", cfg.AllowedDomains, want)
	}
}

func TestLoadConfig_AllowedDomainsAbsent(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowedDomains != nil {
		t.Errorf("AllowedDomains = %v, want nil when unconfigured", cfg.AllowedDomains)
	}
	if cfg.allowedDomainSet != nil {
		t.Errorf("allowedDomainSet = %v, want nil when unconfigured", cfg.allowedDomainSet)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestLoadConfig_AllowedDomains' ./...`
Expected: FAIL to compile — `cfg.AllowedDomains undefined` (type Config has no field or method AllowedDomains).

- [ ] **Step 3: Write minimal implementation**

In `config.go`, three edits.

Edit 1 — extend `fileConfig` (after `MaxAttachmentMB`):

```go
type fileConfig struct {
	SMTP struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
		From string `yaml:"from"`
	} `yaml:"smtp"`
	MaxAttachmentMB int      `yaml:"max_attachment_mb"`
	AllowedDomains  []string `yaml:"allowed_domains"`
}
```

Edit 2 — extend `Config` and add the normalization helper (place `normalizeDomains` as a new function after the `Config` struct):

```go
type Config struct {
	SMTP            SMTPConfig
	MaxAttachmentMB int
	AllowedDomains  []string
	// allowedDomainSet is the lookup form of AllowedDomains (lowercased,
	// trimmed, deduplicated); nil means the allowlist is inactive.
	allowedDomainSet map[string]struct{}
}

// normalizeDomains trims, lowercases, drops empties, and collapses
// duplicates while preserving first-seen order.
func normalizeDomains(in []string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}
```

Edit 3 — in `LoadConfig`, after the `cfg := &Config{...}` literal (which gains `AllowedDomains: fc.AllowedDomains,` in its field list) and before the defaults block, insert:

```go
	// Allowlist normalization: env replaces the file list entirely.
	if v := os.Getenv("ALLOWED_DOMAINS"); v != "" {
		cfg.AllowedDomains = strings.Split(v, ",")
	}
	cfg.AllowedDomains = normalizeDomains(cfg.AllowedDomains)
	if len(cfg.AllowedDomains) > 0 {
		set := make(map[string]struct{}, len(cfg.AllowedDomains))
		for _, d := range cfg.AllowedDomains {
			set[d] = struct{}{}
		}
		cfg.allowedDomainSet = set
	}
```

Note: when `normalizeDomains` returns nil (nothing configured) `AllowedDomains` stays nil and `allowedDomainSet` stays nil → inactive. This block sits with the other env overrides — place it right after the `MAX_ATTACHMENT_MB` env override block, before the final validation section. Add `"strings"` to the imports if not present.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run 'TestLoadConfig' ./...`
Expected: PASS — the three new tests plus all five existing config tests (env override test does not set ALLOWED_DOMAINS; `t.Setenv` isolates it).

- [ ] **Step 5: Commit**

```bash
git add config.go config_test.go
git commit -m "feat: allowed_domains config with env override and normalization

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: tools.go — the allowlist check in SendEmail

**Files:**
- Modify: `tools.go` (SendEmail, after the validEmail loop)
- Test: `tools_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `tools_test.go` (uses the existing `newToolEnv`/`callSendEmail`/`fakeSender` helpers; sets `env.Config.AllowedDomains` + `env.Config.allowedDomainSet` directly to simulate a configured allowlist):

```go
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
	if !strings.Contains(errMsg, "a@evil.com") {
		t.Errorf("error must name the rejected address, got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "example.com") || !strings.Contains(errMsg, "acme.org") {
		t.Errorf("error must list configured domains, got: %q", errMsg)
	}
	if fs.To != nil || fs.Msg != nil {
		t.Errorf("nothing must be sent when rejected (to=%v, msg set=%v)", fs.To, fs.Msg != nil)
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
	if !strings.Contains(errMsg, "b@evil.com") {
		t.Errorf("error must name the rejected cc address, got: %q", errMsg)
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
	if !strings.Contains(errMsg, "user@outside.com") {
		t.Errorf("error must name the rejected fallback address, got: %q", errMsg)
	}
}

func TestSendEmail_DomainAllowlist_MixedBlocked(t *testing.T) {
	env, fs := allowlistEnv(t, "example.com")
	hdr := http.Header{}
	hdr.Set("X-Multica-User-Email", "user@example.com")

	out := callSendEmail(t, env, hdr, map[string]any{
		"to":      []any{"a@example.com", "b@evil.com", "c@example.com"},
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != false {
		t.Fatalf("success = %v, want false (any out-of-list recipient rejects the whole call)", out["success"])
	}
	errMsg, _ := out["error"].(string)
	if !strings.Contains(errMsg, "b@evil.com") {
		t.Errorf("error must name the rejected address, got: %q", errMsg)
	}
	if fs.To != nil || fs.Msg != nil {
		t.Errorf("nothing must be sent when rejected (to=%v, msg set=%v)", fs.To, fs.Msg != nil)
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestSendEmail_DomainAllowlist' ./...`
Expected: FAIL — the blocked-* tests report `success = true` (check not implemented yet) and error-message assertions fail.

- [ ] **Step 3: Write minimal implementation**

In `tools.go`'s `SendEmail`, insert a new stanza immediately after the `validEmail` loop (after line 171's closing `}` of the for loop, before the `// --- attachments ---` comment):

```go
	// --- recipient domain allowlist ------------------------------------------
	if set := env.Config.allowedDomainSet; set != nil {
		var rejected []string
		for _, addr := range append(append([]string{}, to...), cc...) {
			domain := strings.ToLower(addr[strings.LastIndex(addr, "@")+1:])
			if _, ok := set[domain]; !ok {
				rejected = append(rejected, addr)
			}
		}
		if rejected != nil {
			return fail("recipient domain not allowed: %v not in allowed domains %v",
				strings.Join(rejected, ", "), env.Config.AllowedDomains)
		}
	}
```

The `domain check` runs after `validEmail` guaranteed exactly one `@` and non-empty parts, so `LastIndex(addr, "@")+1` slicing is safe. When `to` was empty, the fallback already replaced it with the user email, so the fallback address is checked in the same loop.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run 'TestSendEmail_DomainAllowlist' ./...`
Expected: PASS — all six subtests.

- [ ] **Step 5: Full suite for regressions**

Run: `go test ./...`
Expected: PASS — 59 test functions (53 existing + 3 config + 6 tools). Existing tests set no allowlist → inactive everywhere.

- [ ] **Step 6: Commit**

```bash
git add tools.go tools_test.go
git commit -m "feat: enforce recipient domain allowlist in send-email

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: README — document the allowlist

**Files:**
- Modify: `README.md` (Configure section)

- [ ] **Step 1: Update README**

In the `## Configure` section, extend the yaml example block (after `max_attachment_mb: 10   # optional`):

```markdown
allowed_domains:          # optional; omit = no restriction
  - "example.com"
  - "acme.org"
```

Extend the env-override sentence (after `MAX_ATTACHMENT_MB`):

```markdown
`MAX_ATTACHMENT_MB`. Environment variable `ALLOWED_DOMAINS` (comma-separated)
```

and append after the same paragraph (after "`smtp.from` (or `SMTP_FROM`) is required — the server exits at startup without it."):

```markdown
When `allowed_domains` (or `ALLOWED_DOMAINS`) lists at least one domain, every
recipient address — `to`, `cc`, and the fallback multica user email — must be
at one of those domains (case-insensitive, exact match, subdomains don't
match). Otherwise the whole message is rejected and the error names the
rejected addresses and the configured domains.
```

- [ ] **Step 2: Full suite + build still green**

Run: `make test && make build`
Expected: all tests PASS, binary builds.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: document the recipient domain allowlist

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Release sequence (post-implementation)

Follow `docs/superpowers/specs/2026-11-05-image-release-design.md` conventions (v0.0.2 release: two workflows green, 5 assets, multi-arch GHCR verify, anonymous manifest 200).

1. Final whole-feature code review (subagent, range = the feature commits).
2. `git push origin main` (pushes the pending signature-label commit + this feature's commits).
3. Bump `server.go` Version to `v0.1.0`, README GHCR/asset references `v0.0.2` → `v0.1.0` (3 places: docker pull, docker load, docker run lines), commit `chore: bump version to v0.1.0`, push.
4. `git tag v0.1.0 && git push origin v0.1.0`, watch both workflows to green.
5. Verify release assets (5 files), anonymous GHCR manifest check for `0.1.0` tag.
6. Download the arm64 image tar.gz to `~/Downloads`:
   `gh release download v0.1.0 --repo courage-zen/bigdata-mcp-email --pattern "email-mcp-image-v0.1.0-linux-arm64.tar.gz" --dir ~/Downloads --clobber`
   Verify with `gzip -t`.

## Amendment Log

(Deviations from this plan, appended as numbered items during execution.)
