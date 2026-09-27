# Gorgany documentation

## Building an application

| Document | What it covers |
|----------|----------------|
| [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md) | The recommended layout: `cmd/app` and `cmd/cli`, the composition root, what goes where, import rules, generated code, migrations, the docs an app keeps |
| [APP_TESTING.md](APP_TESTING.md) | Test tiers and build tags, route-contract tests, the integration tier, the e2e harness against the production image |
| [API_CONTRACT.md](API_CONTRACT.md) | `api/openapi.yaml` and `api/routes.txt`, the tests that keep them true, the envelope, versioning |
| [DEPLOYMENT.md](DEPLOYMENT.md) | The image, ignore files, configuration, the Makefile, the pipeline, releasing with migrations, health |
| [app-template/](app-template/) | A working application in that layout. It builds and lints clean, and its unit, integration and e2e tiers pass. To start a service from it, follow "Starting a new service from the template" in PROJECT_STRUCTURE.md |

## Framework features

| Document | What it covers |
|----------|----------------|
| [DIALECTS.md](DIALECTS.md) | Writing a `SQLDialect`, the condition rendering seam, and the tables of what MySQL and SQL Server refuse rather than mistranslate, and what SQL Server translates |
| [TESTING.md](TESTING.md) | The `testsupport` database harness: isolation strategies, both engines, skip-or-fail, the targets it refuses; running the framework's live suite, SQL Server included |
| [VALIDATION.md](VALIDATION.md) | The validation error shape a client receives, the message catalog, and localisation |
| [CSRF.md](CSRF.md) | The client contract: fetch on boot, re-read the header, send on every mutating request |
| [RATE_LIMITING.md](RATE_LIMITING.md) | The token-bucket middleware, its bucket key, and the per-instance caveat |
| [SPA.md](SPA.md) | Serving a built single-page app: fallback, caching, exclusions, traversal |

Upgrading from v1.5.1? Start with [`../MIGRATION_v2.md`](../MIGRATION_v2.md), or hand
[`../MIGRATE_TO_V2_PROMPT.md`](../MIGRATE_TO_V2_PROMPT.md) to a coding agent running in
your application's repository.
