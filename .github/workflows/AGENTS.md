<!-- Hand-curated. The auto-generator emitted Node.js patterns and fabricated files (dependabot.yml, CODEOWNERS, pull_request_template.md) that don't exist here. Sections without AGENTS-GENERATED markers won't be touched on the next regen. -->

# AGENTS.md — workflows

## Overview
GitHub Actions workflows for the Terraform provider. Two workflows ship: a lint/build/test gate (`ci.yml`) and a goreleaser-driven release (`release.yml`).

## Key Files
| File | Purpose |
|------|---------|
| `ci.yml` | Triggered on push to `main` and PRs. Three jobs: `Build & vet` (`go build` + `go vet` + `go mod tidy` drift check), `golangci-lint` (pinned to v2.11.4 to match local), `Docs drift (tfplugindocs)` (regenerate docs and fail if `git diff -- docs/` is non-empty). |
| `release.yml` | Triggered on `v*` tag push (or manual `workflow_dispatch`). Imports the GPG key, runs `goreleaser release --clean`, publishes signed binaries + checksums + SBOM to GitHub Releases. The Terraform Registry indexes within ~10 min. |

## Workflow conventions for this repo
- **Action versions** — currently pinned with `@v4` / `@v5` / `@v7` / `@v8` major-version tags (not full SHAs). Acceptable for a single-maintainer provider; if the repo ever takes outside contributors at scale, consider tightening to commit SHAs.
- **Permissions** — `ci.yml` uses `contents: read` (read-only). `release.yml` uses `contents: write` (needed for `gh release create`). Never `permissions: write-all`.
- **Concurrency** — `ci.yml` cancels in-progress runs on the same ref via `concurrency.group: ci-${{ github.ref }}` + `cancel-in-progress: true`.
- **Go version** — `actions/setup-go` reads from `go.mod` via `go-version-file`; bumping `go.mod` automatically bumps the CI Go version.
- **Caching** — `actions/setup-go` handles module + build cache via `cache: true`.

## Tagging convention
- Annotated tags only: `git tag -a vX.Y.Z -m "release notes"`. The release workflow expects an annotated tag because goreleaser reads the message as the release body.
- Semver: patch for bugfixes (e.g. `v0.6.1`), minor for new features or non-breaking schema additions, major for schema-breaking changes (we haven't bumped to `v1.x` yet).
- Never force-push or delete a published tag — the Registry caches tag → checksum mappings.

## When CI fails
| Job red | Likely cause | Fix |
|---|---|---|
| `Build & vet` | Compile error or dirty `go.mod`/`go.sum` | Run `go mod tidy` locally, commit. |
| `golangci-lint` | Linter found something | Reproduce with `golangci-lint run ./...` locally. **Don't add `nolint:`** — refactor or fix. |
| `Docs drift (tfplugindocs)` | Schema changed without regenerating docs | Run `tfplugindocs generate --provider-name azurefoundry` and commit `docs/`. Edit `MarkdownDescription` in schemas (not `docs/*.md` directly). |
| `release.yml` GPG step | `GPG_PRIVATE_KEY` / `GPG_PASSPHRASE` secret missing or expired | Re-import via the GitHub repo settings; check fingerprint matches `goreleaser.yml`. |

## PR/commit checklist
- [ ] Workflow YAML lints clean (try `actionlint` locally or rely on the Build & vet job's GitHub-side validation).
- [ ] Permissions block is minimal.
- [ ] Secrets accessed via `${{ secrets.X }}` only — never inline.
- [ ] If touching `release.yml`: test in a fork first; a broken release workflow can publish a tag without artifacts and require a tag-yank.

## When stuck
- GitHub Actions docs: https://docs.github.com/en/actions
- goreleaser: https://goreleaser.com/customization/
- tfplugindocs: https://github.com/hashicorp/terraform-plugin-docs
- Check existing workflows in this directory before adding new ones.
