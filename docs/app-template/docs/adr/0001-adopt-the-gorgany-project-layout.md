# 0001. Adopt the Gorgany project layout

- Status: accepted
- Date: 2026-09-26

## Context

We need a layout that the framework's path assumptions accept (working directory =
project root; `config/`, `resource/`, `db/migration` and `pkg/provider` in fixed places),
that `go build ./...`, `go vet ./...` and `go test ./...` can check whole, and that
separates the HTTP server from the console.

## Decision

We follow `docs/PROJECT_STRUCTURE.md` in the Gorgany repository: two binaries
(`cmd/app`, `cmd/cli`) sharing one composition root (`pkg/provider`), layered packages
under `pkg/`, an ordered migration list, and the test tiers of `docs/APP_TESTING.md`.

## Consequences

- The console never starts the job scheduler.
- Import direction, route coverage and the env sample are enforced by tests.
- New decisions that depart from the layout get an ADR of their own.
