<!-- FOR AI AGENTS - Human readability is a side effect, not a goal -->
<!-- Managed by agent: keep sections and order; edit content, not structure -->
<!-- Last updated: 2026-04-25 | Last verified: 2026-04-25 -->

# AGENTS.md

**Precedence:** the **closest `AGENTS.md`** to the files you're changing wins. Root holds global defaults only.

## Commands (unverified)
> Source: go.mod — CI-sourced commands are most reliable

<!-- AGENTS-GENERATED:START commands -->
| Task | Command | ~Time |
|------|---------|-------|
| Typecheck | go build -v ./... | ~15s |
| Lint | golangci-lint run ./... | ~10s |
| Format | gofmt -w . | ~5s |
| Test (single) | go test -v -race | ~2s |
| Test (all) | go test -v -race -short ./... | ~30s |
| Build | go build -v ./... | ~30s |
<!-- AGENTS-GENERATED:END commands -->

> If commands fail, verify against Makefile/package.json/composer.json or ask user to update.

## Workflow
1. **Before coding**: Read nearest `AGENTS.md` + check Golden Samples for the area you're touching
2. **After each change**: Run the smallest relevant check (lint → typecheck → single test)
3. **Before committing**: Run full test suite if changes affect >2 files or touch shared code
4. **Before claiming done**: Run verification and **show output as evidence** — never say "try again" or "should work now" without proof

## Golden Samples (follow these patterns)
<!-- Hand-curated. Removed AGENTS-GENERATED markers to prevent the generator from re-importing skill fixtures from .claude/skills/. -->
| For | Reference | Key patterns |
|-----|-----------|--------------|
| Provider entrypoint | `main.go` | `providerserver.Serve` boilerplate |
| Provider config + auth fallback chain | `internal/provider/provider.go` | `resolveAuth(cfg)` returns one credential method by priority |
| Plugin Framework resource | `internal/resources/foundry_agent_v2.go` | Schema with rich `MarkdownDescription`, `RequiresReplace` plan modifiers, polymorphic tools block |
| HTTP client transport | `internal/client/transport.go` | retry on 408/425/429/5xx, `Retry-After` parsing, `crypto/rand` jitter |
| Tests | `internal/client/client_test.go` | table-driven `roundTripperFunc` + injected backoff |

## Heuristics (quick decisions)
| When | Do |
|------|-----|
| Adding a new resource | Implement `resource.Resource` + `resource.ResourceWithImportState` in `internal/resources/`; add it to `Resources()` in `provider.go`; add an `examples/resources/azurefoundry_<name>/resource.tf` so tfplugindocs picks it up. |
| Adding a tool type to `azurefoundry_agent_v2` | One entry in `toolExtractors` + one entry in `toolWirers` + one wire-type struct in `internal/client/agent_v2.go`. Don't grow the existing dispatch functions. |
| Adding a Foundry API surface | Add the typed CRUD in the matching `internal/client/<resource>.go`; never grow `client.go` itself — that file owns transport only. |
| Probe URL changes | Update both the probe and the doc comment in `WaitForProjectReady`; `tools.type` validator and `kind` validator must mirror their respective enum docs. |
| Adding a dependency | Ask first; we minimize deps. |
| Unsure about pattern | Check Golden Samples above. |

## Repository Settings
<!-- AGENTS-GENERATED:START repo-settings -->
- **Default branch:** `main`
- **Merge strategy:** squash, merge, rebase
<!-- AGENTS-GENERATED:END repo-settings -->

## CI gates
- `Build & vet` — `go build ./...` and `go vet ./...` must pass.
- `golangci-lint` — config in `.golangci.yaml` (v2 syntax). Strict: 40+ linters enabled including `gocyclo`, `errorlint`, `gosec`, `perfsprint`, `prealloc`. **Zero `nolint:` directives are tolerated** — refactor or fix the underlying issue instead. Misspell ignore list is in the config.
- `Docs drift (tfplugindocs)` — `tfplugindocs generate --provider-name azurefoundry` must produce no diff against `docs/`. Edit `MarkdownDescription` on schema attributes to update prose, or `examples/resources/<name>/resource.tf` to update example HCL — never `docs/*.md` directly.
- Releases run on tag push (`v*`). goreleaser builds binaries + checksums + GPG signature; the Terraform Registry indexes within ~10 min.

## Boundaries

### Always Do
- Run pre-commit checks before committing
- Add tests for new code paths
- Use conventional commit format: `type(scope): subject`
- **Show test output as evidence before claiming work is complete** — never say "try again" or "should work now" without proof
- For upstream dependency fixes: run **full** test suite, not just affected tests
- Follow Go 1.25 conventions and idioms

### Ask First
- Adding new dependencies
- Modifying CI/CD configuration
- Changing public API signatures
- Running full e2e test suites
- Repo-wide refactoring or rewrites

### Never Do
- Commit secrets, credentials, or sensitive data
- Modify vendor/, node_modules/, or generated files
- Push directly to main/master branch
- Delete migration files or schema changes
- Commit go.sum without go.mod changes

## Contributing (for AI agents)
- **Comprehension**: Understand the problem before submitting code. Read the linked issue, understand *why* the change is needed, not just *what* to change.
- **Context**: Every PR must explain the trade-offs considered and link to the issue it addresses. Disclose AI assistance if the project requires it.
- **Continuity**: Respond to review feedback. Drive-by PRs without follow-up will be closed.

<!-- AGENTS-GENERATED:START module-boundaries -->
## Module Boundaries
> Source: go-conventions

### Internal Packages (compiler-enforced)
- `internal/resources`
- `internal/provider`
- `internal/client`

### Dependency Rules
| Source | Target | Rule | Reason |
|--------|--------|------|--------|
| * | `'ioutil.*'` | forbidden_call | - |
<!-- AGENTS-GENERATED:END module-boundaries -->

## Scoped AGENTS.md (MUST read when working in these directories)
<!-- AGENTS-GENERATED:START scope-index -->
- `./internal/AGENTS.md` — Backend services (Go)
- `./docs/AGENTS.md` — Project documentation, guides, and reference materials
- `./.github/workflows/AGENTS.md` — GitHub Actions workflows and CI/CD automation
<!-- AGENTS-GENERATED:END scope-index -->

> **Agents**: When you read or edit files in a listed directory, you **must** load its AGENTS.md first. It contains directory-specific conventions that override this root file.

## When instructions conflict
The nearest `AGENTS.md` wins. Explicit user prompts override files.
- For Go-specific patterns, defer to language idioms and standard library conventions
