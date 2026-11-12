# S3 使用标准对齐 设计文档

## 背景与目标

excel-mcp 侧已把三个 MCP 服务的 S3 实践合成为行为契约
（`~/code/excel-mcp/docs/superpowers/specs/2026-11-11-mcp-s3-usage-standard.md`，
下称"标准"）。逐条对照后本项目有 17 项偏差：除规范点名本项目为反面案例
来源的两项（YAML 配置层、私有 env 命名）外，还有 key 校验缺失、URL 归一
化缺失、无白名单、无元数据预检、超时未分层、错误未净化、`/health` 不报
S3 态、S3 超限错误码不区分等实质差距。

目标：**读侧 S3 面全量对齐标准**——配置契约、key 契约、IO 模式、错误
契约四个维度。本项目纯读（S3→SMTP 附件），标准中写侧条目（`output_key`、
上传、`S3_INLINE_MAX_BYTES`、`S3_UPLOAD_FAILED`）不适用。

## 已确认的事实与决策

- **迁移策略（已拍板）**：YAML `s3:` 段本次移除（规范明令禁止的第二配
  置源，不留兼容）；旧 env 名（`S3_ACCESS_KEY` 等）保留一个版本周期可
  读、标准名优先——与 excel-mcp 对 `EXCEL_*` 旧名"一版兼容"的做法一致。
- **minio-go v7.2.1 事实（源码确认）**：
  - `getEndpointURL` 由 `Secure` flag 推导 scheme，endpoint 须裸
    host:port（api-prefixed URL 会被拒）；
  - `BucketLookupAuto` 下自定义 endpoint 一律 path-style，仅
    amazon/google/aliyun endpoint 走 virtual-host——与标准的 path-style
    推断规则**天然吻合**；
  - `Options.Region` 非空即跳过 bucket-location 探测（v7.2.1 在 region
    为空时会发 `GET /{bucket}?location=` 探测）；
  - `BucketLookupPath` / `BucketLookupDNS` 显式强制两种风格。
- **双上限分工**：`S3_MAX_FILE_BYTES`（标准名，默认 100 MiB）管 S3 传输
  层；`max_attachment_mb`（email 私有，默认 10 MiB，继续 YAML/env）管
  SMTP 附件业务层。预检两道都查，流中复查取较小值。
- **附件读取保持整读进内存**（对标准"临时文件流式下载"条目的偏差，
  保留并文档化）：MIME 消息本需全量组装，且双上限有界。
- SMTP/allowed_domains 等非 S3 配置的 YAML+env 形态**不动**——标准只治
  S3 面。

## 改动总览

| # | 改动 | 文件 |
|---|------|------|
| 1 | S3 env 契约重写：标准命名 + 三态软禁用 + 旧名一版兼容 | config.go |
| 2 | YAML `s3:` 段移除（出现即 warning） | config.go |
| 3 | key 校验 + URL 归一化 + 白名单 | s3keys.go（新） |
| 4 | Stat 预检 + 分层超时 + 错误净化 + 超限 sentinel 拆分 | s3.go |
| 5 | `S3_INVALID_KEY` / `S3_OBJECT_TOO_LARGE` 错误码接入 | tools.go |
| 6 | `/health` 报 S3 配置态 | server.go |
| 7 | README / config.example.yaml 同步 + v0.3.0 | 文档 |

## 配置契约（config.go）

S3 相关 env 变量：

| 变量 | 来源 | 语义 |
|---|---|---|
| `S3_BUCKET` | 标准 | 必填三件套之一 |
| `AWS_ACCESS_KEY_ID` | 标准（旧名 `S3_ACCESS_KEY` 一版兼容） | 必填三件套之二 |
| `AWS_SECRET_ACCESS_KEY` | 标准（旧名 `S3_SECRET_KEY`） | 必填三件套之三 |
| `AWS_ENDPOINT_URL` | 标准（旧名 `S3_ENDPOINT` + `S3_SECURE`） | 完整 URL，scheme 决定 Secure（http→false / https→true），剥离 scheme 后的 host:port 传 `minio.New` |
| `S3_REGION` → `AWS_REGION` → `us-east-1` | 标准 | region 回退链；非空即免 location 探测 |
| `S3_USE_PATH_STYLE` | 标准 | `1/true/yes`→`BucketLookupPath`；`0/false/no`→`BucketLookupDNS`；未设置/其他值→`BucketLookupAuto`（minio-go auto 语义恰好等于标准推断：有 endpoint → path，无 → virtual-host） |
| `S3_KEY_PREFIXES` | 标准 | 逗号分隔 key 前缀白名单，段边界匹配；空 = 不限 |
| `S3_MAX_FILE_BYTES` | 标准 | S3 传输层读上限，默认 104857600（100 MiB） |

规则：

1. **三态不存在**：三件套齐备 = S3 可用；缺任一 = `S3Config` nil（软禁
   用，`s3_key` 附件报 `S3_UNAVAILABLE`，服务正常启动）。部分配置（如
   有 access key 无 bucket）同样禁用，启动 warning **指名缺失的变量名**
   （只报变量名，不报值）。
2. 标准名与旧名同时设置：**标准名优先**。
3. 旧名语义保持旧形状：`S3_ENDPOINT` 裸 host:port（无 scheme），
   `S3_SECURE`（默认 false）控制 scheme。
4. `S3_MAX_FILE_BYTES` 解析失败回退默认值（excel-mcp `parse_bytes` 同款
   宽容语义）。
5. YAML `s3:` 段不再读取；检测到段存在 → 启动 log warning 提示迁移到
   env（fileConfig 的 S3 字段保留只为检测段存在与否，值弃用）。
6. 三件套齐备但 endpoint 等值非法（`minio.New` 构造失败）仍是启动
   fatal——"缺失"才软禁用，"配了但坏"快速失败（与 `smtp.from` 风格一
   致）。

## Key 契约（s3keys.go 新文件）

移植 excel-mcp `s3keys.rs` 的读侧语义：

- `resolveInputKey(raw, s3cfg)`：裸 key 直通；`http(s)://` URL → 剥
  query（`X-Amz-Signature` 等 presign 参数）→ virtual-host 形式
  （`<bucket>.<domain>/key`）或 path-style 形式（首段须等于配置
  bucket）还原 key；URL 无 key / bucket 不匹配 → `S3_INVALID_KEY`。还原
  结果统一再走 `validateInputKey`（两形态安全规则一致）。
- `validateInputKey(key, s3cfg)`：非空；不以 `/` 开头；无 `//`；无反斜
  杠；无 `.`/`..`/空段；危险扩展名黑名单（16 项，同 excel-mcp：
  `.exe/.msi/.bat/.cmd/.sh/.ps1/.scr/.vbs/.jar/.app/.dmg/.pkg/.deb/.rpm/.so/.dll`）；
  `S3_KEY_PREFIXES` 段边界匹配（前缀 `workspaces/ws-1` 匹配其自身与
  `workspaces/ws-1/a.xlsx`，不匹配 `workspaces/ws-11/a.xlsx`）。
- 失败返回**带原因类别**的错误（空/绝对路径/遍历/危险扩展名/白名单外），
  不回显 key 本身；调用方错误消息里的 `attachments[i]` 索引已足够定位。
- 工具 schema 与描述同步：`s3_key` 描述改为接受裸 key 或完整对象存储
  URL（帮智能体直接粘贴附件 JSON 里的 presigned URL）。

## IO 模式（s3.go 重写）

`ObjectStore.Get(key, limit)` 内部：

1. ctx 60s 墙钟，覆盖 Stat + Get 整体（下载墙钟层）。
2. **`StatObject` 元数据预检**（不起流直接拒）：
   `NoSuchKey` → `ErrObjectNotFound`；size > `S3_MAX_FILE_BYTES` →
   `ErrObjectTooLarge`（S3 层）；size > limit（附件层）→
   `ErrAttachmentTooLarge`（新 sentinel）。
3. 流式读 `min(S3_MAX_FILE_BYTES, limit)+1` 字节，溢出按 Stat 同规则映
   射（对象在 Stat 与 Get 之间被替换的竞态兜底）。
4. minio-go transport 定制：`DialContext` 10s（connect 层）、
   `ResponseHeaderTimeout` 60s（read 层）。
5. **错误净化**：返回给调用方的错误只含安全文本——minio 的
   `resp.Code`（如 `AccessDenied`、`NoSuchBucket`）可含；
   `resp.Message` / transport 错误原文不含（可能携带
   bucket/key/endpoint）。完整错误 `log.Printf` 进服务端日志（日志不受
   净化约束，运维侧可查）。

处理 30s 超时：不适用（无 CPU 重解析阶段）。临时文件：不适用（整读进
内存，见决策节）。

## 错误契约（tools.go）

新增错误码常量：`S3_INVALID_KEY`、`S3_OBJECT_TOO_LARGE`。

`resolveAttachment` 的 s3 分支新顺序：

1. `env.Store == nil` → `S3_UNAVAILABLE`（配置问题先于输入问题）；
2. `resolveInputKey` 校验失败 → `S3_INVALID_KEY`（原因类别，不回显
   key）；
3. filename 缺省 → 归一化后 key 的 basename；
4. `Store.Get` 错误映射：
   - `ErrObjectNotFound` → `S3_OBJECT_NOT_FOUND`（消息不含 key）；
   - `ErrObjectTooLarge` → `S3_OBJECT_TOO_LARGE`（S3 层超限）；
   - `ErrAttachmentTooLarge` → `ATTACHMENT_TOO_LARGE`（附件层超限；
     inline b64 超限仍用此码，语义不变）；
   - 其余 → `S3_FETCH_FAILED`（净化后文本）。

消息净化范围：bucket、endpoint、完整 key 不回显；filename（调用方自
供或 basename）可回显。

## Health（server.go）

`GET /health` → `{"status":"ok","s3":"configured"|"not-configured"}`
（`env.Store` 是否为 nil）。

## 文档与版本

- README：S3 章节重写——env 变量表（含旧名兼容说明）、双上限分工说
  明、错误码表增 `S3_INVALID_KEY`/`S3_OBJECT_TOO_LARGE`、`/health` 返回
  形状、YAML→env 迁移示例；v0.3.0 破坏性变更说明（YAML `s3:` 失效、S3
  超限从 `ATTACHMENT_TOO_LARGE` 拆分、部分配置从启动 fatal 改软禁
  用）。
- config.example.yaml：`s3:` 段删除，注释指向 env 变量。
- server.go 版本号 `v0.2.1` → `v0.3.0`；镜像 tag 随流水线惯例。

## 测试策略

- **s3keys_test.go（新）**：黑名单 16 项遍历、段边界匹配（含
  trailing-slash 前缀）、URL 三形态（path-style / virtual-host / 带签名
  query）、URL 夹带遍历仍被拒、净化断言（错误消息不含
  bucket/key/URL 原文）。
- **config_test.go**：env 矩阵——标准名三件套齐备、缺一 → nil、部分配
  置 → nil（warning 变量名）、旧名回退、标准名优先、region 回退链、
  path-style 三态取值、`S3_MAX_FILE_BYTES` 默认/覆盖/坏值回退、YAML
  `s3:` 段 → 忽略。
- **s3_test.go**：httptest 假 S3——Stat 预检三分支（404 / S3 层超限 /
  附件层超限）、放行后 Get 流、错误映射与净化断言。
- **tools_test.go**：`S3_INVALID_KEY`（各拒绝原因）、
  `S3_OBJECT_TOO_LARGE`、`ATTACHMENT_TOO_LARGE`（s3 附件层 + inline 两
  处）、`S3_OBJECT_NOT_FOUND` 消息不含 key、`S3_UNAVAILABLE` 先于校验。
- **server_test.go**：`/health` 两种 S3 态。

## 风险与兼容性

- **破坏性**：YAML `s3:` 配置失效——升级须同步改部署 env（启动
  warning 提示）；`ATTACHMENT_TOO_LARGE` 语义收窄（S3 层超限改报
  `S3_OBJECT_TOO_LARGE`），按码分支的调用方需更新；部分配置从启动
  fatal 改为软禁用 + warning（可观测性靠 `/health` 与日志）。
- Stat 预检每附件多一次 HEAD 请求（内网 MinIO 可忽略）。
- 旧 env 名自 v0.4.0 起移除。
- 既有 `content_b64` 老用法、字符串 to/cc、签名、域名白名单全部原样。

## 明确不做

- 写侧一切条目（`output_key`、上传、`S3_INLINE_MAX_BYTES`、
  `S3_UPLOAD_FAILED`）——纯读服务。
- 临时文件流式下载（整读进内存，MIME 组装必需 + 双上限有界；偏差写入
  README）。
- 处理 30s 超时（无 CPU 重解析阶段）。
- 文本输出截断标记（无文本型 S3 输出）。
- `s3://` scheme 的 URL 归一化（excel-mcp 先例同样只处理
  http/https）。
- SMTP / allowed_domains 配置形态改造（标准只治 S3 面）。
