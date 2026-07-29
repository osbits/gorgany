# Gorgany documentation

| Document | What it covers |
|----------|----------------|
| [DIALECTS.md](DIALECTS.md) | Writing a `SQLDialect`, and the table of what MySQL refuses rather than mistranslating |
| [TESTING.md](TESTING.md) | The `testsupport` database harness: isolation strategies, both engines, skip-or-fail |
| [VALIDATION.md](VALIDATION.md) | The validation error shape a client receives, the message catalog, and localisation |
| [CSRF.md](CSRF.md) | The client contract: fetch on boot, re-read the header, send on every mutating request |
| [RATE_LIMITING.md](RATE_LIMITING.md) | The token-bucket middleware, its bucket key, and the per-instance caveat |
| [SPA.md](SPA.md) | Serving a built single-page app: fallback, caching, exclusions, traversal |

Upgrading from v1.5.1? Start with [`../MIGRATION_v2.md`](../MIGRATION_v2.md), or hand
[`../MIGRATE_TO_V2_PROMPT.md`](../MIGRATE_TO_V2_PROMPT.md) to a coding agent running in
your application's repository.
