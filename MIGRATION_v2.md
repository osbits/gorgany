# Migrating an app from gorgany v1.5.1 to v2.0.0

This document has one section per breaking change. Each says what broke and why,
gives compiling before/after code, tells you how to detect whether you are
affected, and states whether the fix is mechanical or needs judgement.

> **Read “Behaviour changes the compiler will not catch” first.**
>
> Eleven of these changes compile cleanly and change what your app *does*. They
> matter more than the signature changes, because `go build` will not point at
> them and a passing test suite may not either. Five of them (`json:"-"`,
> `OPTIONS`, the CSRF token, the CORS refusal, the body-in-log fix) are
> security-relevant, and three (`Query()` memoization, the validation error shape
> and delivery, `omitempty`) can change what a client sees silently.

**Order of work.** Start with [§20](#20-the-module-path-gains-v2) — nothing else can be
resolved until the import paths change, and it is one scripted sweep. Then do §1–§4a and
§12/§16/§17/§22/§23, which need judgement and testing. Then run `go build ./...` and let the
compiler drive §5–§10 and §11/§13/§14/§15. Finally boot the app once for §21, which compiles
cleanly and only fails at runtime.

---

## Behaviour changes the compiler will not catch

| # | Change | Symptom if you are affected |
|---|---|---|
| [§1](#1-sessionquery-returns-a-fresh-builder-behaviour) | `session.Query()` no longer memoizes | Queries that previously inherited a stale `WHERE`/`ORDER BY` now do not — results change |
| [§2](#2-json--is-now-honoured-in-api-responses-behaviour-security) | `json:"-"`, `omitempty`, embedded inlining | Response JSON keys appear/disappear; a previously leaked field is now absent |
| [§3](#3-a-role-mismatch-returns-403-not-401-behaviour) | Role mismatch is 403, not 401 | Clients branching on 401 stop seeing it for authorised-but-wrong-role users |
| [§4](#4-options-no-longer-invokes-your-route-handler-behaviour-security) | `OPTIONS` returns 204, never the handler | An `OPTIONS` call that used to reach a handler now does not |
| [§4a](#4a-connection-pool-limits-now-actually-apply-behaviour) | `properties` pool limits now apply | Connection-pool caps you configured years ago start being enforced |
| [§12](#12-validation-errors-changed-shape-behaviour) | `ValidationError.Field` is the wire name; `Err` is readable | A client matching on the Go field name or parsing the old message stops working |
| [§16](#16-x-csrf-token-on-every-response-and-a-csrf-endpoint-behaviour-security) | `X-CSRF-Token` on every session response; `GET /csrf` registered | A route already at `/csrf` collides at boot |
| [§17](#17-malformed-bodies-are-400-not-a-301-redirect-behaviour) | A malformed body is 400, not a 301 to the Referer | A client relying on the redirect sees a 400 envelope |
| [§18](#18-404-405-and-error-handlers-are-content-negotiated-behaviour) | 404/405/500/401 negotiate JSON vs text | An API client gets an envelope where it got an empty body |
| [§19](#19-var-substitution-what-a-missing-variable-now-does) | An unresolved `${VAR}` is blanked | An optional key reads back as `""` rather than the literal, and a security-relevant one fails the boot |
| [§22](#22-a-validation-failure-is-422-for-an-api-client-303-for-a-browser-behaviour) | Validation is 422 for an API client, 303 for a browser | A client that saw a 301 now gets a readable payload |
| [§23](#23-omitempty-and-embed-collisions-now-match-encodingjson-behaviour) | `omitempty` and embed collisions match `encoding/json` | A zero `time.Time` reappears; a non-nil empty slice disappears |

One that compiles cleanly and fails at **boot** rather than at request time:
[§21](#21-dbprovider-no-longer-registers-a-database-driver) — `DbProvider` no longer
registers a database driver, so the app needs one blank import.

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

## 4a. Connection-pool limits now actually apply (behaviour)

### What broke and why it had to

Viper lowercases every key it reads, so a YAML `maxOpenConnections` arrives as
`maxopenconnections`. The datasource read it back as:

```go
if maxOpenConnections, ok := props["maxOpenConnections"]; ok {   // never matched
    rawDb.SetMaxOpenConns(maxOpenConnections.(int))
}
```

That camelCase lookup never matched a Viper-supplied map, so **all four pool
settings have been silently ignored on every version up to and including v1.5.1**.
Config that looked applied was inert.

Key matching is now case-insensitive, so the values take effect.

### Before / after

Nothing in your code changes. What changes is what the pool does:

```yaml
databases:
  default:
    driver: postgres_gorm
    host: localhost
    db: myapp
    properties:
      maxOpenConnections: 5          # v1.5.1: ignored — pool was unlimited
      maxIdleConnections: 5          # v2.0.0: enforced — at most 5 open connections
      connectionMaxLifetime: 300
      connectionMaxIdleLifetime: 300
```

**This is the one change in v2 that can make a working app slower or stall it.** If
the number was written years ago, never took effect, and is too low for your current
concurrency, requests will now queue waiting for a connection.

### How to detect whether you are affected

```bash
grep -rn -A6 'properties:' config/*.yaml config/*.yml 2>/dev/null
```

If that finds a `properties` block, the values in it are about to start mattering.
Sanity-check them against your actual concurrency before deploying:

- `maxOpenConnections` should be at least your peak concurrent DB-using requests.
  Postgres' own `max_connections` defaults to 100, so a value in the tens is normal
  per instance; a value like `5` is almost certainly a stale guess.
- `maxIdleConnections` should not exceed `maxOpenConnections`.

To keep the pre-v2 behaviour exactly — an uncapped pool — delete the `properties`
block rather than setting it to `0`. A configured `0` is treated as "unset" for the
upload limits, but for pool settings `0` reaches `SetMaxOpenConns(0)`, which Go
documents as unlimited; deleting the block is clearer about intent.

### Mechanical or judgement?

**Judgement, and worth doing before you deploy.** This is the only change here that
can degrade a running system rather than break a build.

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

### The duplicate condition family in `postgres/v2` is removed

`db/sql/gorm/postgres/v2/conditions.go` held `SimpleCondition`, `InCondition`,
`BetweenCondition`, `RawCondition` and `CompositeCondition`, plus `Equal`,
`NotEqual`, `GreaterThan`, `LessThan`, `In`, `Between`, `And`, `Or` and `Raw`.
Every one was engine-agnostic — `?` placeholders throughout, no engine-specific
quoting — and duplicated what `db/sql/core` already provided. The conditions were
never part of the builder, so moving the builder out of this package left them
behind. `model/pagination.go` was the last caller, which is what put
`gorm.io/driver/postgres` on the import path of every package that imports
`model`, and why making the engines opt-in could remove MySQL from a
Postgres-only binary but not Postgres from a MySQL-only one.

Unlike `v2.Builder`, these were **not** aliases. The `dbCore` equivalents are
distinct types with different field names, so this is a rename, not a re-import:

| Removed | Replacement |
|---|---|
| `v2.SimpleCondition{Field, Operator, Value}` | `dbCore.BinaryCondition{Left, Operator, Right}` |
| `v2.InCondition{Field, Values}` | `dbCore.InCondition{Field, Values}` — also gains `Not`, `IsSubquery`, `Subquery` |
| `v2.BetweenCondition{Field, Start, End}` | `dbCore.BetweenCondition{Field, Lower, Upper}` — also gains `Not` |
| `v2.RawCondition{SQL, Args}` | `dbCore.RawCondition{SQL, Args}` |
| `v2.CompositeCondition{Operator, Conditions}` | `dbCore.CompositeCondition{Operator, Conditions}` |
| `v2.Equal(f, v)` … `v2.Raw(sql, args...)` | none — `dbCore` has no constructor helpers; build the struct |

`dbCore` additionally offers `UnaryCondition`, `ExistsCondition`, `LikeCondition`
and `IsNullCondition`, which the removed copy never had.

#### Before / after

```go
// Before
builder = builder.Where(v2.And(
    v2.Equal("status", "active"),
    v2.Between("age", 18, 65),
))

// After
builder = builder.Where(&dbCore.CompositeCondition{
    Operator: "AND",
    Conditions: []dbCore.Condition{
        &dbCore.BinaryCondition{Left: "status", Operator: "=", Right: "active"},
        &dbCore.BetweenCondition{Field: "age", Lower: 18, Upper: 65},
    },
})
```

#### A behaviour difference that has since been fixed rather than documented

The first version of this section warned that `dbCore.BetweenCondition` interpolated a
`string` bound into the SQL text, where the removed `v2.Between` always bound it as a
parameter — and told you to pass a non-string type to avoid it.

That was accurate and it was the wrong resolution. It made `BetweenCondition` the only
member of the family to read a string in a *value* position as SQL, and `Builder.Between`
passes its arguments straight through, so an app filtering a date range from the query
string got `created_at BETWEEN 1 OR 1=1 -- AND 2`. Documenting a SQL injection in a public
API is not a migration note. Both bounds now bind whatever their type, so there is nothing
to do here:

```go
&dbCore.BetweenCondition{Field: "age", Lower: "18", Upper: "65"}
// age BETWEEN ? AND ?    args: ["18", "65"]   — same as v2.Between always did
```

A `*Query` bound still renders as a subquery. If you genuinely need an *identifier* in a
bound — `BETWEEN start_col AND end_col` — use a `RawCondition`, which is the same answer
the family already gives for `BinaryCondition.Right`.

`dbCore.RawCondition` also understands identifier placeholders that the removed
copy passed through verbatim: `"?.id"` consumes one argument and renders
`<arg>.id`, and `"?.?"` consumes two and renders `<arg0>.<arg1>`. Raw SQL that
does not contain the sequence `?.` is unaffected.

#### How to detect whether you are affected

```bash
grep -rnE "v2\.(Simple|In|Between|Raw|Composite)Condition|v2\.(Equal|NotEqual|GreaterThan|LessThan|In|Between|And|Or|Raw)\(" --include="*.go" .
```

**Mechanical**, with one exception: `Between` with string bounds needs
**judgement**, because it compiles after the rename and changes the SQL.

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

## 11. `core.IJob` reshaped, and `gocron` is gone

### What broke and why it had to

Scheduled jobs never ran, on any version. `JobProvider.Boot` did this:

```go
scheduler := &job.Scheduler{}
_ = c.Make(scheduler)          // field-injects a zero value
```

`Container.Make` on a pointer-to-struct fills its `container:"inject"` fields; it does
not hand back the registered singleton. So every job was registered against one
scheduler object and a *different*, empty one was started. Nothing in the repo noticed.

Fixing that required a scheduler the framework controls, and `core.IJob`'s old shape —
`GetJob()`, `GetInterval()`, `GetUnit()` — was built around `gocron`'s API, which put a
third-party dependency in `app/core`, the package every other package imports. `gocron`
is now removed from the module entirely.

### Before / after

```go
// BEFORE
type CleanupJob struct {
    Storage core.ISessionStorage `container:"inject"`
}

func (j CleanupJob) GetInterval() uint64 { return 1 }
func (j CleanupJob) GetUnit() gocron.Unit { return gocron.Hours }
func (j CleanupJob) GetJob() (any, []any) {
    return func() { j.Storage.ClearExpiredSessions() }, nil
}
```

```go
// AFTER
type CleanupJob struct {
    Storage core.ISessionStorage `container:"inject"`
}

func (j *CleanupJob) Schedule() core.JobSchedule {
    return core.JobSchedule{
        Every:        time.Hour,
        RunAtStartup: true,   // was not expressible before
        AllowOverlap: false,  // skip a tick if the previous run is still going
    }
}

func (j *CleanupJob) Run(ctx context.Context) error {
    j.Storage.ClearExpiredSessions()
    return nil
}
```

Registration is unchanged:

```go
jobProvider := provider.NewJobProvider()
jobProvider.AddJob(&CleanupJob{})
```

Note the **pointer** receiver and the pointer at registration. A job registered by
*value* that carries `container:"inject"` tags is now a loud error rather than a silently
unfilled struct — the container cannot fill fields of a value it does not own.

### If your `CleanupJob` was sweeping the framework's sessions table

`JobProvider` now registers `job.ClearExpiredSessionsJob` itself when
`auth.session.storage` is `database`, so you can delete your own copy. If you keep it,
registering a job of that same type is fine — the framework skips its own when yours is
already there, so yours wins along with any `Schedule()` you had tuned.

Two other ways to sweep, if a scheduled job is the wrong shape:

```bash
# a sixth built-in command, for external cron
go run . session:gc
```

```go
// and turn the framework's job off, so it is not doing the same work
jobProvider.DisableSessionGc()
```

The command is worth knowing about because `core.JobSchedule` is interval-only: it can
express "every 24h" but not "daily at 09:00 Europe/Kyiv", and `Every: 24h` re-anchors on
every deploy. Wall-clock maintenance belongs in cron with an explicit `CRON_TZ`.

`GetUnit()`'s `gocron.Unit` becomes a `time.Duration`:

| Before | After |
|--------|-------|
| `GetInterval() 30, GetUnit() gocron.Seconds` | `Every: 30 * time.Second` |
| `GetInterval() 5, GetUnit() gocron.Minutes` | `Every: 5 * time.Minute` |
| `GetInterval() 1, GetUnit() gocron.Hours` | `Every: time.Hour` |
| `GetInterval() 1, GetUnit() gocron.Days` | `Every: 24 * time.Hour` |

### How to detect whether you are affected

```bash
grep -rn "gocron\|GetInterval()\|GetUnit()\|GetJob()" --include="*.go" .
```

Every hit is a job to convert. If there are none, you had no jobs — which, given that
none of them ran, may be why.

Cron expressions are **not** supported. They would need a parser dependency, and shipping
a non-functional `Cron` field would repeat exactly the `core.MongoDb` problem this release
fixes. If you need one, schedule at the finest interval you care about and check the clock
inside `Run`.

### Mechanical or judgement?

**Mechanical**, with one judgement call per job: whether it should run at startup, and
whether a slow run should be allowed to overlap the next tick. The old scheduler had no
answer to either, so there is no prior behaviour to preserve.

Also worth knowing: a job that panics no longer takes the scheduler down with it, and
`Scheduler.Stats()` reports runs, failures and skips per job.

---

## 12. Validation errors changed shape (behaviour)

### What broke and why it had to

`err.ValidationError` carried the Go struct field name and go-playground's raw sentence:

```json
{
  "field": "MobilePhone",
  "err": "Key: 'CreateUserDto.MobilePhone' Error:Field validation for 'MobilePhone' failed on the 'required' tag"
}
```

No UI can display that, and no client can map `MobilePhone` to the `mobile_phone` it
sent. Every serious consumer replaced `core.IValidator` wholesale just to rename fields
and translate messages, which is a framework gap rather than an app concern.

### Before / after

```json
// BEFORE
{"field": "MobilePhone", "err": "Key: 'CreateUserDto.MobilePhone' Error:Field validation for 'MobilePhone' failed on the 'required' tag"}
```

```json
// AFTER
{"field": "mobile_phone", "err": "mobile_phone is required", "rule": "required", "path": "mobile_phone"}
```

The struct gained three `omitempty` fields, so the JSON is additive:

```go
type ValidationError struct {
    Field string `json:"field"`            // now the json/scheme tag, not the Go name
    Err   string `json:"err"`              // now readable, and localised
    Rule  string `json:"rule,omitempty"`   // new: "required", "email", "min"
    Param string `json:"param,omitempty"`  // new: "3" for min=3
    Path  string `json:"path,omitempty"`   // new: "address.postal_code"
}
```

Client-side, key off `rule` rather than parsing `err`:

```js
// BEFORE — brittle, and the field name did not match what was sent
if (error.field === 'MobilePhone' && error.err.includes('required')) { ... }

// AFTER
if (error.field === 'mobile_phone' && error.rule === 'required') { ... }
```

To keep your own messages, put them in your translation files rather than in code:

```yaml
# resource/i18n/en.yaml
validation:
  required: "Please provide {:field}"
  email: "{:field} does not look like an email address"
```

See [`docs/VALIDATION.md`](docs/VALIDATION.md) for the resolution order and the full
placeholder set.

### How to detect whether you are affected

```bash
# Client-side: anything matching on a Go field name or the raw message
grep -rn "Error:Field validation\|Key: '" --include="*.js" --include="*.ts" --include="*.vue" .

# Server-side: a custom IValidator that exists only to rename fields
grep -rn "core.IValidator" --include="*.go" . | grep -v "_test"
```

If you replaced `core.IValidator` for this reason, you can now delete it. If you replaced
it for something else, note that `ValidateStruct` still satisfies the interface —
`ValidateStructForLocale` is a separate optional interface (`core.ILocalizedValidator`),
so nothing forces you to implement it.

One more change in the same area: **two fields of one struct sharing a wire name is now
an error.** The body parser can bind only one of them, so the other silently stayed zero.

```go
// This now fails validation with a descriptive error rather than half-working
type Broken struct {
    Primary   string `json:"email"`
    Secondary string `json:"email"`
}
```

### Mechanical or judgement?

**Judgement** on the client side: you have to decide what to key off. **Mechanical** on
the server side — usually deleting a custom validator.

---

## 13. `Container.Make` errors on a bound struct pointer

### What broke and why it had to

`Make` dispatches on the target's kind: a pointer-to-struct goes to field injection, a
pointer-to-interface goes to resolution. Both returned `nil` on success, so a caller who
passed `&SomeStruct{}` expecting the registered singleton got a zero value and **no
error**. That is how the job scheduler came to tick empty for however long it had shipped.

### Before / after

```go
// BEFORE — compiles, returns nil, gives you a zero value
scheduler := &job.Scheduler{}
_ = c.Make(scheduler)
```

```go
// AFTER — Make returns an error naming the method you wanted
var scheduler *job.Scheduler
if err := c.Resolve(&scheduler); err != nil {
    return err
}
```

The error is explicit about the fix:

```
container: Make(*job.Scheduler) fills a struct's container:"inject" fields, but
*job.Scheduler has a registered binding and this is not the bound instance — use
Resolve(**job.Scheduler) to obtain the registered one
```

`Make` still works for its actual purpose — filling a struct you own — and it still works
on the bound instance itself, which is a legitimate pattern:

```go
// Still fine: filling a struct with no binding for its type
resolver := &http.InputResolver{...}
_ = c.Make(resolver)
```

### How to detect whether you are affected

```bash
grep -rn "\.Make(&" --include="*.go" .
```

For each hit, ask which you meant: fill this struct's dependencies (keep `Make`), or get
the registered instance (switch to `Resolve`). The compiler will not tell you; the new
error will, at runtime, on the first call.

### Mechanical or judgement?

**Judgement per call site**, but the judgement is easy and there are usually only a
handful.

---

## 14. MySQL `ON CONFLICT … DO UPDATE` is refused by default

### What broke and why it had to

The MySQL dialect translated `ON CONFLICT (cols) DO UPDATE` into `ON DUPLICATE KEY
UPDATE`, silently dropping the conflict-target column list. MySQL keys off *any* unique
index, so on a table with more than one the row that gets updated is not the caller's to
control — MySQL's own manual advises against the clause in that case.

That is *valid but wrong* SQL, which is the failure mode the dialect rule exists to
prevent: every construct MySQL cannot express returns an explicit error rather than
emitting SQL the server will accept and misinterpret. A doc warning does not stop it
executing.

### Before / after

```go
// BEFORE — silently emitted ON DUPLICATE KEY UPDATE, ignoring the (email) target
builder.OnConflict([]string{"email"}).DoUpdate(map[string]any{"name": "new"})
```

```yaml
# AFTER, option 1 — opt in, having read the caveat and confirmed one unique index
databases:
  main:
    driver: mysql_gorm
    allow_unfaithful_upsert: true
```

This config key is the route to use. The dialect struct also carries
`AllowUnfaithfulUpsert`, but setting it only helps a builder you construct yourself: the
datasource builds its own dialect, and `session.Query()` and `Transaction()` build every
builder from that — so through v2.0.0-edge the opt-in was unreachable from the ORM.

```go
// AFTER, option 2 — express it in a way MySQL renders faithfully
builder.OnConflict([]string{"email"}).DoNothing()   // unaffected, always worked
// or do the read-then-write explicitly in a transaction
```

`DO NOTHING` is unchanged: its translation is faithful.

### How to detect whether you are affected

```bash
grep -rn "DoUpdate(" --include="*.go" .
```

Only MySQL is affected — Postgres renders `ON CONFLICT … DO UPDATE` natively. If you run
Postgres only, there is nothing to do.

Without the opt-in you get:

```
mysql does not support ON CONFLICT ... DO UPDATE; MySQL's ON DUPLICATE KEY UPDATE
fires on any unique index rather than the conflict target you named, so the
translation is not faithful; set databases.<name>.allow_unfaithful_upsert: true (or
MySQLDialect.AllowUnfaithfulUpsert, when you build the dialect yourself) if the table
has exactly one unique constraint, or do the read-then-write explicitly in a
transaction
```

### Mechanical or judgement?

**Judgement.** The opt-in is one line, but the question it answers — does this table have
exactly one unique index — is a schema question. See
[`docs/DIALECTS.md`](docs/DIALECTS.md).

---

## 15. CORS refuses a wildcard origin with credentials

### What broke and why it had to

The middleware emitted `Access-Control-Allow-Origin: *` from its origins branch and
`Access-Control-Allow-Credentials: true` from `AllowCredentials`, independently. The Fetch
spec forbids that pair, so every credentialed cross-origin request failed in the browser
with nothing on the server to explain it.

### Before / after

```go
// BEFORE — constructed fine, failed silently in every browser
middleware.NewCorsMiddleware(middleware.Options{
    AllowedOrigins:   []string{"*"},
    AllowCredentials: true,
})
```

```go
// AFTER — list the origins
middleware.NewCorsMiddleware(middleware.Options{
    AllowedOrigins:   []string{"https://app.example.com", "https://admin.example.com"},
    AllowCredentials: true,
})

// A wildcard *within* an origin is still fine with credentials
AllowedOrigins: []string{"https://*.example.com"}
```

Watch for the implicit form. An **empty** `AllowedOrigins` with no `AllowOriginFunc` also
means "all origins", so this is the same misconfiguration written without a `*`:

```go
// Also refused
middleware.NewCorsMiddleware(middleware.Options{AllowCredentials: true})
```

For a policy built from configuration you do not control, use the error-returning
constructor:

```go
cors, err := middleware.NewCorsMiddlewareChecked(options)
if err != nil {
    return fmt.Errorf("cors policy: %w", err)
}
```

### How to detect whether you are affected

```bash
grep -rn "AllowCredentials" --include="*.go" . -A 3 -B 3
```

Any hit where `AllowCredentials: true` sits next to `AllowedOrigins: []string{"*"}`, or
next to no `AllowedOrigins` at all. It panics at boot, so a single `go run` also tells
you.

### Mechanical or judgement?

**Judgement**: you have to know which origins to list. But if the pair was configured, the
credentialed requests were already failing, so nothing that worked stops working.

---

## 16. `X-CSRF-Token` on every response, and a CSRF endpoint (behaviour, security)

### What broke and why it had to

There was no way for a client to obtain a CSRF token. `SessionMiddleware` set
`X-CSRF-Token` on the single response that *created* a session and nowhere else;
`CsrfService.GetCSRFToken` had zero call sites; no route handed one out. A client that
missed that one response — a reload, a second tab, a session that already existed — could
never recover.

That was survivable only because `CSRFMiddleware` had two bypasses (§4 and the
"no session, no check" shortcut). Closing both, as v2.0 does, made the token-delivery gap
reliably fatal: the hardened middleware rejects every mutating request from a client
without a token.

### Before / after

Server side, nothing to do — both halves are automatic:

- `X-CSRF-Token` is set on **every** response to a request carrying a session.
- `GET /csrf` is registered by default, returning the token in the body *and* the header.

Client side, the contract is three lines:

```js
// BEFORE — one shot at the header, and no way to recover if you missed it
// (most apps had no working CSRF story at all)

// AFTER
// 1. On boot
const { body } = await (await fetch('/csrf', { credentials: 'include' })).json()
let csrfToken = body.csrf_token

// 2. Re-read it from every response
const fresh = res.headers.get('X-CSRF-Token')
if (fresh) csrfToken = fresh

// 3. Send it on every mutating request
headers: { 'X-CSRF-Token': csrfToken }
```

The token does **not** rotate per request, so step 2 is cheap: rotating would invalidate
the token an in-flight request from another tab is carrying.

See [`docs/CSRF.md`](docs/CSRF.md) for the full contract and a table mapping each
rejection message to its cause.

### How to detect whether you are affected

```bash
# A route already at /csrf will collide at boot with the duplicate-route check
grep -rn '"/csrf"' --include="*.go" .

# Client-side: is anything reading the token header at all?
grep -rn "X-CSRF-Token\|csrf" --include="*.js" --include="*.ts" --include="*.vue" .
```

If `/csrf` is taken, either move the framework's endpoint or replace it:

```go
// Move it
spa := controller.NewCsrfController()
spa.Path = "/api/v1/csrf"
routeProvider.DisableCsrfController()
routeProvider.AddController(spa)

// Or drop it and mount your own
routeProvider.DisableCsrfController()
```

For a cross-origin SPA, expose the header so the client can read it:

```go
middleware.NewCorsMiddleware(middleware.Options{
    AllowedOrigins:   []string{"https://app.example.com"},
    ExposedHeaders:   []string{core.CSRFTokenHeader},
    AllowCredentials: true,
})
```

### Mechanical or judgement?

**Mechanical** server-side. **Judgement** client-side, and it is the work that actually
matters: an app that never had a working CSRF story now needs one, because the middleware
no longer has a hole to fall through.

Two smaller changes in the same area: `CsrfService.ValidateCSRFToken` now compares in
constant time (it used `strings.Compare` under a comment claiming otherwise), and the
duplicated `csrfTokenKey` literal is `core.CSRFSessionKey` in both places.

---

## 17. Malformed bodies are 400, not a 301 redirect (behaviour)

### What broke and why it had to

`err.NewInputBodyParseError` had **zero call sites** while `http/error.go` registered a
handler for it, so app authors reasonably believed it was the hook for malformed bodies. It
was not. Worse, the JSON parser's error classification was dead code:

```go
case errors.Is(err, &json.SyntaxError{}):   // always false
```

`errors.Is` compares with `==` for a type implementing no `Is` method, and both operands
were freshly allocated pointers. Every malformed body fell to the default branch, which
produced an *empty* `ValidationErrors` — a non-nil error with no entries — and
`processValidationErrors` answered it with a **301 redirect to the `Referer`**.

### Before / after

| Request body | Before | After |
|--------------|--------|-------|
| `{"name": "x"` (truncated) | 301 to `Referer` | 400, `InputBodyParseError` |
| `[{"name":"x"}]` (top-level array) | 301 to `Referer` | 400, `InputBodyParseError` |
| over the size limit | 301 to `Referer` | 400, `InputBodyParseError` |
| over the nesting limit | 301 to `Referer` | 400, `InputBodyParseError` |
| `{"age": "not-a-number"}` | 422, `ValidationErrors` | 422, `ValidationErrors` (unchanged) |
| `null` | accepted as no fields | unchanged |

The split is the point: a body that cannot be parsed at all is a 400-class failure, and a
body that parses but holds a value a field rejects is a 422-class one. Conflating them gave
a client no way to tell "I sent broken JSON" from "field 3 is out of range".

If you registered a handler for `InputBodyParseError`, it now actually fires. Report the
*reason*, not `err.Error()`:

```go
func inputErrorHandler(err error, message core.HttpMessage) {
    reason := err.Error()
    if parseError, ok := err.(*error2.InputBodyParseError); ok {
        // Error() includes the raw body for the log's benefit, and a body that failed
        // to parse is exactly the kind that might carry a password halfway through.
        reason = "the request body could not be parsed"
        if parseError.RawError != nil {
            reason = parseError.RawError.Error()
        }
    }
    message.Response().JSON(dto.ReturnObject(nil, core.BadRequestHttpStatus, reason), 400)
}
```

### How to detect whether you are affected

```bash
grep -rn "InputBodyParseError" --include="*.go" .
```

If you registered a handler, check it does not echo the body. If you did not, the
framework's default now answers with the negotiated envelope instead of an empty
`text/plain` 400.

Client-side, look for anything that treated a 301 from a POST as meaningful.

### Mechanical or judgement?

**Mechanical.** Nothing sensible depended on the redirect.

---

## 18. 404, 405 and error handlers are content-negotiated (behaviour)

### What broke and why it had to

The router answered an unknown route with `Bytes(nil, 404)` — an empty body with no content
type — and left chi's bare `405` in place with nothing registered for it. Two of the four
error handlers wrote a literally empty body: `processInputParsingError` was
`Text("", 404)`, `processJwtAuthError` was `Text("", 401)`. An API client got nothing it
could parse and never the standard envelope.

### Before / after

```
# BEFORE
$ curl -i -H 'Accept: application/json' http://localhost:8080/api/nope
HTTP/1.1 404 Not Found
Content-Length: 0

# AFTER
HTTP/1.1 404 Not Found
Content-Type: application/json
{"status":404,"status_code":"NOT_FOUND","body":null,"errors":["No route matches this request"]}
```

`405` is answered at all now, and names the offending method. A browser still gets plain
text: the rule is the api namespace, a JSON `Content-Type`, a *specific* JSON `Accept`, or
an `/api/` path prefix — a browser's `Accept: text/html,...,*/*` deliberately does not
match the wildcard, or every 404 would come back as JSON.

An app's own `SetNotFound` handler still wins, so negotiation is a default rather than an
override.

### How to detect whether you are affected

```bash
# Anything asserting on an empty 404/405/401 body
grep -rn "StatusNotFound\|StatusMethodNotAllowed\|404\|405" --include="*_test.go" .
```

The status codes are unchanged; only the bodies and content types are new. A client that
ignores the body is unaffected.

### Mechanical or judgement?

**Mechanical**, and usually nothing to do.

---

## 19. `${VAR}` substitution: what a missing variable now does

This is not a break — it is the fix to one — but it changes what a misconfigured
environment does, and the previous behaviour was the opposite of what the config sample
implies.

### What it used to do

```go
envValue := os.Getenv(name)   // "" for both "unset" and "set to empty"
viper.Set(k, envValue)        // the *override* layer, unbeatable by any default
```

`os.Getenv` cannot tell an unset variable from an empty one, and the result went into
viper's highest-precedence layer. So a placeholder for a **missing** variable produced a
key that was present, empty, and unbeatable — which defeated the secure-cookie default
this same release added. With the documented `secure: ${SESSION_COOKIE_SECURE}` and the
variable unset — a fresh checkout, a CI runner, a container missing one env line —
`IsSet` was true, the secure-by-default branch was skipped, `GetBool("")` returned false,
and the session cookie shipped **without** `Secure`.

### What it does now

| Case | Behaviour |
|------|-----------|
| `FOO=bar` | substituted, as before |
| `FOO=` (explicitly empty) | substituted with `""` — an empty string is a legitimate value for a blank cookie domain |
| `FOO` unset, ordinary key | **blanked**, with a warning naming the key and the variable |
| `FOO` unset, security-relevant key | **the boot fails** |

Blanking rather than retaining the literal is a correction to the first version of this fix.
Retaining looked like the better diagnostic — a connection error naming `${DB_HOST}` beats one
naming the empty string — but a literal is a value no consumer accepts, and the failure that
matters is the silent one: an unresolved `auth.session.cookie.domain` became
`Domain: "${COOKIE_DOMAIN}"`, an invalid cookie attribute, so the browser dropped
`Set-Cookie` and login failed with nothing in any log. The warning already names every
unresolved key, so nothing diagnostic is lost.

`config.KeepUnresolvedLiterals()` opts back in, for a host key where the literal genuinely
reads better in a connection error:

```go
config.ParseWithOptions([]string{"config/config"}, []config.ResolveOption{
    config.KeepUnresolvedLiterals(),
})
```

Note what is **not** available either way: the key cannot be *unset*. Every key
`viper.AllKeys()` reaches is already in viper's config layer, which outranks `SetDefault`, so
a default will not apply to it whichever option you choose.

The security-relevant keys are `auth.jwt.secret` and `auth.session.cookie.secure`.

### **Check your environments**

**An app carrying `secure: ${SESSION_COOKIE_SECURE}` in its config must confirm the
variable is actually set in every environment**, because until now a missing one silently
disabled the flag and nothing said so:

```bash
# For each environment
grep -rn '\${' config/
# then, for each placeholder found, confirm it is set where the app runs
```

If a security-relevant one is missing, the app now refuses to boot with:

```
config: security-relevant key(s) reference environment variables that are not set:
auth.session.cookie.secure (${SESSION_COOKIE_SECURE}). Set them, or remove the
placeholder so the framework's secure default applies — an unset placeholder must
never silently weaken security
```

Removing the placeholder entirely is a valid fix: the framework's default is
`secure: true`.

### Corrections to the brief

`IMPROVEMENT_v2.1.md` prescribed "leave the key alone so defaults and `IsSet` behave", and
both halves needed correcting.

The stated reason never held: a key written in `config.yaml` lives in viper's *config* layer,
which outranks `SetDefault`. `IsSet` stays true and `GetString` returns the literal `${VAR}`
no matter what the parser does — verified with a probe, not assumed. That is why the real
safety net is the hardened `SessionCookieSecure()`, which treats an unparseable or empty value
as `true`, plus the boot failure above.

And leaving the key alone turned out to be the wrong remedy too, for the reason in the table
above. Corrected to blanking.

---

## 20. The module path gains `/v2`

### What broke and why it had to

Go requires a `/vN` suffix in the module path for major version 2 and above. Without it:

```
require github.com/osbits/gorgany v2.0.0
→ go: errors parsing go.mod: require github.com/osbits/gorgany:
  version "v2.0.0" invalid: should be v0 or v1, not v2
```

So v2.0.0 could not be required as v2.0.0 by any consumer. `+incompatible` does not apply,
because the module has a `go.mod`. The only ways out were to rename the module, to renumber
the release as v1.6.0 — which contradicts everything else in this document — or to make every
consumer carry a `replace` directive forever.

### Before / after

```
// go.mod, BEFORE
require github.com/osbits/gorgany v1.5.1
```

```
// go.mod, AFTER
require github.com/osbits/gorgany/v2 v2.0.0
```

Every import path gains `/v2`:

```go
// BEFORE
import (
    "github.com/osbits/gorgany/v2/app/core"
    "github.com/osbits/gorgany/v2/http/middleware"
)
```

```go
// AFTER
import (
    "github.com/osbits/gorgany/v2/app/core"
    "github.com/osbits/gorgany/v2/http/middleware"
)
```

### How to detect whether you are affected

Every app is affected. It is one sweep:

```bash
grep -rl '"github.com/osbits/gorgany/v2' --include="*.go" . \
  | xargs sed -i '' 's|"github.com/osbits/gorgany/v2/|"github.com/osbits/gorgany/v2/|g; s|"github.com/osbits/gorgany/v2"|"github.com/osbits/gorgany/v2"|g'
gofmt -w .
go mod edit -require=github.com/osbits/gorgany/v2@v2.0.0 -droprequire=github.com/osbits/gorgany
go mod tidy
```

On GNU sed, drop the `''` after `-i`.

Check for the module path outside Go files too — a `replace` directive, a tool config, a
generated file:

```bash
grep -rn "osbits/gorgany" --include="*.mod" --include="*.yml" --include="*.yaml" \
  --include="Dockerfile*" --include="Makefile" .
```

### Mechanical or judgement?

**Mechanical**, and the compiler finds anything the sweep missed.

---

## 21. `DbProvider` no longer registers a database driver

### What broke and why it had to

`provider.DbProvider` blank-imported `db/sql/driver/builtin`, which registers both engines.
So every app using the standard bootstrap linked `gorm.io/driver/mysql`,
`go-sql-driver/mysql` and `filippo.io/edwards25519` whether or not it would ever speak
MySQL — dependency surface and attack surface the app never chose, and with no way to opt
out.

### Before / after

Add one blank import, next to your own provider package's imports:

```go
// AFTER — Postgres only
import (
    _ "github.com/osbits/gorgany/v2/db/sql/driver/postgres"
)

// AFTER — MySQL only
import (
    _ "github.com/osbits/gorgany/v2/db/sql/driver/mysql"
)

// AFTER — both, or if you would rather not think about it
import (
    _ "github.com/osbits/gorgany/v2/db/sql/driver/builtin"
)
```

`driver/builtin` behaves exactly as before, so importing it is a complete no-op change.

### How to detect whether you are affected

This one **compiles cleanly** and fails at boot, so `go build ./... && go vet ./...` will
not find it:

```
datasource config: no datasource drivers are registered, so "postgres_gorm" cannot be
resolved. Import the engine you use for its side effects —
_ "github.com/osbits/gorgany/v2/db/sql/driver/postgres" or
_ "github.com/osbits/gorgany/v2/db/sql/driver/mysql", or
_ "github.com/osbits/gorgany/v2/db/sql/driver/builtin" for both — typically next to your
provider package's imports.
```

Boot the app once. That is the check.

After adding the single-engine import, confirm the other engine is gone:

```bash
go mod tidy
go list -deps ./cmd/server | grep mysql   # should be empty for a Postgres-only app
```

### Mechanical or judgement?

**Mechanical.** One import line, and the error message names it.

---

## 22. A validation failure is 422 for an API client, 303 for a browser (behaviour)

### What broke and why it had to

`processValidationErrors` answered **every** caller with a `301` redirect to the `Referer`,
API clients included — so the reshaped validation payload from §12 was unobservable through
the framework's own handler. `docs/VALIDATION.md` and this document both already described
the `422`.

Two more defects sat in the same four lines. A `301` is permanently *cacheable* and browsers
rewrite it to a `GET`, so a browser could cache "POST this URL → GET that one" indefinitely;
`303 See Other` is the post-redirect-get status and is not cacheable by default. And the
`Referer` was passed through unchecked, so a client that sends none got `Location: ""`.

### Before / after

```
# BEFORE — every caller
HTTP/1.1 301 Moved Permanently
Location: http://example.invalid/page
```

```
# AFTER — API client (JSON Accept, JSON Content-Type, /api/ prefix, or api namespace)
HTTP/1.1 422 Unprocessable Entity
{"status":422,"status_code":"VALIDATION","body":null,
 "errors":[{"field":"title","err":"title is required","rule":"required","path":"title"}]}

# AFTER — browser form with a Referer
HTTP/1.1 303 See Other
Location: http://example.invalid/form

# AFTER — no Referer: there is nowhere to go back to, so the errors are returned
HTTP/1.1 422 Unprocessable Entity
```

The browser branch still flashes the errors into the session under the same key, so a
server-rendered form renders them exactly as before.

### How to detect whether you are affected

```bash
# A client that treats a 301 from a POST as meaningful
grep -rn "301\|StatusMovedPermanently" --include="*.js" --include="*.ts" --include="*.go" . \
  | grep -vi redirect

# Your own ValidationErrors handler, which shadows this one and is unaffected
grep -rn '"ValidationErrors"\|"ValidationError"' --include="*.go" .
```

If you registered your own handler to get a 422 — which was the only way to get one — you
can now delete it. Compare it against the framework's first: the shapes are the same.

### Mechanical or judgement?

**Mechanical** if you had your own handler. **Judgement** for a client that relied on the
redirect, though it is hard to see how one could have.

---

## 23. `omitempty` and embed collisions now match `encoding/json` (behaviour)

### What broke and why it had to

The envelope marshaller exists to serialise a DTO the way `encoding/json` would, and it
diverged in two places.

`omitempty` was gated on `reflect.Value.IsZero()`, which is a different predicate from
`encoding/json`'s:

| field with `,omitempty` | `encoding/json` | before | after |
|---|---|---|---|
| `[]string{}` (non-nil, len 0) | omitted | **emitted** | omitted |
| `map[string]string{}` (len 0) | omitted | **emitted** | omitted |
| `time.Time{}` | emitted | **omitted** | emitted |
| zero nested struct | emitted | **omitted** | emitted |
| nil slice/map/ptr, `""`, `0`, `false` | omitted | omitted | omitted |

The `time.Time` row is the one to check for. A `CreatedAt time.Time` tagged
`json:"created_at,omitempty"` on a not-yet-persisted record used to vanish from the response
and now appears as `"0001-01-01T00:00:00Z"`.

And when an inlined embed and an outer field shared a wire name, the winner depended on
declaration order — the embed was merged with a map copy, the outer field assigned, so
whichever came later won. `encoding/json` always prefers the shallower field.

| declaration order | `encoding/json` | before | after |
|---|---|---|---|
| embed first, outer second | outer | outer | outer |
| outer first, embed second | outer | **embed** | outer |

### How to detect whether you are affected

```bash
# Response DTOs using omitempty on a struct or a slice/map field
grep -rn 'omitempty' --include="*.go" . | grep -iE 'time\.|\[\]|map\['

# DTOs with an anonymous embed and a field that might share one of its wire names
grep -rn -B 3 -A 8 '^\s*[A-Z][A-Za-z0-9]*Dto$' --include="*.go" .
```

The reliable check is to diff a response capture against one from before the upgrade. If a
client is strict about key presence, the `time.Time` row is where it will notice.

### Mechanical or judgement?

**Judgement**, per DTO — but only for the ones the table above actually touches. Dropping
`,omitempty` from a `time.Time` field restores the old *intent* if that is what you wanted;
keeping it now matches `encoding/json`.

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

Then the checks for this round:

```bash
# 5. A job actually fires. Register one at a short interval and watch the log.
#    Nothing in the repo noticed that jobs never ran, so this needs eyes.

# 6. A malformed body is 400 with a parseable envelope, not a 301
curl -i -X POST -H 'Content-Type: application/json' --data '{"a":' \
  http://localhost:8080/api/<a-body-route>

# 7. A CSRF token can be obtained, and the middleware accepts it
TOKEN=$(curl -s -c /tmp/j http://localhost:8080/csrf | sed 's/.*"csrf_token":"\([^"]*\)".*/\1/')
curl -i -b /tmp/j -X POST -H "X-CSRF-Token: $TOKEN" http://localhost:8080/<a-mutating-route>

# 8. An unknown route returns the envelope to an API client
curl -i -H 'Accept: application/json' http://localhost:8080/api/definitely-not-a-route

# 9. Every ${VAR} in config is set where the app runs
grep -rn '\${' config/
```

And, if you run MySQL:

```bash
# 10. orm.Create actually inserts a row and reads its key back. The MySQL driver
#     shipped in v2 with a green suite while being unable to insert anything,
#     because every dialect test asserts strings.
```

The framework's own `testsupport` package is the shortest way to write checks 5 and
10 as real tests — see [`docs/TESTING.md`](docs/TESTING.md).
