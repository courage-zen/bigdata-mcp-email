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

func TestLoadConfig_AttachmentCapTooSmall(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
max_attachment_mb: -5
`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for negative max_attachment_mb")
	}

	path = writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	t.Setenv("MAX_ATTACHMENT_MB", "0")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for zero MAX_ATTACHMENT_MB (would disable the transport body cap)")
	}
}

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

func TestLoadConfig_AllowedDomainsEnvGarbage(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	t.Setenv("ALLOWED_DOMAINS", " , , ")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowedDomains != nil {
		t.Errorf("AllowedDomains = %v, want nil when env holds only separators", cfg.AllowedDomains)
	}
	if cfg.allowedDomainSet != nil {
		t.Errorf("allowedDomainSet = %v, want nil (inactive)", cfg.allowedDomainSet)
	}
}
