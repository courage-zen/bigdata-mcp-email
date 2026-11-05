# Docker Image Release Design

**Date:** 2026-11-05
**Status:** approved (approaches: A — independent docker.yml, forwarder pattern)

## Goal

Publish the email-mcp container image via GitHub: multi-arch image on GHCR, and
on `v*` tags attach per-arch image tarballs (`docker save | gzip`) to the GitHub
Release alongside the existing binary assets, so the image is downloadable as gz.

## Decisions (user-confirmed)

- Repo visibility: **public** (matches local-smtp-forwarder; GHCR image pulls
  and Release downloads work anonymously).
- Image pipeline triggers: **main push + PR + v\* tags** (PRs build without
  pushing, which also closes deferred ledger item w — Dockerfile gets CI
  validation).
- Structure: **Approach A** — a new independent `.github/workflows/docker.yml`
  modeled on local-smtp-forwarder's; the existing `build.yml` and `release.yml`
  are NOT modified (both already cleared their review cycles).
- First release tag: **v0.0.1**, matching the hardcoded `Version: "v0.0.1"` in
  server.go.

## Repository setup

1. `gh repo create courage-zen/bigdata-mcp-email --public` (gh CLI is logged
   in as courage-zen; git operations protocol is SSH).
2. `git remote add origin git@github.com:courage-zen/bigdata-mcp-email.git`.
3. Push `main` (all commits).

Public repo → the GHCR package inherits public visibility (same as the
forwarder): anonymous `docker pull` works, k8s sidecar needs no
imagePullSecrets. Verified empirically after first push by anonymous pull.

## New file: .github/workflows/docker.yml

Forwarder pattern, adapted names:

- **Triggers:** `push: branches: [main], tags: ['v*']` + `pull_request:
  branches: [main]`.
- **Env:** `REGISTRY: ghcr.io`, `IMAGE_NAME: ${{ github.repository }}`.
- **build job** (`permissions: contents: read, packages: write`):
  checkout@v4 → setup-qemu-action@v3 → setup-buildx-action@v3 → login-action@v3
  (skipped on PR, `secrets.GITHUB_TOKEN`) → docker/metadata-action@v5 (tags:
  branch ref, tag ref, semver `{{version}}`, semver `{{major}}.{{minor}}`,
  short sha) → docker/build-push-action@v6 (platforms
  `linux/amd64,linux/arm64`, push except on PR, GHA cache).
- **release job** (`needs: build`, `if: startsWith(github.ref, 'refs/tags/v')`,
  `permissions: contents: write, packages: read`): login to GHCR → per arch
  (amd64, arm64): `docker buildx imagetools inspect` the pushed tag to extract
  the arch manifest digest → `docker pull` the digest → `docker tag` as
  `email-mcp:<tag>-<arch>` → `docker save | gzip` to
  `email-mcp-image-<tag>-linux-<arch>.tar.gz` → softprops/action-gh-release@v2
  uploads both tarballs with `generate_release_notes: true`.

## Release asset layout

One tag triggers both release.yml (binaries) and docker.yml (images). Asset
names are disjoint:

- Binaries (existing): `email-mcp-linux-<arch>.tar.gz`, `checksums.txt`
- Images (new): `email-mcp-image-<tag>-linux-<arch>.tar.gz`

softprops/action-gh-release appends assets to an existing release rather than
failing, so the two workflows arriving in either order converge to the same
Release with all assets. The two pipelines' runtimes naturally stagger (QEMU
multi-arch build is slower than the binary matrix); worst case a failed job
re-runs and appends what it missed. checksums.txt stays binary-only — image
tarballs are reproducible-enough exports, not release artifacts the README
promises.

## New file: .dockerignore

Excludes `.git/`, `docs/`, `.github/`, local `config.yaml`, and the `email-mcp`
build binary — CI build context shrinks to source + go.mod/go.sum + example
config. Nothing sensitive is in the context today (Dockerfile only COPYies
`go.mod go.sum *.go`), this is context bloat hygiene and closes deferred
ledger item w's second half.

## README addition

One subsection under Install: download the image tar.gz from Releases and
`docker load` it, then run with the same env vars as the binary quick-start;
plus the `docker pull ghcr.io/courage-zen/bigdata-mcp-email:<tag>` option for
registry users.

## Verification (acceptance)

1. Push branch → PR-triggered docker.yml build passes (Dockerfile builds on
   both arches via QEMU; nothing pushed).
2. Merge to main → main push tags (`latest`, `<short-sha>`) land on GHCR.
3. Tag `v0.0.1` → both workflows green (`gh run watch`).
4. Release page shows 5 assets: 2 binary tar.gz + checksums.txt + 2 image
   tar.gz.
5. Download an image tar.gz, `docker load`, `docker run` with env vars
   (`MCP_AUTH_KEY`, `SMTP_FROM`), `GET /health` → 200. Cleanup.
6. Anonymous `docker pull ghcr.io/courage-zen/bigdata-mcp-email:v0.0.1`
   succeeds (public visibility confirmed).

## Testing

No new Go code and no unit tests — this is CI/release plumbing. Verification is
the end-to-end pipeline run above. README changes are checked by review only
(no automated doc tests exist in this repo).

## Out of scope

- Modifying build.yml / release.yml (settled through their review cycles).
- Tag↔server.go version consistency check (deferred ledger item y stays
  deferred).
- Graceful shutdown / HTTP server timeout changes (settled in final review).
- Binary release asset changes.

## Failure modes considered

- **Release create race between the two workflows:** softprops appends to
  existing releases; converges. Re-run a failed job if one side loses the
  race in a way that errors.
- **GHCR package visibility stuck private after first push:** fix via
  `gh api` package visibility update; forwarder experience says public repo →
  public package.
- **QEMU arm64 build slow/flaky:** GHA cache mitigates; worst case re-run.
  The arm64 smoke path does not exist for the image (binary pipeline keeps
  amd64-only smoke) — accepted, matches forwarder.
