# send-email 附件通道修复与增强 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让智能体全程走标准 MCP 通道发带附件邮件：补全 inputSchema、attachments 字符串容错、s3_key 附件源、filename 可缺省、error_code 错误码。

**Architecture:** handler 层保留 `In=any` 的 raw-JSON 注册方式，给 `mcp.Tool` 传显式 `json.RawMessage` 完整 schema（SDK 预验证为第一道防线，handler 归一化为第二道）。S3 取文件抽象为 `ObjectStore` 接口（与 `MailSender` 同模式），minio-go 实现，main.go 构造一次注入 `ServerEnv`。错误码贯穿 `errPayload`。

**Tech Stack:** Go 1.26 / MCP Go SDK v1.8.0（`mcp.AddTool` + 显式 InputSchema）/ minio-go v7.2.1 / gopkg.in/yaml.v3。

**Spec:** `docs/superpowers/specs/2026-11-11-send-email-attachments-design.md`

**Conventions:** 包 `main` 在仓库根，一文件一职责，测试在同级 `_test.go`。每步绿了就提交。测试命令统一 `go test ./...`（Makefile `make test` = vet + test）。

---

### Task 1: error_code 错误码基础设施

**Files:**
- Modify: `tools.go`（emailResult、errPayload、错误码常量）
- Modify: `server.go`（invalid-arguments 分支的 errPayload 调用）
- Test: `tools_test.go`（现有失败路径测试加 error_code 断言）

- [ ] **Step 1: Write the failing test — pin error codes on existing failure paths**

在 `tools_test.go` 中给以下现有测试各加一段 error_code 断言（放在 `success` 断言之后）：

```go
if code, _ := out["error_code"].(string); code != "<期望码>" {
    t.Errorf("error_code = %q, want %q", code, "<期望码>")
}
```

| 测试函数 | 期望 error_code |
|---|---|
| `TestSendEmail_MissingSubject` | `MISSING_PARAM` |
| `TestSendEmail_MissingBody` | `MISSING_PARAM` |
| `TestSendEmail_NoRecipient` | `MISSING_PARAM` |
| `TestSendEmail_InvalidEmail` | `INVALID_PARAM_VALUE` |
| `TestSendEmail_BadBase64Attachment` | `INVALID_BASE64` |
| `TestSendEmail_AttachmentsWrongType` | `INVALID_PARAM_TYPE` |
| `TestSendEmail_BodyTypeWrongType` | `INVALID_PARAM_TYPE` |
| `TestSendEmail_AttachmentTooBig` | `ATTACHMENT_TOO_LARGE` |
| `TestSendEmail_SenderFailure` | `SMTP_FAILED` |
| `TestSendEmail_DomainAllowlist_BlockedTo` | `DOMAIN_NOT_ALLOWED` |
| `TestSendEmail_DomainAllowlist_BlockedCc` | `DOMAIN_NOT_ALLOWED` |
| `TestSendEmail_DomainAllowlist_BlockedFallback` | `DOMAIN_NOT_ALLOWED` |
| `TestSendEmail_DomainAllowlist_MixedBlocked` | `DOMAIN_NOT_ALLOWED` |

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run 'TestSendEmail' | grep -c FAIL`
Expected: 13 个 FAIL（error_code 字段尚不存在，断言全部失败）

- [ ] **Step 3: Implement error codes in tools.go**

在 `tools.go` 的 `emailResult` 结构体上方加错误码常量块，`EmailResult` 加 `ErrorCode` 字段，`errPayload` 加 code 参数：

```go
// Error codes carried in failed tool payloads' error_code field, for
// programmatic handling by callers. See the design doc's error table.
const (
	errCodeMissingParam        = "MISSING_PARAM"
	errCodeInvalidParamType    = "INVALID_PARAM_TYPE"
	errCodeInvalidParamValue   = "INVALID_PARAM_VALUE"
	errCodeMissingFilename     = "MISSING_FILENAME"
	errCodeInvalidBase64       = "INVALID_BASE64"
	errCodeAttachmentTooLarge  = "ATTACHMENT_TOO_LARGE"
	errCodeDomainNotAllowed    = "DOMAIN_NOT_ALLOWED"
	errCodeS3Unavailable       = "S3_UNAVAILABLE"
	errCodeS3ObjectNotFound    = "S3_OBJECT_NOT_FOUND"
	errCodeS3FetchFailed       = "S3_FETCH_FAILED"
	errCodeBuildFailed         = "BUILD_FAILED"
	errCodeSMTPFailed          = "SMTP_FAILED"
	errCodeInvalidArguments    = "INVALID_ARGUMENTS"
	errCodeInternal            = "INTERNAL"
)
```

`emailResult` 增加（放在 `Error` 之后）：

```go
	ErrorCode         string            `json:"error_code,omitempty"`
```

`errPayload` 改为：

```go
func errPayload(code, errMsg string, mc map[string]string) []byte {
	raw, _ := json.Marshal(emailResult{
		Success:        false,
		Error:          errMsg,
		ErrorCode:      code,
		MulticaContext: mc,
	})
	return raw
}
```

`SendEmail` 里的 `fail` 闭包改为：

```go
	// fail renders a tool-level error payload; code is one of the errCode
	// constants. Returns a payload, never a protocol error.
	fail := func(code, format string, a ...any) ([]byte, error) {
		return errPayload(code, fmt.Sprintf(format, a...), mc), nil
	}
```

然后逐个更新 `SendEmail` 内的 `fail` 调用点（保持消息文本不变，只加 code）：

| 位置（tools.go 现行行号附近） | code |
|---|---|
| subject 缺失 | `errCodeMissingParam` |
| body 缺失 | `errCodeMissingParam` |
| body_type 非 string | `errCodeInvalidParamType` |
| body_type 枚举 | `errCodeInvalidParamValue` |
| to 归一化错误 | `errCodeInvalidParamType` |
| cc 归一化错误 | `errCodeInvalidParamType` |
| 无收件人 | `errCodeMissingParam` |
| 邮箱格式非法 | `errCodeInvalidParamValue` |
| 域名白名单 | `errCodeDomainNotAllowed` |
| attachments 非数组 | `errCodeInvalidParamType` |
| attachments[i] 非对象 | `errCodeInvalidParamType` |
| filename 缺失 | `errCodeMissingFilename` |
| base64 解码 | `errCodeInvalidBase64` |
| 附件超限 | `errCodeAttachmentTooLarge` |
| BuildMessage 失败 | `errCodeBuildFailed` |
| SMTP 失败 | `errCodeSMTPFailed` |
| marshal 失败 | `errCodeInternal` |

`server.go` 的 invalid-arguments 分支（`errPayload("invalid arguments: "...`）改为 `errPayload(errCodeInvalidArguments, "invalid arguments: "+err.Error(), mc)`。

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: 全部 PASS（50 个左右测试）

- [ ] **Step 5: Commit**

```bash
git add tools.go server.go tools_test.go
git commit -m "feat: machine-readable error_code in tool error payloads"
```

---

### Task 2: to/cc 逗号拆分容错

**Files:**
- Modify: `tools.go`（normalizeRecipients）
- Test: `tools_test.go`

- [ ] **Step 1: Write the failing test**

追加到 `tools_test.go`（放在 `TestSendEmail_StringToNormalizes` 之后）：

```go
func TestSendEmail_StringToCommaSplit(t *testing.T) {
	env, fs := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com, b@x.com",
		"subject": "s",
		"body":    "hi",
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if len(fs.To) != 2 || fs.To[0] != "a@x.com" || fs.To[1] != "b@x.com" {
		t.Errorf("envelope to = %v, want [a@x.com b@x.com]", fs.To)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestSendEmail_StringToCommaSplit`
Expected: FAIL —— 现状把 `"a@x.com, b@x.com"` 整体当地址、validEmail 判非法

- [ ] **Step 3: Implement comma splitting in normalizeRecipients**

`tools.go` 的 `normalizeRecipients` 中 `case string:` 分支改为（数组分支不变）：

```go
	case string:
		var out []string
		for _, part := range strings.Split(x, ",") {
			s := strings.TrimSpace(part)
			if s != "" {
				out = append(out, s)
			}
		}
		return out, nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: 全部 PASS（既有 `TestSendEmail_StringToNormalizes` 传单地址也走这条路径，天然覆盖）

- [ ] **Step 5: Commit**

```bash
git add tools.go tools_test.go
git commit -m "feat: split comma-separated to/cc string addresses"
```

---

### Task 3: S3 配置（config.go）

**Files:**
- Modify: `config.go`
- Modify: `config.example.yaml`
- Test: `config_test.go`

- [ ] **Step 1: Write the failing tests**

追加到 `config_test.go`：

```go
func TestLoadConfig_S3FromFile(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
s3:
  endpoint: "minio.internal:9000"
  access_key: "AK"
  secret_key: "SK"
  bucket: "multica-artifacts"
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.S3
	if s == nil {
		t.Fatal("S3 config not loaded")
	}
	if s.Endpoint != "minio.internal:9000" || s.AccessKey != "AK" || s.SecretKey != "SK" || s.Bucket != "multica-artifacts" {
		t.Fatalf("s3 = %+v", *s)
	}
	if s.Secure {
		t.Error("s3.secure must default to false")
	}
}

func TestLoadConfig_S3Absent(t *testing.T) {
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

func TestLoadConfig_S3EnvOverrides(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
s3:
  endpoint: "minio.internal:9000"
  access_key: "AK"
  secret_key: "SK"
  bucket: "multica-artifacts"
`)
	t.Setenv("S3_ENDPOINT", "10.9.8.7:9000")
	t.Setenv("S3_SECURE", "true")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3.Endpoint != "10.9.8.7:9000" || !cfg.S3.Secure {
		t.Fatalf("env override failed: %+v", *cfg.S3)
	}
}

func TestLoadConfig_S3Incomplete(t *testing.T) {
	path := writeConfig(t, `
smtp:
  from: "noreply@example.com"
s3:
  endpoint: "minio.internal:9000"
`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("incomplete s3 section must fail at startup, not at first use")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestLoadConfig_S3`
Expected: 4 个 FAIL（`cfg.S3` 字段不存在，编译错误）

- [ ] **Step 3: Implement S3 config**

`config.go` 加类型与字段：

```go
// S3Config points at the object storage holding agent-produced files
// (excel-mcp outputs, comment attachments). nil = feature off.
type S3Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Secure    bool
}
```

`fileConfig` 加：

```go
	S3 struct {
		Endpoint  string `yaml:"endpoint"`
		AccessKey string `yaml:"access_key"`
		SecretKey string `yaml:"secret_key"`
		Bucket    string `yaml:"bucket"`
		Secure    bool   `yaml:"secure"`
	} `yaml:"s3"`
```

`Config` 加 `S3 *S3Config`。

`LoadConfig` 中，`if cfg.SMTP.Port == 0 {` 块之后、env 覆盖区之前，插入段存在性判断与构建：

```go
	// S3: an absent section disables the s3_key attachment source; a present
	// but incomplete one fails fast at startup (smtp.from style).
	s3Present := fc.S3.Endpoint != "" || fc.S3.AccessKey != "" || fc.S3.SecretKey != "" || fc.S3.Bucket != ""
	if s3Present {
		cfg.S3 = &S3Config{
			Endpoint:  fc.S3.Endpoint,
			AccessKey: fc.S3.AccessKey,
			SecretKey: fc.S3.SecretKey,
			Bucket:    fc.S3.Bucket,
			Secure:    fc.S3.Secure,
		}
	}
```

env 覆盖区（`ALLOWED_DOMAINS` 覆盖之前）加：

```go
	if cfg.S3 != nil {
		if v := os.Getenv("S3_ENDPOINT"); v != "" {
			cfg.S3.Endpoint = v
		}
		if v := os.Getenv("S3_ACCESS_KEY"); v != "" {
			cfg.S3.AccessKey = v
		}
		if v := os.Getenv("S3_SECRET_KEY"); v != "" {
			cfg.S3.SecretKey = v
		}
		if v := os.Getenv("S3_BUCKET"); v != "" {
			cfg.S3.Bucket = v
		}
		if v := os.Getenv("S3_SECURE"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("S3_SECURE: %w", err)
			}
			cfg.S3.Secure = b
		}
	}
```

末尾校验区（`smtp.from` 校验之后）加：

```go
	if cfg.S3 != nil {
		missing := []string{}
		if cfg.S3.Endpoint == "" {
			missing = append(missing, "endpoint")
		}
		if cfg.S3.AccessKey == "" {
			missing = append(missing, "access_key")
		}
		if cfg.S3.SecretKey == "" {
			missing = append(missing, "secret_key")
		}
		if cfg.S3.Bucket == "" {
			missing = append(missing, "bucket")
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("s3 section is present but incomplete: missing %s (unset fields disable s3_key attachments: leave the whole s3: section empty)",
				strings.Join(missing, ", "))
		}
	}
```

`config.example.yaml` 在 `allowed_domains` 注释段之后追加：

```yaml

# Optional: object storage for agent-produced files (excel-mcp outputs,
# comment attachments). Attachments may then reference {"s3_key": "..."}.
# Absent section = s3_key attachments disabled. If present, all four
# fields are required.
# s3:
#   endpoint: "minio.internal:9000"
#   access_key: "..."
#   secret_key: "..."
#   bucket: "multica-artifacts"
#   secure: false
```

文件末尾注释区补一行：`S3_ENDPOINT / S3_ACCESS_KEY / S3_SECRET_KEY / S3_BUCKET / S3_SECURE` override the file。

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
git add config.go config_test.go config.example.yaml
git commit -m "feat: s3 config section with env overrides and fail-fast validation"
```

---

### Task 4: ObjectStore 抽象 + minio 实现（s3.go）

**Files:**
- Modify: `go.mod` / `go.sum`（minio-go 依赖）
- Create: `s3.go`
- Create: `s3_test.go`

- [ ] **Step 1: Add the minio-go dependency**

Run: `go get github.com/minio/minio-go/v7@v7.2.1 && go mod tidy`
Expected: go.mod 新增 direct require，无错误

- [ ] **Step 2: Write the failing test — fake S3 server exercises error mapping**

Create `s3_test.go`:

```go
package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// newFakeS3 serves GET /{bucket}/{key} with canned behavior per key.
// Keys: "ok/<n>bytes" → n zero bytes; "404" → S3 NoSuchKey XML error;
// "err" → connection close (transport error, not an S3 error response).
func newFakeS3(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path[len("/"):]
		if key == "404" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>gone</Message></Error>`))
			return
		}
		if key == "err" {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("server cannot hijack")
			}
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		// "ok/<n>"
		n, err := strconv.Atoi(key[len("ok/"):])
		if err != nil {
			t.Fatalf("unexpected key %q", key)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, string(make([]byte, n)))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, ts.Listener.Addr().String()
}

func newTestObjectStore(t *testing.T) *MinioObjectStore {
	t.Helper()
	_, addr := newFakeS3(t)
	// minio.New prepends the scheme itself from the secure flag, and rejects
	// scheme-prefixed endpoints ("cannot have fully qualified paths") — so
	// pass the bare host:port (probed against minio-go v7.2.1).
	store, err := NewMinioObjectStore(addr, "AK", "SK", "multica-artifacts", false)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestObjectStore_Get_OK(t *testing.T) {
	store := newTestObjectStore(t)
	data, err := store.Get("ok/5", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 5 {
		t.Fatalf("len = %d, want 5", len(data))
	}
}

func TestObjectStore_Get_ObjectNotFound(t *testing.T) {
	store := newTestObjectStore(t)
	_, err := store.Get("404", 1024)
	if err != ErrObjectNotFound {
		t.Fatalf("err = %v, want ErrObjectNotFound", err)
	}
}

func TestObjectStore_Get_TooLarge(t *testing.T) {
	store := newTestObjectStore(t)
	_, err := store.Get("ok/10", 5)
	if err != ErrObjectTooLarge {
		t.Fatalf("err = %v, want ErrObjectTooLarge", err)
	}
}

func TestObjectStore_Get_OtherError(t *testing.T) {
	store := newTestObjectStore(t)
	_, err := store.Get("err", 1024)
	if err == nil || err == ErrObjectNotFound || err == ErrObjectTooLarge {
		t.Fatalf("err = %v, want a wrapped transport error", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./... -run TestObjectStore`
Expected: FAIL（编译错误：`s3.go` 尚不存在）

- [ ] **Step 4: Implement s3.go**

Create `s3.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ObjectStore reads attachment objects from the configured bucket. The
// interface keeps the tool testable, mirroring MailSender.
type ObjectStore interface {
	// Get fetches key, reading at most limit bytes: larger objects return
	// ErrObjectTooLarge without buffering. A missing object returns
	// ErrObjectNotFound; anything else wraps the underlying error.
	Get(key string, limit int64) ([]byte, error)
}

var (
	ErrObjectNotFound = errors.New("s3 object not found")
	ErrObjectTooLarge = errors.New("s3 object exceeds attachment size limit")
)

// MinioObjectStore is the ObjectStore over a minio-go client.
type MinioObjectStore struct {
	client *minio.Client
	bucket string
}

// NewMinioObjectStore builds a store. endpoint is bare host:port — minio.New
// derives the scheme from the secure flag (http when false, the in-cluster
// plaintext trust model) and rejects scheme-prefixed endpoints.
// A malformed endpoint is a startup failure: config was validated already,
// so the caller (main.go) treats the error as fatal.
func NewMinioObjectStore(endpoint, accessKey, secretKey, bucket string, secure bool) (*MinioObjectStore, error) {
	cli, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 endpoint %q: %w", endpoint, err)
	}
	return &MinioObjectStore{client: cli, bucket: bucket}, nil
}

// Get implements ObjectStore.
func (s *MinioObjectStore) Get(key string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, mapS3Error(key, err)
	}
	defer obj.Close()

	// limit+1 detects oversize in one read: reading exactly limit bytes can't
	// distinguish "exactly at limit" from "more to come".
	data, err := io.ReadAll(io.LimitReader(obj, limit+1))
	if err != nil {
		return nil, mapS3Error(key, err)
	}
	if int64(len(data)) > limit {
		return nil, ErrObjectTooLarge
	}
	return data, nil
}

// mapS3Error folds minio error responses onto the store's sentinel errors.
func mapS3Error(key string, err error) error {
	resp := minio.ToErrorResponse(err)
	// ToErrorResponse returns a zero value for transport errors (no XML
	// error body), so gate on the canonical missing-object codes.
	if resp.Code == "NoSuchKey" || resp.Code == "NoSuchObject" || resp.StatusCode == 404 {
		return fmt.Errorf("%w: %s", ErrObjectNotFound, key)
	}
	return fmt.Errorf("s3 get %q: %w", key, err)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./... -run TestObjectStore`
Expected: 4 个 PASS。若 `TestObjectStore_Get_OtherError` 因 hijack 后 minio 重试而慢，可将 fake server 的响应都设为无 Content-Length 的流式；测试超时上限默认 10 分钟，不会阻塞。

- [ ] **Step 6: Run full suite + vet**

Run: `go vet ./... && go test ./...`
Expected: 全部 PASS

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum s3.go s3_test.go
git commit -m "feat: ObjectStore abstraction with minio-go client"
```

---

### Task 5: 附件解析重构——字符串容错 + s3_key + filename 缺省（tools.go）

**Files:**
- Modify: `tools.go`（attachments 解析段整体重写）
- Modify: `tools_test.go`
- Modify: `server_test.go`（newTestHTTPServer 加 store 参数）

- [ ] **Step 1: Write the failing tests — attachments 字符串容错**

追加到 `tools_test.go`：

```go
func TestSendEmail_AttachmentsStringParses(t *testing.T) {
	env, fs := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": `[{"filename":"x.txt","content_b64":"aGVsbG8="}]`,
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if got, _ := out["attachment_count"].(float64); got != 1 {
		t.Errorf("attachment_count = %v, want 1", out["attachment_count"])
	}
	if !strings.Contains(string(fs.Msg), "x.txt") {
		t.Errorf("attachment not sent:\n%s", fs.Msg)
	}
}

func TestSendEmail_AttachmentsStringInvalid(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":          "a@x.com",
		"subject":     "s",
		"body":        "hi",
		"attachments": "not-json",
	})
	if out["success"] != false {
		t.Fatal("success must be false for unparseable attachments string")
	}
	if code, _ := out["error_code"].(string); code != "INVALID_PARAM_TYPE" {
		t.Errorf("error_code = %q, want INVALID_PARAM_TYPE", code)
	}
}
```

- [ ] **Step 2: Write the failing tests — s3_key 附件（用 fake ObjectStore）**

`tools_test.go` 顶部加 fake store：

```go
// fakeObjectStore implements ObjectStore with canned data per key.
type fakeObjectStore struct {
	objects map[string][]byte
	err     error // when non-nil, returned for every key
}

func (f *fakeObjectStore) Get(key string, limit int64) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	data, ok := f.objects[key]
	if !ok {
		return nil, ErrObjectNotFound
	}
	if int64(len(data)) > limit {
		return nil, ErrObjectTooLarge
	}
	return data, nil
}
```

`newToolEnv` 不动；s3 相关测试自建 env：

```go
func TestSendEmail_S3Attachment(t *testing.T) {
	env, fs := newToolEnv(t)
	env.Store = &fakeObjectStore{objects: map[string][]byte{
		"workspace-42/reports/销量.xlsx": []byte("xlsx-bytes"),
	}}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key": "workspace-42/reports/销量.xlsx",
		}},
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if !strings.Contains(string(fs.Msg), "销量.xlsx") {
		t.Errorf("basename not used as filename:\n%s", fs.Msg)
	}
}

func TestSendEmail_S3AttachmentExplicitFilename(t *testing.T) {
	env, fs := newToolEnv(t)
	env.Store = &fakeObjectStore{objects: map[string][]byte{"k": []byte("v")}}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key":   "k",
			"filename": "report.txt",
		}},
	})
	if out["success"] != true {
		t.Fatalf("success = %v, err: %v", out["success"], out["error"])
	}
	if !strings.Contains(string(fs.Msg), "filename=\"report.txt\"") {
		t.Errorf("explicit filename not used:\n%s", fs.Msg)
	}
}

func TestSendEmail_S3Unavailable(t *testing.T) {
	env, _ := newToolEnv(t) // Store nil
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key": "k",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false when s3 is unconfigured")
	}
	if code, _ := out["error_code"].(string); code != "S3_UNAVAILABLE" {
		t.Errorf("error_code = %q, want S3_UNAVAILABLE", code)
	}
}

func TestSendEmail_S3ObjectNotFound(t *testing.T) {
	env, _ := newToolEnv(t)
	env.Store = &fakeObjectStore{} // empty map
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key": "missing",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for missing object")
	}
	if code, _ := out["error_code"].(string); code != "S3_OBJECT_NOT_FOUND" {
		t.Errorf("error_code = %q, want S3_OBJECT_NOT_FOUND", code)
	}
}

func TestSendEmail_S3TooLarge(t *testing.T) {
	env, _ := newToolEnv(t)
	env.Config.MaxAttachmentMB = 1
	env.Store = &fakeObjectStore{objects: map[string][]byte{"k": make([]byte, 2<<20)}}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key": "k",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for oversized object")
	}
	if code, _ := out["error_code"].(string); code != "ATTACHMENT_TOO_LARGE" {
		t.Errorf("error_code = %q, want ATTACHMENT_TOO_LARGE", code)
	}
}

func TestSendEmail_S3FetchFailed(t *testing.T) {
	env, _ := newToolEnv(t)
	env.Store = &fakeObjectStore{err: fmt.Errorf("dial: connection refused")}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key": "k",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for store failure")
	}
	if code, _ := out["error_code"].(string); code != "S3_FETCH_FAILED" {
		t.Errorf("error_code = %q, want S3_FETCH_FAILED", code)
	}
}

func TestSendEmail_AttachmentBothSources(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key":      "k",
			"content_b64": "aGVsbG8=",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for both sources given")
	}
	if code, _ := out["error_code"].(string); code != "INVALID_PARAM_VALUE" {
		t.Errorf("error_code = %q, want INVALID_PARAM_VALUE", code)
	}
}

func TestSendEmail_AttachmentNoSource(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"filename": "x.txt",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for no source given")
	}
	if code, _ := out["error_code"].(string); code != "INVALID_PARAM_VALUE" {
		t.Errorf("error_code = %q, want INVALID_PARAM_VALUE", code)
	}
}

func TestSendEmail_S3KeyTrailingSlash(t *testing.T) {
	env, _ := newToolEnv(t)
	env.Store = &fakeObjectStore{objects: map[string][]byte{"a/b": []byte("v")}}
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"s3_key": "workspace-42/reports/",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for trailing-slash key")
	}
	if code, _ := out["error_code"].(string); code != "INVALID_PARAM_VALUE" {
		t.Errorf("error_code = %q, want INVALID_PARAM_VALUE", code)
	}
}

func TestSendEmail_ContentB64MissingFilename(t *testing.T) {
	env, _ := newToolEnv(t)
	out := callSendEmail(t, env, http.Header{}, map[string]any{
		"to":      "a@x.com",
		"subject": "s",
		"body":    "hi",
		"attachments": []any{map[string]any{
			"content_b64": "aGVsbG8=",
		}},
	})
	if out["success"] != false {
		t.Fatal("success must be false for missing filename")
	}
	if code, _ := out["error_code"].(string); code != "MISSING_FILENAME" {
		t.Errorf("error_code = %q, want MISSING_FILENAME", code)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./... -run 'TestSendEmail'`
Expected: 新增 12 个测试 FAIL（`env.Store` 字段、新解析分支不存在，编译错误）

- [ ] **Step 4: Implement — ServerEnv.Store 与附件解析重构**

`tools.go` 的 `ServerEnv` 加字段：

```go
// ServerEnv carries the dependencies shared by tool handlers.
type ServerEnv struct {
	Config *Config
	Sender MailSender
	Store  ObjectStore // nil = s3_key attachments unavailable
}
```

将 `SendEmail` 中 `--- attachments ---` 段整体替换为：

```go
	// --- attachments ---------------------------------------------------------
	// Two source shapes: inline base64 (content_b64) or an object-store
	// reference (s3_key). A string attachments value is parsed as JSON first
	// (proxy frameworks without schema awareness serialize arrays as strings).
	var attachments []Attachment
	limitBytes := int64(env.Config.MaxAttachmentMB) * 1024 * 1024
	if v, present := args["attachments"]; present && v != nil {
		var rawList []any
		switch x := v.(type) {
		case string:
			if err := json.Unmarshal([]byte(x), &rawList); err != nil {
				return fail(errCodeInvalidParamType, "parameter \"attachments\" is a string but not valid JSON array: %v", err)
			}
		case []any:
			rawList = x
		default:
			return fail(errCodeInvalidParamType, "parameter \"attachments\" must be an array of objects, got %T", v)
		}
		for i, raw := range rawList {
			m, ok := raw.(map[string]any)
			if !ok {
				return fail(errCodeInvalidParamType, "attachments[%d] must be an object", i)
			}
			att, err := resolveAttachment(env, m, i, limitBytes)
			if err != nil {
				var ae *attachmentError
				if errors.As(err, &ae) {
					return fail(ae.code, "%s", ae.msg)
				}
				return fail(errCodeInternal, "attachments[%d]: %v", i, err)
			}
			attachments = append(attachments, att)
		}
	}
```

`tools.go` 需要 import `"errors"` 与 `"path"`。

`tools.go` 末尾（envelopeRecipients 之前）新增错误类型与解析函数。`resolveAttachment` 返回 `*attachmentError`（类型化错误携带 code），由 `SendEmail` 调用处 `errors.As` 拆开、经 `fail` 渲染成带 `error_code` 的 payload——不能在 `resolveAttachment` 里直接 `errPayload`，因为它拿不到 multica_context：

```go
// attachmentError carries a tool error code through resolveAttachment; the
// SendEmail caller unpacks it into an errPayload via fail.
type attachmentError struct {
	code string
	msg  string
}

func (e *attachmentError) Error() string { return e.msg }

// resolveAttachment builds one Attachment from a raw argument map. Exactly
// one of content_b64 / s3_key must be present; filename defaults to the
// s3 key's base name. limitBytes caps the decoded/object size.
func resolveAttachment(env *ServerEnv, m map[string]any, i int, limitBytes int64) (Attachment, error) {
	fail := func(code, format string, a ...any) error {
		return &attachmentError{code: code, msg: fmt.Sprintf(format, a...)}
	}

	b64Raw, hasB64 := m["content_b64"]
	s3keyRaw, hasS3 := m["s3_key"]
	if hasB64 {
		if _, ok := b64Raw.(string); !ok {
			return Attachment{}, fail(errCodeInvalidParamType, "attachments[%d].content_b64 must be a string, got %T", i, b64Raw)
		}
	}
	if hasS3 {
		if _, ok := s3keyRaw.(string); !ok {
			return Attachment{}, fail(errCodeInvalidParamType, "attachments[%d].s3_key must be a string, got %T", i, s3keyRaw)
		}
	}
	b64, _ := b64Raw.(string)
	s3key := strings.TrimSpace(s3keyRaw.(string))
	b64 = strings.TrimSpace(b64) // tolerate caller-wrapped base64 whitespace

	if hasB64 && hasS3 {
		return Attachment{}, fail(errCodeInvalidParamValue, "attachments[%d]: give exactly one of content_b64 or s3_key, got both", i)
	}
	if !hasB64 && !hasS3 {
		return Attachment{}, fail(errCodeInvalidParamValue, "attachments[%d]: content_b64 or s3_key is required", i)
	}

	filename, _ := m["filename"].(string)
	if v, present := m["filename"]; present {
		s, ok := v.(string)
		if !ok {
			return Attachment{}, fail(errCodeInvalidParamType, "attachments[%d].filename must be a string, got %T", i, v)
		}
		if strings.TrimSpace(s) == "" {
			return Attachment{}, fail(errCodeInvalidParamType, "attachments[%d].filename must be a non-empty string", i)
		}
		filename = s
	}

	var content []byte
	if hasB64 {
		if strings.TrimSpace(filename) == "" {
			return Attachment{}, fail(errCodeMissingFilename, "attachments[%d].filename is required for content_b64 attachments (s3_key attachments derive it from the key)", i)
		}
		var err error
		content, err = base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return Attachment{}, fail(errCodeInvalidBase64, "attachments[%d].content_b64 is not valid base64: %v", i, err)
		}
	} else {
		if s3key == "" {
			return Attachment{}, fail(errCodeInvalidParamValue, "attachments[%d].s3_key must be a non-empty string", i)
		}
		// Reject directory-ish keys before the basename fallback: Go's
		// path.Base strips trailing slashes, so "a/b/" would yield the
		// misleading name "b" rather than ".".
		if strings.HasPrefix(s3key, "/") || strings.HasSuffix(s3key, "/") {
			return Attachment{}, fail(errCodeInvalidParamValue, "attachments[%d].s3_key %q must not start or end with \"/\"", i, s3key)
		}
		if filename == "" {
			filename = path.Base(s3key)
		}
		if env.Store == nil {
			return Attachment{}, fail(errCodeS3Unavailable, "s3 attachments are not configured (add the s3 section to config)")
		}
		var err error
		content, err = env.Store.Get(s3key, limitBytes)
		if err != nil {
			if errors.Is(err, ErrObjectNotFound) {
				return Attachment{}, fail(errCodeS3ObjectNotFound, "attachments[%d].s3_key: %v", i, err)
			}
			if errors.Is(err, ErrObjectTooLarge) {
				return Attachment{}, fail(errCodeAttachmentTooLarge, "attachments[%d] (s3_key %q) exceeds the %d MiB limit", i, s3key, env.Config.MaxAttachmentMB)
			}
			return Attachment{}, fail(errCodeS3FetchFailed, "attachments[%d].s3_key: %v", i, err)
		}
	}

	if int64(len(content)) > limitBytes {
		return Attachment{}, fail(errCodeAttachmentTooLarge, "attachment %q is %d bytes, exceeding the %d MiB limit", filename, len(content), env.Config.MaxAttachmentMB)
	}

	ct, _ := m["content_type"].(string)
	return Attachment{Filename: filename, Content: content, ContentType: ct}, nil
}
```

注意 `env.Store == nil` 判断放在 basename 推导之后、Get 之前——错误消息里若带 filename 更友好，但顺序上必须先拒绝目录形态 key 再推导。

`server_test.go` 的 `newTestHTTPServer` 同步加 nil Store（`env.Store` 为 nil 时 s3 不可用，HTTP 层暂不测 s3 路径）：

```go
	env := &ServerEnv{Config: cfg, Sender: fs, Store: nil}
```

（即维持现状——本行只为显式化，编译必需。）

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./...`
Expected: 全部 PASS

- [ ] **Step 6: Commit**

```bash
git add tools.go tools_test.go server_test.go
git commit -m "feat: s3_key attachment source, string attachments tolerance, optional filename"
```

---

### Task 6: 显式 inputSchema + 工具描述（server.go）

**Files:**
- Modify: `server.go`
- Test: `server_test.go`

- [ ] **Step 1: Write the failing test — tools/list 必须暴露完整 schema**

追加到 `server_test.go`：

```go
func TestServer_ToolSchemaExposed(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPServer(t, "", fs))
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	status, resp := mcpCall(t, ts.URL, nil, body)
	if status != 200 {
		t.Fatalf("status = %d: %s", status, resp)
	}
	for _, want := range []string{
		`"subject"`, `"body"`, `"body_type"`, `"to"`, `"cc"`,
		`"attachments"`, `"filename"`, `"content_b64"`, `"content_type"`, `"s3_key"`,
		`"anyOf"`,
	} {
		if !strings.Contains(resp, want) {
			t.Errorf("tools/list schema missing %s: %.500s", want, resp)
		}
	}
}

func TestServer_StringAttachmentsOverMCP(t *testing.T) {
	fs := &fakeSender{}
	ts := httptest.NewServer(newTestHTTPServer(t, "", fs))
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send-email","arguments":{"subject":"s","body":"hi","to":["a@x.com"],"attachments":"[{\"filename\":\"x.txt\",\"content_b64\":\"aGVsbG8=\"}]"}}}`
	status, resp := mcpCall(t, ts.URL, nil, body)
	if status != 200 {
		t.Fatalf("status = %d: %s", status, resp)
	}
	if !strings.Contains(resp, `"success":true`) {
		t.Fatalf("string attachments must pass the SDK schema then parse: %.300s", resp)
	}
	if !strings.Contains(resp, `"attachment_count":1`) {
		t.Fatalf("attachment not sent: %.300s", resp)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestServer_ToolSchemaExposed`
Expected: FAIL（当前 inputSchema 只有 `{"type":"object"}`，不含任何属性名）

Run: `go test ./... -run TestServer_StringAttachmentsOverMCP`
Expected: FAIL 或 PASS 不定——SDK 现在对 string attachments 会因空 schema 意外放行（`In=any` 无验证），到 handler 后被 `must be an array` 拒绝，预期 FAIL

- [ ] **Step 3: Implement the explicit schema in server.go**

`server.go` 中 `mcp.AddTool` 调用处改为（`InputSchema` 用 `json.RawMessage`；描述动态拼上限）：

```go
	tool := &mcp.Tool{
		Name: "send-email",
		Description: fmt.Sprintf("Send an email via the configured SMTP relay. Attachments (max %d MiB each) come in two shapes: inline base64 ({\"filename\": \"x.xlsx\", \"content_b64\": \"...\"}) or an object-store reference ({\"s3_key\": \"workspace-42/reports/x.xlsx\"} — filename defaults to the key's base name; requires the s3 config section). If \"to\" is empty, the multica caller's user email is used; if that is also absent the call fails.",
			env.Config.MaxAttachmentMB),
		InputSchema: json.RawMessage(inputSchemaJSON),
	}
	mcp.AddTool(server, tool, func(ctx context.Context, req *mcp.CallToolRequest, in any) (*mcp.CallToolResult, any, error) {
```

`server.go` 底部加 schema 常量：

```go
// inputSchemaJSON is the deliberately-lenient tool schema. Required-ness,
// content_b64/s3_key exclusivity and filename defaults are enforced by the
// handler (uniform error_code payloads), not by the schema — so those
// violations reach callers as readable tool errors rather than SDK schema
// rejections. The anyOf string forms let schema-less proxy frameworks
// pass array params as JSON strings through SDK pre-validation, where the
// handler's JSON.parse tolerance takes over.
const inputSchemaJSON = `{
  "type": "object",
  "properties": {
    "to": {
      "anyOf": [
        {"type": "string", "description": "recipient, or comma-separated recipients"},
        {"type": "array", "items": {"type": "string"}, "description": "recipient addresses"}
      ],
      "description": "recipients; empty falls back to the multica caller's email"
    },
    "cc": {
      "anyOf": [
        {"type": "string", "description": "carbon-copy address, or comma-separated addresses"},
        {"type": "array", "items": {"type": "string"}, "description": "carbon-copy addresses"}
      ],
      "description": "carbon-copy recipients; optional"
    },
    "subject": {"type": "string", "description": "email subject line (required)"},
    "body": {"type": "string", "description": "email body, plain text or HTML per body_type (required)"},
    "body_type": {"type": "string", "enum": ["text", "html"], "description": "body rendering; default \"text\""},
    "attachments": {
      "anyOf": [
        {
          "type": "array",
          "items": {
            "type": "object",
            "properties": {
              "filename": {"type": "string", "description": "attachment name shown to the recipient; defaults to the s3_key base name (required for content_b64)"},
              "content_b64": {"type": "string", "description": "base64-encoded attachment content (mutually exclusive with s3_key)"},
              "content_type": {"type": "string", "description": "MIME type; guessed from filename when empty"},
              "s3_key": {"type": "string", "description": "object key in the configured bucket; mutually exclusive with content_b64"}
            }
          }
        },
        {"type": "string", "description": "JSON-encoded array of attachment objects (tolerated for schema-less proxies)"}
      ],
      "description": "optional attachments, max size per attachment from config"
    }
  }
}`
```

import 区加 `"fmt"`。

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: 全部 PASS，含两个新测试。

注意：若 `TestServer_StringAttachmentsOverMCP` 在 SDK 预验证处失败（任何 anyOf 分支都不匹配 string 的意外情形），先用一个最小 Go 程序直接把 `{"attachments":"..."}` 对 `inputSchemaJSON` 做 `jsonschema.Resolve + Validate` 探针，确认 anyOf 第二分支接受 string；设计上已接受，正常应 PASS。

- [ ] **Step 5: Commit**

```bash
git add server.go server_test.go
git commit -m "feat: explicit lenient inputSchema for send-email (fixes empty-schema root cause)"
```

---

### Task 7: main.go 接线 + README

**Files:**
- Modify: `main.go`
- Modify: `README.md`

- [ ] **Step 1: Wire the ObjectStore in main.go**

`main.go` 的 `env := &ServerEnv{...}` 行改为：

```go
	env := &ServerEnv{Config: cfg, Sender: &SMTPSender{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port}}
	if cfg.S3 != nil {
		store, err := NewMinioObjectStore(cfg.S3.Endpoint, cfg.S3.AccessKey, cfg.S3.SecretKey, cfg.S3.Bucket, cfg.S3.Secure)
		if err != nil {
			log.Fatalf("s3: %v", err)
		}
		env.Store = store
	}
```

（Task 4 的 `NewMinioObjectStore` 已定稿为返回 `(*MinioObjectStore, error)`，与上面的 `log.Fatalf` fail-fast 接线一致，无需再改签名。）

启动日志行加上 S3 状态：

```go
	s3State := "off"
	if env.Store != nil {
		s3State = cfg.S3.Bucket
	}
	log.Printf("email-mcp listening on %s (smtp %s:%d, from %s, s3 %s)", addr, cfg.SMTP.Host, cfg.SMTP.Port, cfg.SMTP.From, s3State)
```

- [ ] **Step 2: Run full suite + vet**

Run: `go vet ./... && go test ./...`
Expected: 全部 PASS

- [ ] **Step 3: Update README**

`README.md` 的 send-email 工具参数表改为：

```markdown
| argument | type | notes |
|----------|------|-------|
| `to` | string \| string[] | empty → multica user email; comma-separated string splits |
| `cc` | string \| string[] | optional; comma-separated string splits |
| `subject` | string | required |
| `body` | string | required |
| `body_type` | "text" \| "html" | default "text" |
| `attachments` | array \| string | `{filename?, content_b64 \| s3_key, content_type?}`; a JSON-encoded array string is also accepted (schema-less proxies) |
```

参数表之后加一节：

```markdown
### Attachments

Two mutually exclusive sources per attachment:

- `{"filename": "x.xlsx", "content_b64": "..."}` — inline base64; `filename` is required.
- `{"s3_key": "workspace-42/reports/x.xlsx"}` — object key in the configured
  bucket; `filename` defaults to the key's base name. Requires the `s3` config
  section (see Configure); without it, `s3_key` attachments fail with
  `S3_UNAVAILABLE`.

Size limit per attachment is `max_attachment_mb` (default 10) for both shapes.
Failed calls return `success: false` with an `error` message and a
machine-readable `error_code` (e.g. `S3_OBJECT_NOT_FOUND`,
`ATTACHMENT_TOO_LARGE`, `MISSING_FILENAME`, `INVALID_PARAM_TYPE`).

Direct HTTP callers (bypassing the proxy) can set `X-Multica-*` headers
themselves to get task context echoed in `multica_context` for tracing.
```

Configure 节的 yaml 示例加 s3 段（在 `allowed_domains` 之后）：

```yaml
s3:                    # optional; omit = s3_key attachments disabled
  endpoint: "minio.internal:9000"
  access_key: "..."
  secret_key: "..."
  bucket: "multica-artifacts"
```

Configure 节的环境变量说明句补：`S3_ENDPOINT` `S3_ACCESS_KEY` `S3_SECRET_KEY` `S3_BUCKET` `S3_SECURE` override the file。

- [ ] **Step 4: Commit**

```bash
git add main.go README.md
git commit -m "feat: wire ObjectStore at startup, document s3_key attachments and error codes"
```

---

### Task 8: 版本 v0.2.0

**Files:**
- Modify: `server.go`（Implementation Version）
- Modify: `README.md`（GHCR tag、image load 示例）

- [ ] **Step 1: Bump version strings**

`server.go` 的 `&mcp.Implementation{Name: "email-mcp", Version: "v0.1.0"}` → `"v0.2.0"`。
`README.md` 两处 `v0.1.0` → `v0.2.0`（GHCR pull 与 image load 示例）。

- [ ] **Step 2: Run full suite + vet**

Run: `go vet ./... && go test ./... && go build -o /dev/null .`
Expected: 全部 PASS、构建成功

- [ ] **Step 3: Commit**

```bash
git add server.go README.md
git commit -m "chore: bump version to v0.2.0"
```

---

## Task 顺序依赖

- Task 1（错误码）先行：后续所有 fail 调用都带 code。
- Task 4 依赖 Task 3（config 提供凭证）；Task 5 依赖 Task 1 + 4。
- Task 6 依赖 Task 5（schema 描述与 handler 实际行为对齐）。
- Task 7 依赖 Task 4（NewMinioObjectStore 签名在 Task 7 定稿为返回 error）。
- Task 8 收尾。

## 验收清单（对 spec）

- [ ] `tools/list` 返回完整 schema（含 `attachments`/`s3_key`/`anyOf`）——Task 6
- [ ] 字符串 attachments 走完整 MCP 通道成功——Task 6
- [ ] 字符串解析失败 → `INVALID_PARAM_TYPE`——Task 5
- [ ] content_b64/s3_key 互斥、缺源 → `INVALID_PARAM_VALUE`——Task 5
- [ ] content_b64 缺 filename → `MISSING_FILENAME`（消息提示 s3_key 自动补）——Task 5
- [ ] s3_key 缺省 filename → basename；`a/b/` → `INVALID_PARAM_VALUE`——Task 5
- [ ] 未配 s3 → `S3_UNAVAILABLE`；对象缺失 → `S3_OBJECT_NOT_FOUND`；超限 → `ATTACHMENT_TOO_LARGE`；其余 → `S3_FETCH_FAILED`——Task 5
- [ ] 大小上限两形态统一（超限读满即断）——Task 4/5
- [ ] s3 段字段不全启动失败；缺省禁用——Task 3
- [ ] to/cc 逗号拆分——Task 2
- [ ] 直连 multica_context 文档说明——Task 7（README）
- [ ] 版本 v0.2.0——Task 8
