# Migrating an app from gorgany v1.5.1 to v2.0.0

This document has one section per breaking change. Each says what broke and why,
gives compiling before/after code, tells you how to detect whether you are
affected, and states whether the fix is mechanical or needs judgement.

> **Read “Behaviour changes the compiler will not catch” first.**
>
> Four of these changes compile cleanly and change what your app *does*. They
> matter more than the signature changes, because `go build` will not point at
> them and a passing test suite may not either. Two of them (`json:"-"`,
> `OPTIONS`) are security-relevant, and one (`Query()` memoization) can change
> query results silently.

**Order of work.** Do §1–§4 first — they need judgement and testing. Then run
`go build ./...` and let the compiler drive §5–§10, which are mechanical.

---

## Behaviour changes the compiler will not catch

| # | Change | Symptom if you are affected |
|---|---|---|
| [§1](#1-sessionquery-returns-a-fresh-builder-behaviour) | `session.Query()` no longer memoizes | Queries that previously inherited a stale `WHERE`/`ORDER BY` now do not — results change |
| [§2](#2-json--is-now-honoured-in-api-responses-behaviour-security) | `json:"-"`, `omitempty`, embedded inlining | Response JSON keys appear/disappear; a previously leaked field is now absent |
| [§3](#3-a-role-mismatch-returns-403-not-401-behaviour) | Role mismatch is 403, not 401 | Clients branching on 401 stop seeing it for authorised-but-wrong-role users |
| [§4](#4-options-no-longer-invokes-your-route-handler-behaviour-security) | `OPTIONS` returns 204, never the handler | An `OPTIONS` call that used to reach a handler now does not |

---

## 1. `session.Query()` returns a fresh builder (behaviour)

### What broke and why it had to

`sessionImpl.Query()` memoized one builder per session:

```go
func (s *sessionImpl) Query() core.IQueryBuilder {
    if s.query == nil {
        s.query = NewBuilder()
    }
    return s.query
}
```

A second `Query()` call on the same session returned the *same* builder, already
carrying whatever `WHERE`, `ORDER BY`, `LIMIT` and `FROM` the first query had
accumulated. That is a silent wrong-results bug: no error, no panic, just extra
predicates on a query you thought was fresh. It could not be left as-is, because
correctness of every second query on a session depended on nobody noticing.

### Before / after

Both of these compile before and after. The difference is what they return.

```go
session, err := ds.NewSession()
if err != nil {
    return err
}

// A first, unrelated query on this session.
orderSQL, _, err := session.Query().
    Select("id").From("orders").
    Eq("tenant_id", 42).
    OrderBy("created_at", "desc").
    ToSQL()
// orderSQL is the same in both versions:
//   SELECT id FROM orders WHERE tenant_id = ? ORDER BY "created_at" DESC

// A second query on the SAME session. This is the one that changed.
widgetSQL, widgetArgs, err := session.Query().Select("id").From("widgets").ToSQL()
//   v1.5.1: SELECT id FROM widgets WHERE tenant_id = ? ORDER BY "created_at" DESC
//           widgetArgs == []any{42}          <- leaked from the first query
//   v2.0.0: SELECT id FROM widgets
//           widgetArgs == nil
```

If you were *relying* on the shared builder to carry a common filter, make it
explicit:

```go
// Before (accidental): relied on Query() handing back the same builder.
session.Query().Eq("tenant_id", tenantID)
rows := session.Query().Select("*").From("orders")   // inherited the Eq

// After (deliberate): build a base and derive from it. The builder is
// copy-on-write, so each derivation is independent.
base := session.Query().Eq("tenant_id", tenantID)
orders := base.Select("*").From("orders")
widgets := base.Select("*").From("widgets")
```

### How to detect whether you are affected

```bash
grep -rn 'Query()' --include='*.go' . | grep -v '_test.go'
```

Look for any place that calls `Query()` twice on one session, or that calls
`Query()` and discards the returned builder. A discarded return value is the
tell — the builder has been copy-on-write for every other method all along, so
`session.Query().Eq(...)` with the result thrown away only ever did anything
because of the memoization.

Symptom in a running app: a query that used to return fewer rows than its own SQL
suggests now returns more.

### Mechanical or judgement?

**Judgement.** The compiler cannot help. Read every multi-`Query()` call site and
decide whether the shared state was intentional.

---

## 2. `json:"-"` is now honoured in API responses (behaviour, security)

### What broke and why it had to

`dto.ReturnObject` bodies are not marshalled by `encoding/json`; `ApiReturnObject`
reflects over the struct field by field. Its tag parser treated `json:"-"`
identically to an absent tag:

```go
func parseJSONTag(rtField reflect.StructField) string {
    if jsonTag == "-" || jsonTag == "" {
        return rtField.Name          // <- "-" fell through to the Go field name
    }
    ...
}
```

So a field marked `json:"-"` specifically to keep a secret off the wire was
serialised anyway, under its Go name. **A password hash on a DTO marked
`json:"-"` went out in the response.** That is why this had to change regardless
of the response-shape churn it causes.

Two related bugs in the same marshaller are fixed at the same time:

- `omitempty` was ignored, so zero fields shipped as `null` / `""` / `0`.
- An anonymous embedded struct was written under its Go **type** name, producing
  `{"OwnerCardDto": {…}}` where `encoding/json` inlines the fields.

### Before / after

```go
type UserDto struct {
    ID           string `json:"id"`
    Email        string `json:"email"`
    PasswordHash string `json:"-"`
    Nickname     string `json:"nickname,omitempty"`
}
```

```jsonc
// v1.5.1 response body — note the leak and the empty nickname
{ "id": "u1", "email": "a@b.c", "PasswordHash": "$2a$10$…", "nickname": "" }

// v2.0.0
{ "id": "u1", "email": "a@b.c" }
```

Embedded structs:

```go
type OwnerCardDto struct {
    OwnerID   string `json:"owner_id"`
    OwnerName string `json:"owner_name"`
}

type CardDto struct {
    OwnerCardDto            // anonymous embed
    CardID       string `json:"card_id"`
}
```

```jsonc
// v1.5.1
{ "OwnerCardDto": { "owner_id": "o1", "owner_name": "Ann" }, "card_id": "c1" }

// v2.0.0 — matches encoding/json
{ "owner_id": "o1", "owner_name": "Ann", "card_id": "c1" }
```

If a client depends on the old nested shape, name the embed explicitly. A tagged
embed is *not* inlined, in v2 or in `encoding/json`:

```go
type CardDto struct {
    OwnerCardDto `json:"OwnerCardDto"`   // keeps the v1.5.1 shape
    CardID       string `json:"card_id"`
}
```

### How to detect whether you are affected

```bash
# DTOs with a suppressed field — check whether any client depended on receiving it
grep -rn 'json:"-"' --include='*.go' .

# DTOs with omitempty
grep -rn 'omitempty' --include='*.go' .

# Anonymous embeds in types that reach dto.ReturnObject. This finds candidates;
# an embedded field is a line inside a struct with a type and no field name.
grep -rn -B20 'dto.ReturnObject' --include='*.go' . | grep -E '^\s*[A-Z][A-Za-z0-9_]*(Dto|DTO)\s*$'
```

**Re-check every DTO with an embedded struct.** That is the change most likely to
alter a response shape your clients parse, and the one a grep for `json:"-"`
will not surface.

Test for it directly — compare your DTO against `encoding/json`, which is now the
reference behaviour:

```go
func TestUserDtoResponseShape(t *testing.T) {
    payload := UserDto{ID: "u1", Email: "a@b.c", PasswordHash: "secret"}

    raw, err := json.Marshal(dto.ReturnObject(payload, core.SuccessHttpStatus, nil))
    require.NoError(t, err)

    // The suppressed field must not appear anywhere.
    assert.NotContains(t, string(raw), "secret")

    var got struct{ Body map[string]any `json:"body"` }
    require.NoError(t, json.Unmarshal(raw, &got))

    var want map[string]any
    reference, _ := json.Marshal(payload)
    require.NoError(t, json.Unmarshal(reference, &want))

    assert.Equal(t, want, got.Body, "envelope must agree with encoding/json")
}
```

### One documented difference from `encoding/json`

An embedded field whose **type is unexported** is skipped, where `encoding/json`
would promote its exported fields:

```go
type inner struct { Public string `json:"public"` }   // unexported type

type Outer struct {
    inner                            // skipped entirely by the envelope
    ID string `json:"id"`
}
```

This marshaller reads values through `reflect.Value.Interface()`, which panics on
anything reached through an unexported field. v1.5.1 skipped these too, so nothing
regressed — but if you embed an unexported type and expect its fields in the
response, **export the type**.

### Mechanical or judgement?

**Judgement.** Each changed response shape is a contract with your clients.

---

## 3. A role mismatch returns 403, not 401 (behaviour)

### What broke and why it had to

`AuthMiddleware` emitted 401 for a *role* mismatch, so an authenticated user with
the wrong role was told they were unauthenticated. That is wrong per RFC 9110 and
actively harmful in practice: a client that redirects to login on 401 sends a
logged-in user to log in again, which cannot fix a role problem, and loops.

Related, in the same middleware: `CurrentUser` can return `(nil, nil)` and only
`err` was checked, so `user.GetRole()` dereferenced nil and took the connection
down. That one is a straight bug fix with no migration.

### Before / after

```go
// Client code that branches on the status.

// Before
if resp.StatusCode == http.StatusUnauthorized {
    redirectToLogin()   // fired for BOTH "not logged in" and "wrong role"
}

// After
switch resp.StatusCode {
case http.StatusUnauthorized: // 401: no session, or an invalid one
    redirectToLogin()
case http.StatusForbidden:    // 403: logged in, wrong role
    showPermissionDenied()
}
```

Browser (non-JSON) requests changed too: a 401 still redirects to
`auth.login.formUrl`, but a 403 now renders `Forbidden` with status 403 rather
than redirecting.

### How to detect whether you are affected

```bash
grep -rn 'StatusUnauthorized\|== 401\|NotAuthorizedHttpStatus' --include='*.go' .
```

Also grep your frontend for `401`. Anything that treats 401 as "user lacks
permission" needs a 403 branch.

### Mechanical or judgement?

**Mechanical on the server** (nothing to change). **Judgement on the client**:
you have to decide what a 403 should do in your UI.

---

## 4. `OPTIONS` no longer invokes your route handler (behaviour, security)

### What broke and why it had to

`ChiRouterAdapter.RegisterRoute` registered every route under `OPTIONS` in
addition to its declared method, with the *same handler*:

```go
r.engine.With(mws...).MethodFunc(method, pattern, h)
r.engine.With(mws...).Options(pattern, h)          // <- the route's own handler
```

Meanwhile `CSRFMiddleware` exempted `OPTIONS`. Together, that was a token-free
path to every mutating endpoint in every app: `OPTIONS /widgets/1` ran the
`DELETE` handler and deleted the row.

Now the router installs a preflight responder that answers `204` with an `Allow`
header and never calls the route handler, and `CSRFMiddleware` answers `OPTIONS`
itself with `204`.

### Before / after

```go
// If you genuinely need OPTIONS to reach a handler, declare it as a route.
// An explicitly declared OPTIONS route wins over the generic responder, in
// either registration order.

func (c *WidgetController) GetRoutes() []core.IRouteConfig {
    return []core.IRouteConfig{
        router.RouteConfig{
            Path: "/widgets/{id}", Method: core.Method(http.MethodDelete),
            Name: "widgets.delete", Handler: c.Delete,
        },
        // After: declare it explicitly if you want it.
        router.RouteConfig{
            Path: "/widgets/{id}", Method: core.Method(http.MethodOptions),
            Name: "widgets.options", Handler: c.Options,
        },
    }
}
```

If you used the implicit `OPTIONS` route for CORS preflight, the `204` + `Allow`
response is what you want; add your CORS headers in `CorsMiddleware` as a `/**`
filter, which runs before the responder.

### How to detect whether you are affected

```bash
grep -rn 'MethodOptions\|"OPTIONS"' --include='*.go' .
```

Then, against a running app:

```bash
# Must be 204 with an Allow header, and must not have deleted anything.
curl -i -X OPTIONS http://localhost:8080/api/widgets/1
```

Regression test worth having:

```go
func TestOptionsHasNoSideEffect(t *testing.T) {
    before := countWidgets(t)

    rec := httptest.NewRecorder()
    app.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/widgets/1", nil))

    assert.Equal(t, http.StatusNoContent, rec.Code)
    assert.Equal(t, before, countWidgets(t), "OPTIONS must not mutate")
}
```

### Mechanical or judgement?

**Judgement**, but usually a no-op: almost nobody deliberately routed `OPTIONS`
to a mutating handler. Check your CORS setup.

---

## 5. `IQueryBuilder.ToSQL()` returns an error

### What broke and why it had to

`ToSQL()` returned `(string, []any)`. With MySQL in the picture, a query can now
be unrepresentable — `RETURNING`, `DISTINCT ON`, `FULL OUTER JOIN`, `CUBE`,
`GROUPING SETS` — and the framework's rule is that such a query returns an
explicit error rather than emitting SQL the server will reject. There is nowhere
to put that error without changing the signature.

### Before / after

```go
// Before
sql, args := builder.Select("id").From("users").Eq("id", 1).ToSQL()
rows, err := db.Query(sql, args...)

// After
sql, args, err := builder.Select("id").From("users").Eq("id", 1).ToSQL()
if err != nil {
    return fmt.Errorf("cannot render user query: %w", err)
}
rows, err := db.Query(sql, args...)
```

To branch on why it failed:

```go
sql, args, err := b.ToSQL()
if err != nil {
    var unsupported *dbCore.UnsupportedError
    if errors.As(err, &unsupported) {
        log.Printf("%s cannot express %s", unsupported.Dialect, unsupported.Construct)
    }
    return err
}
```

`Executor.Exec`, `Find` and `Count` already check it for you and surface it as
`QueryResult.Error`, so if you only ever go through the executor you have nothing
to change.

### How to detect whether you are affected

The compiler tells you:

```
assignment mismatch: 2 variables but builder.ToSQL returns 3 values
```

```bash
grep -rn '\.ToSQL()' --include='*.go' .
```

Note that `Condition.ToSQL()` — `BinaryCondition`, `RawCondition`, and friends —
is **unchanged** and still returns two values. Only `IQueryBuilder.ToSQL()` grew
an error.

### Mechanical or judgement?

**Mechanical.**

---

## 6. `SQLDialect` reshaped

### What broke and why it had to

Every method now returns an `error`; `FormatGroupBy` takes the whole
`*GroupByClause`; and `Name()` plus `QuoteIdentifier()` are new.

The error return is what lets a dialect refuse a construct. `FormatGroupBy` had
to change because engines place grouping modifiers differently — Postgres writes
`GROUP BY ROLLUP (a, b)`, MySQL writes `GROUP BY a, b WITH ROLLUP` — which
`FormatGroupBy([]string)` cannot express. (It also silently dropped `Rollup()`,
`Cube()` and `GroupingSets()` entirely, emitting a bare `GROUP BY ` when they
were the only grouping present.)

### Before / after

```go
// Before
func (d *MyDialect) FormatGroupBy(fields []string) (string, []any) {
    return "GROUP BY " + strings.Join(fields, ", "), nil
}

// After
func (d *MyDialect) Name() string { return "mydialect" }

func (d *MyDialect) QuoteIdentifier(field string) string {
    parts := strings.Split(field, ".")
    for i, p := range parts {
        parts[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
    }
    return strings.Join(parts, ".")
}

func (d *MyDialect) FormatGroupBy(groupBy *dbCore.GroupByClause) (string, []any, error) {
    if groupBy == nil || len(groupBy.Fields) == 0 {
        return "", nil, nil          // never emit a bare GROUP BY
    }
    if len(groupBy.Cube) > 0 {
        return "", nil, dbCore.Unsupported(d.Name(), "CUBE", "use a UNION ALL of GROUP BY queries")
    }
    return "GROUP BY " + strings.Join(groupBy.Fields, ", "), nil, nil
}
```

### How to detect whether you are affected

```bash
grep -rn 'SQLDialect\|FormatQuery\|FormatGroupBy' --include='*.go' .
```

Almost certainly **you are not**. Before v2 there was no way to inject a dialect:
`NewBuilder()` hard-coded `&PostgresDialect{}` and the `Config{Dialect}` struct
was never read by anything. If you had implemented `SQLDialect`, you could not
have used it without forking the builder.

### Mechanical or judgement?

**Judgement** if you have a custom dialect: you must decide, per construct, what
your engine can express. [`docs/DIALECTS.md`](docs/DIALECTS.md) walks through it.

---

## 7. `NewDataSource` returns an error instead of panicking

### What broke and why it had to

It did unchecked type assertions on the raw config map:

```go
gormConfig := postgres.Config{
    DSN: dsn,
    PreferSimpleProtocol: config["prefer_simple_protocol"].(bool),   // panics if absent
}
...
rawDb.SetMaxOpenConns(maxOpenConnections.(int))                      // panics if a string
if config["log"].(bool) { … }                                        // panics if absent
```

Any hand-built config map — exactly what a test helper writes — took the process
down with an unhelpful `interface conversion` panic.

### Before / after

```go
// Before
dbProvider.AddConnection("reports", func() dbCore.IDataSource {
    return v2.NewDataSource(map[string]any{
        "host": "localhost", "port": 5432, "db": "reports",
        "ssl": "disable", "log": false, "prefer_simple_protocol": false,
    })
})

// After — the typed constructor is clearer, and the map form now returns an error.
dbProvider.AddConnectionE("reports", func() (dbCore.IDataSource, error) {
    return v2.NewDataSourceWithConfig(dsconfig.DataSource{
        Host: "localhost", Port: 5432, Database: "reports", SSL: "disable",
    })
})
```

Keeping the map form:

```go
dbProvider.AddConnectionE("reports", func() (dbCore.IDataSource, error) {
    return v2.NewDataSource(map[string]any{
        "host": "localhost", "port": 5432, "db": "reports", "ssl": "disable",
    })
})
```

Note that `log` and `prefer_simple_protocol` are now **optional** — omitting them
no longer panics. An **unknown** key is now reported, so a typo like `databse`
fails at boot instead of being ignored.

### How to detect whether you are affected

The compiler:

```
multiple-value v2.NewDataSource(conf) in single-value context
```

```bash
grep -rn 'NewDataSource\|AddConnection' --include='*.go' .
```

### Mechanical or judgement?

**Mechanical.**

---

## 8. Unnamed `dbCore.ISession` injection resolves to `default` only

### What broke and why it had to

`DbProvider.Register` re-bound transient `dbCore.ISession`, `IQueryExecutor` and
`IQueryBuilder` once per connection inside its registration loop, and
`Container.bind` overwrites. A bare injected `dbCore.ISession` therefore resolved
to whichever connection happened to register **last** — and since the loop walked
a Go map, that changed on every boot. With no `databases` key at all, the
closures were registered anyway over a nil connection and panicked if resolved.

### Before / after

```go
// A bare injection now always means the `default` connection.
type ReportService struct {
    session dbCore.ISession `container:"inject"`   // always `default` in v2
}

// To reach a second database, resolve it by name. This was always the correct
// path; in v2 it is the only one that can succeed.
type ReportService struct {
    dbContext core.IDBContext `container:"inject"`
}

func (s *ReportService) load(ctx context.Context) error {
    session, err := s.dbContext.GetDataSource("reports").NewSession()
    if err != nil {
        return err
    }
    defer session.Close()
    ...
}
```

If your app has **no** connection named `default`, the three unnamed bindings are
not registered at all, and injecting `dbCore.ISession` now fails to resolve with
a clear container error instead of panicking later. Either name a connection
`default`, or switch to named resolution. The provider logs a warning naming your
configured connections at boot.

### How to detect whether you are affected

```bash
# Bare session/executor/builder injections
grep -rn 'dbCore.ISession\|dbCore.IQueryExecutor\|dbCore.IQueryBuilder' --include='*.go' . \
  | grep 'container:"inject"'

# Do you have more than one database, and is one of them called `default`?
grep -rn -A2 'databases:' config/*.yaml config/*.yml 2>/dev/null
```

If you configure exactly one database and it is called `default`, you are
unaffected.

### Mechanical or judgement?

**Judgement** for a multi-database app — you must work out which connection each
injection site actually meant, which is precisely the question the old code
answered at random.

---

## 9. `db:migrate` / `db:seed` / `db:diff` and `--datasource`

### What broke and why it had to

All three hard-coded `GetDataSource(core.DefaultKeyInRegistrar)`, so in a
two-datasource app a migration written for the second database executed against
the first — a Postgres DDL statement fired at a MySQL connection, with no
warning. `db:migrate down` was additionally an empty stub that reported success
and did nothing.

### Before / after

```bash
# Before: always `default`, no way to say otherwise.
go run cmd/cli.go db:migrate up

# After: still `default` by default, and now selectable.
go run cmd/cli.go db:migrate up
go run cmd/cli.go db:migrate up --datasource=reports
go run cmd/cli.go db:migrate down --steps=2
go run cmd/cli.go db:seed --datasource=reports
go run cmd/cli.go db:diff --datasource=reports
```

Declare a migration's target so it can never run against the wrong engine:

```go
package migration

type ReportsIndexMigration struct{}

func (m *ReportsIndexMigration) Name() string { return "reports_add_index" }

// DataSourceName implements db.DatasourceScoped. Without it this migration is
// treated as targeting `default`.
func (m *ReportsIndexMigration) DataSourceName() string { return "reports" }

func (m *ReportsIndexMigration) Up() core.MigrationClosure {
    return func(db *gorm.DB) error {
        return db.Migrator().CreateIndex(&Report{}, "idx_reports_created_at")
    }
}

func (m *ReportsIndexMigration) Down() core.MigrationClosure {
    return func(db *gorm.DB) error {
        return db.Migrator().DropIndex(&Report{}, "idx_reports_created_at")
    }
}
```

Rules:

- Declares nothing → treated as `default`. **Every existing migration keeps its
  current behaviour**, and `--datasource=other` cannot sweep them onto the wrong
  engine.
- Declares a different *configured* datasource → skipped with a log line.
- Declares a datasource that is **not configured** → the command fails, naming
  the migration and the datasource. This is the "fails loudly" case: such a
  migration would otherwise never run and never say so.

`db:migrate down` needs every recorded migration to still be registered — there
is no `Down()` to run otherwise, and dropping the bookkeeping row silently would
leave the schema and the `migrations` table disagreeing. It refuses instead.

### How to detect whether you are affected

If you have one database, nothing changes. With more than one:

```bash
ls db/migration/
grep -rln 'DataSourceName' db/migration/     # which ones already declare a target
```

Every migration not in that list will run against `default`. Add
`DataSourceName()` to the ones that belong elsewhere.

### Mechanical or judgement?

**Judgement**: you have to say which database each migration belongs to. The
default is the safe one.

---

## 10. Smaller source-breaking changes

### `EventProvider.Boot` no longer returns an error

`Boot` returned `error`, which does **not** satisfy `core.IProvider`, so
`*EventProvider` could never be passed to `Bootstrapper.AddProvider` and the
entire events subsystem was unreachable. Nothing in the repo used it, so nothing
caught it.

```go
// This now compiles. Before v2 it did not.
eventProvider := provider.NewEventProvider()
eventProvider.RegisterSubscriber("user.created", func() core.ISubscriber {
    return &WelcomeEmailSubscriber{}
})
bootstrapper.AddProvider(eventProvider)
```

A wiring failure now panics at boot, matching the other providers.
**Mechanical** — and if you had worked around it with an adapter, delete the
adapter.

### `RecoveryMiddleware` is registered automatically

`RouteProvider` prepends it as the first `/**` filter. If your app already
installs its own recovery filter, either drop yours or opt out:

```go
routeProvider := provider.NewRouteProvider()
routeProvider.DisableRecoveryMiddleware()
```

**Mechanical.** Note the upside: a registered `JwtAuthError` handler now actually
fires, because `JwtMiddleware` reports failure by panicking and previously
nothing recovered to re-dispatch it.

### Route-scoped middleware no longer leaks across methods

Route-level middleware configs used to be published into the shared
`webCtx` middleware list keyed by the route's pattern, so a later
`RegisterRoute` for the same method-agnostic pattern re-matched and re-attached
them. Registering `GET /x` then `PUT /x` fired a side-effecting middleware
**twice** on one request, and middleware declared only on `GET` also ran on
`PUT`.

If you were relying on that to share middleware between methods of one path,
declare it on each route, or register it globally with a pattern:

```go
routeProvider.AddMiddleware(
    grghttp.NewMiddlewareConfigBuilder().
        WithPattern("/widgets/**").
        WithMiddleware(middleware.NewAuditMiddleware()).
        Build(),
)
```

**Judgement**, but only if you have two methods on one path and middleware on
one of them.

### The builder moved to `db/sql/builder`

`v2.Builder` and `v2.Config` are type **aliases**, so `import v2 ".../postgres/v2"`,
`*v2.Builder` type assertions and `v2.NewBuilder()` all keep working. Nothing to
do. New code should prefer `builder.New(dialect)` or
`v2.NewBuilderWithDialect(dialect)`.

### Config keys with new behaviour

| Key | Default | Note |
|---|---|---|
| `auth.session.cookie.secure` | `true` | Set `false` **only** for local dev over `http://`. Was hard-coded `true`, which broke plain-HTTP login (Safari most strictly). |
| `http.upload.maxMultipartSize` | `33554432` (32 MB) | Was a compile-time constant. |
| `http.upload.maxFiles` | `100` | Was a compile-time constant. |
| `http.upload.maxFileSize` | `10485760` (10 MB) | Was a compile-time constant. |
| `databases.<name>.search_path` | unset | New. Postgres only; the MySQL driver rejects it, since a MySQL schema *is* a database. |
| `databases.<name>.options` | unset | New. Arbitrary driver DSN parameters. |

All defaults preserve v1.5.1 behaviour, so an app that configures nothing is
unaffected. A configured `0` for an upload limit falls back to the default rather
than disabling the limit.

---

## Verification

```bash
go build ./... && go vet ./... && go test ./... -count=1
```

Then the checks no compiler covers:

```bash
# 1. OPTIONS has no side effect and returns 204 + Allow
curl -i -X OPTIONS http://localhost:8080/api/<a-mutating-route>

# 2. No suppressed field appears in any response body
curl -s http://localhost:8080/api/<a-dto-route> | grep -iE 'password|hash|secret|token'

# 3. A wrong-role user gets 403, a logged-out user gets 401
# 4. Two queries on one session do not share state — assert on the second one's SQL
```

And, if you run more than one database:

```bash
go run cmd/cli.go db:migrate up                        # `default` only
go run cmd/cli.go db:migrate up --datasource=<other>   # the other one only
```

Boot the app ten times and confirm every datasource resolves by name on each
boot. The pre-v2 registration was nondeterministic, so a single successful boot
proved nothing.
