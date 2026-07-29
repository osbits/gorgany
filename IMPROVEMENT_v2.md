# gorgany v2 — framework improvement brief

You are working in the gorgany framework repo: `github.com/osbits/gorgany`, at
`/Users/anton/Documents/projects/gorgany/framework/gorgany`. Current
`FrameworkVersion = "1.5.1"` (`constants.go:5`), HEAD `ed88d5c`.

Your job is to fix the defects listed below, ship MySQL support, and produce the
migration material that lets existing apps upgrade mechanically.

Every claim below was verified by reading this repo at that commit. Each carries a
file:line so you do not have to re-derive the evidence.

## Ground rules

1. **Breaking changes are authorized.** Bump to `v2.0.0` (or `v1.6.0` if you end up
   with no source-breaking change — justify whichever you pick in the CHANGELOG).
2. **Every breaking change must ship with a migration instruction.** This is a hard
   requirement, not a nice-to-have. See *Deliverables* — a break landed without its
   migration entry is an incomplete task.
3. **Every fix needs a test.** The repo has 14 `_test.go` files total; that is the
   root cause of most of what follows. A fix with no test does not count as done.
4. **Verify before you change.** Read each cited line first. If a claim is wrong, say
   so and skip it rather than "fixing" working code.
5. **The working tree is dirty** — `README.md`, `http/multipart_parser.go`,
   `http/query_parser.go` and their tests have uncommitted edits. Do not revert them.
   Check with the user before touching those four files.
6. Work in small, reviewable commits — one work item per commit, tests in the same
   commit as the fix.
7. Known consumers: `e2e/fixture-app` in this repo is the only v1.5.1-compatible
   sample app and must keep passing. Sibling apps (`pro`, `dog1`, `shift*`) are on
   v1.0.43, a different major — they are not upgrade targets.

## Tier 1 — Datasource and DB core

These make the framework unusable for any app with more than one database, and one of
them is the reason a downstream project is about to fork ~1,460 lines.

**T1.1 — Only one datasource can be configured, and which one is random.**
`provider/db_provider.go:26-35`:

```go
databases := viper.GetStringMap("databases")
for name, cfg := range databases {
    ...
    return name, v2.NewDataSource(conf)   // returns inside the loop
}
```

Go randomises map iteration order, so configuring two entries under `databases`
registers exactly one of them, **nondeterministically per boot**. The loop also only
understands `postgres_gorm`.

Fix: register every configured datasource, keyed by its config name. Introduce a
driver registry so a driver name maps to a constructor (`postgres_gorm`, `mysql_gorm`,
…) instead of a hard-coded branch. `AddConnection(name, ctor)` (`:46`) must keep
working — apps use it to register connections the config loop cannot express.

**T1.2 — The transient session binding is last-writer-wins.**
`provider/db_provider.go:73-83` rebinds transient `dbCore.ISession`,
`IQueryExecutor` and `IQueryBuilder` once per connection, and `Container.bind`
overwrites. A bare injected `dbCore.ISession` therefore resolves to whichever
connection happened to register last. With no `databases` key at all, the built-in
constructor returns `("", nil)` and those three closures still get registered,
capturing a nil connection — they panic if ever resolved.

Fix: bind the unnamed transients from the `default` connection only, skip registration
entirely when there is no connection to bind, and offer named resolution
(`GetDataSource(name).NewSession()` is already the correct path — make it the only one
that can succeed, or make the unnamed binding deterministic and documented).

**T1.3 — `NewDataSource` panics on a missing or mistyped config key.**
`db/sql/gorm/postgres/v2/data_source.go` does unchecked type assertions:
`config["prefer_simple_protocol"].(bool)` (`:21`), `maxOpenConnections.(int)` (`:41`),
`.(int)` again at `:45`, `:49`, `:53`, and `config["log"].(bool)` (`:57`).
`db_provider.go` asserts `conf["driver"].(string)`. Any hand-built config map — which
is exactly what a test helper builds — panics instead of returning an error.

Fix: a typed config struct with an `error` return. Keep `NewDataSource(map[string]any)`
as a thin adapter that validates the map and returns a descriptive error naming the
offending key and expected type.

**T1.4 — The DSN builder cannot express `search_path`.**
`data_source.go:67`:

```go
return fmt.Sprintf("host=%s port=%v user=%s password=%s dbname=%s sslmode=%s", …)
```

No `options=` or `search_path` hook, which makes schema-per-test isolation impossible —
consumers are forced into `CREATE DATABASE`-per-test-run plus truncate-between-tests.

Fix: pass through `search_path` and arbitrary `options`. Escape values properly.

**T1.5 — The framework's own `sessions` migration is invalid MySQL.**
`db/migration/sessions_migration.go:32,35` use `CREATE INDEX IF NOT EXISTS` (MySQL has
no `IF NOT EXISTS` on `CREATE INDEX`) and `:43` uses `DROP TABLE … CASCADE` (not MySQL
syntax). `DbProvider.Boot` adds it unconditionally, so `db:migrate up` fails outright
on MySQL before the app's own migrations run.

Fix: make the DDL dialect-aware, or express it through GORM's `Migrator` so it is
portable by construction. `auth/db_session.go` is already a plain GORM model with
`TableName() "sessions"` — only the DDL is the problem, so
`auth.session.storage: database` works on MySQL the moment this is fixed.

**T1.6 — `db:migrate`/`db:seed`/`db:diff` can only ever touch `default`.**
`command/db/migrate.go:47`, `command/db/seed.go:22`, `command/db/diff.go:46` all
hard-code `GetDataSource(core.DefaultKeyInRegistrar)`. In a two-datasource app, a
migration written for the second database silently executes against the first — a
Postgres DDL statement fired at a MySQL connection.

Fix: a `--datasource=<name>` flag defaulting to `default`, and let a migration declare
its target datasource so a mis-registered migration fails loudly instead of running
against the wrong engine.

**T1.7 — `db:migrate down` is an empty stub.** `command/db/migrate.go:98`:
`func (thiz MigrateCommand) down() {}`. It reports success and does nothing.

Fix: implement it, or make it exit non-zero with "rollback is not supported; restore
from a dump". Silently succeeding is the worst of the three options.

## Tier 2 — Query builder and dialect (this is what unlocks MySQL)

**T2.1 — The dialect is not injectable.** `db/sql/gorm/postgres/v2/builder.go:10`
declares an unexported `dialect dbCore.SQLDialect`; `:286-289` `NewBuilder()`
hard-codes `&PostgresDialect{}`; and `:293` declares

```go
type Config struct {
    Dialect dbCore.SQLDialect
}
```

which **nothing constructs or reads** — dead code that looks like the intended seam.
The only way for a consumer to reach another engine today is to fork the whole 886-line
builder.

Fix: add `NewBuilderWithDialect(dbCore.SQLDialect)` and make `Config` live. Thread the
dialect from the datasource → session → builder so `session.Query()` returns a builder
already speaking the right dialect. Move the builder out of the
`db/sql/gorm/postgres/v2` path if it is genuinely engine-agnostic once the dialect is
injected — decide and justify.

**T2.2 — Ship a MySQL datasource and dialect.** `SQLDialect`
(`db/sql/core/interfaces.go:78`) is 16 methods; `PostgresDialect`
(`db/sql/gorm/postgres/v2/dialect.go`, 478 lines) implements them.

Add `MySQLDialect` plus a MySQL datasource over `gorm.io/driver/mysql`, with a **typed**
config struct (so T1.3 cannot recur). Two things already work in your favour: the
existing dialect emits `?` placeholders, not `$n`, so placeholder style needs no
change; and session storage is GORM-based, so it ports for free once T1.5 lands.

The governing rule: **every construct MySQL cannot express returns an explicit error
rather than emitting invalid SQL**, with a test per method pinning either the exact SQL
or the error.

| Construct | MySQL | Action |
|---|---|---|
| Identifier quoting | backticks | rewrite `quoteIdentifier` (`"x"` → `` `x` ``) |
| `RETURNING` | unsupported | error; callers use `LAST_INSERT_ID()` |
| `DISTINCT ON` | unsupported | error |
| `FULL OUTER JOIN` | unsupported | error |
| `GROUPING SETS`, `CUBE` | unsupported | error |
| `ROLLUP` | `GROUP BY … WITH ROLLUP` | different syntax position |
| `ILIKE` | none | `LIKE` (case-insensitive under `utf8mb4_unicode_ci`) |
| CTEs (`WITH`) | 8.0+ | supported as-is |
| `LATERAL` | 8.0.14+ | supported as-is |
| Booleans | `TINYINT(1)` | GORM handles it |

Target MySQL **8.0+**, default charset `utf8mb4` / collation `utf8mb4_unicode_ci`, and
assume `sql_mode` includes `ONLY_FULL_GROUP_BY` (MySQL 8's default) — do not emit SQL
that relies on loose grouping. Dialect tests are pure string assertions and must run
with no server: that suite is the highest-value test in this whole brief. Aim ≥90%
statement coverage on the dialect and builder.

**T2.3 — `session.Query()` memoizes one builder per session.**
`db/sql/gorm/postgres/v2/session.go:44-49`:

```go
func (s *sessionImpl) Query() core.IQueryBuilder {
    if s.query == nil {
        s.query = NewBuilder()
    }
    return s.query
}
```

A second `Query()` call on the same session returns the same builder with accumulated
`WHERE`/`ORDER` state — a silent wrong-results bug, not a crash.

Fix: return a fresh builder on every call. **Breaking behaviour change** — any app
relying on the memoization gets a migration entry.

## Tier 3 — HTTP and security

**T3.1 — Nothing in the HTTP pipeline recovers from a panic.** `ls http/middleware/`
shows nine middlewares and no recovery. A panic drops the connection, and it means a
registered `JwtAuthError` handler can never be reached, since the auth middleware
panics rather than returning.

Fix: ship `RecoveryMiddleware`. If the recovered value is an `error`, re-dispatch it
through the existing error-handler chain (`grghttp.Catch`) so registered handlers fire;
otherwise log the stacktrace and emit the standard 500 envelope. Register it by default
as the first `/**` filter in the standard setup — an app should not have to remember.

**T3.2 — CSRF is bypassable on every mutating endpoint.** Two independent holes:

- `http/middleware/csrf_middleware.go:27` sets
  `ExemptMethods: []string{"GET","HEAD","OPTIONS","TRACE"}` — and
  `http/router/gorgany.go:136` registers *every* route under `OPTIONS` as well. So any
  mutating handler is reachable via `OPTIONS` with no token, and it runs.
- `:56` `if !authStrategy.IsRequestMadeWithStrategy(...)` skips the check entirely —
  "no cookie ⇒ no check".

Also, the rejections at `:50,65,74,83,91` emit a bare `{"error": "..."}` rather than the
framework's standard response envelope.

Fix: exempt only `{GET, HEAD, TRACE}`; answer `OPTIONS` with `204` from the middleware
itself without invoking the handler; never skip the check on the basis of an absent
session; emit the standard envelope. Add a regression test asserting
`OPTIONS /<mutating-route>` produces **no side effect**.

**T3.3 — `JwtMiddleware` panics whenever `Roles` is set.**
`http/middleware/jwt_middleware.go:17` calls `auth.NewJwtService()` outside the
container, so its `userService` is nil; `:33-43` then dereferences it for the role
check. The documented way to use the middleware is the way that crashes.

Fix: resolve the service from the container.

**T3.4 — `AuthMiddleware` panics on nil users and returns 401 where 403 is correct.**
`http/middleware/auth_middleware.go:35` calls `user.GetRole()` having checked only
`err` — and `CurrentUser` can return `(nil, nil)` on several paths. `:42-43` emits 401
for a *role* mismatch, so an authenticated user with the wrong role is told they are
unauthenticated.

Fix: nil-check the user; return **403** on a role mismatch and 401 only for a missing or
invalid session; and either document `IUserService.Get`/`GetByUsername` as
never-`(nil, nil)` or defend against it at the call site. Also: the JSON-vs-HTML
decision at `:42` keys on `Content-Type == application/json` **or**
`PathParam("namespace") == "api"` — a GET carries no Content-Type, so an app is forced
to put every route in an `api` namespace to get a JSON 401. Honour `Accept` and an
`/api/` path prefix too.

**T3.5 — `json:"-"` leaks through the API envelope.**
`model/dto_api_wrapper.go:195`:

```go
func parseJSONTag(rtField reflect.StructField) string {
    if jsonTag == "-" || jsonTag == "" {
        return rtField.Name
    }
    ...
}
```

The envelope marshaller does not use `encoding/json` — it reflects field by field
(`:155-193`). So inside `dto.ReturnObject`, `json:"-"` is **ignored and the field is
serialised under its Go name**, `omitempty` is ignored, and an anonymous embedded struct
is written under its Go type name (`{"OwnerCardDto":{…}}`) instead of being inlined. A
password hash on a DTO marked `json:"-"` goes out on the wire. Treat this as a security
fix.

Fix: honour `-`, honour `omitempty`, and inline anonymous embedded fields the way
`encoding/json` does. This changes response shapes → migration entry, and the entry must
tell app authors to re-check any DTO with an embedded struct.

**T3.6 — The session cookie hard-codes `Secure: true`.**
`auth/standard_auth_strategy.go:69` and `:185`. Plain-HTTP local development silently
fails to authenticate (notably in Safari).

Fix: drive it from config, defaulting to `true`, with the dev override documented.

**T3.7 — Upload limits are compile-time constants.**
`http/multipart_parser.go:22-26`: `maxMultipartSize = 32MB`, `maxFiles = 100`,
`maxFileSize = 10MB`. An app that needs an 11 MB upload has to read
`Request().BodyReader` by hand.

Fix: make all three configurable, keeping today's values as defaults. **This file has
uncommitted local changes — confirm with the user before editing.**

## Tier 4 — Providers, routing, ergonomics

**T4.1 — `EventProvider` cannot be registered.** `provider/event_provider.go:44` is
`func (p *EventProvider) Boot(c core.IContainer) error`, but `core.IProvider`
(`app/core/provider.go:7-10`) requires `Boot(IContainer)` with no return. So
`NewEventProvider()` does not satisfy the interface it exists to implement — the events
subsystem is unreachable. Fix the signature and add a compile-time
`var _ core.IProvider = (*EventProvider)(nil)` assertion for every shipped provider.

**T4.2 — `I18nProvider` panics on a missing locale file.**
`provider/i18n_provider.go:39` panics if `resource/i18n/<lang>.yaml` is absent for any
configured language, taking down boot. Fix: fail with a returned error or degrade to the
fallback locale with a warning.

**T4.3 — Route middleware is attached more than once.**
`http/router/gorgany.go:85-99`: `RegisterRoute` re-scans the shared middleware list on
every subsequent registration and re-attaches non-filter configs whose *method-agnostic*
pattern matches. Register `GET /x` then `PUT /x`, and a side-effecting middleware fires
twice on one request. Fix: attach once per (method, pattern); add a test asserting one
side effect per request for a same-path method pair.

**T4.4 — Silent container rebinding.** `Container.bind` overwrites, so the last
provider to register a core interface wins with no signal. That is currently the only
mechanism apps have for overriding `core.IValidator` or `core.IDataContext`, so keep it
working — but log at warn when a core interface is rebound, and document
"register your override provider last" as the supported pattern.

## Deliverables

Code, plus **all four** of the following. The migration material is a hard requirement
of this brief.

1. **`CHANGELOG.md`** — every change, grouped Added / Changed / Fixed / **Breaking**,
   with the version you picked and why.

2. **`MIGRATION_v2.md`** — one section per breaking change. Each section carries:
   - what broke and why it had to break;
   - **before / after code**, compiling, not pseudocode;
   - how an app author detects whether they are affected (a concrete `grep`, a symptom,
     or a compiler error to expect);
   - whether it is mechanical or needs judgement.

   Behaviour-only breaks — `Query()` no longer memoizing (T2.3), `json:"-"` now honoured
   (T3.5), role mismatch now 403 (T3.4), OPTIONS now CSRF-checked (T3.2) — matter *more*
   here than signature changes, because the compiler will not catch them. Call that out
   explicitly at the top of the file.

3. **`MIGRATE_TO_V2_PROMPT.md`** — a self-contained prompt an app team runs in *their*
   repo (not this one) to perform the upgrade. It must: state that the target is an app
   depending on `github.com/osbits/gorgany`; list what to grep for per breaking change;
   give the edit for each hit; name the behaviour changes no grep will find and how to
   test for them; and end with a verification block (`go build ./... && go vet ./... &&
   go test ./...` plus the app-level smoke checks worth running). Write it so it works
   with no prior conversation context — same standard as this file.

4. **`docs/DIALECTS.md`** — how to implement a third dialect against the now-injectable
   seam, and the table of what MySQL refuses and why.

Also: bump `FrameworkVersion` in `constants.go:5`, and keep `e2e/fixture-app` building
and passing throughout — if a change breaks it, fix the fixture in the same commit and
treat that diff as your own migration guide's first worked example.

## Verification

```bash
cd /Users/anton/Documents/projects/gorgany/framework/gorgany

go build ./... && go vet ./... && go test ./... -count=1

# the dialect suite must pass with no database running
go test ./db/sql/... -count=1 -v

# coverage on the new/changed DB layer
go test -coverprofile=cover.out ./db/... && go tool cover -func=cover.out | tail -1

# the only v1.5.1-compatible consumer still works
(cd e2e/fixture-app && go build ./... && go test ./... -count=1)
```

Then, against live engines (Docker):

```bash
docker run --rm -d --name gorgany-mysql -e MYSQL_ROOT_PASSWORD=test \
  -e MYSQL_DATABASE=gorgany_test -p 3307:3306 mysql:8 \
  --character-set-server=utf8mb4 --collation-server=utf8mb4_unicode_ci

docker run --rm -d --name gorgany-pg -e POSTGRES_PASSWORD=test \
  -e POSTGRES_DB=gorgany_test -p 5433:5432 postgres:16-alpine
```

and prove the Tier-1 fixes end to end:

- two datasources configured under `databases` both resolve by name, on **ten
  consecutive boots** (T1.1 was nondeterministic — one boot proves nothing);
- `NewDataSource` with a config map missing `log` returns an error naming the key
  instead of panicking (T1.3);
- a datasource configured with `search_path` connects into that schema (T1.4);
- `db:migrate up` creates `sessions` **on MySQL**, both indexes present, and a second
  run applies nothing (T1.5);
- `db:migrate up --datasource=creatio` runs against the second connection and not the
  first (T1.6).

Report at the end: what you fixed, what you found wrong in this brief, and anything you
deliberately left alone.
