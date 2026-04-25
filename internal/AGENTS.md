<!-- Managed by agent: keep sections and order; edit content, not structure. Last updated: 2026-04-25 -->

# AGENTS.md — internal

<!-- AGENTS-GENERATED:START overview -->
## Overview
Backend services (Go)
<!-- AGENTS-GENERATED:END overview -->

<!-- Hand-curated. Removed AGENTS-GENERATED markers because the auto-generator imported the SPDX header line as the "Purpose" column. -->
## Key Files
| File | Purpose |
|------|---------|
| `provider/provider.go` | Provider schema + 5-tier auth fallback (`resolveAuth`); registers all resources. |
| `client/client.go` | `FoundryClient` core: constructors, request plumbing, `WaitForProjectReady` write-style probe, `APIError`. |
| `client/transport.go` | HTTP retry transport: 408/425/429/5xx + network errors, `Retry-After`, exponential backoff, `crypto/rand` jitter. |
| `client/agent.go` | Classic Assistants API CRUD + types. |
| `client/agent_v2.go` | v2 `/agents` CRUD + all 9 tool wire types + `WaitForAgentV2Ready`. |
| `client/file.go` | File CRUD shared between v1/v2 generations. |
| `client/vector_store.go` | Vector store CRUD shared between v1/v2 generations + `WaitForVectorStore`. |
| `client/memory_store_v2.go` | Memory store CRUD (preview, separate `MemoryStoreAPIVersion`). |
| `client/toolbox_v2.go` | Toolbox CRUD (preview, `Toolboxes=V1Preview`); versioned via POST `/toolboxes/{name}/versions` + PATCH default_version. |
| `client/client_test.go` | First-class testing pattern: `roundTripperFunc` + injected backoff for `WaitForProjectReady`. |
| `resources/foundry_agent.go` | `azurefoundry_agent` (classic Assistants resource). |
| `resources/foundry_agent_v2.go` | `azurefoundry_agent_v2` — biggest file; polymorphic tools dispatch via `toolExtractors` / `toolWirers` maps. |
| `resources/foundry_file.go` | `azurefoundry_file`. |
| `resources/foundry_file_v2.go` | `azurefoundry_file_v2`. Note: currently calls v1 client methods (`UploadFile`/`GetFile`/`DeleteFile`); the matching `*V2` client methods exist but are unused — known cleanup. |
| `resources/foundry_vector_store.go` | `azurefoundry_vector_store`. |
| `resources/foundry_vector_store_v2.go` | `azurefoundry_vector_store_v2`. |
| `resources/foundry_memory_store_v2.go` | `azurefoundry_memory_store_v2` (preview). |
| `resources/foundry_toolbox_v2.go` | `azurefoundry_toolbox_v2` (preview). Reuses `toolExtractors`/`toolWirers` from agent_v2 for tool variant dispatch. Versions are append-only — Update posts a new version + optionally promotes it. |

## Golden Samples (follow these patterns)
| Pattern | Reference |
|---------|-----------|
| Plugin Framework resource (full lifecycle) | `resources/foundry_agent_v2.go` — schema + Create/Read/Update/Delete + ImportState + helper extraction |
| Polymorphic dispatch over tool types | `resources/foundry_agent_v2.go` — `toolExtractors` and `toolWirers` maps + per-tool extractor/wirer functions |
| Conflict-recovery on Create | `resources/foundry_agent.go` — `isNotFound` / `isConflict` via `errors.As`, `alreadyExistsError` import-hint helper |
| Test injection for retry loops | `client/client_test.go` — `roundTripperFunc`, atomic call counter, ms-cadence backoff |

<!-- AGENTS-GENERATED:START setup -->
## Setup & environment
- Install: `go mod download`
- Go version: 1.25
- Required tools: golangci-lint, gofmt
<!-- AGENTS-GENERATED:END setup -->

<!-- AGENTS-GENERATED:START commands -->
## Build & tests
- Vet (static analysis): `go vet ./...`
- Format: `gofmt -w .`
- Lint: `golangci-lint run ./...`
- Test: `go test -v -race ./...`
- Test specific: `go test -v -race -run TestName ./...`
- Build: `go build -v ./...`
<!-- AGENTS-GENERATED:END commands -->

<!-- AGENTS-GENERATED:START code-style -->
## Code style & conventions
- Follow Go 1.25 idioms
- Use standard library over external deps when possible
- Errors: wrap with `fmt.Errorf("context: %w", err)`, lowercase no punctuation
- Naming: `camelCase` for private, `PascalCase` for exported; ID/URL/HTTP not Id/Url/Http
- Struct tags: use canonical form (json, yaml, etc.)
- Comments: complete sentences ending with period
- Package docs: first sentence summarizes purpose
- Prefer `any` over `interface{}`; use generics `[T any]` where appropriate
- Run `go fix ./...` after Go version upgrades to apply modernizers
<!-- AGENTS-GENERATED:END code-style -->

<!-- AGENTS-GENERATED:START security -->
## Security & safety
- Validate all inputs from external sources
- Use `context.Context` for cancellation and timeouts
- Avoid goroutine leaks: always ensure termination paths
- Sensitive data: never log or include in errors
- SQL: use parameterized queries only
- File paths: validate and sanitize user-provided paths
<!-- AGENTS-GENERATED:END security -->

<!-- AGENTS-GENERATED:START quality-gates -->
## Quality gates
Run these checks before completing any review:
```bash
golangci-lint run ./...   # 40+ linters, zero nolint tolerated
go vet ./...              # Static analysis
go test -race ./...       # Race detection
```
<!-- AGENTS-GENERATED:END quality-gates -->

<!-- AGENTS-GENERATED:START checklist -->
## PR/commit checklist
- [ ] Tests pass: `go test -race ./...`
- [ ] Lint clean: `golangci-lint run ./...`
- [ ] Formatted: `gofmt -w .` (golangci-lint runs `gofumpt` + `goimports` formatters too)
- [ ] No `nolint:` directives added; refactor or fix the underlying issue
- [ ] Error messages are descriptive and wrapped with `%w`
- [ ] `context.Context` passed and respected in all I/O paths
- [ ] Conventional commit format: `type(scope): subject`
<!-- AGENTS-GENERATED:END checklist -->

<!-- AGENTS-GENERATED:START examples -->
## Patterns to Follow
> **Prefer looking at real code in this repo over generic examples.**
> See **Golden Samples** section above for files that demonstrate correct patterns.

Key patterns:
- Context handling: always pass and respect `context.Context`
- Interfaces: define where used, not where implemented
<!-- AGENTS-GENERATED:END examples -->

<!-- AGENTS-GENERATED:START help -->
## When stuck
- Check Go documentation: https://pkg.go.dev
- Review existing patterns in this codebase
- Check root AGENTS.md for project-wide conventions
- Run `go doc <package>` for standard library help
<!-- AGENTS-GENERATED:END help -->
