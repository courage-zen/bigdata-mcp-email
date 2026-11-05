# Image Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish the email-mcp multi-arch container image to GHCR and attach per-arch image tar.gz assets to GitHub Releases, verified end-to-end at tag `v0.0.1`.

**Architecture:** One new workflow `.github/workflows/docker.yml` (forwarder pattern: QEMU+buildx multi-arch push to ghcr.io on main pushes and `v*` tags, PRs build-only; on tags, export each arch via `docker save | gzip` and attach to the Release). Existing `build.yml`/`release.yml` are untouched — both workflows append assets to the same Release. Repo is created public and pushed via gh CLI + SSH; verification is live CI runs plus local `docker load`/`docker run` from the downloaded asset.

**Tech Stack:** GitHub Actions (docker/login-action@v3, metadata-action@v5, build-push-action@v6, setup-qemu/buildx@v3, softprops/action-gh-release@v2), GHCR, gh CLI, docker.

**Context:** Repo /Users/zhengyong/code/bigdata-mcp-email, branch `feature/image-release` (base: main @ `4512725`, plus spec commit `684bcf5`). gh is logged in as `courage-zen` (SSH protocol for git). No git remote configured yet. 48 Go tests pass; this plan adds NO Go code. Design spec: `docs/superpowers/specs/2026-11-05-image-release-design.md`. No unit tests apply to CI plumbing — each task's "test" is a validation command (YAML parse, suite green, live CI run, live container run).

**Conventions:** Every commit message ends with a `Co-Authored-By: Claude Code <noreply@anthropic.com>` trailer (blank line before it). PR bodies end with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`. Deviations from this plan get appended as numbered items to the amendment block at the end.

---

### Task 1: docker.yml + .dockerignore + README image docs

**Files:**
- Create: `.github/workflows/docker.yml`
- Create: `.dockerignore`
- Modify: `README.md` (Install section, after the "Or run in Docker:" block)

- [ ] **Step 1: Write `.dockerignore`**

```
.git
.github
docs
config.yaml
email-mcp
```

(Local `config.yaml` and the `email-mcp` build binary must never ride in the CI build context; `.git`/`docs`/`.github` are pure bloat. The Dockerfile only COPYies `go.mod go.sum *.go`, so this changes nothing about the image itself.)

- [ ] **Step 2: Write `.github/workflows/docker.yml`**

```yaml
name: Build and publish image

# Builds a multi-arch (amd64 + arm64) Linux image and pushes it to ghcr.io.
# - On push to main: tag as `latest` + `<short-sha>`.
# - On tag v*: tag as the version (e.g. v1.0.0) and `latest`, then attach the
#   image as .tar.gz files (one per arch) to the GitHub Release.
# - On PR: build only, no push (validates the Dockerfile still builds).

on:
  push:
    branches: [main]
    tags: ['v*']
  pull_request:
    branches: [main]

env:
  REGISTRY: ghcr.io
  IMAGE_NAME: ${{ github.repository }}

jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write

    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up QEMU
        uses: docker/setup-qemu-action@v3

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Log in to GHCR
        if: github.event_name != 'pull_request'
        uses: docker/login-action@v3
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Derive image tags
        id: meta
        uses: docker/metadata-action@v5
        with:
          images: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}
          tags: |
            type=ref,event=branch
            type=ref,event=tag
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=sha,prefix=,format=short

      - name: Build and push
        uses: docker/build-push-action@v6
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          push: ${{ github.event_name != 'pull_request' }}
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha
          cache-to: type=gha,mode=max

  release:
    # Only on version tags: export the just-pushed image per arch as
    # image-<arch>.tar.gz and attach them to the GitHub Release.
    needs: build
    if: startsWith(github.ref, 'refs/tags/v')
    runs-on: ubuntu-latest
    permissions:
      contents: write
      packages: read

    steps:
      - name: Log in to GHCR
        uses: docker/login-action@v3
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Export images per arch
        id: export
        run: |
          tag="${GITHUB_REF_NAME}"   # e.g. v1.0.0
          ref="${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}:${tag}"
          for arch in amd64 arm64; do
            # Pull the arch-specific manifest digest, tag it with a clear name,
            # then save so `docker load` preserves the repository:tag label.
            digest=$(docker buildx imagetools inspect "${ref}" --format "{{range .Manifest.Manifests}}{{if eq .Platform.Architecture \"${arch}\"}}{{.Digest}}{{end}}{{end}}")
            local_tag="email-mcp:${tag}-${arch}"
            docker pull "${ref}@${digest}"
            docker tag "${ref}@${digest}" "${local_tag}"
            docker save "${local_tag}" | gzip > "email-mcp-image-${tag}-linux-${arch}.tar.gz"
            echo "asset-${arch}=email-mcp-image-${tag}-linux-${arch}.tar.gz" >> "$GITHUB_OUTPUT"
          done

      - name: Attach to Release and upload assets
        uses: softprops/action-gh-release@v2
        with:
          files: |
            ${{ steps.export.outputs.asset-amd64 }}
            ${{ steps.export.outputs.asset-arm64 }}
          generate_release_notes: true
```

Notes on the three deliberate adaptations from the forwarder original: asset names carry the `-image-` infix (`email-mcp-image-<tag>-linux-<arch>.tar.gz`) because binary assets `email-mcp-linux-<arch>.tar.gz` already exist in the same Release; the local (loaded) tag is `email-mcp:<tag>-<arch>`; `IMAGE_NAME: ${{ github.repository }}` resolves to `courage-zen/bigdata-mcp-email`, already lowercase so metadata-action needs no case handling.

- [ ] **Step 3: README — add image options to Install**

After the existing "Or run in Docker:" block (the `docker build` / `docker run` snippet), append:

````markdown
Or pull the image from GHCR:

```sh
docker pull ghcr.io/courage-zen/bigdata-mcp-email:v0.0.1
```

Or download a per-arch image tar.gz from [Releases](https://github.com/courage-zen/bigdata-mcp-email/releases) and load it — no registry access needed (air-gapped path):

```sh
docker load -i email-mcp-image-v0.0.1-linux-arm64.tar.gz
docker run -p 8000:8000 -e MCP_AUTH_KEY=your-key -e SMTP_FROM=you@example.com email-mcp:v0.0.1-arm64
```
````

- [ ] **Step 4: Validate locally**

```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/docker.yml')); print('yaml ok')"
go vet ./... && go test ./... -count=1   # still 48 PASS (nothing should change, but prove it)
git status --short                        # .dockerignore, .github/, README.md
```

Expected: `yaml ok`, tests pass, exactly the three expected files modified/created.

Optionally attempt `docker build -t email-mcp-ci-check .` — if the local daemon cannot reach Docker Hub (network/proxy), that is NOT a failure of this task; the PR run in Task 2 is the real Dockerfile validation. Clean up with `docker rmi email-mcp-ci-check` if it built.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/docker.yml .dockerignore README.md
git commit -m "feat: ghcr multi-arch image pipeline with release tar.gz assets

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: Create public repo, push, open PR, PR checks green

**Files:** none (operational — gh CLI + git). No commit.

- [ ] **Step 1: Create the repo and remote**

```bash
gh repo create courage-zen/bigdata-mcp-email --public \
  --description "MCP server exposing a send-email tool to multica agents via local-smtp-forwarder"
git remote add origin git@github.com:courage-zen/bigdata-mcp-email.git
```

Expected: repo URL printed, no error. (If the name is somehow taken, stop and report — do not pick a different name unilaterally.)

- [ ] **Step 2: Push main, then the feature branch**

```bash
git push -u origin main
git push -u origin feature/image-release
```

Expected: both pushes succeed (SSH auth via gh's configured key). main lands at `4512725`-plus-spec/plan commits — whatever local main holds — and becomes the repo's default branch.

- [ ] **Step 3: Open the PR**

```bash
gh pr create --base main --head feature/image-release \
  --title "feat: image release pipeline (GHCR multi-arch + release gz assets)" \
  --body "$(cat <<'EOF'
## Summary
- New `.github/workflows/docker.yml`: multi-arch (amd64/arm64) image builds, pushed to ghcr.io on main pushes and v* tags; PRs build only
- On v* tags, exports per-arch image tar.gz (`docker save | gzip`) and attaches them to the GitHub Release alongside the existing binary assets
- `.dockerignore` trims the CI build context; README documents `docker pull` / `docker load` options

## Test Plan
- [ ] PR checks green (build.yml vet+test+build; docker.yml QEMU multi-arch build, no push)
- [ ] After merge: main-push GHCR tags (`latest`, short-sha) with both arches
- [ ] After tag v0.0.1: Release has 2 binary tar.gz + checksums.txt + 2 image tar.gz
- [ ] `docker load` + `docker run` from the image asset; `/health` returns 200
- [ ] Anonymous pull of ghcr.io/courage-zen/bigdata-mcp-email:v0.0.1

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: PR URL printed.

- [ ] **Step 4: Watch PR checks to green**

```bash
gh pr checks --watch
```

Expected: two checks, both eventually pass — `Build` (build.yml: vet, 48 tests, compile) and `Build and publish image / build` (docker.yml: QEMU amd64+arm64 build, no push). Budget 5–15 minutes; the QEMU arm64 build is the slow one. If docker.yml fails: read `gh run view --log-failed`, fix, push to the feature branch, re-watch. Common causes: YAML indentation, action version typos — all were pre-validated in Task 1, so a failure is most likely transient (re-run the job).

---

### Task 3: Merge, push main, verify GHCR tags + public visibility

**Files:** none (operational).

- [ ] **Step 1: Fast-forward merge and push**

```bash
git checkout main
git merge --ff-only feature/image-release
git push origin main
```

Expected: fast-forward succeeds, push succeeds, the PR auto-closes as merged (verify: `gh pr view --json state --jq .state` → `MERGED`). Keep the feature branch for now (deleted during branch finishing, not here).

- [ ] **Step 2: Watch the main-push workflows**

```bash
run_id=$(gh run list --workflow build.yml --limit 1 --json databaseId --jq '.[0].databaseId')
gh run watch "$run_id" --exit-status
run_id=$(gh run list --workflow docker.yml --limit 1 --json databaseId --jq '.[0].databaseId')
gh run watch "$run_id" --exit-status
```

Expected: both succeed. The docker.yml run now pushes (login step active, `latest` + `main` + short-sha tags via metadata-action).

- [ ] **Step 3: Verify multi-arch tags on GHCR**

```bash
docker buildx imagetools inspect ghcr.io/courage-zen/bigdata-mcp-email:latest
```

Expected: a manifest list with two manifests — `linux/amd64` and `linux/arm64`. If local buildx cannot reach ghcr.io (network/proxy), fall back to proving the package exists via Step 4's `gh api` call, and defer the arch proof to Task 4's asset download (which only works if both arches were exported).

- [ ] **Step 4: Verify public visibility (the spec's anonymous-pull guarantee)**

```bash
gh api /user/packages/container/bigdata-mcp-email --jq '.visibility'
```

Expected: `public`. If it prints `private` (new GHCR packages do not always inherit repo visibility): open https://github.com/users/courage-zen/packages/container/bigdata-mcp-email/settings → Danger Zone → Change visibility → Public, then re-run the command. Then prove anonymity without touching local docker credentials:

```bash
token=$(curl -s "https://ghcr.io/token?scope=repository:courage-zen/bigdata-mcp-email:pull" | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $token" \
  -H "Accept: application/vnd.oci.image.index.v1+json" \
  https://ghcr.io/v2/courage-zen/bigdata-mcp-email/manifests/latest
```

Expected: `200` (a truly anonymous token can read the manifest).

---

### Task 4: Tag v0.0.1, both release workflows, end-to-end verification

**Files:** none (operational).

- [ ] **Step 1: Push the tag**

```bash
git tag v0.0.1
git push origin v0.0.1
```

Expected: tag push triggers two workflows: `Release` (release.yml: test → binaries matrix + smoke → publish) and `Build and publish image` (docker.yml: build+push → export → attach).

- [ ] **Step 2: Watch both to green**

```bash
for wf in release.yml docker.yml; do
  run_id=$(gh run list --workflow "$wf" --limit 1 --json databaseId --jq '.[0].databaseId')
  gh run watch "$run_id" --exit-status
done
```

Expected: both succeed. Contingency — if one workflow fails on the release-create race (both call softprops on the same tag), re-run just the failed job: `gh run rerun <id> --failed`; softprops appends to the existing release, so a re-run converges.

- [ ] **Step 3: Verify Release assets**

```bash
gh release view v0.0.1 --json assets --jq '.assets[].name' | sort
```

Expected exactly five:

```
checksums.txt
email-mcp-image-v0.0.1-linux-amd64.tar.gz
email-mcp-image-v0.0.1-linux-arm64.tar.gz
email-mcp-linux-amd64.tar.gz
email-mcp-linux-arm64.tar.gz
```

- [ ] **Step 4: Load and run the image from the downloaded asset (no registry)**

```bash
arch=$(uname -m); [ "$arch" = "arm64" ] && a=arm64 || a=amd64
mkdir -p /tmp/email-mcp-imgtest
gh release download v0.0.1 --pattern "email-mcp-image-v0.0.1-linux-${a}.tar.gz" --dir /tmp/email-mcp-imgtest
docker load -i /tmp/email-mcp-imgtest/email-mcp-image-v0.0.1-linux-${a}.tar.gz
docker run -d --name email-mcp-verify -p 18000:8000 \
  -e MCP_AUTH_KEY=verify-key -e SMTP_FROM=verify@example.com email-mcp:v0.0.1-${a}
sleep 2
curl -s http://127.0.0.1:18000/health
```

Expected: `{"status":"ok"}` (proves the loaded image boots — config validation passes with env-only settings, health endpoint answers).

- [ ] **Step 5: Full tool call through the image**

```bash
curl -s -X POST http://127.0.0.1:18000/mcp \
  -H "Authorization: Bearer verify-key" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -H "X-Multica-User-Email: verify@example.com" \
  -d '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"send-email","arguments":{"subject":"image-verify","body":"hi"}},"id":1}' \
  | grep -q "verify@example.com" && echo "context echo OK"
```

Expected: `context echo OK` — auth middleware, stateless MCP handler, tool, error payload with `multica_context` all work from inside the image (the send fails at SMTP dial since no forwarder is running; the failure response still echoes the header email — that is the assertion).

- [ ] **Step 6: Cleanup and registry-tag sanity**

```bash
docker rm -f email-mcp-verify
docker rmi email-mcp:v0.0.1-${a}
rm -rf /tmp/email-mcp-imgtest
docker buildx imagetools inspect ghcr.io/courage-zen/bigdata-mcp-email:0.0.1 >/dev/null && echo "semver tag 0.0.1 present"
gh release view v0.0.1 --json url --jq .url
```

Expected: `semver tag 0.0.1 present` (metadata-action's semver tag alongside `v0.0.1`) and the release URL — which is the README's Releases link now resolving to a real page.

- [ ] **Step 7: Report**

No commit in this task. Report: workflow run URLs, the five asset names, the `/health` output, the tool-call grep result, and the anonymous-manifest status code from Task 3.

---

## Deviation / amendment block

(Record implementation deviations from this plan as numbered items, appended during execution.)

1. (none yet)
