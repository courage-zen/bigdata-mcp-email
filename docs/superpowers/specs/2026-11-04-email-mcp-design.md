# bigdata-mcp-email 设计文档

## 背景与目标

为 multica 中的 agent 提供发邮件能力的 MCP 服务。调用链路：

```
multica agent (Claude Code)
  └─ multica-stdio-mcp-proxy (stdio)
       └─ HTTP POST → email-mcp :8000/mcp
            ├─ Bearer 认证 (MCP_AUTH_KEY)
            ├─ 提取 X-Multica-* → request context
            └─ tool: send-email
                 ├─ 收件人回退: 参数 to 为空 → header user_email → 都空则报错
                 ├─ 构造 MIME
                 └─ 明文 SMTP → local-smtp-forwarder :2525
                      └─ TLS+AUTH+From改写 → 上游真实邮件服务器
```

服务本身**无状态、职责单一**：认证 + 一个工具 + 极简 SMTP 客户端。所有邮件复杂度
（TLS、认证、From 改写）留在 forwarder。

## 技术选型

- **Go**。理由：
  1. 与 local-smtp-forwarder、multica-stdio-mcp-proxy 同语言，整条链路技术栈统一；
  2. `CGO_ENABLED=0` 交叉编译出真静态二进制，发布流程比 pg-mcp 的 PyInstaller 简单可靠；
  3. `net/smtp` 标准库够用（明文直连 forwarder，无需 TLS/AUTH 逻辑）。
- **MCP Go SDK**（`github.com/modelcontextprotocol/go-sdk`）：streamable HTTP
  stateless 模式，wrap 成 `http.Handler` 后套自己的中间件，与 pg-mcp 的传输模式对齐。
- **MIME 手工构造**（约 150 行 + 测试）：支持范围有限（text/html + base64 附件 +
  RFC 2047 编码 Subject），不引入完整邮件库。依赖只有 MCP SDK + `gopkg.in/yaml.v3`。

## 项目结构

```
bigdata-mcp-email/
  main.go          入口：加载配置、启动 HTTP 服务
  config.go        config.yaml + 环境变量覆盖
  middleware.go    Bearer 认证 + X-Multica-* 提取到 request context
  mime.go          MIME 构造（text/html、附件、RFC 2047 Subject）
  sender.go        SMTP 客户端（明文连 forwarder）
  tools.go         send-email 工具注册与参数 schema
  identity.go      multica context 类型定义
  config.example.yaml
  Dockerfile / Makefile
  .github/workflows/{build,release}.yml
```

## send-email 工具

### 参数

```json
{
  "to": ["a@x.com"],            // 可选，字符串或字符串数组；空时回退 user_email
  "cc": ["b@x.com"],            // 可选，同上形态
  "subject": "报告",             // 必填
  "body": "...",                // 必填
  "body_type": "text",          // "text" | "html"，默认 "text"
  "attachments": [              // 可选
    {
      "filename": "report.csv",
      "content_b64": "SGVsbG8...",
      "content_type": "text/csv"   // 可选，缺省按扩展名猜，未知用 application/octet-stream
    }
  ]
}
```

`to`/`cc` 允许字符串或数组两种形态（方便 LLM 调用），归一化为数组。

### 行为

1. **收件人解析**：`to` 为空 → 取 `X-Multica-User-Email`；也为空 → 返回错误
   （信息中说明"参数 to 与 multica user_email 均未提供"）。
2. **From**：固定为 `SMTP_FROM`（裸地址，不设显示名）。有效 From 实际由上游
   SMTP 账号决定：Gmail 等会把 From 改写为认证账号地址，`SMTP_FROM` 应配置为
   forwarder 账号地址（或其 send-as 别名）。
3. **Reply-To**：发生收件人回退（to 为空、用了 user_email）时，加
   `Reply-To: <user_email>`，保证直接回复能到达发起人。
4. **自动头**：生成 `Message-ID`（`<随机@SMTP_FROM域>`) 和 `Date`。
5. **校验**：email 格式（含 `@`）、base64 可解码、附件大小上限
   （`max_attachment_mb`，默认 10MB）、`body_type` 枚举。HTML 正文按 text/html
   + UTF-8 charset 发送。

### 返回格式

对齐 pg-mcp 风格，JSON：

```json
{
  "success": true,
  "message_id": "<...@domain>",
  "to": ["a@x.com"],
  "cc": [],
  "from": "noreply@example.com",
  "reply_to": "user@example.com",
  "attachment_count": 1,
  "duration_ms": 350,
  "multica_context": {"workspace_id": "...", "user_email": "..."}
}
```

错误时 `success: false` + `error` 字段，`multica_context` 照常回显。缺失的
header 对应键不出现；无 multica header 时（本地开发）`multica_context` 为 `{}`。

## 配置

`config.yaml`（路径可用 `EMAIL_MCP_CONFIG` 覆盖，默认工作目录下 `config.yaml`）：

```yaml
smtp:
  host: "127.0.0.1"       # local-smtp-forwarder 地址
  port: 2525
  from: "noreply@example.com"
max_attachment_mb: 10      # 可选
```

环境变量覆盖 config：`SMTP_HOST` / `SMTP_PORT` / `SMTP_FROM` /
`MAX_ATTACHMENT_MB`。服务级环境变量（与 pg-mcp 命名一致）：
`MCP_AUTH_KEY`（逗号分隔多个）、`MCP_HOST`（默认 0.0.0.0）、`MCP_PORT`（默认 8000）。
`MCP_AUTH_KEY` 未设置时不做认证（本地开发）；设置后所有请求必须带
`Authorization: Bearer <key>`。`/health` 端点免认证，供探活。

## 认证与 multica 上下文

镜像 pg-mcp 的语义，Go 实现：

- 中间件校验 Bearer token ∈ AUTH_KEYS，失败返回 401 JSON
- 提取 6 个 `X-Multica-*` header（workspace/agent/task id + user id/name/email）
  存入 `context.Context`，随请求传递给工具
- **不做 workspace/agent 白名单**（已确认：发邮件场景所有 agent 权限相同）
- 工具返回 JSON 中回显 `multica_context`；user_email 仅作收件人回退，不作权限维度

## 错误处理

- 参数校验失败：`success: false` + 具体原因（哪个参数、什么问题），不 panic
- SMTP 连接/发送失败：`success: false` + 错误链（dial/auth/data 各阶段包裹错误）
- base64 解码失败、附件超限：同参数校验失败处理
- 所有错误都在工具返回 JSON 中体现，`multica_context` 始终回显，便于排查

## 测试

- `mime_test.go`：纯文本/HTML/中文主题（RFC 2047 断言）/附件（boundary、
  Content-Type、base64 折行）的构造断言
- `config_test.go`：YAML 加载 + 环境变量覆盖优先级（对齐 forwarder 的测试模式）
- `tools_test.go`：收件人回退三分支（有 to / 无 to 有 header / 都无 → 报错）、
  to 字符串/数组归一化、参数校验
- `sender_test.go`：测试内起假 SMTP server（`net.Listen`，收 DATA），端到端
  断言信封（MAIL FROM/RCPT TO）与正文
- CI 冒烟（对齐 pg-mcp release.yml 的 smoke 步骤）：起服务 → curl 带全部
  multica header 调 `tools/call` → 断言响应含 `multica_context`

## 发布

- push：`go vet` + `go test` + 交叉编译检查
- tag `v*`：test → `CGO_ENABLED=0 go build -trimpath -ldflags='-s -w'` 交叉编译
  `email-mcp-linux-amd64.tar.gz` / `email-mcp-linux-arm64.tar.gz` + `checksums.txt`
  → GitHub Release
- 仓库：`courage-zen/bigdata-mcp-email`
- Dockerfile 对齐 forwarder：多阶段构建（golang:alpine 编译 → alpine 运行，非 root）

## 部署形态（与 local-smtp-forwarder 的关系）

**代码仓库分离，部署时同一 k8s pod 双容器（sidecar 模式）。** 决策依据：

- 依赖关系只到协议层：email-mcp 对下游的全部依赖是"一个 SMTP 服务器"
  （明文提交、无认证），forwarder 只是这个依赖当前的提供方。将来指向
  任何接受明文提交的 SMTP 端点都是纯配置变更（SMTP_HOST/SMTP_PORT）；
  若要直连需要 STARTTLS/AUTH 的真实邮件服务器，只需给 sender.go 增加
  TLS/AUTH 支持——受控的局部升级，不影响工具层与 MIME 层
- forwarder 已在服务 multica 生态的多个调用方，是有独立价值的通用工具，
  保持独立仓库和独立发布节奏，不与 email-mcp 合并
- 同 pod 双容器共享网络命名空间，email-mcp 连 `127.0.0.1:2525` 天然可达，
  设计中的 smtp 配置（host/port）无需任何调整
- 凭证隔离：上游 SMTP 密码只在 forwarder 容器内，`MCP_AUTH_KEY` 只在
  email-mcp 容器内，互相不可见
- forwarder 的 2525 端口随之从节点级监听收窄为 pod 内监听，安全面更小

```yaml
# pod 布局示意
containers:
  - name: email-mcp            # 暴露 8000，对外提供 MCP 服务
    env: [MCP_AUTH_KEY, ...]
  - name: forwarder            # 2525 仅 pod 内可达
    env: [MAILER_UPSTREAM_*]   # SMTP 凭证只在这里
```

演进选项（本期不做）：若将来想省掉 loopback SMTP 一跳，可让 forwarder 把
relay 逻辑提炼成可导入的 Go 包，email-mcp 通过 go module 引用后单二进制
运行——但这会把协议级依赖变成代码级依赖，需要动 forwarder 仓库并管理
跨仓库依赖，与"依赖只到协议层"的原则相悖，当前收益不抵成本。

## 未来扩展（本期不做）

- **From 显示名**：`From: "数据助手（来自 AIOps 工作区）" <noreply@x.com>`。
  email-mcp 侧直接构造显示名即可穿过 forwarder（forwarder 的 from_display_names
  只改写无显示名的发件人，有显示名的透传）。header 中 agent/workspace 只有
  UUID，需要 config 维护 id 前缀 → 名字映射，或从 multica API 查询。留作后续参数。
- workspace/agent 白名单（如出现分权限需求，照 pg-mcp 的前缀匹配模式加）
- 收件（IMAP）能力
