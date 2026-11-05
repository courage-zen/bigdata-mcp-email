# Recipient Domain Allowlist Design

**Date:** 2026-11-05
**Status:** approved (approach A — config-normalized set + pure check at the existing validation loop)

## Goal

When the operator configures a list of allowed domains (yaml list or env
comma-separated), `send-email` rejects any request whose recipient addresses
(to, cc, and the fallback multica user email) fall outside those domains. An
empty/missing configuration means no restriction — today's behavior.

## Decisions (user-confirmed)

- **Scope:** to + cc + the fallback-injected multica `user_email` — every
  address the message would actually be delivered to is constrained.
- **Fallback address is NOT exempt:** if `to` is empty and the header-derived
  user email's domain is not allowlisted, the call fails (uniform rule, no
  surprises).
- **Matching:** case-insensitive, whitespace-trimmed, exact domain match
  (subdomains do NOT match — `a.example.com` ≠ `example.com`).
- **Empty configuration = no restriction** (backward compatible). At least one
  configured domain activates the check.
- **Naming:** yaml key `allowed_domains` (native yaml list), env variable
  `ALLOWED_DOMAINS` (comma-separated). `ALLOWED_DOMAINS` overrides the file,
  same as `SMTP_FROM` etc.
- **Violation handling:** reject the whole message (no partial send). The error
  lists the rejected addresses and the configured domains so the calling agent
  can self-correct.
- **Check timing:** after recipient fallback, alongside the existing
  `validEmail` loop in `SendEmail` — one validation stanza, consistent error
  format. Startup only parses/normalizes the domain list.

## Approach

**Approach A (chosen): config-time normalization + pure check.**

- `config.go`: `fileConfig` gains `AllowedDomains []string \`yaml:"allowed_domains"\``;
  `Config` gains `AllowedDomains []string` (normalized: trimmed, lowercased,
  empties dropped, duplicates collapsed). The env override splits on commas,
  applies the same normalization, and replaces the file value.
- `tools.go`: after the recipient fallback stanza, in/next to the existing
  `validEmail` loop, one check — if the allowlist is active, every address in
  `to`+`cc` must have its domain (the part after the last `@`, lowercased) in
  the set. First violation fails the whole call with both the rejected
  addresses and the configured domain list in the error message.

Rejected alternatives: B (parse per call — duplicated normalization logic
drifts, wasted work per request) and C (MIME/SMTP-layer filtering — recipient
policy is a business rule; errors could not surface in the tool result).

## Validation logic

- Domain extraction: `strings.LastIndex(addr, "@")`; the part after it,
  lowercased. (`validEmail` already guarantees exactly one `@` and non-empty
  parts, so the extraction is safe at this point in the flow.)
- The normalized domain list is stored as a `map[string]struct{}` on `Config`
  (e.g. `allowedDomainSet`), built once at load time — O(1) membership checks,
  no per-call parsing.
- Both the config list in the error message and the checked domain are
  normalized (lowercase) form, so the message matches what was configured.

## Error format

```
recipient domain not allowed: "foo@evil.com" not in allowed domains [example.com, acme.org]
```

(All rejected addresses when there are several; the bracketed list is the
configured domains, normalized order-preserving.) The failure goes through the
existing `fail()` path — `success: false`, the error in `emailResult.error`,
`multica_context` echoed as usual.

## Changes

1. `config.go` — `allowed_domains` yaml key, `ALLOWED_DOMAINS` env override,
   normalization to `AllowedDomains []string` + `allowedDomainSet` map.
2. `tools.go` — the check in `SendEmail` (one stanza after recipient fallback,
   co-located with `validEmail`).
3. `README.md` — Configure section: document `allowed_domains` /
   `ALLOWED_DOMAINS` and the rejection semantics.
4. Tests: `config_test.go` (yaml list, env override, empty→inactive,
   normalization), `tools_test.go` (to/cc/fallback each blocked when
   out-of-list; mixed to+cc blocked; all-in-list passes; empty config passes
   any domain; error message contains rejected address + configured domains).

## Testing

- Config: yaml list parses; env replaces file; whitespace/case normalized;
  empty/absent → nil set (inactive); malformed entries (empty strings) dropped.
- Tools (via fake sender): blocked to; blocked cc; blocked fallback user_email;
  mixed allowed+blocked → whole call fails, nothing sent; all-allowed passes;
  inactive (no config) passes any domain; error text includes the address and
  the configured list; `signature_appended` unaffected.
- Existing suite: all 53 current test functions must stay green (no
  allowed_domains configured in any of them → inactive path everywhere).

## Out of scope

- Subdomain matching (wildcards) — can be added later if needed.
- Per-workspace or per-agent domain policies.
- Allowlisting the From/Reply-To side (From is fixed by config already).
- Rate limiting or content filtering of any kind.
