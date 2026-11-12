# email-mcp

MCP Server for sending email from multica agents. One tool: `send-email`.

```
multica agent → multica-stdio-mcp-proxy → email-mcp :8000/mcp → local-smtp-forwarder :2525 → upstream SMTP
```

- Streamable HTTP transport (stateless, JSON responses), Bearer auth
- Recipient fallback: empty `to` → `X-Multica-User-Email` → error
- From is fixed (`smtp.from`); on fallback a `Reply-To` points at the caller
- `X-Multica-*` headers are echoed in the tool result as `multica_context`
- Agent signature: when `X-Multica-Agent-Name` and `X-Multica-Workspace-Name`
  are both present, an attribution block identifying the agent and workspace
  is appended to the body; the tool result reports it via `signature_appended`

## Install

Download a self-contained binary from [Releases](https://github.com/courage-zen/bigdata-mcp-email/releases):

```sh
tar -xzf email-mcp-linux-arm64.tar.gz   # or email-mcp-linux-amd64.tar.gz
MCP_AUTH_KEY=your-key SMTP_FROM=you@example.com ./email-mcp
```

Or run from source:

```bash
make build
cp config.example.yaml config.yaml   # edit smtp.from
make run
make test
```

Or run in Docker:

```sh
docker build -t email-mcp .
docker run -p 8000:8000 -e MCP_AUTH_KEY=your-key -e SMTP_FROM=noreply@example.com email-mcp
```

Or pull the image from GHCR:

```sh
docker pull ghcr.io/courage-zen/bigdata-mcp-email:v0.3.0
```

Or download a per-arch image tar.gz from [Releases](https://github.com/courage-zen/bigdata-mcp-email/releases) and load it — no registry access needed (air-gapped path):

```sh
docker load -i email-mcp-image-v0.3.0-linux-arm64.tar.gz
docker run -p 8000:8000 -e MCP_AUTH_KEY=your-key -e SMTP_FROM=you@example.com email-mcp:v0.3.0-arm64
```

## Configure

`config.yaml` (path override: `EMAIL_MCP_CONFIG`):

```yaml
smtp:
  host: "127.0.0.1"     # local-smtp-forwarder
  port: 2525
  from: "noreply@example.com"
max_attachment_mb: 10   # optional
allowed_domains:        # optional; omit = no restriction
  - "example.com"
  - "acme.org"
```

Environment variables override the file: `SMTP_HOST` `SMTP_PORT` `SMTP_FROM`
`MAX_ATTACHMENT_MB`, and `ALLOWED_DOMAINS` (comma-separated) overrides the
`allowed_domains` file list. Server env: `MCP_AUTH_KEY` (comma-separated bearer
keys; unset = no auth), `MCP_HOST` (default 0.0.0.0), `MCP_PORT` (default 8000).
`/health` is exempt from auth. `smtp.from` (or `SMTP_FROM`) is required — the
server exits at startup without it.

S3 attachments are env-only (the S3 usage standard — no YAML section):

| variable | required | meaning |
|----------|----------|---------|
| `S3_BUCKET` | yes | bucket holding agent-produced files |
| `AWS_ACCESS_KEY_ID` | yes | static access key |
| `AWS_SECRET_ACCESS_KEY` | yes | static secret |
| `S3_REGION` → `AWS_REGION` | no | region; default `us-east-1` |
| `AWS_ENDPOINT_URL` | no | S3-compatible endpoint (MinIO/R2), full URL; `http://` = plaintext. Empty = native AWS S3 |
| `S3_USE_PATH_STYLE` | no | `1/true/yes` or `0/false/no`; unset = path-style when `AWS_ENDPOINT_URL` is set, virtual-host otherwise |
| `S3_KEY_PREFIXES` | no | comma-separated key-prefix allowlist, segment-boundary match; empty = whole bucket |
| `S3_MAX_FILE_BYTES` | no | S3 transport read cap, default 100 MiB |

Missing any required variable disables S3 attachments (`S3_UNAVAILABLE` per
call, `/health` reports `"not-configured"`); the server still starts. The
legacy names `S3_ACCESS_KEY` / `S3_SECRET_KEY` / `S3_ENDPOINT` +
`S3_SECURE` remain readable for one version (standard names win); the yaml
`s3:` section is ignored.

v0.3.0 changes: the yaml `s3:` section is removed (ignored, startup warning),
S3-layer oversize is renamed from `ATTACHMENT_TOO_LARGE` to
`S3_OBJECT_TOO_LARGE`, and a partially-configured S3 is a soft disable
(`/health` `"not-configured"`, `S3_UNAVAILABLE` per call) instead of a
startup error.

When `allowed_domains` (or `ALLOWED_DOMAINS`) lists at least one domain, every
recipient address — `to`, `cc`, and the fallback multica user email — must be
at one of those domains (case-insensitive, exact match, subdomains don't
match). Otherwise the whole message is rejected and the error names the
rejected addresses and the configured domains.

## Multica wiring

Configure the agent's mcp_config through multica-stdio-mcp-proxy:

```json
{
  "mcpServers": {
    "email-mcp": {
      "command": "multica-stdio-mcp-proxy",
      "args": [
        "--url", "http://your-email-mcp-host:8000/mcp",
        "--token", "<upstream-bearer-key>",
        "--allow-http"
      ]
    }
  }
}
```

The proxy injects the `X-Multica-*` identity headers automatically.

## send-email tool

| argument | type | notes |
|----------|------|-------|
| `to` | string \| string[] | empty → multica user email; comma-separated string splits |
| `cc` | string \| string[] | optional; comma-separated string splits |
| `subject` | string | required |
| `body` | string | required |
| `body_type` | "text" \| "html" | default "text" |
| `attachments` | array \| string | `{filename?, content_b64 \| s3_key, content_type?}`; a JSON-encoded array string is also accepted (schema-less proxies) |

### Attachments

Two mutually exclusive sources per attachment:

- `{"filename": "x.xlsx", "content_b64": "..."}` — inline base64; `filename` is required.
- `{"s3_key": "workspace-42/reports/x.xlsx"}` — object key in the configured
  bucket; `filename` defaults to the key's base name. Requires the S3 env
  config (see Configure); without it, `s3_key` attachments fail with
  `S3_UNAVAILABLE`.

Size limit per attachment is `max_attachment_mb` (default 10) for both shapes.
Two size caps apply to `s3_key` attachments: `S3_MAX_FILE_BYTES` (S3 layer,
default 100 MiB, error `S3_OBJECT_TOO_LARGE`) and `max_attachment_mb`
(attachment layer, default 10 MiB, error `ATTACHMENT_TOO_LARGE`). `s3_key`
accepts a plain key or a full object-store URL (presigned query strings are
stripped). Keys are validated: no traversal, no dangerous extensions
(`.exe` etc.), and `S3_KEY_PREFIXES` allowlist when set (violation:
`S3_INVALID_KEY`).
Failed calls return `success: false` with an `error` message and a
machine-readable `error_code` (e.g. `S3_OBJECT_NOT_FOUND`,
`S3_OBJECT_TOO_LARGE`, `S3_INVALID_KEY`, `ATTACHMENT_TOO_LARGE`,
`MISSING_FILENAME`, `INVALID_PARAM_TYPE`).

Direct HTTP callers (bypassing the proxy) can set `X-Multica-*` headers
themselves to get task context echoed in `multica_context` for tracing.

### Response

Returns JSON: `success`, `message_id`, `to`, `cc`, `from`, `reply_to`,
`attachment_count`, `signature_appended`, `duration_ms`, `multica_context`;
`error` is present on failure (omitted on success), alongside
`error_code` (machine-readable, on failure).

When `X-Multica-Agent-Name` and `X-Multica-Workspace-Name` both carry a
non-empty value, this block is appended to the end of the body:

```text
--
智能体：{agent_name}
工作区：{workspace_name}
```

html bodies get the same lines with `<br>` breaks. Otherwise the body is sent
unchanged and `signature_appended` is `false`.

## Deployment (k8s sidecar)

Run email-mcp and [local-smtp-forwarder](https://github.com/courage-zen/local-smtp-forwarder)
as two containers in one pod: email-mcp exposes 8000, the forwarder listens on
2525 pod-locally and holds the upstream SMTP credentials. The dependency
between the two is the SMTP protocol itself — point `smtp.host`/`smtp.port`
at any plaintext submission endpoint.
