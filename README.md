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
docker pull ghcr.io/courage-zen/bigdata-mcp-email:v0.0.2
```

Or download a per-arch image tar.gz from [Releases](https://github.com/courage-zen/bigdata-mcp-email/releases) and load it — no registry access needed (air-gapped path):

```sh
docker load -i email-mcp-image-v0.0.2-linux-arm64.tar.gz
docker run -p 8000:8000 -e MCP_AUTH_KEY=your-key -e SMTP_FROM=you@example.com email-mcp:v0.0.2-arm64
```

## Configure

`config.yaml` (path override: `EMAIL_MCP_CONFIG`):

```yaml
smtp:
  host: "127.0.0.1"     # local-smtp-forwarder
  port: 2525
  from: "noreply@example.com"
max_attachment_mb: 10   # optional
allowed_domains:          # optional; omit = no restriction
  - "example.com"
  - "acme.org"
```

Environment variables override the file: `SMTP_HOST` `SMTP_PORT` `SMTP_FROM`
`MAX_ATTACHMENT_MB`, and `ALLOWED_DOMAINS` (comma-separated) overrides the
`allowed_domains` file list. Server env: `MCP_AUTH_KEY` (comma-separated bearer keys;
unset = no auth), `MCP_HOST` (default 0.0.0.0), `MCP_PORT` (default 8000).
`/health` is exempt from auth. `smtp.from` (or `SMTP_FROM`) is required — the
server exits at startup without it.

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
| `to` | string \| string[] | empty → multica user email |
| `cc` | string \| string[] | optional |
| `subject` | string | required |
| `body` | string | required |
| `body_type` | "text" \| "html" | default "text" |
| `attachments` | array | `{filename, content_b64, content_type?}` |

Returns JSON: `success`, `message_id`, `to`, `cc`, `from`, `reply_to`,
`attachment_count`, `signature_appended`, `duration_ms`, `multica_context`;
`error` is present on failure (omitted on success).

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
