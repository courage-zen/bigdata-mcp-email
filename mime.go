package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Email is a fully-resolved message ready to be rendered.
type Email struct {
	From        string
	To          []string
	Cc          []string
	ReplyTo     string // empty = no Reply-To header
	Subject     string
	Body        string
	BodyType    string // "text" | "html"
	Attachments []Attachment
}

type Attachment struct {
	Filename    string
	Content     []byte
	ContentType string // guessed by extension when empty
}

// contentTypeByExt maps common extensions; unknown extensions get
// application/octet-stream.
func contentTypeByExt(name string) string {
	switch {
	case strings.HasSuffix(name, ".csv"):
		return "text/csv"
	case strings.HasSuffix(name, ".txt"):
		return "text/plain"
	case strings.HasSuffix(name, ".html"), strings.HasSuffix(name, ".htm"):
		return "text/html"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	case strings.HasSuffix(name, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".jpg"), strings.HasSuffix(name, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(name, ".xlsx"):
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	default:
		return "application/octet-stream"
	}
}

var nonASCII = regexp.MustCompile(`[^\x20-\x7E]`)

// encodeRFC2047 encodes s as an RFC 2047 B-encoded word when it contains
// non-ASCII bytes, otherwise returns it unchanged.
func encodeRFC2047(s string) string {
	if !nonASCII.MatchString(s) {
		return s
	}
	return "=?utf-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
}

// foldHeader inserts CRLF+space soft breaks so no header line exceeds 998
// bytes (RFC 5322 line-length limit). Only safe for unstructured values.
func foldHeader(line string) string {
	const limit = 900
	if len(line) <= limit {
		return line
	}
	var b strings.Builder
	for len(line) > limit {
		cut := strings.LastIndexAny(line[:limit], " \t")
		if cut <= 0 {
			cut = limit
		}
		b.WriteString(line[:cut])
		b.WriteString("\r\n ")
		line = strings.TrimSpace(line[cut:])
	}
	b.WriteString(line)
	return b.String()
}

// wrapBase64 splits data into 76-char base64 lines (RFC 2045 §6.8).
func wrapBase64(data []byte) string {
	enc := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	return b.String()
}

// normalizeCRLF converts bare LF to CRLF and strips stray CR.
func normalizeCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// wrapBody folds body lines longer than 998 chars.
func wrapBody(s string) string {
	lines := strings.Split(s, "\r\n")
	for i, line := range lines {
		if len(line) > 998 {
			lines[i] = foldHeader(line)
		}
	}
	return strings.Join(lines, "\r\n")
}

// newBoundary generates a random multipart boundary.
func newBoundary() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("boundary-%d", time.Now().UnixNano())
	}
	return "=_multipart_" + strings.ToLower(base64.RawURLEncoding.EncodeToString(b[:]))
}

// newMessageID builds <random@domain-of-from>.
func newMessageID(from string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("<%d.fallback@%s>", time.Now().UnixNano(), domainOf(from))
	}
	domain := domainOf(from)
	if domain == "" {
		domain = "email-mcp.invalid"
	}
	return fmt.Sprintf("<%s@%s>", strings.ToLower(base64.RawURLEncoding.EncodeToString(b[:])), domain)
}

// domainOf returns the part after the last '@'.
func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return ""
}

// BuildMessage renders Email as full RFC 5322 bytes (CRLF endings).
func BuildMessage(e Email) ([]byte, error) {
	if e.BodyType != "text" && e.BodyType != "html" {
		return nil, fmt.Errorf("body_type must be \"text\" or \"html\", got %q", e.BodyType)
	}

	var h strings.Builder
	h.WriteString("From: " + e.From + "\r\n")
	h.WriteString("To: " + strings.Join(e.To, ", ") + "\r\n")
	if len(e.Cc) > 0 {
		h.WriteString("Cc: " + strings.Join(e.Cc, ", ") + "\r\n")
	}
	if e.ReplyTo != "" {
		h.WriteString("Reply-To: " + e.ReplyTo + "\r\n")
	}
	h.WriteString("Subject: " + foldHeader(encodeRFC2047(e.Subject)) + "\r\n")
	h.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	h.WriteString("Message-ID: " + newMessageID(e.From) + "\r\n")
	h.WriteString("MIME-Version: 1.0\r\n")

	textCT := "text/plain; charset=UTF-8"
	if e.BodyType == "html" {
		textCT = "text/html; charset=UTF-8"
	}
	body := wrapBody(normalizeCRLF(e.Body))

	var out strings.Builder
	if len(e.Attachments) == 0 {
		out.WriteString(h.String())
		out.WriteString("Content-Type: " + textCT + "\r\n")
		out.WriteString("Content-Transfer-Encoding: 8bit\r\n")
		out.WriteString("\r\n")
		out.WriteString(body)
		return []byte(out.String()), nil
	}

	boundary := newBoundary()
	out.WriteString(h.String())
	out.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n")
	out.WriteString("\r\n")
	out.WriteString("This is a multipart message in MIME format.\r\n")

	// Text part.
	out.WriteString("--" + boundary + "\r\n")
	out.WriteString("Content-Type: " + textCT + "\r\n")
	out.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	out.WriteString("\r\n")
	out.WriteString(body)
	out.WriteString("\r\n")

	// Attachment parts.
	for _, a := range e.Attachments {
		ct := a.ContentType
		if ct == "" {
			ct = contentTypeByExt(a.Filename)
		}
		out.WriteString("--" + boundary + "\r\n")
		out.WriteString("Content-Type: " + ct + "\r\n")
		out.WriteString("Content-Transfer-Encoding: base64\r\n")
		out.WriteString("Content-Disposition: attachment; filename=\"" + a.Filename + "\"\r\n")
		out.WriteString("\r\n")
		out.WriteString(wrapBase64(a.Content))
		out.WriteString("\r\n")
	}
	out.WriteString("--" + boundary + "--\r\n")
	return []byte(out.String()), nil
}
