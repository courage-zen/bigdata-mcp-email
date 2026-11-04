package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfig_FromFile(t *testing.T) {
	path := writeConfig(t, `
smtp:
  host: "127.0.0.1"
  port: 2525
  from: "noreply@example.com"
max_attachment_mb: 5
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTP.Host != "127.0.0.1" || cfg.SMTP.Port != 2525 || cfg.SMTP.From != "noreply@example.com" {
		t.Fatalf("smtp not loaded: %+v", cfg.SMTP)
	}
	if cfg.MaxAttachmentMB != 5 {
		t.Fatalf("max_attachment_mb = %d, want 5", cfg.MaxAttachmentMB)
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	path := writeConfig(t, "")
	// smtp.from is required (see TestLoadConfig_MissingFrom); supply it via
	// env so this test exercises the host/port/size defaults only.
	t.Setenv("SMTP_FROM", "noreply@example.com")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTP.Host != "127.0.0.1" || cfg.SMTP.Port != 2525 {
		t.Fatalf("smtp defaults wrong: %+v", cfg.SMTP)
	}
	if cfg.MaxAttachmentMB != 10 {
		t.Fatalf("default MaxAttachmentMB = %d, want 10", cfg.MaxAttachmentMB)
	}
}

func TestLoadConfig_EnvOverrides(t *testing.T) {
	path := writeConfig(t, `
smtp:
  host: "127.0.0.1"
  port: 2525
  from: "noreply@example.com"
`)
	t.Setenv("SMTP_HOST", "10.1.2.3")
	t.Setenv("SMTP_PORT", "999")
	t.Setenv("SMTP_FROM", "ops@example.com")
	t.Setenv("MAX_ATTACHMENT_MB", "25")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTP.Host != "10.1.2.3" || cfg.SMTP.Port != 999 || cfg.SMTP.From != "ops@example.com" {
		t.Fatalf("env override failed: %+v", cfg.SMTP)
	}
	if cfg.MaxAttachmentMB != 25 {
		t.Fatalf("MAX_ATTACHMENT_MB override failed: %d", cfg.MaxAttachmentMB)
	}
}

func TestLoadConfig_MissingFrom(t *testing.T) {
	path := writeConfig(t, `
smtp:
  host: "127.0.0.1"
  port: 2525
`)
	t.Setenv("SMTP_FROM", "")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error when smtp.from is missing and SMTP_FROM unset")
	}
}
