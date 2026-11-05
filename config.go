package main

import (
	"fmt"
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

type fileConfig struct {
	SMTP struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
		From string `yaml:"from"`
	} `yaml:"smtp"`
	MaxAttachmentMB int      `yaml:"max_attachment_mb"`
	AllowedDomains  []string `yaml:"allowed_domains"`
}

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

// LoadConfig reads a YAML config file, applies defaults, then env overrides:
// SMTP_HOST / SMTP_PORT / SMTP_FROM / MAX_ATTACHMENT_MB. An empty path means
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
