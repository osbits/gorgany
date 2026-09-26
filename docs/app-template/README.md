# myapp

One paragraph: what this service does, who calls it, and what it owns.

## Quickstart

```bash
cp .env.sample .env
make dev-up        # postgres on 127.0.0.1:$DB_PORT (5432 unless .env says otherwise)
make migrate
make seed
make run           # http://localhost:8080/healthz
```

## Commands

`make help` lists every target. The ones you need daily:

| Target | What it does |
|--------|--------------|
| `make test` | Unit tier, with the race detector. No database needed |
| `make lint` | golangci-lint, at the version CI uses |
| `make test-integration` | Integration tier against a fresh Postgres on `:5433` |
| `make e2e` | Builds the production image and runs the black-box suite against it |
| `make verify` | Formatting, `go vet` (including tagged tests) and module tidiness |
| `make cli ARGS="db:migrate up"` | Any console command |

## Layout

This repository follows the Gorgany [project structure](https://github.com/osbits/gorgany/blob/HEAD/docs/PROJECT_STRUCTURE.md).
The short version:

| Path | Holds |
|------|-------|
| `cmd/app`, `cmd/cli` | The two binaries: HTTP server and console. No logic |
| `pkg/provider` | Wiring: which providers each binary boots, and in what order |
| `pkg/…` | Application code, layered `domain` ← `service` ← `controller` |
| `db/migration`, `db/seeder` | Schema history (`All()` is the ordered list) and reference data |
| `api/` | `openapi.yaml` (the HTTP contract) and `routes.txt` (every route, checked by a test) |
| `test/arch`, `test/e2e` | Repository invariants; the black-box suite |
| `docs/` | Architecture, configuration, runbooks, decisions |

## Documentation

Start at [docs/README.md](docs/README.md). Security reports: [SECURITY.md](SECURITY.md).
