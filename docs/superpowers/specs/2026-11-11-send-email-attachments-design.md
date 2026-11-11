# send-email 附件通道修复与增强 设计文档

## 背景与目标

一次实际交付（带 Excel 附件的邮件）暴露了 send-email 在标准 MCP 通道下的三个问题：

1. **inputSchema 为空**。`server.go` 用 `mcp.AddTool` + `In=any` 注册工具，SDK 对
   `In=any` 且未显式给 schema 的情况一律填 `{"type":"object"}`（已从 SDK
   `toolForErr`/`setSchema` 源码确认）。调用方框架无 schema 约束时，数组参数
   （attachments）被统一按字符串透传，服务端校验必然拒绝——无论怎么传都会被判成
   string，在当前封装通道下无解。
2. **必填项不可发现**。`filename` 必填但 schema/描述完全没提，只能靠报错试错，每轮
   试错消耗一次完整调用。
3. **content_b64 结构性瓶颈**。线上 v0.1.0 附件只有 base64 内联一种形态，文件内容
   必须穿过智能体的工具调用参数——大附件在 token 层面就不可行。本次成功是靠"直连
   HTTP + 脚本读文件再 base64"绕过去的。智能体侧的文件（excel-mcp 产出、评论附件）
   都在 S3/MinIO 里，因此 **s3_key 附件源是让标准 MCP 通道真正可用的核心，不只是
   锦上添花**。

目标：落地后智能体可全程走标准 MCP 通道完成带附件的邮件发送，不再需要绕行。

## 已确认的事实与决策

- 线上部署的就是本仓库 v0.1.0（content_b64 方案，无 path 能力）。
- 智能体侧文件都在 S3/MinIO；`path` 本地文件读取在当前部署形态下用不上，不做。
- **s3_key 形态**：纯对象 key（如 `workspace-42/reports/销量.xlsx`），bucket 固定
  写在 email-mcp 配置里，与 excel-mcp 产出物的 s3_key 原样衔接。
- **S3 接入方式**：email-mcp 自持凭证（endpoint + access_key + secret_key +
  bucket），密钥只落在 email-mcp 容器，智能体无需任何 S3 权限。
- **S3 客户端**：minio-go v7（只需 GetObject + 超限截断，范围很小）。
- **Schema 路线**：保持 `mcp.AddTool` 泛型注册，给 `Tool.InputSchema` 传显式
  `json.RawMessage` 完整 schema；SDK 预验证为第一道防线，handler 归一化保留为
  第二道。已确认 SDK `setSchema` 对非 nil schema 走 remarshal → Resolve 路径，
  `json.RawMessage` 是文档支持的形态，schema 非法会在启动时 panic（fail-fast）。

## 改动总览

| # | 改动 | 优先级来源 |
|---|------|-----------|
| 1 | 显式完整 inputSchema（raw JSON） | P1 能力可发现 |
| 2 | attachments 字符串容错解析（JSON.parse） | P1 兼容现有封装通道 |
| 3 | filename 可缺省（s3_key 形态取 key 的 basename） | P2 |
| 4 | s3_key 附件源（email-mcp 自持凭证，minio-go） | P2 |
| 5 | 错误码 `error_code` 字段 | P3 |

## inputSchema 设计

**刻意宽松**是本设计的核心取舍：

- `to` / `cc`：`anyOf [string, array-of-strings]`——声明现状容错，字符串形态在
  SDK 层就合法。
- `attachments`：`anyOf [array-of-objects, string]`——**string 形态在 schema 里就
  是合法的**，封装框架传来的字符串能穿过 SDK 预验证、到达 handler 做 JSON.parse
  容错。这正是让现有封装通道立刻可用的机制。
- attachment item：`filename` / `content_b64` / `content_type` / `s3_key` 四个属性
  全部声明类型+描述，但**不设 `required`、不设 `additionalProperties:false`**——
  必填性、互斥关系（content_b64 XOR s3_key）、缺省 filename 语义都留给 handler，
  保证这些错误走统一的 `error_code` 友好格式，而不是 SDK 的 schema 报错文本。
- `subject` / `body`：描述里标注 (required)，不设 schema required（同理，错误格式
  统一）。
- 工具 Description 动态构造：包含附件两种形态的完整 JSON 示例 + 当前配置的附件
  大小上限（`%d MiB`），让智能体一次看全、不再试错。

## 附件解析与容错（handler 层，tools.go）

```
args["attachments"] 为 string
  → json.Unmarshal 成 []any 失败 → INVALID_PARAM_TYPE（错误信息保持可读）
  → 成功 → 按数组继续
每个 item：
  s3_key 与 content_b64 都给 → INVALID_PARAM_VALUE
  都不给 → INVALID_PARAM_VALUE
  都需为 string，非 string → INVALID_PARAM_TYPE
  content_b64 形态：filename 仍必填 → MISSING_FILENAME（错误信息提示 s3_key 形态会自动补）
  s3_key 形态：key 去首尾空白后为空或以 / 结尾（目录形态）→ INVALID_PARAM_VALUE
    （须在取 Base 前显式拒绝：Go 的 path.Base 会剥掉尾斜杠，"a/b/" 会得到 "b" 而非 "."）
  filename 缺省 → path.Base(key)（通过上述校验后必有非空段）
  size 校验统一放在内容取得之后（两种形态一致，超限即中止不再读后续附件）
```

另有一个小容错：`to`/`cc` 的字符串形态支持**逗号分隔多地址**
（`"a@x.com, b@x.com"` → 拆分、去空）。现状这种输入会整个被判非法；拆分后与
RFC 5322 地址分隔语义一致。

## s3_key 附件源（新文件 s3.go + config 扩展）

配置（yaml `s3:` 段，环境变量覆盖，遵循现有覆盖规则）：

```yaml
s3:
  endpoint: "minio.internal:9000"
  access_key: "..."
  secret_key: "..."
  bucket: "multica-artifacts"
  secure: false        # 可选，默认 false（内网明文，与 SMTP hop 同信任模型）
```

- 环境变量：`S3_ENDPOINT` / `S3_ACCESS_KEY` / `S3_SECRET_KEY` / `S3_BUCKET` /
  `S3_SECURE`。
- 段缺省 → S3 功能关闭（`content_b64` 照常，`s3_key` 报 `S3_UNAVAILABLE`）；
  段存在但字段不全 → **启动时报错退出**（对齐 `smtp.from` 的 fail-fast 风格）。
- minio 客户端在 main.go 构造一次注入 `ServerEnv`。

取文件接口（为可测试性抽象，与 MailSender 同模式）：

```go
// ObjectStore reads attachment objects from the configured bucket.
type ObjectStore interface {
    // Get fetches key, reading at most limit bytes（读满即断，超限不缓冲整个对象）.
    // 缺对象 → ErrObjectNotFound；超限 → ErrObjectTooLarge；其余 wrap 原始错误.
    Get(key string, limit int64) ([]byte, error)
}
```

minio 实现：`GetObject` + `io.LimitReader(limit+1)` 超限即返回——单次请求、内存
有界、天然防超大对象。404 通过 `minio.ToErrorResponse` 识别映射为
`ErrObjectNotFound`。

## 错误码表（P3）

失败 payload 增加 `error_code` 字段；`error` 保留原文，`multica_context` 照常
回显。纯增量、向后兼容。

| code | 触发 |
|------|------|
| `MISSING_PARAM` | subject / body 缺失 |
| `INVALID_PARAM_TYPE` | 参数类型/形态错（含 attachments 字符串解析失败） |
| `INVALID_PARAM_VALUE` | body_type 枚举、邮箱格式、content_b64/s3_key 互斥、s3_key 无文件名段 |
| `MISSING_FILENAME` | content_b64 形态缺 filename |
| `INVALID_BASE64` | base64 解码失败 |
| `ATTACHMENT_TOO_LARGE` | 超大小上限（两种形态统一） |
| `DOMAIN_NOT_ALLOWED` | 收件域白名单拒绝 |
| `S3_UNAVAILABLE` | 未配置 s3 却传了 s3_key |
| `S3_OBJECT_NOT_FOUND` | 对象不存在（独立成码，便于程序化纠错） |
| `S3_FETCH_FAILED` | 其余 S3 取文件失败 |
| `BUILD_FAILED` / `SMTP_FAILED` / `INVALID_ARGUMENTS` / `INTERNAL` | 构造/发送/参数反序列化/内部错误 |

P3 里"直连模式 multica_context 为空"：这是调用方没注入 `X-Multica-*` header，
服务端无从合成，本仓库只做文档说明（README 注明直连调用可自带这些 header 获得
回显追踪）。

## 测试策略

- **handler 层**（tools_test.go，直接调 SendEmail）：字符串 attachments 解析
  成功/失败、s3_key 各错误分支（fake ObjectStore 按接口契约返回 sentinel
  error）、filename 缺省、error_code 断言、逗号拆分。
- **HTTP 层**（server_test.go，走完整 MCP 协议）：**`tools/list` 断言
  inputSchema 含全部属性定义**（端到端钉住本次根因修复）、字符串 attachments 走
  完整 MCP 通道成功（钉住 anyOf+容错链路）。
- **s3 实现层**（s3_test.go，新）：httptest 假 S3 服务器（200 / 404 / 超限三种
  响应）→ minio 客户端 → 断言错误映射。
- **config 层**（config_test.go）：s3 段加载、env 覆盖、字段不全报错、缺省 nil。

## 风险与兼容性

- **错误面变化**：类型违规的参数（如 `to: 42`）现在被 SDK 预验证拦截（IsError
  文本），而非 handler 的 JSON payload。消息仍然可读，且这类错误本来就该在调用前
  避免。
- **minio-go 依赖**：go.mod 新增一个直接依赖及其间接依赖——已确认接受。
- **既有行为不变**：content_b64 老用法、字符串 to/cc、签名、白名单全部原样；
  新增字段全是可选。
- 版本 v0.2.0，走现有 tag → Release → GHCR 流水线。

## 明确不做

- `path` 本地文件读取（智能体文件都在 S3，当前部署形态用不上）
- presigned URL 附件源
- S3 region 配置（MinIO 内网部署用不到）
- 收件（IMAP）能力
- 直连模式 multica_context 合成
