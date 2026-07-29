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

There are also five **behaviour** changes that no compiler will catch — a memoized
query builder, `json:"-"`, a role-mismatch status code, `OPTIONS` routing, and
connection-pool limits that start being enforced. Those are the ones most likely to
surprise, and they are why [`MIGRATION_v2.md`](MIGRATION_v2.md) leads with them.

`e2e/fixture-app`, the only v1.5.1-compatible sample app in this repo, needed
**no source changes at all**, and its full dockerised suite (7 tests over a real
Postgres, exercising routing, session auth, JWT, relations, validation, multipart
and the CLI) passes against v2. That is a useful signal about blast radius, not a
claim that no app is affected: an app that calls `builder.ToSQL()` directly,
implements `SQLDialect`, or relies on any of the five behaviour changes will need
work.

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
- **Connection-pool limits under `properties` now actually apply.** Viper
  lowercases config keys, so the camelCase lookups
  (`props["maxOpenConnections"]`) never matched and all four settings were
  silently ignored on every version up to v1.5.1. **Behaviour change, no compiler
  error** — and the only one here that can slow a working app rather than break a
  build, if the configured cap is a stale guess. See MIGRATION_v2.md §4a.
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
- **`core.IJob` reshaped: `Schedule() JobSchedule` and `Run(ctx) error`** replace
  `GetJob()`/`GetInterval()`/`GetUnit()`, and `gocron` is gone from the module
  entirely. Scheduled jobs never ran on any prior version — `JobProvider.Boot`
  called `c.Make(&job.Scheduler{})`, which field-injected a zero value instead of
  resolving the registered one, so the scheduler that was started was always
  empty. Compile break; see MIGRATION_v2.md §13.
- **`err.ValidationError` carries `Rule`, `Param` and `Path`, and `Field`/`Err`
  changed meaning.** `Field` is now the wire name (the `json`/`scheme` tag) rather
  than the Go field name, and `Err` is a readable message rather than
  go-playground's raw `Key: 'Dto.Email' Error:Field validation for 'Email' failed
  on the 'email' tag`. The new keys are `omitempty`, so the JSON shape is additive.
  **Behaviour change, no compiler error** — a client matching on the old field name
  or parsing the old message will notice. See MIGRATION_v2.md §14.
- **`Container.Make` errors when handed a pointer-to-struct whose type has a
  registered binding holding a different instance.** That call silently
  field-injected a zero value where the caller expected the registered singleton,
  which is how the job scheduler came to tick empty. Use `Resolve` instead. See
  MIGRATION_v2.md §15.
- **A malformed request body is `400` with `err.InputBodyParseError`, not a `301`
  redirect.** `err.NewInputBodyParseError` had zero call sites while
  `http/error.go` registered a handler for it, so every parse failure arrived as an
  *empty* `ValidationErrors` and `processValidationErrors` redirected to the
  `Referer`. **Behaviour change, no compiler error.**
- **MySQL `ON CONFLICT … DO UPDATE` is refused by default.** It translated to `ON
  DUPLICATE KEY UPDATE`, silently dropping the conflict-target columns — valid but
  wrong SQL, which is the failure mode the dialect rule exists to prevent. Opt in
  with `MySQLDialect{AllowUnfaithfulUpsert: true}`. `DO NOTHING` is unaffected.
  See MIGRATION_v2.md §16.
- **`NewCorsMiddleware` refuses `AllowCredentials` together with a wildcard
  origin.** Browsers reject that pair, so it was a silent failure with no
  diagnostic. Use `NewCorsMiddlewareChecked` for an error instead of a panic. Note
  that an *empty* `AllowedOrigins` with no `AllowOriginFunc` also means "all
  origins". See MIGRATION_v2.md §17.
- **`core.MongoDb` removed.** It was a `DbType` constant with no driver behind it,
  so `driver: mongo` failed at boot while the exported constant advertised support.
- **`X-CSRF-Token` is now set on every session-carrying response**, and
  `GET /csrf` is registered by default. **Behaviour change, no compiler error.**
  Call `RouteProvider.DisableCsrfController()` to opt out. See MIGRATION_v2.md §18.
- **404, 405 and the framework's error handlers are content-negotiated.** An API
  client gets the standard envelope where it previously got an empty body or
  `text/plain`. `405` is answered at all now — chi's bare default was in place.
  **Behaviour change, no compiler error.**
- **A DTO whose `ContentType()` has no body parser now panics at route
  registration** rather than serving 500s. Compile-clean, boot-time break.

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
- **`testsupport`**: a database test harness — throwaway-per-run connection,
  migrate once per package, truncate-between-tests or transaction-rollback-per-test,
  Postgres and MySQL, skip-or-fail when no engine is reachable. See
  [`docs/TESTING.md`](docs/TESTING.md).
- **`middleware.RateLimitMiddleware`**: an in-memory token bucket keyed by IP plus
  route, with a `RateLimitStore` seam for a distributed backend. Nothing in the
  framework rate-limited anything before. See
  [`docs/RATE_LIMITING.md`](docs/RATE_LIMITING.md).
- **`controller.SpaController`**: static files with an `index.html` fallback, so
  client-side routing works. `no-store` on the entry document, immutable caching on
  hashed assets, API prefixes excluded from the fallback, and a path-traversal
  guard. See [`docs/SPA.md`](docs/SPA.md).
- **`controller.CsrfController`** at `GET /csrf`, registered by default, backed by
  `CsrfService.GetCSRFToken` — which existed with zero call sites. See
  [`docs/CSRF.md`](docs/CSRF.md).
- **`job.Scheduler`**: the framework's own interval scheduler, replacing `gocron`.
  `Add`, `Start(ctx)`, `Stop`, `Jobs`, `Stats`, with per-job overlap protection and
  panic recovery.
- **A per-rule validation message catalog**, localised through `i18n` under
  `validation.<rule>` with English defaults and a rule-naming catch-all.
  `validator.DefaultMessages()` exposes what an app is overriding. See
  [`docs/VALIDATION.md`](docs/VALIDATION.md).
- **`core.ILocalizedValidator`** (`ValidateStructForLocale`), an optional interface
  the HTTP input resolver uses to render messages in the request's locale.
- **`model.ParseStructTag` / `model.WireFieldName`**: the shared `json`/`scheme`
  tag handling, so the response marshaller and the validator cannot drift.
- **`i18n.Interpolate`** and **`i18n.HasManager`**.
- **`http.WantsJSON`**: the content-negotiation decision, exported so the auth
  middleware, the router's 404/405 and the error handlers cannot answer
  differently for the same request.
- **`auth.RoleFromClaims`** and a `role` claim on generated JWTs — informational
  only; the user-service lookup remains the authority so a role change takes
  effect immediately.
- **`dbCore.LastInsertIDExecutor`** and `dbCore.SupportsReturning`, so `orm.Create`
  reads a generated key back on an engine without `RETURNING`.
- **`config.ResolveEnvPlaceholders`**, and `${VAR}` substitution that distinguishes
  an unset variable from an empty one. An unresolved security-relevant key
  (`auth.jwt.secret`, `auth.session.cookie.secure`) now stops the boot.
- **`core.MethodNotAllowedHttpStatus`**, `core.CSRFSessionKey`,
  `core.DefaultCSRFTokenPath`.
- **`RouteProvider.DisableCsrfController()`**.
- **A build-failing guard test** (`db/sql/builder/no_hardcoded_dialect_test.go`)
  against any new hard-coded-Postgres builder outside the dialect packages.
- Test coverage went from 14 `_test.go` files to 47. Measured with no database
  running: `db/sql/builder` 96.6%, MySQL dialect 95.3%, Postgres dialect 96.6%.
  A `livedb`-tagged suite additionally verifies the Tier-1 fixes against real
  MySQL 8 and Postgres 16.

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
- **Config keys are matched case-insensitively.** Viper lowercases every key it
  reads, so camelCase lookups such as `props["maxOpenConnections"]` never matched
  a config-file value. Two consequences, both fixed: the pool settings were
  silently ignored (see Breaking), and MySQL DSN options written in camelCase —
  `readTimeout`, `parseTime`, `multiStatements` — arrived lowercased at a driver
  whose parameter names are case-sensitive, which would have made them unusable
  from YAML. The MySQL DSN builder restores the canonical spelling.
- **The `sessions` migration could not run on MySQL.**
  `CREATE INDEX IF NOT EXISTS` has no MySQL equivalent, and `Up()` passed three
  `;`-separated statements to one `db.Exec`, which `go-sql-driver/mysql` rejects
  unless `multiStatements=true`. `DbProvider.Boot` adds this migration
  unconditionally, so `db:migrate up` failed before an app's own migrations ran.
  It now goes through GORM's `Migrator`.
- **`db:migrate`, `db:seed`, `db:diff` could only touch `default`**, so a
  migration written for a second database executed against the first.
- **`db:migrate down` was an empty stub** that reported success and did nothing.
- **The ORM ignored the session's dialect entirely.** Thirteen sites called
  `v2.NewBuilder()`, which hard-codes Postgres, so every ORM query against a MySQL
  datasource emitted Postgres SQL — double-quoted identifiers, `$1` placeholders, a
  `RETURNING` clause. The MySQL driver shipped in v2 with a green test suite while
  being unable to insert a row, because every dialect test asserts strings and no
  test drove the ORM against a real engine. All thirteen now route through
  `o.db.Query()`, and a build-failing guard test rejects any new one.
- **`orm.Create` required `RETURNING`.** On an engine without it, the generated key
  was never read back, so a created entity came away with a zero id. It now uses
  `sql.Result.LastInsertId()` through GORM's `ConnPool` — which avoids the
  connection-affinity race a separate `LAST_INSERT_ID()` query would have — and
  refuses, rather than guessing, on a table with more than one auto-increment key.
- **`${VAR}` substitution wiped its siblings.** Writing each resolved key with
  `viper.Set` puts it in the *override* layer, and a map fetch resolves against the
  highest layer holding the key without deep-merging the ones below — so after
  substituting `databases.default.host`, `GetStringMap("databases")` returned only
  that key and `driver`, `log` and `properties` were gone. The app died at boot with
  "datasource config: 'driver' is required". Substitutions now go through
  `MergeConfigMap`, which deep-merges into the config layer.

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
- **There was no way for a client to obtain a CSRF token.** `X-CSRF-Token` was set
  on exactly one response per session — the one that created it — so a reload, a
  second tab, or a pre-existing session left a client permanently unable to make a
  mutating request. Closing the two bypasses above made that reliably fatal.
- **`CsrfService.ValidateCSRFToken` used `strings.Compare`** under a comment
  claiming constant-time comparison. It short-circuits on the first differing byte,
  so the timing leaked the length of the matching prefix.
- **`csrfTokenKey` was declared twice under the same literal**, once in `auth` and
  once in `http/middleware`. Renaming either would have left the middleware
  comparing against a key nothing wrote.
- **Eight unchecked type assertions on request paths.** Two in `auth/jwt_service.go`
  — anything able to produce a valid signature chooses the claim set, so a token
  with `username` absent or of the wrong JSON type panicked inside `GetUser` on
  every role-guarded route. Three in `decoder/query.go`, reachable straight from the
  query string: `?items[0][name]=a&items[0][name]=b` panicked the handler, and
  `?items=x&items[0][name]=y` panicked depending on which key Go's randomised map
  iteration yielded first. One of them had carried a `// todo: Test it` since it was
  written.
- **A DTO embedding two self-marshalling types panicked in the response
  marshaller**, because `buildBodyElement` returns a `json.RawMessage` where a map
  was asserted.
- **The JSON parser's error classification was entirely dead code.**
  `errors.Is(err, &json.SyntaxError{})` compares with `==` for a type implementing
  no `Is` method, and both operands were freshly allocated pointers, so no case
  could ever match. Every malformed body fell to the default branch and produced an
  empty `ValidationErrors`.
- **Two guaranteed panics in the input resolver**: an unresolvable body parser was
  warned about and then dereferenced on the next line, and a non-primitive
  non-`HttpCommand` handler parameter reached `reflect.Call` with too few arguments.
  Both are now boot-time errors at route registration.
- **`getOverriddenFields` had five silent faults**, four of them verified by running
  the old code: an accumulating namespace made the exclusion nondeterministic across
  embedded structs; embedded pointers were keyed by `reflect.Type.Name()`, which is
  `""` for a pointer; `field.Addr()` panicked for a struct passed by value, so
  `ValidateStruct(SomeDto{})` went down outright; and a nil embedded pointer
  panicked on `reflect.Value.Type`. The exclusions also never worked at all — the
  namespaces were built from wire names while `StructExcept` matches Go names.
- **`ValidateStruct(nil)` panicked** inside `util.IndirectType`.
- **Two fields of one struct sharing a wire name is now an error.** The body parser
  can bind only one of them, so the other silently stayed zero and validation
  reported a name matching neither.

#### Jobs

- **Scheduled jobs now run.** `JobProvider.Boot` called `c.Make(&job.Scheduler{})`,
  which field-injected a zero value rather than resolving the registered scheduler,
  so every job was registered against one object and a different, empty one was
  started. Nothing in the repo noticed, on any version.
- The scheduler dispatches each tick on its own goroutine. A blocking job used to
  stop the ticker being read, and `time.Ticker` buffers exactly one tick — so
  `AllowOverlap` could never take effect and a skipped run was never recorded.
- A job that panics no longer takes the scheduler down with it.
- Job dependencies are injected: a pointer job goes through `Make`, and a *value*
  job carrying `container:"inject"` tags is a loud error rather than a silently
  unfilled struct.

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

### Corrections to the briefs

Five claims across `IMPROVEMENT_v2.md` and `IMPROVEMENT_v2.1.md` did not survive
verification:

1. **`DROP TABLE ... CASCADE` is not invalid MySQL.** MySQL 8 documents
   `RESTRICT` and `CASCADE` on `DROP TABLE` as accepted no-ops "to make porting
   easier", so `sessions_migration.go`'s `Down()` already worked. The fatal
   construct was `CREATE INDEX IF NOT EXISTS` alone — plus the multi-statement
   `Exec` the brief did not mention.
2. **Ground rule 5 was stale.** The working tree was clean at `HEAD`; the edits to
   `README.md`, `http/multipart_parser.go`, `http/query_parser.go` and their tests
   had already landed in `f3557d0`, so there was nothing to preserve or confirm.
3. **A5's prescribed fix does not work as stated.** "If the variable is absent,
   leave the key alone so defaults and `IsSet` behave" cannot work: a key written
   in `config.yaml` lives in viper's *config* layer, which outranks `SetDefault`.
   `IsSet` stays true and `GetString` returns the literal `${VAR}` no matter what
   the parser does — verified with a probe, not assumed. Leaving it there would
   have left the cookie guard failing open, so the actual safety net is the
   hardened `SessionCookieSecure()`, and an unresolved security-relevant key stops
   the boot outright.
4. **B1's count of 42 is wrong.** The brief's own grep yields 11 at that HEAD, of
   which 3 are comments v2 wrote describing the code it had replaced — leaving 8
   real sites, exactly the set the brief's own table lists. The table was right and
   the total was not. The 42 corresponds to a broader all-type-assertions pattern
   (46 at that HEAD, 39 after).
5. **A2's step 3 was not deferred.** `gocron` was *removed* rather than swapped,
   so there is no third-party scheduler to replace. Cron expressions are
   deliberately not supported: they would need a parser dependency, and shipping a
   non-functional `Cron` field would repeat exactly the `core.MongoDb` problem the
   same brief asked to fix.

---

## [1.5.1] and earlier

See git history.
