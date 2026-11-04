package main

import (
	"bytes"
	"encoding/base64"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestBuildMessage_PlainText(t *testing.T) {
	msg, err := BuildMessage(Email{
		From:     "noreply@example.com",
		To:       []string{"a@x.com", "b@x.com"},
		Subject:  "weekly report",
		Body:     "hello world",
		BodyType: "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(msg)
	for _, want := range []string{
		"From: noreply@example.com\r\n",
		"To: a@x.com, b@x.com\r\n",
		"Subject: weekly report\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n",
		"Date: ",
		"Message-ID: ",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "multipart") {
		t.Error("plain text mail must not be multipart")
	}
	if !strings.HasSuffix(s, "hello world") {
		t.Errorf("body not last, got tail %q", s[len(s)-30:])
	}
}

func TestBuildMessage_SubjectRFC2047(t *testing.T) {
	msg, err := BuildMessage(Email{
		From:     "noreply@example.com",
		To:       []string{"a@x.com"},
		Subject:  "数据周报",
		Body:     "hi",
		BodyType: "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(msg)
	// Non-ASCII subjects must be encoded words, never raw UTF-8 on the header line.
	if strings.Contains(s, "数据周报") {
		t.Error("subject contains raw non-ASCII")
	}
	re := regexp.MustCompile(`Subject: =\?utf-8\?[Bb]\?.+\?=\r\n`)
	if !re.MatchString(s) {
		t.Errorf("no RFC 2047 B-encoded subject line:\n%s", s)
	}
}

func TestBuildMessage_ReplyTo(t *testing.T) {
	msg, err := BuildMessage(Email{
		From:     "noreply@example.com",
		To:       []string{"user@example.com"},
		ReplyTo:  "user@example.com",
		Subject:  "s",
		Body:     "hi",
		BodyType: "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "Reply-To: user@example.com\r\n") {
		t.Errorf("Reply-To missing:\n%s", string(msg))
	}
}

func TestBuildMessage_HTMLBody(t *testing.T) {
	msg, err := BuildMessage(Email{
		From:     "noreply@example.com",
		To:       []string{"a@x.com"},
		Subject:  "s",
		Body:     "<p>hello</p>",
		BodyType: "html",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "Content-Type: text/html; charset=UTF-8\r\n") {
		t.Errorf("html content type missing:\n%s", string(msg))
	}
}

func TestBuildMessage_Attachment(t *testing.T) {
	content := []byte("id,rev\n1,2\n")
	msg, err := BuildMessage(Email{
		From:     "noreply@example.com",
		To:       []string{"a@x.com"},
		Subject:  "s",
		Body:     "see attached",
		BodyType: "text",
		Attachments: []Attachment{
			{Filename: "report.csv", Content: content, ContentType: "text/csv"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(msg)
	if !strings.Contains(s, "Content-Type: multipart/mixed; boundary=") {
		t.Errorf("not multipart/mixed:\n%s", s)
	}
	if !strings.Contains(s, "Content-Type: text/csv\r\n") {
		t.Error("attachment content type missing")
	}
	if !strings.Contains(s, "Content-Disposition: attachment; filename=\"report.csv\"\r\n") {
		t.Error("attachment disposition/filename missing")
	}
	if !strings.Contains(s, "Content-Transfer-Encoding: base64\r\n") {
		t.Error("attachment must be base64")
	}
	// base64 body must decode back to the exact content, and lines must be <=76 chars.
	dec := regexp.MustCompile(`(?m)^([A-Za-z0-9+/=]+)\r?$`)
	var got []byte
	for _, m := range dec.FindAllStringSubmatch(s, -1) {
		if len(m[1]) > 76 {
			t.Fatalf("base64 line longer than 76: %d", len(m[1]))
		}
		b, err := base64.StdEncoding.DecodeString(m[1])
		if err != nil || !bytes.Equal(b, content) {
			continue // header line or text part; skip
		}
		got = b
	}
	if !bytes.Equal(got, content) {
		t.Error("attachment content did not round-trip")
	}
}

func TestBuildMessage_AsciiOnlyNoEncodedWords(t *testing.T) {
	msg, err := BuildMessage(Email{
		From:     "noreply@example.com",
		To:       []string{"a@x.com"},
		Subject:  "s",
		Body:     "x",
		BodyType: "text",
		Attachments: []Attachment{
			{Filename: "report.csv", Content: []byte("x"), ContentType: "text/csv"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(msg), "=?utf-8?") {
		t.Error("all-ASCII message must contain no encoded words")
	}
}

func TestBuildMessage_BodyLineEndings(t *testing.T) {
	msg, err := BuildMessage(Email{
		From:     "noreply@example.com",
		To:       []string{"a@x.com"},
		Subject:  "s",
		Body:     "line1\nline2\n",
		BodyType: "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "line1\r\nline2\r\n") {
		t.Error("body LF must be normalized to CRLF")
	}
}

func TestBuildMessage_LongBodyLineFolded(t *testing.T) {
	long := strings.Repeat("a", 2000)
	msg, err := BuildMessage(Email{
		From:     "noreply@example.com",
		To:       []string{"a@x.com"},
		Subject:  "s",
		Body:     long,
		BodyType: "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(msg), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line exceeds 998 chars: %d", len(line))
		}
	}
}

func TestBuildMessage_BadBodyType(t *testing.T) {
	if _, err := BuildMessage(Email{
		From: "noreply@example.com", To: []string{"a@x.com"},
		Subject: "s", Body: "x", BodyType: "rich",
	}); err == nil {
		t.Error("body_type rich must be rejected")
	}
}

func TestEncodeRFC2047_ASCII(t *testing.T) {
	if got := encodeRFC2047("plain"); got != "plain" {
		t.Fatalf("got %q", got)
	}
}

// decodeQP reverses encodeQuotedPrintable: soft breaks vanish, =XX hex pairs
// decode, everything else passes through.
func decodeQP(s string) string {
	s = strings.ReplaceAll(s, "=\r\n", "")
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '=' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// decodeAllEncodedWords finds every B-encoded word in s, base64-decodes each,
// and concatenates the results.
func decodeAllEncodedWords(t *testing.T, s string) string {
	t.Helper()
	re := regexp.MustCompile(`=\?utf-8\?B\?([A-Za-z0-9+/=]+)\?=`)
	matches := re.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		t.Fatal("no encoded words found")
	}
	var b strings.Builder
	for _, m := range matches {
		dec, err := base64.StdEncoding.DecodeString(m[1])
		if err != nil {
			t.Fatalf("encoded word %q does not base64-decode: %v", m[1], err)
		}
		b.Write(dec)
	}
	return b.String()
}

func TestBuildMessage_FilenameInjectionRejected(t *testing.T) {
	for _, fn := range []string{
		"a.csv\"\r\nBcc: evil@example.com",
		`a.csv\evil`,
	} {
		if _, err := BuildMessage(Email{
			From: "noreply@example.com", To: []string{"a@x.com"},
			Subject: "s", Body: "x", BodyType: "text",
			Attachments: []Attachment{{Filename: fn, Content: []byte("x")}},
		}); err == nil {
			t.Errorf("filename %q must be rejected", fn)
		}
	}
}

func TestBuildMessage_ContentTypeInvalid(t *testing.T) {
	for _, ct := range []string{
		"text/csv\r\nX-Evil: 1",
		"not a type",
	} {
		if _, err := BuildMessage(Email{
			From: "noreply@example.com", To: []string{"a@x.com"},
			Subject: "s", Body: "x", BodyType: "text",
			Attachments: []Attachment{{Filename: "a.csv", Content: []byte("x"), ContentType: ct}},
		}); err == nil {
			t.Errorf("content type %q must be rejected", ct)
		}
	}
}

func TestBuildMessage_LongNonASCIISubjectRoundTrip(t *testing.T) {
	subject := strings.Repeat("数据周报", 75)
	msg, err := BuildMessage(Email{
		From:     "noreply@example.com",
		To:       []string{"a@x.com"},
		Subject:  subject,
		Body:     "x",
		BodyType: "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(msg)
	for _, line := range strings.Split(s, "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line exceeds 998 chars: %d", len(line))
		}
	}
	if got := decodeAllEncodedWords(t, s); got != subject {
		t.Errorf("subject does not round-trip: %d runes decoded, want %d", len([]rune(got)), len([]rune(subject)))
	}
	re := regexp.MustCompile(`=\?utf-8\?B\?[A-Za-z0-9+/=]+\?=`)
	if n := len(re.FindAllString(s, -1)); n <= 1 {
		t.Errorf("want more than one encoded word, got %d", n)
	}
}

func TestBuildMessage_BodyQuotedPrintableRoundTrip(t *testing.T) {
	bodies := []string{
		strings.Repeat("a", 2000),
		strings.Repeat("b", 898) + strings.Repeat("数", 200),
		"trailing space \nnext line\n",
	}
	for _, body := range bodies {
		msg, err := BuildMessage(Email{
			From:     "noreply@example.com",
			To:       []string{"a@x.com"},
			Subject:  "s",
			Body:     body,
			BodyType: "text",
		})
		if err != nil {
			t.Fatal(err)
		}
		s := string(msg)
		if !strings.Contains(s, "Content-Transfer-Encoding: quoted-printable\r\n\r\n") {
			t.Fatalf("body part is not quoted-printable:\n%s", s)
		}
		if got := decodeQP(extractBody(s)); got != normalizeCRLF(body) {
			t.Errorf("body does not round-trip: %q... != %q...", got[:min(60, len(got))], normalizeCRLF(body)[:min(60, len(body))])
		}
		for _, line := range strings.Split(s, "\r\n") {
			if len(line) > 998 {
				t.Fatalf("line exceeds 998 chars: %d", len(line))
			}
		}
		// QP lines must honor the 76-char cap (RFC 2045 §6.7(5)).
		for _, line := range strings.Split(extractBody(s), "\r\n") {
			if len(line) > 76 {
				t.Fatalf("QP line exceeds 76 chars: %d", len(line))
			}
		}
	}
}

// extractBody returns everything after the first blank line (the single-part
// message body).
func extractBody(s string) string {
	if i := strings.Index(s, "\r\n\r\n"); i >= 0 {
		return s[i+4:]
	}
	return s
}

func TestBuildMessage_CRLFInAddressHeadersRejected(t *testing.T) {
	if _, err := BuildMessage(Email{
		From: "a@x.com\r\nBcc: e@x.com", To: []string{"a@x.com"},
		Subject: "s", Body: "x", BodyType: "text",
	}); err == nil {
		t.Error("From with CRLF must be rejected")
	}
	if _, err := BuildMessage(Email{
		From: "a@x.com", To: []string{"a@x.com\r\nBcc: e@x.com"},
		Subject: "s", Body: "x", BodyType: "text",
	}); err == nil {
		t.Error("To with CRLF must be rejected")
	}
	if _, err := BuildMessage(Email{
		From: "a@x.com", To: []string{"a@x.com"}, Cc: []string{"c@x.com\r\nBcc: e@x.com"},
		Subject: "s", Body: "x", BodyType: "text",
	}); err == nil {
		t.Error("Cc with CRLF must be rejected")
	}
	if _, err := BuildMessage(Email{
		From: "a@x.com", To: []string{"a@x.com"}, ReplyTo: "r@x.com\r\nBcc: e@x.com",
		Subject: "s", Body: "x", BodyType: "text",
	}); err == nil {
		t.Error("Reply-To with CRLF must be rejected")
	}
}
