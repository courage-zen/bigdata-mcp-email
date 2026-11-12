package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// SMTPConfig points at the local-smtp-forwarder (or any plaintext SMTP
// submission endpoint) and names the envelope/From address.
type SMTPConfig struct {
	Host string
	Port int
	From string
}

func (s SMTPConfig) Addr() string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

// S3Config is the standard-mandated env contract. nil = S3 attachments
// disabled (any of the three required vars missing — a soft failure; the
// tri-state "configured but incomplete" does not exist).
type S3Config struct {
	Bucket       string
	Region       string
	AccessKeyID  string
	SecretKey    string
	EndpointURL  string // full URL with scheme (AWS_ENDPOINT_URL); empty = legacy pair or startup error (minio rejects a bare host-less config)
	Endpoint     string // bare host:port after scheme strip (minio.New wants this)
	Secure       bool
	UsePathStyle bool // resolved S3_USE_PATH_STYLE; default inferred (custom endpoint -> true)
	// PathStyleExplicit records whether S3_USE_PATH_STYLE carried an explicit
	// value; when false, UsePathStyle is the endpoint-based inference and the
	// client lookup is left to minio-go's auto mode.
	PathStyleExplicit bool
	// AllowedKeyPrefixes is the segment-aware S3_KEY_PREFIXES allowlist;
	// empty = whole bucket readable.
	AllowedKeyPrefixes []string
	// MaxFileBytes is the S3 transport-layer read cap (S3_MAX_FILE_BYTES,
	// default 100 MiB). The SMTP-attachment cap (MaxAttachmentMB) is a
	// separate, email-specific limit.
	MaxFileBytes int64
}

type fileConfig struct {
	SMTP struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
		From string `yaml:"from"`
	} `yaml:"smtp"`
	MaxAttachmentMB int      `yaml:"max_attachment_mb"`
	AllowedDomains  []string `yaml:"allowed_domains"`
	// S3 exists only to detect a s3: section for the deprecation warning;
	// its values are discarded (env is the sole S3 config source).
	S3 struct {
		Endpoint  string `yaml:"endpoint"`
		AccessKey string `yaml:"access_key"`
		SecretKey string `yaml:"secret_key"`
		Bucket    string `yaml:"bucket"`
		Secure    bool   `yaml:"secure"`
	} `yaml:"s3"`
}

type Config struct {
	SMTP            SMTPConfig
	MaxAttachmentMB int
	AllowedDomains  []string
	S3              *S3Config
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

// s3Env returns the effective value for an S3 variable: the standard name
// first, then the legacy name (readable for one version cycle, standard wins).
func s3Env(std, legacy string) string {
	if v := os.Getenv(std); v != "" {
		return v
	}
	return os.Getenv(legacy)
}

// parseBytesEnv parses a byte-count env var; empty, unparseable, or
// non-positive falls back to def (excel-mcp parse_bytes semantics).
func parseBytesEnv(raw string, def int64) int64 {
	if n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && n > 0 {
		return n
	}
	return def
}

// parseBoolEnv parses a boolean env var with strconv.ParseBool's vocabulary
// (1/t/true/0/f/false); unset or unparseable values log and fall back to
// def (a bad value warns loudly instead of silently flipping the trust
// model).
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

// parsePathStyleEnv maps 1/true/yes -> true and 0/false/no -> false (both
// explicit: carried through to the client as BucketLookupPath/DNS); anything
// else (including unset) falls back to the inference: custom endpoint ->
// path style, native AWS -> virtual host (minio-go auto).
func parsePathStyleEnv(raw string, hasEndpoint bool) (value, explicit bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes":
		return true, true
	case "0", "false", "no":
		return false, true
	}
	return hasEndpoint, false
}

// splitCSV splits a comma-separated env var into trimmed, non-empty,
// deduplicated parts (first-seen order, like normalizeDomains).
func splitCSV(raw string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// LoadConfig reads a YAML config file, applies defaults, then env overrides:
// SMTP_HOST / SMTP_PORT / SMTP_FROM / MAX_ATTACHMENT_MB / ALLOWED_DOMAINS.
// S3 is env-only per the S3 usage standard (S3_BUCKET / AWS_ACCESS_KEY_ID /
// AWS_SECRET_ACCESS_KEY + optional AWS_ENDPOINT_URL, S3_REGION, S3_USE_PATH_STYLE,
// S3_KEY_PREFIXES, S3_MAX_FILE_BYTES; legacy S3_* names readable for one
// version). A YAML s3: section is a deprecated no-op. An empty path means
// "config.yaml in the current directory" (missing file is not an error when
// env vars provide everything).
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = "config.yaml"
	}
	var fc fileConfig
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, &fc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	cfg := &Config{
		SMTP: SMTPConfig{
			Host: fc.SMTP.Host,
			Port: fc.SMTP.Port,
			From: fc.SMTP.From,
		},
		MaxAttachmentMB: fc.MaxAttachmentMB,
		AllowedDomains:  fc.AllowedDomains,
	}

	if cfg.SMTP.Host == "" {
		cfg.SMTP.Host = "127.0.0.1"
	}
	if cfg.SMTP.Port == 0 {
		cfg.SMTP.Port = 2525
	}
	if cfg.MaxAttachmentMB == 0 {
		cfg.MaxAttachmentMB = 10
	}

	// S3: env-only contract (the S3 usage standard). A YAML s3: section is a
	// deprecated no-op: warn so operators notice, then ignore its values.
	if fc.S3.Endpoint != "" || fc.S3.AccessKey != "" || fc.S3.SecretKey != "" || fc.S3.Bucket != "" || fc.S3.Secure {
		log.Printf("config: the yaml s3: section is deprecated and ignored; set S3_BUCKET / AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (+ AWS_ENDPOINT_URL, S3_REGION, S3_USE_PATH_STYLE, S3_KEY_PREFIXES, S3_MAX_FILE_BYTES) instead")
	}

	// Required trio: bucket + static credentials. Any one missing = S3 off
	// (soft disable; the caller gets S3_UNAVAILABLE per call).
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET")) // no legacy name: S3_BUCKET was already standard
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
		// Values are trimmed here — minio.New errors confusingly on
		// whitespace-padded hosts.
		if v := strings.TrimSpace(s3Env("AWS_ENDPOINT_URL", "")); v != "" {
			s3.EndpointURL = v
		} else if v := strings.TrimSpace(os.Getenv("S3_ENDPOINT")); v != "" {
			s3.Endpoint = v
			s3.Secure = parseBoolEnv("S3_SECURE", false)
		}
		s3.UsePathStyle, s3.PathStyleExplicit = parsePathStyleEnv(s3Env("S3_USE_PATH_STYLE", ""), s3.EndpointURL != "" || s3.Endpoint != "")
		s3.AllowedKeyPrefixes = splitCSV(s3Env("S3_KEY_PREFIXES", ""))
		s3.MaxFileBytes = parseBytesEnv(s3Env("S3_MAX_FILE_BYTES", ""), 100*1024*1024)
	}

	// Env overrides.
	if v := os.Getenv("SMTP_HOST"); v != "" {
		cfg.SMTP.Host = v
	}
	if v := os.Getenv("SMTP_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("SMTP_PORT: %w", err)
		}
		cfg.SMTP.Port = p
	}
	if v := os.Getenv("SMTP_FROM"); v != "" {
		cfg.SMTP.From = v
	}
	if v := os.Getenv("MAX_ATTACHMENT_MB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("MAX_ATTACHMENT_MB: %w", err)
		}
		cfg.MaxAttachmentMB = n
	}

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

	if cfg.SMTP.From == "" {
		return nil, fmt.Errorf("smtp.from (or SMTP_FROM) is required: the envelope sender address")
	}
	if cfg.MaxAttachmentMB < 1 {
		return nil, fmt.Errorf("max_attachment_mb (or MAX_ATTACHMENT_MB) must be >= 1 MiB, got %d", cfg.MaxAttachmentMB)
	}
	return cfg, nil
}
