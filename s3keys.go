package main

import (
	"errors"
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
