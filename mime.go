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
	MessageID   string // pre-generated; BuildMessage writes it and falls back to generating one
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

// tokenChar matches a full run of RFC 2045 token characters (alphanumerics
// plus "!#$%&'*+-^_`|~" and "."), and nothing else.
var tokenChar = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

// contentTypeValid reports whether ct is a strict "type/subtype" pair of
// RFC 2045 tokens — no parameters, no whitespace, no CR/LF.
func contentTypeValid(ct string) bool {
	slash := strings.IndexByte(ct, '/')
	if slash <= 0 || slash == len(ct)-1 {
		return false
	}
	return tokenChar.MatchString(ct[:slash]) && tokenChar.MatchString(ct[slash+1:])
}

// validFilename rejects filenames containing control characters, quotes, or
// backslashes: any of these can break out of the quoted-string in the
// Content-Disposition header.
func validFilename(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x20 || c == 0x7F || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

// encodeRFC2047 encodes s as RFC 2047 B-encoded words when it contains
// non-ASCII bytes, otherwise returns it unchanged. Long inputs are split at
// rune boundaries into <=45-byte chunks (45 source bytes -> <=60 base64
// chars -> <=72-char encoded word) so each word stays short; words are
// separated by a single space, which is exactly what RFC 2047 requires
// between adjacent encoded words and what header unfolding restores.
func encodeRFC2047(s string) string {
	if !nonASCII.MatchString(s) {
		return s
	}
	var words []string
	for remaining := s; remaining != ""; {
		chunk := remaining
		if len(chunk) > 45 {
			chunk = chunk[:45]
			// Back off so the byte after the chunk starts a new rune; never
			// split a multi-byte rune. Three steps is the most a valid UTF-8
			// sequence needs, and the cap keeps pathological invalid UTF-8
			// from stalling the loop.
			for i := 0; i < 3 && remaining[len(chunk)]&0xC0 == 0x80; i++ {
				chunk = chunk[:len(chunk)-1]
			}
		}
		words = append(words, "=?utf-8?B?"+base64.StdEncoding.EncodeToString([]byte(chunk))+"?=")
		remaining = remaining[len(chunk):]
	}
	return strings.Join(words, " ")
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

// encodeQuotedPrintable encodes s per RFC 2045 §6.7: bytes 33-126 pass
// through except "=", controls/high bytes and "=" become "=XX" (uppercase
// hex), and lines are capped at 76 chars with "=\r\n" soft breaks. Space and
// tab are literal unless they would end a line, where they become =20/=09 so
// trailing whitespace survives transport.
func encodeQuotedPrintable(s string) string {
	s = normalizeCRLF(s)
	var b strings.Builder
	lineLen := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		// Hard line breaks pass through and reset the line length.
		if c == '\n' {
			b.WriteString("\r\n")
			lineLen = 0
			continue
		}
		if c == '\r' {
			continue // CRLF pairs are re-emitted with the '\n' above
		}
		var enc string
		switch {
		case c == ' ' || c == '\t':
			// Literal unless it would end a line — then encode it so the
			// trailing whitespace survives transport (RFC 2045 §6.7(3)).
			// i+1 >= len(s) means end of input, which is also a line end.
			if i+1 >= len(s) || s[i+1] == '\n' || s[i+1] == '\r' {
				enc = fmt.Sprintf("=%02X", c)
			} else {
				enc = string(c)
			}
		case c >= 33 && c <= 126 && c != '=':
			enc = string(c)
		default:
			enc = fmt.Sprintf("=%02X", c)
		}
		// Soft-break before writing enc when it would push the line past 72
		// chars: content caps at 72, and the trailing "=\r\n" of a soft break
		// keeps every line at 75 chars, under the 76-char QP limit (RFC 2045
		// §6.7(5)). Hard breaks bypass this, so a soft break never lands
		// directly before a hard break.
		if lineLen+len(enc) > 72 {
			b.WriteString("=\r\n")
			lineLen = 0
		}
		b.WriteString(enc)
		lineLen += len(enc)
	}
	return b.String()
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

	// CRLF in an address header is a header-injection vector: reject it
	// outright. Subject is exempt because B-encoding makes it safe.
	recipients := append([]string{e.From}, e.To...)
	recipients = append(recipients, e.Cc...)
	if e.ReplyTo != "" {
		recipients = append(recipients, e.ReplyTo)
	}
	for _, addr := range recipients {
		if strings.ContainsAny(addr, "\r\n\x00") {
			return nil, fmt.Errorf("header value %q contains control characters", addr)
		}
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
	if e.MessageID == "" {
		e.MessageID = newMessageID(e.From)
	}
	h.WriteString("Message-ID: " + e.MessageID + "\r\n")
	h.WriteString("MIME-Version: 1.0\r\n")

	textCT := "text/plain; charset=UTF-8"
	if e.BodyType == "html" {
		textCT = "text/html; charset=UTF-8"
	}
	body := encodeQuotedPrintable(e.Body)

	var out strings.Builder
	if len(e.Attachments) == 0 {
		out.WriteString(h.String())
		out.WriteString("Content-Type: " + textCT + "\r\n")
		out.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
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
	out.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	out.WriteString("\r\n")
	out.WriteString(body)
	out.WriteString("\r\n")

	// Attachment parts.
	for _, a := range e.Attachments {
		if !validFilename(a.Filename) {
			return nil, fmt.Errorf("attachment filename %q contains forbidden characters (control characters, quotes, or backslashes)", a.Filename)
		}
		ct := a.ContentType
		if ct == "" {
			ct = contentTypeByExt(a.Filename)
		}
		if !contentTypeValid(ct) {
			return nil, fmt.Errorf("attachment %q has invalid content type %q", a.Filename, ct)
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
