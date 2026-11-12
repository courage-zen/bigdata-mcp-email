# S3 Usage Standard Alignment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Align the read-side S3 surface (env contract, key contract, IO, error contract, health) with the cross-service S3 usage standard.

**Architecture:** Rewrite the S3 config resolution to the standard's env-only contract (soft-disable tri-state, legacy names readable for one version), port excel-mcp's key validation/URL-normalization/allowlist into a new `s3keys.go`, rebuild `MinioObjectStore` with a Stat precheck + layered timeouts + sanitized sentinel errors, and wire new error codes (`S3_INVALID_KEY`, `S3_OBJECT_TOO_LARGE`) into the tool layer.

**Tech Stack:** Go 1.26, minio-go v7.2.1, gopkg.in/yaml.v3, stdlib testing.

**Spec:** `docs/superpowers/specs/2026-11-12-s3-standard-alignment-design.md`

**Conventions:** Run tests via `go test ./...` (or `make test` = `go vet ./... && go test ./...`). Commit after every task. Existing tests use `t.Setenv` + `LoadConfig(path)` — keep that pattern. This project works on `main` directly (no worktree required; the brainstorming/writing-plans flow did not create one).

**Key facts (verified against minio-go v7.2.1 source):**

- `minio.New(endpoint, opts)` requires a bare host:port endpoint; scheme comes from `opts.Secure`. `opts.Transport` (an `http.RoundTripper`) overrides the built-in transport; `minio.DefaultTransport(secure)` returns `(*http.Transport, error)` and its dial/response-header timeouts are mutable fields.
- `minio.BucketLookupAuto` (the default) uses path-style for custom endpoints and virtual-host only for amazon/google/aliyun — exactly the standard's inference rule, so "unset" maps to auto.
- `Options.Region` non-empty skips the `GET /{bucket}?location=` probe.
- A 404 on a HEAD/GET with an object name is synthesized as `ErrorResponse{Code: "NoSuchKey"}` (api-error-response.go), so `StatObject` missing objects map through the same `ToErrorResponse` check as `GetObject`.
- `httpRespToErrorResponse` parses the XML `<Code>` when present (fake S3 must keep emitting it), and synthesizes by status code otherwise.

**Test S3 server shape (shared by s3_test.go):** the existing `newFakeS3` serves `GET /multica-artifacts/{key}` path-style. `StatObject` issues `HEAD /multica-artifacts/{key}` — the same mux path — so the fake handler must dispatch on `r.Method` as well.

---

### Task 1: S3Config — new fields and env resolution

**Files:**
- Modify: `config.go` (S3Config struct, fileConfig, LoadConfig)
- Test: `config_test.go`

The new `S3Config` (replacing the current 5-field struct):

```go
// S3Config is the standard-mandated env contract. nil = S3 attachments
// disabled (any of the three required vars missing — a soft failure; the
// tri-state "configured but incomplete" does not exist).
type S3Config struct {
	Bucket       string
	Region       string
	AccessKeyID  string
	SecretKey    string
	EndpointURL  string // full URL with scheme (AWS_ENDPOINT_URL) or "" = native AWS
	Endpoint     string // bare host:port after scheme strip (minio.New wants this)
	Secure       bool
	UsePathStyle bool // resolved S3_USE_PATH_STYLE; default inferred (custom endpoint -> true)
	// AllowedKeyPrefixes is the segment-aware S3_KEY_PREFIXES allowlist;
	// empty = whole bucket readable.
	AllowedKeyPrefixes []string
	// MaxFileBytes is the S3 transport-layer read cap (S3_MAX_FILE_BYTES,
	// default 100 MiB). The SMTP-attachment cap (MaxAttachmentMB) is a
	// separate, email-specific limit.
	MaxFileBytes int64
}
```

`fileConfig.S3` keeps its yaml struct **only** to detect "a s3: section exists" for the deprecation warning; its values are discarded.

**New helper functions in config.go:**

```go
// s3Env returns the effective value for an S3 variable: the standard name
// first, then the legacy name (readable for one version cycle, standard wins).
func s3Env(std, legacy string) string {
	if v := os.Getenv(std); v != "" {
		return v
	}
	return os.Getenv(legacy)
}

// parseBytesEnv parses a byte-count env var; empty or unparseable falls back
// to def (excel-mcp parse_bytes semantics).
func parseBytesEnv(raw string, def int64) int64 {
	if n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && n > 0 {
		return n
	}
	return def
}
```

**LoadConfig's S3 block (replaces the current s3Present + env-override + incomplete-check code):**

```go
	// S3: env-only contract (the S3 usage standard). A YAML s3: section is a
	// deprecated no-op: warn so operators notice, then ignore its values.
	if fc.S3.Endpoint != "" || fc.S3.AccessKey != "" || fc.S3.SecretKey != "" || fc.S3.Bucket != "" || fc.S3.Secure {
		log.Printf("config: the yaml s3: section is deprecated and ignored; set S3_BUCKET / AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (+ AWS_ENDPOINT_URL, S3_REGION, S3_USE_PATH_STYLE, S3_KEY_PREFIXES, S3_MAX_FILE_BYTES) instead")
	}

	// Required trio: bucket + static credentials. Any one missing = S3 off
	// (soft disable; the caller gets S3_UNAVAILABLE per call).
	bucket := strings.TrimSpace(s3Env("S3_BUCKET", "S3_BUCKET"))
	accessKeyID := strings.TrimSpace(s3Env("AWS_ACCESS_KEY_ID", "S3_ACCESS_KEY"))
	secretKey := strings.TrimSpace(s3Env("AWS_SECRET_ACCESS_KEY", "S3_SECRET_KEY"))
	partial := bucket != "" || accessKeyID != "" || secretKey != ""
	if bucket == "" || accessKeyID == "" || secretKey == "" {
		if partial {
			// Name only the missing vars, never their values.
			missing := []string{}
			if bucket == "" {
				missing = append(missing, "S3_BUCKET")
			}
			if accessKeyID == "" {
				missing = append(missing, "AWS_ACCESS_KEY_ID")
			}
			if secretKey == "" {
				missing = append(missing, "AWS_SECRET_ACCESS_KEY")
			}
			log.Printf("config: s3 disabled, missing env: %s (s3_key attachments return S3_UNAVAILABLE)", strings.Join(missing, ", "))
		}
	} else {
		cfg.S3 = &S3Config{
			Bucket:      bucket,
			AccessKeyID: accessKeyID,
			SecretKey:   secretKey,
		}
	}
	if cfg.S3 != nil {
		s3 := cfg.S3
		// Region fallback chain: S3_REGION -> AWS_REGION -> us-east-1.
		s3.Region = strings.TrimSpace(s3Env("S3_REGION", ""))
		if s3.Region == "" {
			s3.Region = strings.TrimSpace(os.Getenv("AWS_REGION"))
		}
		if s3.Region == "" {
			s3.Region = "us-east-1"
		}
		// Endpoint: standard form is a full URL whose scheme picks Secure;
		// the legacy pair is a bare host:port + S3_SECURE (default false).
		if v := s3Env("AWS_ENDPOINT_URL", ""); v != "" {
			s3.EndpointURL = v
		} else if v := os.Getenv("S3_ENDPOINT"); v != "" {
			s3.Endpoint = v
			s3.Secure = parseBoolEnv("S3_SECURE", false)
		}
		s3.UsePathStyle = parsePathStyleEnv(s3Env("S3_USE_PATH_STYLE", ""), s3.EndpointURL != "" || s3.Endpoint != "")
		s3.AllowedKeyPrefixes = splitCSV(s3Env("S3_KEY_PREFIXES", ""))
		s3.MaxFileBytes = parseBytesEnv(s3Env("S3_MAX_FILE_BYTES", ""), 100*1024*1024)
	}
```

Where (also in config.go):

```go
// parsePathStyleEnv maps 1/true/yes -> true, 0/false/no -> false; anything
// else (including unset) falls back to the inference: custom endpoint ->
// path style, native AWS -> virtual host.
func parsePathStyleEnv(raw string, hasEndpoint bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	return hasEndpoint
}

// splitCSV splits a comma-separated env var into trimmed non-empty parts.
func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}
```

And a bool parser (strict, like the current S3_SECURE behavior):

```go
// parseBoolEnv parses a boolean env var; unparseable non-empty values are a
// hard error (bad values must not silently flip the trust model).
func parseBoolEnv(name string, def bool) bool {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
		// A bad value at this point means a misconfigured deployment; treat
		// it as fatal via panic is overkill — fall back to default but log.
		log.Printf("config: %s=%q is not a valid bool, using %v", name, v, def)
	}
	return def
}
```

The **old S3 env-override block** (`if cfg.S3 != nil { if v := os.Getenv("S3_ENDPOINT") ... }`) and the **old incomplete-section startup error** (`s3 section is present but incomplete`) are **deleted**. Add `"log"` to imports.

**Delete/replace these existing tests in config_test.go** (their subject no longer exists): `TestLoadConfig_S3FromFile`, `TestLoadConfig_S3EnvOverrides`, `TestLoadConfig_S3Incomplete`, `TestLoadConfig_S3EnvCompletesFileSection`, `TestLoadConfig_S3EmptySection`, `TestLoadConfig_S3SecureGarbage`. Keep `TestLoadConfig_S3Absent` (now asserts env-only default). Add:

```go
func TestLoadConfig_S3EnvTrio(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	t.Setenv("S3_BUCKET", "multica-artifacts")
	t.Setenv("AWS_ACCESS_KEY_ID", "AK")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SK")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.S3
	if s == nil {
		t.Fatal("S3 = nil, want configured from env trio")
	}
	if s.Bucket != "multica-artifacts" || s.AccessKeyID != "AK" || s.SecretKey != "SK" {
		t.Fatalf("s3 = %+v", *s)
	}
	if s.Region != "us-east-1" {
		t.Errorf("Region = %q, want us-east-1 default", s.Region)
	}
	if s.UsePathStyle {
		t.Errorf("UsePathStyle = true, want virtual-host without endpoint")
	}
	if s.MaxFileBytes != 100*1024*1024 {
		t.Errorf("MaxFileBytes = %d, want 100 MiB default", s.MaxFileBytes)
	}
	if s.AllowedKeyPrefixes != nil {
		t.Errorf("AllowedKeyPrefixes = %v, want nil", s.AllowedKeyPrefixes)
	}
}

func TestLoadConfig_S3PartialTrioSoftDisable(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	// Two of three: partial config must NOT be a startup error — S3 is off.
	t.Setenv("S3_BUCKET", "B")
	t.Setenv("AWS_ACCESS_KEY_ID", "AK")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("partial trio must soft-disable, not fail: %v", err)
	}
	if cfg.S3 != nil {
		t.Fatalf("S3 = %+v, want nil (soft disable)", *cfg.S3)
	}
}

func TestLoadConfig_S3LegacyNames(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	t.Setenv("S3_BUCKET", "B")
	t.Setenv("S3_ACCESS_KEY", "AK")
	t.Setenv("S3_SECRET_KEY", "SK")
	t.Setenv("S3_ENDPOINT", "minio.internal:9000")
	t.Setenv("S3_SECURE", "true")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.S3
	if s == nil {
		t.Fatal("S3 = nil, want legacy names accepted for one version")
	}
	if s.Endpoint != "minio.internal:9000" || !s.Secure {
		t.Fatalf("legacy endpoint/secure: %+v", *s)
	}
}

func TestLoadConfig_S3StandardNameWins(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	t.Setenv("S3_BUCKET", "B")
	t.Setenv("AWS_ACCESS_KEY_ID", "std-AK")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "std-SK")
	t.Setenv("S3_ACCESS_KEY", "legacy-AK")
	t.Setenv("AWS_ENDPOINT_URL", "https://s3.example.com")
	t.Setenv("S3_ENDPOINT", "legacy:9000")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.S3
	if s.AccessKeyID != "std-AK" {
		t.Errorf("AccessKeyID = %q, want standard name to win", s.AccessKeyID)
	}
	if s.EndpointURL != "https://s3.example.com" {
		t.Errorf("EndpointURL = %q, want standard endpoint name to win", s.EndpointURL)
	}
	if s.Endpoint != "" || s.Secure {
		t.Errorf("legacy endpoint must be inert when standard endpoint set: %+v", *s)
	}
}

func TestLoadConfig_S3EndpointURLSchemePicksSecure(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	t.Setenv("S3_BUCKET", "B")
	t.Setenv("AWS_ACCESS_KEY_ID", "AK")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SK")
	t.Setenv("AWS_ENDPOINT_URL", "http://minio.internal:9000")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3.EndpointURL != "http://minio.internal:9000" {
		t.Errorf("EndpointURL = %q", cfg.S3.EndpointURL)
	}
}

func TestLoadConfig_S3RegionChain(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	base := func(t *testing.T) {
		t.Helper()
		t.Setenv("S3_BUCKET", "B")
		t.Setenv("AWS_ACCESS_KEY_ID", "AK")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "SK")
	}
	t.Run("S3_REGION wins", func(t *testing.T) {
		base(t)
		t.Setenv("S3_REGION", "cn-north-1")
		t.Setenv("AWS_REGION", "us-west-2")
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.S3.Region != "cn-north-1" {
			t.Errorf("Region = %q", cfg.S3.Region)
		}
	})
	t.Run("AWS_REGION fallback", func(t *testing.T) {
		base(t)
		t.Setenv("AWS_REGION", "us-west-2")
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.S3.Region != "us-west-2" {
			t.Errorf("Region = %q", cfg.S3.Region)
		}
	})
}

func TestLoadConfig_S3UsePathStyle(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	tests := []struct {
		name, env, endpoint string
		want                bool
	}{
		{"explicit true no endpoint", "1", "", true},
		{"explicit true", "yes", "http://m:9000", true},
		{"explicit false overrides endpoint", "false", "http://m:9000", false},
		{"inferred from endpoint", "", "http://m:9000", true},
		{"inferred virtual-host", "", "", false},
		{"garbage falls back to inference", "maybe", "http://m:9000", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("S3_BUCKET", "B")
			t.Setenv("AWS_ACCESS_KEY_ID", "AK")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "SK")
			if tt.env != "" {
				t.Setenv("S3_USE_PATH_STYLE", tt.env)
			}
			if tt.endpoint != "" {
				t.Setenv("AWS_ENDPOINT_URL", tt.endpoint)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.S3.UsePathStyle != tt.want {
				t.Fatalf("UsePathStyle = %v, want %v", cfg.S3.UsePathStyle, tt.want)
			}
		})
	}
}

func TestLoadConfig_S3KeyPrefixes(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	t.Setenv("S3_BUCKET", "B")
	t.Setenv("AWS_ACCESS_KEY_ID", "AK")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SK")
	t.Setenv("S3_KEY_PREFIXES", " workspaces/ws-1 , reports/ ,, workspaces/ws-1 ")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"workspaces/ws-1", "reports/"}
	got := cfg.S3.AllowedKeyPrefixes
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("AllowedKeyPrefixes = %v, want %v", got, want)
	}
}

func TestLoadConfig_S3MaxFileBytes(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	t.Setenv("S3_BUCKET", "B")
	t.Setenv("AWS_ACCESS_KEY_ID", "AK")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SK")
	t.Setenv("S3_MAX_FILE_BYTES", "4096")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3.MaxFileBytes != 4096 {
		t.Errorf("MaxFileBytes = %d, want 4096", cfg.S3.MaxFileBytes)
	}
}

func TestLoadConfig_YamlS3SectionIgnored(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
s3:
  endpoint: "minio.internal:9000"
  access_key: "AK"
  secret_key: "SK"
  bucket: "multica-artifacts"
`)
	// No env trio: the yaml section alone must not configure S3.
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3 != nil {
		t.Fatalf("S3 = %+v, want nil (yaml s3: is a deprecated no-op)", *cfg.S3)
	}
}
```

- [ ] **Step 1: Write the new tests** (delete the six obsolete S3 tests, add the ten above). The build fails — `S3Config` lacks the new fields.
- [ ] **Step 2: Run** `go test ./... 2>&1 | head -30`. Expected: compile errors in config.go/config_test.go (unknown fields `AccessKeyID` etc.).
- [ ] **Step 3: Implement** the new `S3Config`, helpers, and LoadConfig block in config.go. Delete the old S3 override/validation blocks. Update `main.go`'s `NewMinioObjectStore` call site to compile: change signature usage to pass `cfg.S3` wholesale (see Task 3; for now pass the fields that still exist — build must stay green). Minimal interim: `NewMinioObjectStore(cfg.S3.Endpoint, cfg.S3.AccessKey, cfg.S3.SecretKey, cfg.S3.Bucket, cfg.S3.Secure)` using `AccessKeyID`/`SecretKey`.
- [ ] **Step 4: Run** `go vet ./... && go test ./...`. Expected: PASS (s3_test.go and tools_test.go still compile — fake stores implement the unchanged `ObjectStore` interface).
- [ ] **Step 5: Commit** `feat: s3 env contract — standard names, soft-disable tri-state, legacy one-cycle`

---

### Task 2: s3keys.go — key validation, URL normalization, allowlist

**Files:**
- Create: `s3keys.go`
- Test: `s3keys_test.go` (new)

Port of excel-mcp `src/s3keys.rs` (read side only). Errors carry a reason category, never the key:

```go
package main

import (
	"fmt"
	"strings"
)

// dangerousExtensions is the shared S3 usage-standard blocklist of
// extensions never worth fetching (smuggling executables through an
// attachment channel).
var dangerousExtensions = []string{
	".exe", ".msi", ".bat", ".cmd", ".sh", ".ps1", ".scr", ".vbs",
	".jar", ".app", ".dmg", ".pkg", ".deb", ".rpm", ".so", ".dll",
}

// invalidKeyError reports why a key was refused, without echoing the key.
type invalidKeyError struct{ reason string }

func (e *invalidKeyError) Error() string { return "invalid s3 key: " + e.reason }

func invalidKey(reason string) error { return &invalidKeyError{reason: reason} }

// IsInvalidKey reports whether err is a key-rejection (caller maps to
// S3_INVALID_KEY).
func IsInvalidKey(err error) bool {
	var ik *invalidKeyError
	return errors.As(err, &ik)
}

// validateInputKey enforces the key rules: plain '/'-separated path, no
// traversal, no dangerous extension, inside the allowlist when one is set.
func validateInputKey(key string, s3 *S3Config) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", invalidKey("key is empty")
	}
	if strings.HasPrefix(key, "/") {
		return "", invalidKey("key must not start with '/'")
	}
	if strings.Contains(key, "\\") || strings.Contains(key, "//") {
		return "", invalidKey("key must be a plain '/'-separated path")
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", invalidKey("key must not contain '.', '..' or empty segments")
		}
	}
	lower := strings.ToLower(key)
	for _, ext := range dangerousExtensions {
		if strings.HasSuffix(lower, ext) {
			return "", invalidKey("key extension is refused")
		}
	}
	if !prefixAllowed(key, s3.AllowedKeyPrefixes) {
		return "", invalidKey("key is outside the configured allowlist")
	}
	return key, nil
}

// prefixAllowed matches key against prefixes at a segment boundary:
// prefix "workspaces/ws-1" matches "workspaces/ws-1" and
// "workspaces/ws-1/a.xlsx", not "workspaces/ws-11/a.xlsx". An empty prefix
// list allows everything.
func prefixAllowed(key string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if key == p {
			return true
		}
		if strings.HasPrefix(key, p) && (strings.HasSuffix(p, "/") || len(key) > len(p) && key[len(p)] == '/') {
			return true
		}
	}
	return false
}

// resolveInputKey normalizes a tool-supplied s3_key argument: a plain key
// passes through; a full object-store URL (as pasted from attachment JSON,
// including presigned query signatures) is reduced to its key. Both forms
// then pass the same validateInputKey rules.
func resolveInputKey(raw string, s3 *S3Config) (string, error) {
	raw = strings.TrimSpace(raw)
	rest, isURL := trimScheme(raw)
	if !isURL {
		return validateInputKey(raw, s3)
	}
	// Drop the query string (presign signatures) before splitting.
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest = rest[:i]
	}
	host, path, ok := strings.Cut(rest, "/")
	if !ok {
		return "", invalidKey("URL contains no object key; pass s3_key as a plain key or a full object-store URL of an object in the configured bucket")
	}
	// Virtual-host style: host is "<bucket>.<domain>".
	if domain, isVH := strings.CutPrefix(host, s3.Bucket+"."); isVH && domain != "" {
		return validateInputKey(path, s3)
	}
	// Path style: the first path segment must be the bucket.
	if path == s3.Bucket || path == s3.Bucket+"/" {
		return "", invalidKey("URL contains no object key; pass s3_key as a plain key or a full object-store URL of an object in the configured bucket")
	}
	key, isPS := strings.CutPrefix(path, s3.Bucket+"/")
	if !isPS {
		return "", invalidKey("URL host or first path segment does not match the configured bucket; expected an object-store URL of an object in the configured bucket")
	}
	return validateInputKey(key, s3)
}

func trimScheme(raw string) (rest string, isURL bool) {
	if r, ok := strings.CutPrefix(raw, "http://"); ok {
		return r, true
	}
	if r, ok := strings.CutPrefix(raw, "https://"); ok {
		return r, true
	}
	return raw, false
}
```

`errors` must be imported. **The sanitize property**: every error message is a constant reason string; the key/URL never appears. **Bucket name leakage**: the reason strings deliberately do not include `s3.Bucket`.

**Tests (s3keys_test.go, new):**

```go
package main

import (
	"errors"
	"testing"
)

func keyCfg(prefixes ...string) *S3Config {
	return &S3Config{
		Bucket:             "uploads",
		Region:             "r",
		AccessKeyID:        "ak",
		SecretKey:          "sk",
		EndpointURL:        "http://minio:9000",
		AllowedKeyPrefixes: prefixes,
		MaxFileBytes:       100 << 20,
	}
}

func TestValidateInputKey_RejectsBadForms(t *testing.T) {
	cfg := keyCfg()
	for _, key := range []string{
		"", "  ", "/abs/path.xlsx", "a//b.xlsx", "a/./b.xlsx", "a/../b.xlsx",
		`a\b.xlsx`, "a/", "a/./",
	} {
		if _, err := validateInputKey(key, cfg); err == nil {
			t.Errorf("key %q must be rejected", key)
		}
	}
}

func TestValidateInputKey_RejectsDangerousExtensions(t *testing.T) {
	cfg := keyCfg()
	for _, ext := range dangerousExtensions {
		if _, err := validateInputKey("payload"+ext, cfg); err == nil {
			t.Errorf("extension %s must be rejected", ext)
		}
		if _, err := validateInputKey("payload"+strings.ToUpper(ext), cfg); err == nil {
			t.Errorf("extension %s (upper) must be rejected", ext)
		}
	}
	if _, err := validateInputKey("report.xlsx", cfg); err != nil {
		t.Errorf("normal key rejected: %v", err)
	}
}

func TestValidateInputKey_AllowlistSegmentAware(t *testing.T) {
	cfg := keyCfg("workspaces/ws-1")
	if _, err := validateInputKey("workspaces/ws-1", cfg); err != nil {
		t.Errorf("exact prefix: %v", err)
	}
	if _, err := validateInputKey("workspaces/ws-1/a.xlsx", cfg); err != nil {
		t.Errorf("inside prefix: %v", err)
	}
	if _, err := validateInputKey("workspaces/ws-11/a.xlsx", cfg); err == nil {
		t.Error("ws-11 must not match prefix ws-1 (segment boundary)")
	}
	if _, err := validateInputKey("other/a.xlsx", cfg); err == nil {
		t.Error("outside allowlist must be rejected")
	}
}

func TestValidateInputKey_TrailingSlashPrefix(t *testing.T) {
	cfg := keyCfg("workspaces/")
	if _, err := validateInputKey("workspaces/a.xlsx", cfg); err != nil {
		t.Errorf("inside prefix: %v", err)
	}
	if _, err := validateInputKey("other/a.xlsx", cfg); err == nil {
		t.Error("outside allowlist must be rejected")
	}
}

func TestResolveInputKey_PlainKeyPasses(t *testing.T) {
	cfg := keyCfg()
	k, err := resolveInputKey("workspaces/a/b.xlsx", cfg)
	if err != nil || k != "workspaces/a/b.xlsx" {
		t.Fatalf("resolve = %q, %v", k, err)
	}
}

func TestResolveInputKey_PathStyleURL(t *testing.T) {
	cfg := keyCfg()
	k, err := resolveInputKey("http://minio:9000/uploads/workspaces/a/b.xlsx", cfg)
	if err != nil || k != "workspaces/a/b.xlsx" {
		t.Fatalf("resolve = %q, %v", k, err)
	}
}

func TestResolveInputKey_PresignedURL(t *testing.T) {
	cfg := keyCfg()
	k, err := resolveInputKey(
		"http://minio:9000/uploads/workspaces/a/b.xlsx?X-Amz-Signature=abc&X-Amz-Expires=600",
		cfg,
	)
	if err != nil || k != "workspaces/a/b.xlsx" {
		t.Fatalf("resolve = %q, %v", k, err)
	}
}

func TestResolveInputKey_VirtualHostURL(t *testing.T) {
	cfg := keyCfg()
	k, err := resolveInputKey("https://uploads.s3.amazonaws.com/workspaces/a/b.xlsx", cfg)
	if err != nil || k != "workspaces/a/b.xlsx" {
		t.Fatalf("resolve = %q, %v", k, err)
	}
}

func TestResolveInputKey_URLTraversalStillRejected(t *testing.T) {
	cfg := keyCfg()
	if _, err := resolveInputKey("http://minio:9000/uploads/../etc/passwd", cfg); err == nil {
		t.Error("traversal smuggled in URL must be rejected")
	}
	if _, err := resolveInputKey("https://uploads.s3.amazonaws.com/../../etc/passwd", cfg); err == nil {
		t.Error("traversal in virtual-host URL must be rejected")
	}
}

func TestResolveInputKey_NoKeyForms(t *testing.T) {
	cfg := keyCfg()
	for _, raw := range []string{
		"http://minio:9000",
		"http://minio:9000/uploads",
		"http://minio:9000/uploads/",
	} {
		if _, err := resolveInputKey(raw, cfg); err == nil {
			t.Errorf("URL %q must be rejected (no key)", raw)
		}
	}
}

func TestResolveInputKey_WrongBucket(t *testing.T) {
	cfg := keyCfg()
	_, err := resolveInputKey("http://other-host:9000/otherbucket/workspaces/a.xlsx", cfg)
	if err == nil {
		t.Fatal("wrong bucket must be rejected")
	}
	// Sanitization: the message must not leak the bucket or echo the URL.
	msg := err.Error()
	if strings.Contains(msg, "uploads") || strings.Contains(msg, "other-host") || strings.Contains(msg, "otherbucket") {
		t.Errorf("error leaks internals: %s", msg)
	}
}

func TestIsInvalidKey(t *testing.T) {
	if !IsInvalidKey(invalidKey("test")) {
		t.Error("IsInvalidKey must recognize its own errors")
	}
	if IsInvalidKey(errors.New("other")) {
		t.Error("IsInvalidKey must not claim unrelated errors")
	}
}
```

(`strings` import needed in the test file.)

- [ ] **Step 1: Write s3keys_test.go** (above; adjust imports — it needs `strings`).
- [ ] **Step 2: Run** `go test ./... 2>&1 | head -20`. Expected: FAIL — `s3keys.go` does not exist.
- [ ] **Step 3: Create s3keys.go** with the implementation (with the single query-strip form).
- [ ] **Step 4: Run** `go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: s3key validation, URL normalization, prefix allowlist`

---

### Task 3: s3.go — Stat precheck, layered timeouts, sanitized errors

**Files:**
- Modify: `s3.go` (rewrite)
- Modify: `main.go` (construction call site)
- Test: `s3_test.go`

The rebuilt store. Interface and sentinel set change:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ObjectStore reads attachment objects from the configured bucket. The
// interface keeps the tool testable, mirroring MailSender.
type ObjectStore interface {
	// Get fetches key, reading at most limit bytes: larger objects fail
	// without buffering. A missing object returns ErrObjectNotFound; size
	// rejections distinguish the two caps (S3 transport vs attachment);
	// anything else wraps the underlying error — sanitized, no
	// bucket/key/endpoint in the message.
	Get(key string, limit int64) ([]byte, error)
}

var (
	ErrObjectNotFound     = errors.New("s3 object not found")
	ErrObjectTooLarge     = errors.New("s3 object exceeds the s3 transport size limit")
	ErrAttachmentTooLarge = errors.New("s3 object exceeds the attachment size limit")
)

// timeout constants (the standard's layered values): 10s connect, 60s
// response-header, 60s whole-download wall clock.
const (
	s3ConnectTimeout   = 10 * time.Second
	s3ReadTimeout      = 60 * time.Second
	s3DownloadDeadline = 60 * time.Second
)

// MinioObjectStore is the ObjectStore over a minio-go client.
type MinioObjectStore struct {
	client *minio.Client
	bucket string
	// maxBytes is the S3 transport cap (S3_MAX_FILE_BYTES).
	maxBytes int64
}

// NewMinioObjectStore builds a store from the resolved S3Config: the
// standard endpoint (full URL, scheme picks Secure) or the legacy bare
// host:port + Secure flag. Layered timeouts are pinned on the transport
// (10s dial, 60s response header); Region is always set (skips minio-go's
// bucket-location probe); UsePathStyle forces the bucket addressing mode,
// defaulting to minio-go's auto inference (path-style for custom
// endpoints). A malformed endpoint is a startup failure.
func NewMinioObjectStore(cfg *S3Config) (*MinioObjectStore, error) {
	endpoint := cfg.Endpoint
	secure := cfg.Secure
	if cfg.EndpointURL != "" {
		u, err := url.Parse(cfg.EndpointURL)
		if err != nil {
			return nil, fmt.Errorf("s3: parse AWS_ENDPOINT_URL: %w", err)
		}
		if u.Host == "" {
			return nil, fmt.Errorf("s3: AWS_ENDPOINT_URL %q has no host", redactURL(cfg.EndpointURL))
		}
		endpoint = u.Host
		secure = u.Scheme == "https"
	}
	transport, err := minio.DefaultTransport(secure)
	if err != nil {
		return nil, fmt.Errorf("s3: default transport: %w", err)
	}
	transport.DialContext = (&net.Dialer{
		Timeout:   s3ConnectTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.ResponseHeaderTimeout = s3ReadTimeout

	lookup := minio.BucketLookupAuto
	if cfg.UsePathStyle {
		lookup = minio.BucketLookupPath
	}
	cli, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretKey, ""),
		Secure:       secure,
		Region:       cfg.Region,
		BucketLookup: lookup,
		Transport:    transport,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 endpoint %q: %w", endpoint, err)
	}
	return &MinioObjectStore{client: cli, bucket: cfg.Bucket, maxBytes: cfg.MaxFileBytes}, nil
}
```

**Note:** `UsePathStyle=false` maps to `BucketLookupAuto`, NOT `BucketLookupDNS` — auto already yields virtual-host for native AWS and path-style for custom endpoints, which is the standard's exact inference; forcing DNS would break custom endpoints that set `S3_USE_PATH_STYLE=0`. `redactURL` is:

```go
// redactURL strips credentials and query from an endpoint URL for startup
// error messages (query may carry nothing today, but be safe by habit).
func redactURL(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		if u.User != nil {
			u.User = nil
		}
		u.RawQuery = ""
		return u.String()
	}
	return "<unparseable endpoint>"
}
```

**Get with Stat precheck + sanitized mapping:**

```go
// Get implements ObjectStore.
func (s *MinioObjectStore) Get(key string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s3DownloadDeadline)
	defer cancel()

	// Metadata precheck: refuse before streaming.
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return nil, s.mapError("stat", key, err)
	}
	if info.Size > s.maxBytes {
		return nil, ErrObjectTooLarge
	}
	if info.Size > limit {
		return nil, ErrAttachmentTooLarge
	}

	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, s.mapError("get", key, err)
	}
	defer obj.Close()

	// Stream-cap re-check (object replaced between Stat and Get): read
	// min(maxBytes, limit)+1, oversize maps like the precheck.
	maxRead := s.maxBytes
	if limit < maxRead {
		maxRead = limit
	}
	data, err := io.ReadAll(io.LimitReader(obj, maxRead+1))
	if err != nil {
		return nil, s.mapError("get", key, err)
	}
	if int64(len(data)) > maxRead {
		if limit < s.maxBytes {
			return nil, ErrAttachmentTooLarge
		}
		return nil, ErrObjectTooLarge
	}
	return data, nil
}

// mapError folds minio error responses onto the sentinel set. Messages
// surfaced to callers are sanitized: the minio error Code (e.g.
// "NoSuchKey", "AccessDenied") is safe to include; the raw error text is
// not (it embeds bucket/key/endpoint). The raw error goes to the log.
func (s *MinioObjectStore) mapError(stage, key string, err error) error {
	resp := minio.ToErrorResponse(err)
	if resp.Code == "NoSuchKey" || resp.Code == "NoSuchObject" {
		return fmt.Errorf("%w", ErrObjectNotFound)
	}
	log.Printf("s3 %s %q failed: %v", stage, key, err)
	switch resp.Code {
	case "NoSuchBucket":
		return fmt.Errorf("s3: bucket not found (check S3_BUCKET/endpoint)")
	case "AccessDenied":
		return fmt.Errorf("s3: access denied (check S3 credentials)")
	case "":
		return fmt.Errorf("s3: %s failed (transport error)", stage)
	default:
		return fmt.Errorf("s3: %s failed: code %s", stage, resp.Code)
	}
}
```

(Add `"io"` and `"net/url"` to imports; note the deliberate bare `%w` wrap — no key.)

**main.go changes** (replacing the field-by-field call):

```go
	if cfg.S3 != nil {
		store, err := NewMinioObjectStore(cfg.S3)
		if err != nil {
			log.Fatalf("s3: %v", err)
		}
		env.Store = store
	}
```

**s3_test.go rewrite** — the fake server must answer HEAD (Stat) then GET; helpers change shape:

```go
package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// fakeS3Handler serves /{bucket}/{key} for both HEAD (stat) and GET:
// "ok/<n>bytes" -> n zero bytes; "404" -> NoSuchKey; "no-bucket" ->
// NoSuchBucket; "err" -> connection close.
func fakeS3Handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/multica-artifacts/")
		key = strings.TrimPrefix(key, "/")
		writeErr := func(code, msg string) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>` + code + `</Code><Message>` + msg + `</Message></Error>`))
		}
		switch {
		case key == "404":
			writeErr("NoSuchKey", "gone")
		case key == "no-bucket":
			writeErr("NoSuchBucket", "no such bucket")
		case key == "err":
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("server cannot hijack")
			}
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		case strings.HasPrefix(key, "ok/"):
			n, err := strconv.Atoi(key[len("ok/"):])
			if err != nil {
				t.Fatalf("unexpected key %q", key)
			}
			w.Header().Set("Content-Length", strconv.Itoa(n))
			w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, string(make([]byte, n)))
			}
		default:
			writeErr("NoSuchKey", "gone")
		}
	})
	return mux
}

func newTestObjectStore(t *testing.T, s3Cap int64) *MinioObjectStore {
	t.Helper()
	ts := httptest.NewServer(fakeS3Handler(t))
	t.Cleanup(ts.Close)
	cfg := &S3Config{
		Bucket:      "multica-artifacts",
		Region:      "us-east-1",
		AccessKeyID: "AK",
		SecretKey:   "SK",
		Endpoint:    ts.Listener.Addr().String(),
		Secure:      false,
		MaxFileBytes: s3Cap,
	}
	store, err := NewMinioObjectStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestObjectStore_Get_OK(t *testing.T) {
	store := newTestObjectStore(t, 1<<20)
	data, err := store.Get("ok/5", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 5 {
		t.Fatalf("len = %d, want 5", len(data))
	}
}

func TestObjectStore_Get_ObjectNotFound(t *testing.T) {
	store := newTestObjectStore(t, 1<<20)
	_, err := store.Get("404", 1024)
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want ErrObjectNotFound", err)
	}
}

func TestObjectStore_Get_S3CapTooLarge(t *testing.T) {
	// Object 10 bytes, S3 cap 5: S3-layer rejection, distinct sentinel.
	store := newTestObjectStore(t, 5)
	_, err := store.Get("ok/10", 1024)
	if !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("err = %v, want ErrObjectTooLarge", err)
	}
}

func TestObjectStore_Get_AttachmentCapTooLarge(t *testing.T) {
	// Object 10 bytes, S3 cap 1MiB, attachment limit 5: attachment layer.
	store := newTestObjectStore(t, 1<<20)
	_, err := store.Get("ok/10", 5)
	if !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("err = %v, want ErrAttachmentTooLarge", err)
	}
}

func TestObjectStore_Get_AtBoundaryOK(t *testing.T) {
	store := newTestObjectStore(t, 5)
	if _, err := store.Get("ok/5", 5); err != nil {
		t.Fatalf("at-limit Get err = %v, want nil", err)
	}
}

func TestObjectStore_Get_OtherErrorSanitized(t *testing.T) {
	store := newTestObjectStore(t, 1<<20)
	_, err := store.Get("err", 1024)
	if err == nil || errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want a non-sentinel error", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "multica-artifacts") || strings.Contains(msg, "127.0.0.1") {
		t.Errorf("error leaks internals: %s", msg)
	}
}

func TestObjectStore_Get_NoSuchBucket(t *testing.T) {
	store := newTestObjectStore(t, 1<<20)
	_, err := store.Get("no-bucket", 1024)
	if err == nil || errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want a non-ErrObjectNotFound error", err)
	}
	if !strings.Contains(err.Error(), "bucket") {
		t.Errorf("err should hint at bucket config: %v", err)
	}
}
```

- [ ] **Step 1: Rewrite s3_test.go** with the above (delete the old helper + five tests; `fakeObjectStore` in tools_test.go needs updating for the new sentinel — see Task 4; to keep this task compiling, update `fakeObjectStore` now, minimally: it returns `ErrAttachmentTooLarge` where it returned `ErrObjectTooLarge`).
- [ ] **Step 2: Run** `go test ./...`. Expected: compile errors (`NewMinioObjectStore` signature change).
- [ ] **Step 3: Rewrite s3.go** and the main.go call site.
- [ ] **Step 4: Run** `go vet ./... && go test ./...`. Expected: PASS — note `TestSendEmail_S3TooLarge` now expects `S3_OBJECT_TOO_LARGE`-adjacent behavior; if it fails on the error code, that is Task 4's subject — fix the fake store's mapping here so the suite is green (fake returns `ErrAttachmentTooLarge`, existing test expects `ATTACHMENT_TOO_LARGE` — still passes since Task 4 keeps that mapping for the attachment layer).
- [ ] **Step 5: Commit** `feat: s3 stat precheck, layered timeouts, sanitized error mapping`

---

### Task 4: tools.go — S3_INVALID_KEY / S3_OBJECT_TOO_LARGE wiring

**Files:**
- Modify: `tools.go` (error code constants, resolveAttachment s3 branch)
- Test: `tools_test.go`

New constants:

```go
	errCodeS3InvalidKey      = "S3_INVALID_KEY"
	errCodeS3ObjectTooLarge  = "S3_OBJECT_TOO_LARGE"
```

**resolveAttachment's s3 branch — new order** (unavailable check first, then key resolution, then fetch):

```go
	} else {
		if s3key == "" {
			return Attachment{}, fail(errCodeInvalidParamValue, "attachments[%d].s3_key must be a non-empty string", i)
		}
		if env.Store == nil {
			return Attachment{}, fail(errCodeS3Unavailable, "s3 attachments are not configured (set S3_BUCKET / AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY)")
		}
		key, err := resolveInputKey(s3key, env.Config.S3)
		if err != nil {
			return Attachment{}, fail(errCodeS3InvalidKey, "attachments[%d].s3_key: %v", i, err)
		}
		if filename == "" {
			filename = path.Base(key)
		}
		content, err = env.Store.Get(key, limitBytes)
		if err != nil {
			if errors.Is(err, ErrObjectNotFound) {
				return Attachment{}, fail(errCodeS3ObjectNotFound, "attachments[%d].s3_key: object not found", i)
			}
			if errors.Is(err, ErrObjectTooLarge) {
				return Attachment{}, fail(errCodeS3ObjectTooLarge, "attachments[%d] exceeds the S3_MAX_FILE_BYTES limit", i)
			}
			if errors.Is(err, ErrAttachmentTooLarge) {
				return Attachment{}, fail(errCodeAttachmentTooLarge, "attachments[%d] exceeds the %d MiB attachment limit", i, env.Config.MaxAttachmentMB)
			}
			return Attachment{}, fail(errCodeS3FetchFailed, "attachments[%d].s3_key: %v", i, err)
		}
	}
```

The old leading-/trailing-slash check is subsumed by `validateInputKey` (leading `/` and empty trailing segment are both rejected) — delete it.

**Guard against nil S3 config:** `resolveInputKey(s3key, env.Config.S3)` would nil-deref when Store is nil — but the `env.Store == nil` check runs first, and Store is non-nil only when `cfg.S3 != nil` (main.go). For direct-use safety in tests, when `env.Config.S3 == nil` fall back to a key-only config:

```go
		s3cfg := env.Config.S3
		if s3cfg == nil {
			s3cfg = &S3Config{}
		}
		key, err := resolveInputKey(s3key, s3cfg)
```

(The empty config: no allowlist, no bucket match on URLs — plain keys still validate.)

**tools_test.go updates:**

- `fakeObjectStore.Get`: map oversize to `ErrAttachmentTooLarge` (attachment layer is what the tool-level limit governs); keep a way to return `ErrObjectTooLarge` for the new test:

```go
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
```

- New tests:

```go
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
		// Sanitization: the rejected key must not be echoed.
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
	// No bucket match (fake store has no bucket notion): rejected as
	// invalid, success=false — OR resolved if we align the bucket; assert
	// invalid here because the fake env has no S3Config bucket.
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
```

- Update `TestSendEmail_S3ObjectNotFound`: the message no longer contains the key — add the sanitization assertion:

```go
	if msg, _ := out["error"].(string); strings.Contains(msg, "missing") {
		t.Errorf("error message echoes the key: %s", msg)
	}
```

- `TestSendEmail_S3Unavailable` message expectation: the S3_UNAVAILABLE message text changed (env vars, not yaml section) — update any assertion that greps for "config" if it breaks (it only checks error_code, so it passes as-is).

- [ ] **Step 1: Write the new/updated tests.** Run `go test ./...` — compile fails (`errCodeS3InvalidKey` undefined).
- [ ] **Step 2: Implement** the constants + new s3 branch in tools.go.
- [ ] **Step 3: Run** `go vet ./... && go test ./...`. Expected: PASS. **Note** the s3 branch now sits inside `else` where the old code checked `s3key == ""` first — the empty-key check must stay first (it is in the new code above).
- [ ] **Step 4: Commit** `feat: S3_INVALID_KEY and S3_OBJECT_TOO_LARGE error codes, sanitized messages`

---

### Task 5: /health reports s3 state + tool description update

**Files:**
- Modify: `server.go` (health handler, tool description, version, input schema s3_key description)
- Test: `server_test.go`

**Health handler:**

```go
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		s3State := "not-configured"
		if env.Store != nil {
			s3State = "configured"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","s3":"` + s3State + `"}`))
	})
```

**Tool description** — replace the s3_key parenthetical:

```
an object-store reference ({"s3_key": "workspace-42/reports/x.xlsx"} — a plain
key or a full object-store URL; filename defaults to the key's base name; requires S3 env config)
```

**inputSchemaJSON s3_key property description** — same change:

```json
"s3_key": {"type": "string", "description": "object key (or full object-store URL) in the configured bucket; mutually exclusive with content_b64"}
```

**Version**: `mcp.NewServer(&mcp.Implementation{Name: "email-mcp", Version: "v0.3.0"}, nil)`.

**server_test.go — update health test:**

```go
func TestServer_HealthEndpoint(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPServer(t, "secret", fs))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"s3":"not-configured"`) {
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
	if resp2.StatusCode != 200 || !strings.Contains(string(b2), `"s3":"configured"`) {
		t.Fatalf("health with store = %d %s", resp2.StatusCode, b2)
	}
}
```

(The `newTestHTTPHandlerForHealth` alias wrapper can be deleted; its only caller is this test.)

- [ ] **Step 1: Update the test.** Run — fails on `"s3":"not-configured"` not found.
- [ ] **Step 2: Implement** the handler + description + version changes in server.go.
- [ ] **Step 3: Run** `go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 4: Commit** `feat: /health s3 state, URL-capable s3_key description, v0.3.0`

---

### Task 6: docs — README + config.example.yaml

**Files:**
- Modify: `README.md`
- Modify: `config.example.yaml`

**README changes:**

1. Replace the S3 paragraph in "Configure" with:

```markdown
S3 attachments are env-only (the S3 usage standard — no YAML section):

| variable | required | meaning |
|----------|----------|---------|
| `S3_BUCKET` | yes | bucket holding agent-produced files |
| `AWS_ACCESS_KEY_ID` | yes | static access key |
| `AWS_SECRET_ACCESS_KEY` | yes | static secret |
| `S3_REGION` → `AWS_REGION` | no | region; default `us-east-1` |
| `AWS_ENDPOINT_URL` | no | S3-compatible endpoint (MinIO/R2), full URL; `http://` = plaintext. Empty = native AWS S3 |
| `S3_USE_PATH_STYLE` | no | `1/true/yes` or `0/false/no`; unset = path-style when `AWS_ENDPOINT_URL` is set, virtual-host otherwise |
| `S3_KEY_PREFIXES` | no | comma-separated key-prefix allowlist, segment-boundary match; empty = whole bucket |
| `S3_MAX_FILE_BYTES` | no | S3 transport read cap, default 100 MiB |

Missing any required variable disables S3 attachments (`S3_UNAVAILABLE` per
call, `/health` reports `"not-configured"`); the server still starts. The
legacy names `S3_ACCESS_KEY` / `S3_SECRET_KEY` / `S3_ENDPOINT` +
`S3_SECURE` remain readable for one version (standard names win); the yaml
`s3:` section is ignored.
```

2. In "Attachments", note the two size caps:

```markdown
Two size caps apply to `s3_key` attachments: `S3_MAX_FILE_BYTES` (S3 layer,
default 100 MiB, error `S3_OBJECT_TOO_LARGE`) and `max_attachment_mb`
(attachment layer, default 10 MiB, error `ATTACHMENT_TOO_LARGE`). `s3_key`
accepts a plain key or a full object-store URL (presigned query strings are
stripped). Keys are validated: no traversal, no dangerous extensions
(`.exe` etc.), and `S3_KEY_PREFIXES` allowlist when set (violation:
`S3_INVALID_KEY`).
```

3. Update the error-code example list to include `S3_INVALID_KEY` and `S3_OBJECT_TOO_LARGE`.
4. Add a migration note: v0.3.0 removes the yaml `s3:` section (ignored, startup warning), renames S3-layer oversize from `ATTACHMENT_TOO_LARGE` to `S3_OBJECT_TOO_LARGE`, and makes a partially-configured S3 a soft disable instead of a startup error.

**config.example.yaml:** delete the `s3:` example block and its comment lines; replace with:

```yaml
# S3 (s3_key attachments) is env-only — see README "Configure":
#   S3_BUCKET / AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (required trio)
#   AWS_ENDPOINT_URL, S3_REGION, S3_USE_PATH_STYLE, S3_KEY_PREFIXES,
#   S3_MAX_FILE_BYTES (optional)
```

- [ ] **Step 1: Edit README.md and config.example.yaml** as above.
- [ ] **Step 2: Run** `go vet ./... && go test ./...` (docs-only; suite must stay green).
- [ ] **Step 3: Commit** `docs: S3 standard env contract, two size caps, v0.3.0 migration notes`

---

### Task 7: final verification

- [ ] **Step 1:** Run `make test` (= `go vet ./... && go test ./...`). Expected: all PASS.
- [ ] **Step 2:** Run `make build`. Expected: clean build.
- [ ] **Step 3:** Spot-check the standard's checklist once more against the code: env contract (config.go), key contract (s3keys.go), IO (s3.go Stat/timeouts), error codes (tools.go), health (server.go). Any miss: fix + re-run tests.
- [ ] **Step 4: Commit** any straggler fixes; else nothing to commit.

---

## Self-Review

**Spec coverage:** env contract (Task 1), key contract (Task 2), IO + timeouts + sanitization (Task 3), error codes (Task 4), health + schema/description (Task 5), docs/version (Task 6). Spec's "not doing" list (write side, temp files, processing timeout, truncation markers, `s3://` scheme, SMTP config) — none appear in tasks. ✓

**Placeholder scan:** every step carries complete code or an exact edit; no TBD/TODO. ✓

**Type consistency:** `S3Config` fields (`Bucket/Region/AccessKeyID/SecretKey/EndpointURL/Endpoint/Secure/UsePathStyle/AllowedKeyPrefixes/MaxFileBytes`) used consistently across Tasks 1–3; `NewMinioObjectStore(*S3Config)` in Tasks 1(interim)/3/main.go; sentinels `ErrObjectNotFound/ErrObjectTooLarge/ErrAttachmentTooLarge` defined Task 3, consumed Task 4; `resolveInputKey/validateInputKey/IsInvalidKey` defined Task 2, consumed Task 4. ✓
