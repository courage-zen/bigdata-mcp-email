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

// unsetS3Env blanks every S3-family env var the contract reads so ambient
// developer/deployment env can't leak into a test. Empty string is
// equivalent to unset for every consumer in config.go (s3Env's fallback and
// the `!= ""` / TrimSpace chains all treat "" as unset).
func unsetS3Env(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"S3_BUCKET",
		"AWS_ACCESS_KEY_ID", "S3_ACCESS_KEY",
		"AWS_SECRET_ACCESS_KEY", "S3_SECRET_KEY",
		"AWS_ENDPOINT_URL", "S3_ENDPOINT", "S3_SECURE",
		"S3_REGION", "AWS_REGION",
		"S3_USE_PATH_STYLE",
		"S3_KEY_PREFIXES",
		"S3_MAX_FILE_BYTES",
	} {
		t.Setenv(name, "")
	}
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

func TestLoadConfig_S3Absent(t *testing.T) {
	unsetS3Env(t)
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3 != nil {
		t.Fatalf("S3 = %+v, want nil (feature off)", *cfg.S3)
	}
}

func TestLoadConfig_S3EnvTrio(t *testing.T) {
	unsetS3Env(t)
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
	unsetS3Env(t)
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
	unsetS3Env(t)
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
	unsetS3Env(t)
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	t.Run("standard beats legacy", func(t *testing.T) {
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
	})
	// s3Env falls back only on an empty standard value, not on mere presence:
	// AWS_ACCESS_KEY_ID="" must let S3_ACCESS_KEY through.
	t.Run("empty standard falls back to legacy", func(t *testing.T) {
		t.Setenv("S3_BUCKET", "B")
		t.Setenv("AWS_ACCESS_KEY_ID", "")
		t.Setenv("S3_ACCESS_KEY", "legacy-AK")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "SK")
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.S3.AccessKeyID != "legacy-AK" {
			t.Errorf("AccessKeyID = %q, want legacy name to win when standard is empty", cfg.S3.AccessKeyID)
		}
	})
}

func TestLoadConfig_S3EndpointURLStored(t *testing.T) {
	unsetS3Env(t)
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
	// The URL is stored verbatim; deriving Secure from the scheme lands with
	// the s3.go rebuild (Task 3), where the scheme split happens.
	if cfg.S3.EndpointURL != "http://minio.internal:9000" {
		t.Errorf("EndpointURL = %q", cfg.S3.EndpointURL)
	}
}

func TestLoadConfig_S3RegionChain(t *testing.T) {
	unsetS3Env(t)
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
			unsetS3Env(t)
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
	unsetS3Env(t)
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
	unsetS3Env(t)
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
	t.Run("valid", func(t *testing.T) {
		base(t)
		t.Setenv("S3_MAX_FILE_BYTES", "4096")
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.S3.MaxFileBytes != 4096 {
			t.Errorf("MaxFileBytes = %d, want 4096", cfg.S3.MaxFileBytes)
		}
	})
	// Zero, negative, and unparseable all fall back to the 100 MiB default
	// (the n > 0 guard in parseBytesEnv — 0 would otherwise disable the cap).
	for _, raw := range []string{"0", "-1", "abc"} {
		t.Run("fallback_"+raw, func(t *testing.T) {
			base(t)
			t.Setenv("S3_MAX_FILE_BYTES", raw)
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.S3.MaxFileBytes != 100*1024*1024 {
				t.Errorf("S3_MAX_FILE_BYTES=%q: MaxFileBytes = %d, want 100 MiB default", raw, cfg.S3.MaxFileBytes)
			}
		})
	}
}

func TestLoadConfig_S3SecureGarbageFallsBack(t *testing.T) {
	unsetS3Env(t)
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
`)
	// S3_SECURE=maybe is unparseable by strconv.ParseBool: the new contract
	// logs and falls back to the default rather than failing startup.
	t.Setenv("S3_BUCKET", "B")
	t.Setenv("AWS_ACCESS_KEY_ID", "AK")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SK")
	t.Setenv("S3_ENDPOINT", "minio.internal:9000")
	t.Setenv("S3_SECURE", "maybe")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("S3_SECURE=maybe must log and fall back, not fail: %v", err)
	}
	if cfg.S3 == nil {
		t.Fatal("S3 = nil, want configured from env trio")
	}
	if cfg.S3.Secure {
		t.Error("Secure = true, want false (default) for unparseable S3_SECURE")
	}
}

func TestLoadConfig_YamlS3SectionIgnored(t *testing.T) {
	unsetS3Env(t)
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
