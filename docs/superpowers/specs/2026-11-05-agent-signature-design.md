# Agent Email Signature Design

**Date:** 2026-11-05
**Status:** approved (approach A — extend multica context + dedicated signature function)

## Goal

When the calling agent reaches email-mcp through multica-stdio-mcp-proxy, the
proxy injects `X-Multica-*` identity headers. If the headers carry an agent
display name and a workspace display name, append an agent signature block to
the end of the outgoing email body, so human recipients can see which agent in
which workspace composed the message on the caller's behalf.

## Decisions (user-confirmed)

- **Format:** two-line block with a `--` separator:

  ```
  --
  Multica 智能体：{agent_name}
  工作区：{workspace_name}
  ```

- **Missing names → no signature.** `X-Multica-Agent-Name` and
  `X-Multica-Workspace-Name` are best-effort headers (they may be absent when
  the proxy's startup resolution fails). If either is absent or empty, the
  body is sent unchanged. No fallback to raw IDs.
- **No user line, no Task-Id line** — signature carries only agent name and
  workspace name. The Reply-To mechanism already covers "who to answer".
- **Uniform plain-text style for both body types.** For `html` bodies the
  same two-line block is appended with `<br>` line breaks. No styled div.
- **Always on, no config switch** — behavior is fully determined by the
  presence of the headers, keeping the config surface unchanged.

## Approach

**Approach A (chosen): extend multica context + dedicated signature function.**

- `identity.go`'s `multicaHeaderToField` gains two mappings:
  `X-Multica-Agent-Name → agent_name`,
  `X-Multica-Workspace-Name → workspace_name`. As a side effect the
  `multica_context` object echoed in tool results now also reports these two
  name fields (an additive, backward-compatible change that helps debugging).
- New `signature.go` holds `appendAgentSignature(body, bodyType string, mc
  map[string]string) string` — one pure function that decides whether to sign
  and renders the block.
- `tools.go`'s `SendEmail` calls it right after body validation, before
  building the `Email` struct.

Rejected alternatives: reading the headers ad hoc in the handler (B — two
places maintaining "which multica headers exist") and appending inside
`mime.go`'s BuildMessage (C — drags identity context into the pure MIME
rendering layer).

## Signature rendering

- **text body:** `{body}\n\n--\nMultica 智能体：{agent_name}\n工作区：{workspace_name}`
- **html body:** `{body}\n<br>--<br>\nMultica 智能体：{agent_name}\n<br>工作区：{workspace_name}\n<br>`

The leading `\n\n` / `\n<br>` separates the signature from the body. Values
come from the `mc` map, so they pass through the existing
quoted-printable body encoding; they never enter the header section, so there
is no new header-injection surface. For html bodies the values are inserted
raw, same as the caller-controlled body itself — no new HTML attack surface
either.

## Changes

1. `identity.go` — add the two name-header mappings (echo-only effect).
2. `signature.go` (new, ~30 lines) — `appendAgentSignature`.
3. `tools.go` — call site after body validation; `emailResult` gains
   `signature_appended bool` so the calling agent can see in the tool result
   that the body was signed.
4. README — document the signature behavior in the tool description.

## Testing

- `signature_test.go` (new): signs text and html bodies when both names
  present; leaves body unchanged when either name is missing/empty; nil
  mc map.
- `tools_test.go` (existing suite): end-to-end — request with
  `X-Multica-Agent-Name`/`X-Multica-Workspace-Name` headers produces an SMTP
  message whose body part contains the signature; request without them does
  not; `signature_appended` true/false respectively.
- `identity_test.go` (existing suite): the two new header→field mappings.
