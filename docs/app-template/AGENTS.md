# AGENTS.md

Rules for anyone changing this repository, human or agent. Facts that live in
`go.mod`, `config/config.yml` or the Makefile are not repeated here: read them there.

## Before you finish

- `make verify test` passes. If you touched SQL, `make test-integration` passes too.
- A route change updates `api/openapi.yaml` and `api/routes.txt`
  (`go test ./pkg/provider -run TestRouteInventory -update`, then review the diff).
- A new `${VAR}` in `config/config.yml` is added to `.env.sample` and `docs/configuration.md`.
- A release-relevant change has a line in `CHANGELOG.md`, with deployment notes if an
  operator has to do anything.

## Where things go

- New binaries: none. `cmd/app` and `cmd/cli` are the only two; a new task is a console
  command in `pkg/command`, registered in `pkg/provider/command_provider.go`.
- Wiring and configuration reads: `pkg/provider` only (`health.Probe` reads `SERVER_PORT`,
  and nothing else may).
- A migration: a new file in `db/migration`, appended to `All()` in `registry.go`. Never
  edit a released migration or change its `Name()`. Migrations import nothing from `pkg/`.
- Import rules are enforced by `test/arch/layering_test.go`, whose `allowed` table is
  the whole policy. Do not add lines to `test/arch/known_violations.txt`; fix the import.
- A mutating API route is behind the CSRF filter and the same-origin check. A route that
  must accept bearer tokens instead uses the `api` strategy under its own pattern.

## Never

- Edit anything under a `generated/` directory, or add a `Code generated … DO NOT EDIT.`
  header to a hand-written file.
- Commit `.env`, key material or real personal data (seeders and fixtures are synthetic).
- Put authorisation in request DTOs. It belongs in route middleware or services.
- Add a test-only backdoor (auth strategy, flag, route) to the production binaries.
