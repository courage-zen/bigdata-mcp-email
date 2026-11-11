package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// MailSender abstracts the SMTP hop so the tool is testable.
type MailSender interface {
	Send(from string, to []string, msg []byte) error
}

// ServerEnv carries the dependencies shared by tool handlers.
type ServerEnv struct {
	Config *Config
	Sender MailSender
}

// sendEmailInput is the typed MCP tool input (schema inferred by the SDK).
type sendEmailInput struct {
	To          []string        `json:"to,omitempty" jsonschema:"recipients; empty to fall back to the multica user's email"`
	Cc          []string        `json:"cc,omitempty" jsonschema:"carbon-copy recipients"`
	Subject     string          `json:"subject" jsonschema:"email subject line"`
	Body        string          `json:"body" jsonschema:"email body (plain text or HTML per body_type)"`
	BodyType    string          `json:"body_type,omitempty" jsonschema:"\"text\" or \"html\"; default \"text\""`
	Attachments []attachmentArg `json:"attachments,omitempty" jsonschema:"optional attachments"`
}

type attachmentArg struct {
	Filename    string `json:"filename" jsonschema:"attachment file name shown to the recipient"`
	ContentB64  string `json:"content_b64" jsonschema:"base64-encoded attachment content"`
	ContentType string `json:"content_type,omitempty" jsonschema:"MIME type; guessed from filename when empty"`
}

// attachmentArg needs custom unmarshaling: to/cc accept string-or-array.
// The SDK validates against the inferred schema, so we declare []string and
// normalize string→[]string before validation via a raw pre-step in the
// handler (see SendEmail: arguments arrive as map[string]any there).
//
// For the MCP registration path we use the raw-JSON Server.AddTool with our
// own normalization, keeping string-or-array flexibility.

// Error codes carried in failed tool payloads' error_code field, for
// programmatic handling by callers. See the design doc's error table.
const (
	errCodeMissingParam       = "MISSING_PARAM"
	errCodeInvalidParamType   = "INVALID_PARAM_TYPE"
	errCodeInvalidParamValue  = "INVALID_PARAM_VALUE"
	errCodeMissingFilename    = "MISSING_FILENAME"
	errCodeInvalidBase64      = "INVALID_BASE64"
	errCodeAttachmentTooLarge = "ATTACHMENT_TOO_LARGE"
	errCodeDomainNotAllowed   = "DOMAIN_NOT_ALLOWED"
	errCodeS3Unavailable      = "S3_UNAVAILABLE"
	errCodeS3ObjectNotFound   = "S3_OBJECT_NOT_FOUND"
	errCodeS3FetchFailed      = "S3_FETCH_FAILED"
	errCodeBuildFailed        = "BUILD_FAILED"
	errCodeSMTPFailed         = "SMTP_FAILED"
	errCodeInvalidArguments   = "INVALID_ARGUMENTS"
	errCodeInternal           = "INTERNAL"
)

// emailResult is the JSON payload returned by the tool.
type emailResult struct {
	Success           bool              `json:"success"`
	MessageID         string            `json:"message_id,omitempty"`
	To                []string          `json:"to"`
	Cc                []string          `json:"cc,omitempty"`
	From              string            `json:"from"`
	ReplyTo           string            `json:"reply_to,omitempty"`
	AttachmentCount   int               `json:"attachment_count"`
	SignatureAppended bool              `json:"signature_appended"`
	DurationMs        float64           `json:"duration_ms"`
	Error             string            `json:"error,omitempty"`
	ErrorCode         string            `json:"error_code,omitempty"`
	MulticaContext    map[string]string `json:"multica_context"`
}

func errPayload(code, errMsg string, mc map[string]string) []byte {
	raw, _ := json.Marshal(emailResult{
		Success:        false,
		Error:          errMsg,
		ErrorCode:      code,
		MulticaContext: mc,
	})
	return raw
}

// validEmail is a loose address check: exactly one '@', non-empty local and
// domain parts. Full RFC 5322 validation is out of scope for an MCP tool.
func validEmail(s string) bool {
	at := strings.Count(s, "@")
	if at != 1 {
		return false
	}
	i := strings.Index(s, "@")
	return i > 0 && i < len(s)-1 && !strings.ContainsAny(s, " \t\r\n")
}

// normalizeRecipients converts a to/cc argument that may be a string or an
// array of strings into []string, trimming whitespace and dropping empties.
func normalizeRecipients(v any) ([]string, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return nil, nil
		}
		return []string{s}, nil
	case []any:
		var out []string
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("recipient list must contain only strings, got %T", item)
			}
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("recipient argument must be a string or array of strings, got %T", v)
	}
}

// SendEmail contains the tool logic, separated from the MCP wiring so it is
// directly testable. hdr is the HTTP request header carrying X-Multica-*.
// It returns the JSON payload bytes (never a protocol-level error).
func (env *ServerEnv) SendEmail(hdr http.Header, args map[string]any) ([]byte, error) {
	mc := MulticaContextFromHeaders(hdr)
	start := time.Now()

	// fail renders a tool-level error payload; code is one of the errCode
	// constants. Returns a payload, never a protocol error.
	fail := func(code, format string, a ...any) ([]byte, error) {
		return errPayload(code, fmt.Sprintf(format, a...), mc), nil
	}

	// --- normalize arguments -------------------------------------------------
	subject, _ := args["subject"].(string)
	if strings.TrimSpace(subject) == "" {
		return fail(errCodeMissingParam, "parameter \"subject\" is required")
	}
	body, _ := args["body"].(string)
	if body == "" {
		return fail(errCodeMissingParam, "parameter \"body\" is required")
	}
	bodyType := ""
	if v, present := args["body_type"]; present && v != nil {
		s, ok := v.(string)
		if !ok {
			return fail(errCodeInvalidParamType, "parameter \"body_type\" must be a string, got %T", v)
		}
		bodyType = s
	}
	if bodyType == "" {
		bodyType = "text"
	}
	if bodyType != "text" && bodyType != "html" {
		return fail(errCodeInvalidParamValue, "parameter \"body_type\" must be \"text\" or \"html\", got %q", bodyType)
	}

	to, err := normalizeRecipients(args["to"])
	if err != nil {
		return fail(errCodeInvalidParamType, "parameter \"to\": %v", err)
	}
	cc, err := normalizeRecipients(args["cc"])
	if err != nil {
		return fail(errCodeInvalidParamType, "parameter \"cc\": %v", err)
	}

	// --- recipient fallback --------------------------------------------------
	userEmail := mc["user_email"]
	replyTo := ""
	if len(to) == 0 {
		if userEmail == "" {
			return fail(errCodeMissingParam, "no recipient: parameter \"to\" is empty and multica header user_email is absent — provide \"to\" or call from a multica task with a resolvable user email")
		}
		to = []string{userEmail}
		replyTo = userEmail
	}

	for _, addr := range append(append([]string{}, to...), cc...) {
		if !validEmail(addr) {
			return fail(errCodeInvalidParamValue, "invalid email address %q", addr)
		}
	}

	// --- recipient domain allowlist ------------------------------------------
	if set := env.Config.allowedDomainSet; set != nil {
		var rejected []string
		for _, addr := range append(append([]string{}, to...), cc...) {
			domain := strings.ToLower(addr[strings.LastIndex(addr, "@")+1:])
			if _, ok := set[domain]; !ok {
				rejected = append(rejected, addr)
			}
		}
		if rejected != nil {
			quoted := make([]string, len(rejected))
			for i, addr := range rejected {
				quoted[i] = fmt.Sprintf("%q", addr)
			}
			return fail(errCodeDomainNotAllowed, "recipient domain not allowed: %s not in allowed domains [%s]",
				strings.Join(quoted, ", "), strings.Join(env.Config.AllowedDomains, ", "))
		}
	}

	// --- attachments ---------------------------------------------------------
	var attachments []Attachment
	if v, present := args["attachments"]; present && v != nil {
		rawList, ok := v.([]any)
		if !ok {
			return fail(errCodeInvalidParamType, "parameter \"attachments\" must be an array of objects, got %T", v)
		}
		for i, raw := range rawList {
			m, ok := raw.(map[string]any)
			if !ok {
				return fail(errCodeInvalidParamType, "attachments[%d] must be an object", i)
			}
			filename, _ := m["filename"].(string)
			if strings.TrimSpace(filename) == "" {
				return fail(errCodeMissingFilename, "attachments[%d].filename is required", i)
			}
			b64, _ := m["content_b64"].(string)
			content, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return fail(errCodeInvalidBase64, "attachments[%d].content_b64 is not valid base64: %v", i, err)
			}
			limitBytes := int64(env.Config.MaxAttachmentMB) * 1024 * 1024
			if int64(len(content)) > limitBytes {
				return fail(errCodeAttachmentTooLarge, "attachment %q is %d bytes, exceeding the %d MiB limit", filename, len(content), env.Config.MaxAttachmentMB)
			}
			ct, _ := m["content_type"].(string)
			attachments = append(attachments, Attachment{
				Filename:    filename,
				Content:     content,
				ContentType: ct,
			})
		}
	}

	// --- build & send ---------------------------------------------------------
	// --- agent signature -----------------------------------------------------
	signedBody := appendAgentSignature(body, bodyType, mc)
	signatureAppended := signedBody != body

	email := Email{
		From:        env.Config.SMTP.From,
		To:          to,
		Cc:          cc,
		ReplyTo:     replyTo,
		Subject:     subject,
		MessageID:   newMessageID(env.Config.SMTP.From),
		Body:        signedBody,
		BodyType:    bodyType,
		Attachments: attachments,
	}
	msg, err := BuildMessage(email)
	if err != nil {
		return fail(errCodeBuildFailed, "build message: %v", err)
	}

	if err := env.Sender.Send(env.Config.SMTP.From, envelopeRecipients(to, cc), msg); err != nil {
		return fail(errCodeSMTPFailed, "smtp send failed: %v", err)
	}

	result := emailResult{
		Success:           true,
		MessageID:         email.MessageID,
		To:                to,
		Cc:                cc,
		From:              env.Config.SMTP.From,
		ReplyTo:           replyTo,
		AttachmentCount:   len(attachments),
		SignatureAppended: signatureAppended,
		DurationMs:        float64(time.Since(start).Microseconds()) / 1000.0,
		MulticaContext:    mc,
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return errPayload(errCodeInternal, "marshal result: "+err.Error(), mc), nil
	}
	return raw, nil
}

// envelopeRecipients concatenates to + cc for RCPT TO.
func envelopeRecipients(to, cc []string) []string {
	out := make([]string, 0, len(to)+len(cc))
	out = append(out, to...)
	out = append(out, cc...)
	return out
}
