# Changelog

All notable changes to the gorgany framework.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [2.0.0] — 2026-07-29

### Why 2.0.0 and not 1.6.0

This release contains source-breaking changes, so a major bump is required:

- `dbCore.IQueryBuilder.ToSQL()` returns `(string, []any, error)` instead of
  `(string, []any)`, and the interface gained a `Dialect()` method.
- Every `dbCore.SQLDialect` method returns an `error`; `FormatGroupBy` takes the
  whole `*GroupByClause`; and the interface gained `Name()` and
  `QuoteIdentifier()`.
- `v2.NewDataSource(map[string]any)` returns `(IDataSource, error)` instead of
  panicking.
- `provider.EventProvider.Boot` no longer returns an `error` (it previously did
  not satisfy `core.IProvider` at all).

There are also four **behaviour** changes that no compiler will catch — a
memoized query builder, `json:"-"`, a role-mismatch status code, and `OPTIONS`
routing. Those are the ones most likely to surprise, and they are why
[`MIGRATION_v2.md`](MIGRATION_v2.md) leads with them.

`e2e/fixture-app`, the only v1.5.1-compatible sample app in this repo, needed
**no changes at all** — it builds and passes untouched. That is a useful signal
about blast radius, not a claim that no app is affected: an app that calls
`builder.ToSQL()` directly, implements `SQLDialect`, or relies on any of the four
behaviour changes will need work.

### Breaking

- **`IQueryBuilder.ToSQL()` now returns an error.** This is what allows a dialect
  to refuse a construct its engine cannot express instead of emitting SQL the
  server will reject. Mechanical to adopt.
- **`SQLDialect` reshaped.** All methods return `(string, []any, error)`;
  `FormatGroupBy(*GroupByClause)` replaces `FormatGroupBy([]string)` so ROLLUP,
  CUBE and GROUPING SETS can be rendered per engine; `Name()` and
  `QuoteIdentifier()` are new. Nothing outside the framework could have
  implemented this interface before, because there was no way to inject a dialect.
- **`session.Query()` and `transaction.Query()` return a fresh builder on every
  call.** Previously one builder was memoized per session and handed back
  repeatedly, so a second `Query()` arrived carrying the first query's `WHERE`
  and `ORDER BY`. **Behaviour change, no compiler error.**
- **`json:"-"` is now honoured inside `dto.ReturnObject` bodies**, along with
  `omitempty` and `encoding/json`-style inlining of anonymous embedded structs.
  Response shapes change for any DTO using those. **Behaviour change, no compiler
  error.**
- **A role mismatch in `AuthMiddleware` returns 403, not 401.** **Behaviour
  change, no compiler error.**
- **`OPTIONS` no longer invokes the route handler.** The router registers a `204`
  preflight responder with an `Allow` header instead of the route's own handler,
  and `CSRFMiddleware` answers `OPTIONS` itself. **Behaviour change, no compiler
  error.**
- **`v2.NewDataSource` returns `(IDataSource, error)`** rather than panicking on a
  missing or mistyped config key.
- **`EventProvider.Boot(core.IContainer)`** no longer returns an error, so
  `*EventProvider` finally satisfies `core.IProvider`.
- **`RecoveryMiddleware` is registered automatically** as the first `/**` filter
  by `RouteProvider`. An app that already installs its own recovery filter should
  call `RouteProvider.DisableRecoveryMiddleware()`.
- **Unnamed transient `dbCore.ISession` / `IQueryExecutor` / `IQueryBuilder`
  bindings resolve against the `default` connection only**, and are not registered
  at all when there is no `default`. Previously they resolved to whichever
  connection registered last, nondeterministically.
- **`db:migrate`, `db:seed` and `db:diff` respect `--datasource`.** A migration
  that declares a target datasource is skipped when a different one is selected.
- **Route-scoped middleware is no longer published into the shared
  `webCtx` middleware list.** It now attaches to its own route only.

### Added

- **MySQL support** (`db/sql/gorm/mysql/v2`): `MySQLDialect` and a MySQL
  datasource over `gorm.io/driver/mysql`, targeting MySQL 8.0+ with `utf8mb4` /
  `utf8mb4_unicode_ci` and `ONLY_FULL_GROUP_BY`-safe SQL. Every construct MySQL
  cannot express returns an explicit `dbCore.UnsupportedError` — see
  [`docs/DIALECTS.md`](docs/DIALECTS.md) for the full table.
- **An injectable dialect seam.** `builder.New(dialect)`,
  `builder.NewFromConfig(Config)` and `v2.NewBuilderWithDialect(dialect)`. The
  dialect is threaded datasource → session → builder, so `session.Query()` returns
  a builder already speaking the right dialect.
- **`db/sql/builder`**: the engine-agnostic query builder, moved out of
  `db/sql/gorm/postgres/v2`. `v2.Builder` and `v2.Config` are type *aliases*, so
  existing imports and `*v2.Builder` type assertions keep working.
- **`db/sql/driver`**: a driver registry mapping a config driver name to a
  constructor, plus `db/sql/driver/builtin` which registers `postgres_gorm` and
  `mysql_gorm`. An unknown driver name is now a boot error listing the names that
  do exist.
- **`db/sql/config`**: a typed `DataSource` config struct with `Validate()`, and a
  strict `Parse(map[string]any)` that names the offending key and expected type.
- **`dbCore.UnsupportedError`** and `dbCore.IsUnsupported(err)`.
- **`search_path` and arbitrary `options`** in the Postgres DSN, which is what
  makes schema-per-test isolation possible.
- **`RecoveryMiddleware`** (`http/middleware/recovery_middleware.go`). A recovered
  `error` is re-dispatched through the registered error handlers, so a registered
  `JwtAuthError` handler finally fires.
- **`db:migrate down`**, with `--steps=<n>` (default 1).
- **`--datasource=<name>`** on `db:migrate`, `db:seed` and `db:diff`.
- **`db.DatasourceScoped`**: an optional interface a migration or seeder
  implements to declare its target datasource.
- **`DbProvider.AddConnectionE`** for connection constructors that can fail.
- **Config keys**: `auth.session.cookie.secure` (default `true`),
  `http.upload.maxMultipartSize`, `http.upload.maxFiles`,
  `http.upload.maxFileSize`.
- **`provider/provider_assertions.go`**: a compile-time
  `var _ core.IProvider = (*X)(nil)` for every provider the framework ships.
- **`dbCore.SortedKeys`** for deterministic map-driven SQL generation.
- **`i18n.Manager.FallbackTag`** and locale fallback in `GetConfig`.
- **`core.GormMySQL`** (`"mysql_gorm"`) alongside `core.GormPostgreSQL`.
- Test coverage went from 14 `_test.go` files to 30. Measured with no database
  running: `db/sql/builder` 96.6%, MySQL dialect 95.3%, Postgres dialect 96.6%.

### Fixed

#### Datasource and DB core

- **Only one datasource could be configured, and which one was random**
  (`provider/db_provider.go`). The constructor returned from inside its loop over
  the `databases` map, so two configured databases registered exactly one — and
  Go randomises map iteration order, so which one changed on every boot. Every
  configured datasource is now registered, in sorted-name order.
- **The transient session binding was last-writer-wins.** With no `databases` key
  at all, the constructor returned `("", nil)` and the three transient closures
  were registered anyway over a nil connection, panicking if ever resolved.
- **`NewDataSource` panicked on a missing or mistyped config key** — unchecked
  type assertions on `prefer_simple_protocol`, `maxOpenConnections`, `log` and
  others.
- **The DSN was built by raw string interpolation**, so a password containing a
  space silently truncated it and one containing a quote corrupted it. Values are
  now escaped per libpq rules.
- **The `sessions` migration could not run on MySQL.**
  `CREATE INDEX IF NOT EXISTS` has no MySQL equivalent, and `Up()` passed three
  `;`-separated statements to one `db.Exec`, which `go-sql-driver/mysql` rejects
  unless `multiStatements=true`. `DbProvider.Boot` adds this migration
  unconditionally, so `db:migrate up` failed before an app's own migrations ran.
  It now goes through GORM's `Migrator`.
- **`db:migrate`, `db:seed`, `db:diff` could only touch `default`**, so a
  migration written for a second database executed against the first.
- **`db:migrate down` was an empty stub** that reported success and did nothing.

#### Query builder and dialect

- **`Builder.Insert` mutated the receiver** and returned it, the single hole in
  the builder's copy-on-write contract: an `INSERT` on a shared builder
  contaminated every query built from it afterwards.
- **`Rollup()`, `Cube()` and `GroupingSets()` were silently dropped.** The dialect
  only ever read `GroupByClause.Fields`, so with no plain `GroupBy()` fields it
  emitted a bare `GROUP BY ` — a syntax error.
- **`Builder.Subquery()` in the `FROM` position emitted `FROM  AS alias`.**
  `formatSelect` passed only `From.Table` and `From.Alias` to `FormatFrom` and
  never looked at `From.IsSubquery`, so the subquery was dropped.
- **`ORM[T].Count()` emitted `SELECT COUNT(*)` with no `FROM` clause**, because it
  applied its `FROM` with the result discarded.
- **`UPDATE ... SET` and `ON CONFLICT DO UPDATE SET` column order was
  nondeterministic**, being generated by ranging a Go map. Results were never
  wrong, but the SQL was untestable and no statement cache keyed on query text
  could hit.

#### HTTP and security

- **Nothing in the HTTP pipeline recovered from a panic.** A panic dropped the
  connection, and a registered error handler could never fire for a middleware
  that reports failure by panicking — which `JwtMiddleware` does, by design.
- **CSRF was bypassable on every mutating endpoint, two independent ways.**
  `OPTIONS` was exempt while the router registered every route under `OPTIONS`
  with its own handler, so `OPTIONS /widgets/1` ran the `DELETE` handler; and the
  check was skipped entirely when no session cookie was present, which is exactly
  the shape a cross-site forgery has.
- **CSRF rejections emitted a bare `{"error": "..."}`** instead of the standard
  envelope, and compared tokens with `!=` rather than `crypto/subtle`.
- **`JwtMiddleware` panicked whenever `Roles` was set**, because it constructed
  its `JwtService` outside the container and the service's own `userService` field
  was never filled. The documented way to use the middleware was the way that
  crashed.
- **`AuthMiddleware` dereferenced a nil user.** `CurrentUser` can return
  `(nil, nil)` and only `err` was checked.
- **`AuthMiddleware`'s JSON-vs-HTML decision keyed only on `Content-Type` or an
  `api` namespace path param.** A GET carries no `Content-Type`, so apps were
  pushed into an `api` namespace purely to get a JSON 401. `Accept` and an
  `/api/` path prefix are now honoured.
- **`json:"-"` leaked through the API envelope** — see Breaking.
- **The session cookie hard-coded `Secure: true`**, so plain-HTTP local
  development silently failed to authenticate.
- **Upload limits were compile-time constants**, and the total-form-size
  accumulator summed `int(file.Size)` in an `int`, which on a 32-bit build could
  wrap a large upload to a small positive total and pass the check.
- **`ChiRouterAdapter.Init` built the `ResponseWriterWrapper` with four unchecked
  type assertions** (`http.Flusher`, `http.Hijacker`, `io.ReaderFrom`,
  `io.StringWriter`). Any `ResponseWriter` not implementing all four panicked —
  including `httptest.ResponseRecorder` and any writer wrapped by an upstream
  middleware, so a compression or metrics middleware in front of the router took
  every request down.

#### Providers, routing, ergonomics

- **`EventProvider` could not be registered at all**, so the whole events
  subsystem was unreachable — its `Boot` signature did not satisfy
  `core.IProvider`.
- **`I18nProvider` panicked on a missing locale file**, taking down boot over one
  absent translation.
- **`i18n.Manager.GetConfig` returned nil for an unconfigured locale**, and
  `Translation` dereferenced it immediately.
- **Route middleware was attached more than once.** Route-scoped configs were
  published into the shared list keyed by pattern, so registering `GET /x` then
  `PUT /x` fired a side-effecting middleware twice on one request — and leaked
  GET's middleware onto PUT.
- **`Container.bind` overwrote silently.** Rebinding still works, but replacing a
  core interface now warns.

### Corrections to the v2 brief

Two claims in `IMPROVEMENT_v2.md` did not survive verification:

1. **`DROP TABLE ... CASCADE` is not invalid MySQL.** MySQL 8 documents
   `RESTRICT` and `CASCADE` on `DROP TABLE` as accepted no-ops "to make porting
   easier", so `sessions_migration.go`'s `Down()` already worked. The fatal
   construct was `CREATE INDEX IF NOT EXISTS` alone — plus the multi-statement
   `Exec` the brief did not mention.
2. **Ground rule 5 was stale.** The working tree was clean at `HEAD`; the edits to
   `README.md`, `http/multipart_parser.go`, `http/query_parser.go` and their tests
   had already landed in `f3557d0`, so there was nothing to preserve or confirm.

---

## [1.5.1] and earlier

See git history.
