package main

import (
	"errors"
	"strings"
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

func TestValidateInputKey_TrailingDotExtensionBypass(t *testing.T) {
	cfg := keyCfg()
	// Win32 path.Base keeps trailing dots and the filesystem strips them on
	// save: "evil.exe." must not slip past the suffix check.
	for _, key := range []string{"evil.exe.", "payload.EXE.", "evil.exe..."} {
		if _, err := validateInputKey(key, cfg); err == nil {
			t.Errorf("key %q must be rejected (trailing-dot bypass)", key)
		}
	}
	for _, key := range []string{"report.v1.2.xlsx", "archive.tar.gz"} {
		if _, err := validateInputKey(key, cfg); err != nil {
			t.Errorf("legit dotted key %q rejected: %v", key, err)
		}
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
	// '?' is a legal S3 key char and must survive in a plain (non-URL) key.
	if k, err := resolveInputKey("workspaces/a b?q1=v.xlsx", cfg); err != nil || k != "workspaces/a b?q1=v.xlsx" {
		t.Fatalf("plain key with '?': resolve = %q, %v", k, err)
	}
	// Surrounding whitespace is trimmed before validation.
	if k, err := resolveInputKey("  workspaces/a/b.xlsx  ", cfg); err != nil || k != "workspaces/a/b.xlsx" {
		t.Fatalf("padded key: resolve = %q, %v", k, err)
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

func TestResolveInputKey_CaseInsensitiveScheme(t *testing.T) {
	cfg := keyCfg()
	k, err := resolveInputKey("HTTP://minio:9000/uploads/workspaces/a/b.xlsx", cfg)
	if err != nil || k != "workspaces/a/b.xlsx" {
		t.Fatalf("resolve = %q, %v", k, err)
	}
}

func TestResolveInputKey_VirtualHostWrongBucket(t *testing.T) {
	cfg := keyCfg()
	_, err := resolveInputKey("https://otherbucket.s3.amazonaws.com/workspaces/a.xlsx", cfg)
	if err == nil {
		t.Fatal("virtual-host wrong bucket must be rejected")
	}
	if !IsInvalidKey(err) {
		t.Errorf("expected an invalid-key error, got %v", err)
	}
	// Sanitization: neither bucket name may leak into the message.
	msg := err.Error()
	if strings.Contains(msg, "uploads") || strings.Contains(msg, "otherbucket") {
		t.Errorf("error leaks internals: %s", msg)
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

func TestHasDangerousExtension(t *testing.T) {
	for _, name := range []string{
		"evil.exe", "evil.EXE", "evil.exe.", "evil.exe...", "evil.CMD.",
		"evil.exe ", "evil.exe. ", "payload.dll", "tool.app",
		"archive.tar.gz.exe",
	} {
		if !hasDangerousExtension(name) {
			t.Errorf("name %q must be flagged", name)
		}
	}
	for _, name := range []string{
		"report.xlsx", "archive.tar.gz", "evil.txt", "exev", "evil.exe.txt",
		"", "readme", "evil",
	} {
		if hasDangerousExtension(name) {
			t.Errorf("name %q must not be flagged", name)
		}
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
