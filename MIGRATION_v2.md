# Migrating an app from gorgany v1.5.1 to v2.2.0

> **This document targets v2.2.0.** A second security audit of the v2.0.0 remediation found
> thirteen further release blockers; closing them added §35–§49, several of which are
> behaviour changes the compiler will not catch. **If you already pulled the `v2.0.0` tag,
> read §35–§49 as well as the rest** — that tag was never released and is superseded.

This document has one section per breaking change. Each says what broke and why,
gives compiling before/after code, tells you how to detect whether you are
affected, and states whether the fix is mechanical or needs judgement.

> **Two things will surprise you if you read nothing else.**
>
> 1. **[§24](#24-the-session-cookie-is-__host--prefixed-behaviour-security): the session
>    cookie is renamed, and every signed-in user is logged out on the first request
>    after the upgrade.** Nothing about this is a compile error, and nothing about it
>    shows up in a test suite that starts each test with a fresh cookie jar.
> 2. **[§31](#31-public-is-nosniff--attachment-and-svg-stops-rendering-behaviour-security):
>    `/public/*` no longer renders SVG.** `<img src="/public/logo.svg">` breaks. SVG
>    icons are a common static asset, so grep for them before you deploy.
>
> **Read “Behaviour changes the compiler will not catch” first.**
>
> Nineteen of these changes compile cleanly and change what your app *does*. They
> matter more than the signature changes, because `go build` will not point at
> them and a passing test suite may not either. Most of them are security-relevant,
> and several (`Query()` memoization, the validation error shape and delivery,
> `omitempty`, the upload extension, the `/public/*` retyping) can change what a client
> sees silently.

**Order of work.** Start with [§20](#20-the-module-path-gains-v2) — nothing else can be
resolved until the import paths change, and it is one scripted sweep. Then do §1–§4a and
§12/§16/§17/§22/§23, which need judgement and testing. Then run `go build ./...` and let the
compiler drive §5–§10 and §11/§13/§14/§15. Then §24–§34, the security round: §26–§28 are
compile breaks the compiler will drive, §24/§25/§29–§34 are behaviour and need judgement,
and §25 in particular is a **compile-clean** break in an app that has its own login handler
— it will build and quietly log nobody in. Finally boot the app once for §21 and §28, which
compile cleanly and only fail at runtime.

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
| [§24](#24-the-session-cookie-is-__host--prefixed-behaviour-security) | Session cookie renamed to `__Host-GRG_SESSION_ID` | **Every signed-in user is logged out on upgrade** |
| [§25](#25-login-rotates-the-session-identifier-behaviour-security) | `Login` rotates the session and mints a new CSRF token | Your own login handler builds fine and logs nobody in; login responses carry two `Set-Cookie`s |
| [§29](#29-request-bodies-are-capped-before-they-are-read-behaviour) | Request bodies capped by `MaxBytesReader` | A large upload or streamed body is refused at the reader |
| [§30](#30-uploads-are-stored-by-sniffed-content-not-by-filename-behaviour-security) | Upload extension comes from sniffed content; unlisted types refused | Stored filenames change; some uploads that worked are now rejected |
| [§31](#31-public-is-nosniff--attachment-and-svg-stops-rendering-behaviour-security) | `/public/*` is `nosniff` + `attachment`, SVG retyped | **`<img src="/public/logo.svg">` stops rendering**; `.html` downloads instead of opening |
| [§32](#32-mail-refuses-crlf-in-a-header-value-behaviour-security) | `MailService.Send` rejects CR/LF in headers, emits CRLF and RFC 2047 | A send that used to succeed now errors; golden-file tests break |
| [§34](#34-security-headers-are-emitted-by-default-behaviour-security) | `nosniff`, `X-Frame-Options: SAMEORIGIN`, `Referrer-Policy`, HSTS on every response | A page framed by a third party stops loading; a template rendered without a `Content-Type` now gets `text/html` |

Two that compile cleanly and fail at **boot** rather than at request time:
[§21](#21-dbprovider-no-longer-registers-a-database-driver) — `DbProvider` no longer
registers a database driver, so the app needs one blank import — and
[§28](#28-authjwtsecret-must-be-a-real-key-boot-failure) — an absent, empty or weak
`auth.jwt.secret` now panics the boot.

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

**Step 2 is mandatory, not an optimisation.** The token does not rotate per request —
rotating would invalidate the token an in-flight request from another tab is carrying —
but the session *is* replaced more often than you would guess, and one of those
replacements replaces the token:

- **The session's own rotations keep the token.** The framework moves a session onto a
  fresh identifier once it has been idle past the activity timeout, and again once it is
  older than the rotation interval. Both carry the existing token across, deliberately:
  they happen on a request the client did not ask to rotate anything on, so a client
  cannot know one occurred, and minting a new token there would reject the next form
  submitted from an already-rendered page. (v2.0 as originally written minted a new one
  and produced exactly that spurious 403.)
- **Logging in replaces it.** `Login` rotates the session identifier and mints a **new**
  token — see [§25](#25-login-rotates-the-session-identifier-behaviour-security). Every
  secret a pre-login session held was chosen by whoever presented that session, so
  carrying the token across the authentication boundary would leave a party who learned
  the pre-login token holding a valid token for the authenticated one. The login
  response carries the replacement in `X-CSRF-Token`; a client that keeps its pre-login
  token has every mutating request rejected with `Invalid CSRF token` until it calls
  `GET /csrf` again.

So: a session keeps one token for as long as it is the same session **and the same
principal**. Most of the time the header you read back is the token you already had,
which is what makes step 2 cheap — but it is not always, and the request where it is not
is the login.

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
    "github.com/osbits/gorgany/app/core"
    "github.com/osbits/gorgany/http/middleware"
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
# Guard: this must print nothing. The rewrite below is not idempotent — running it on a
# tree that is already on /v2 produces github.com/osbits/gorgany/v2/v2.
grep -rn '"github.com/osbits/gorgany/v2' --include="*.go" .

grep -rl '"github.com/osbits/gorgany' --include="*.go" . \
  | xargs sed -i '' \
      -e 's|"github.com/osbits/gorgany/|"github.com/osbits/gorgany/v2/|g' \
      -e 's|"github.com/osbits/gorgany"|"github.com/osbits/gorgany/v2"|g'
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

## 24. The session cookie is `__Host-` prefixed (behaviour, security)

### What broke and why it had to

The session cookie was named by a compile-time constant, `core.SessionCookieName =
"GRG_SESSION_ID"`. An unprefixed cookie name is a token that **any** host under the
registrable domain can also write, even when the app's own cookie is host-only.
`sibling.example.com` sends

```
Set-Cookie: GRG_SESSION_ID=chosen; Domain=example.com; Path=/login
```

RFC 6265 serialises the more specific path first, `http.Request.Cookie` returns the first
match, and the framework reads the identifier the sibling picked. Assigning an
authenticated user to an identifier a third party chose is session fixation, and because
the name was a constant an app could not opt out of it — there was no hook to make it
`__Host-` prefixed. A browser refuses to let any other host set a `__Host-` cookie for
yours, which is the property that closes it.

So the cookie is now named **`__Host-GRG_SESSION_ID`** by default, and the constant is
gone.

> **This logs every existing session out on upgrade.** A browser holding the old cookie
> sends a name the framework no longer reads, so on the first request after the deploy
> every signed-in user starts a fresh anonymous session. There is deliberately **no**
> fallback to the old name — reading the unprefixed cookie when the prefixed one is
> absent would reopen exactly the hole the prefix closes. Announce it, or deploy in a
> window where a mass re-login is acceptable.

Two configurations keep the old name, because a `__Host-` cookie cannot exist without
them:

| Configuration | Cookie name | Why |
|---|---|---|
| default (nothing set) | `__Host-GRG_SESSION_ID` | Secure, `Path=/`, no `Domain` — no other host can set it |
| `auth.session.cookie.secure: false` | `GRG_SESSION_ID` | a `__Host-` cookie is only accepted when Secure |
| `auth.session.cookie.domain: example.com` | `GRG_SESSION_ID` | **a `__Host-` cookie may not carry a `Domain`** |

Both are explicit opt-outs rather than silent downgrades: an app that takes one keeps the
pre-upgrade cookie name and logs nobody out, and also keeps the exposure described above.
The form in use is logged once, with its reason, on the first resolution:

```
INFO auth: session cookie is named "__Host-GRG_SESSION_ID" — host-locked: no other host can set this cookie for yours
INFO auth: session cookie is named "GRG_SESSION_ID" — auth.session.cookie.domain is "example.com", and a __Host- cookie may not carry a Domain; this app has opted into a cookie its sibling subdomains can also set
```

Note the interaction with local development: `auth.session.cookie.secure: false` is the
documented setting for plain HTTP, and it also drops the prefix. That is correct — a
`__Host-` cookie that is not Secure is discarded by the browser without a word, so
emitting one would make every local request look logged-out with nothing anywhere
reporting an error. It does mean your development cookie name differs from production.

### Before / after

```go
// BEFORE
cookie := messageContext.GetCookieManager().GetCookie(core.SessionCookieName)

messageContext.GetCookieManager().SetCookie(&http.Cookie{
    Name:     core.SessionCookieName,
    Value:    session.GetId(),
    Path:     "/",
    MaxAge:   maxAge,
    Secure:   auth.SessionCookieSecure(),
    HttpOnly: true,
    SameSite: http.SameSiteLaxMode,
    Domain:   viper.GetString("auth.session.cookie.domain"),
})

// AFTER
cookie := messageContext.GetCookieManager().GetCookie(auth.SessionCookieName())

messageContext.GetCookieManager().SetCookie(
    auth.NewSessionCookie(session.GetId(), maxAge, http.SameSiteLaxMode))
```

`auth.NewSessionCookie(value string, maxAge int, sameSite http.SameSite) *http.Cookie` is
the only correct way to build the cookie. The name, `Secure` and `Domain` are one
decision, not three: pairing a `__Host-` name with a `Domain` produces a cookie no browser
keeps, and there is no diagnostic when that happens. Use `maxAge < 0` to expire it.

The full replacement API:

| Gone | Use |
|---|---|
| `core.SessionCookieName` (const) | `auth.SessionCookieName()` — the resolver, on both sides |
| — | `core.SessionCookieBaseName` = `"GRG_SESSION_ID"` |
| — | `core.HostPrefixedSessionCookieName` = `"__Host-GRG_SESSION_ID"` |
| — | `auth.SessionCookieDomain()`, `auth.ConfigSessionCookieDomain` |
| — | `auth.NewSessionCookie(value, maxAge, sameSite)` |

The constant was deliberately **not** kept as a deprecated alias. Code reading the cookie
under a hard-coded name would still compile and would silently read the wrong cookie; a
compile error naming the resolver is the safer outcome.

### How to detect whether you are affected

```bash
# Anything naming the cookie, in Go or anywhere else
grep -rn 'SessionCookieName\|GRG_SESSION_ID' --include='*.go' .
grep -rn 'GRG_SESSION_ID' --include='*.js' --include='*.ts' --include='*.yaml' \
  --include='*.yml' --include='*.conf' .

# Do you already opt out? Either of these keeps the old name.
grep -rn 'auth.session.cookie' config/
```

Three places outside Go code are worth checking specifically, because none of them
produce a compile error:

- **A reverse proxy or CDN rule keyed on the cookie name** — cache-bypass rules, sticky
  sessions, WAF exemptions. `__Host-GRG_SESSION_ID` will not match `GRG_SESSION_ID`
  exactly, though a suffix or prefix match may.
- **A test suite or a synthetic monitor** that sets the cookie by name.
- **Anything reading the cookie in the browser.** It is `HttpOnly`, so nothing legitimate
  should be — but a smoke test driving a real browser may.

Every app is affected in the sense that every session ends. Nothing needs changing for
that; it just needs to be known before rather than after.

### Mechanical or judgement?

**Mechanical** in code — the compiler finds every use of the constant. **Judgement** on
the deploy: whether to accept the mass logout (the default, and the point), or to set
`auth.session.cookie.domain` and keep the old name along with the exposure. Do not set
the domain purely to avoid the logout; set it only if you genuinely share a session across
subdomains.

---

## 25. `Login` rotates the session identifier (behaviour, security)

### What broke and why it had to

`StandardAuthStrategy.Login` took whatever session the client presented and assigned the
user id to it. That means the identifier that authenticates after a login is one the
client presented — and a client can be made to present an identifier somebody else chose:
a cookie written by a sibling of the registrable domain ([§24](#24-the-session-cookie-is-__host--prefixed-behaviour-security)),
an XSS on the app, or a minute alone with an unlocked browser. The victim then logs in on
it and the attacker's copy of that identifier is authenticated as the victim, with
nothing to notice and nothing to expire. Neither existing rotation trigger helped: they
fire on inactivity and on age, and the victim's own page loads keep refreshing the
activity stamp.

`Login` now:

1. revokes the presented session **first**, and abandons the login if there was nothing
   to revoke (somebody else already revoked it, so no identity is carried anywhere);
2. mints a fresh identifier;
3. carries over the user id and the last-activity stamp, and **nothing else** —
   attributes on a pre-authentication session were chosen by whoever presented it;
4. mints a **new** CSRF token, deliberately not carrying the old one;
5. persists through the storage, so a login whose write never landed is reported as a
   failed login rather than as a session nothing holds.

It fails closed at every step: if the old session cannot be revoked, or the new one
cannot be persisted, it returns an error and no session at all.

### Before / after — your own login handler

**This is the part that will not produce a compile error.** Because the identifier
changes, the session your request resolved on the way in no longer exists when `Login`
returns, and two places cache it for the life of the request: the message's session
scope, and the message context — which `StandardAuthStrategy.ResolveSessionId` consults
*before* it looks at the cookie. A handler that does not republish leaves
`message.Session().Get()` and `IsLoggedIn` looking at a deleted session, so a login that
fully succeeded is indistinguishable from one that failed.

```go
// BEFORE — compiles against v2 unchanged, and logs nobody in
_, err := thiz.authContext.ResolveAuthStrategyByContext(message.Context()).
    Login(user, message.Context())
if err != nil { /* ... */ }
message.Response().Redirect(homeUrl, 301)

// AFTER
session, err := thiz.authContext.ResolveAuthStrategyByContext(message.Context()).
    Login(user, message.Context())
if err != nil { /* ... */ }

grghttp.PublishSession(message, session) // github.com/osbits/gorgany/v2/http

message.Response().Redirect(homeUrl, 301)
```

`http.PublishSession(message core.HttpMessage, session core.ISession)` installs the
session on the session scope **and** the message context, drops the request's memoised
identity (see [§33](#33-rolebasedaccesscontrol-holds-no-per-request-state-behaviour)),
and rewrites `X-CSRF-Token` on the response from the new session's token. It replaces the
hand-rolled `message.Session().(core.IEditableSessionScope).Set(...)` dance, which
covered only the first of those four.

### Two consequences of rotating

- **A login response now carries two `Set-Cookie` headers for the session** when the
  request arrived without a session cookie: `SessionMiddleware` starts one, and the login
  then rotates it. Browsers apply them in order and keep the last, and so does any real
  cookie jar. A hand-written client — or a test — that takes the **first** match will pick
  up an identifier that has just been revoked. If you have such a client, take the last.
- **Every login creates and immediately revokes one extra session**: one extra `INSERT`
  and `DELETE` on database-backed storage.

### The already-authenticated guard has to go

`LoginController.Login` used to answer an already-authenticated request with a redirect
home. If you copied that idiom into your own handler, delete it. A login POST answered
with a redirect is a login POST whose credentials were never compared to anything, so
whoever the session already belonged to keeps it and the poster is handed that identity.
Turned around it is an attack: a party who can plant the session cookie plants their own
**signed-in** session rather than a blank one, the victim's login bounces off the guard,
their password is never checked, and they spend the visit inside the planted account
while the planter holds a live cookie for the same session.

Authenticating over a live session is safe now precisely because `Login` replaces the
session rather than reusing it — the hazard the guard was added for, two different user
ids reaching one session object, cannot happen when the object is replaced.

```go
// DELETE this from your POST handler
if strategy.IsLoggedIn(message.Context()) {
    message.Response().Redirect(homeUrl, 301)
    return
}
```

Keep it on the **GET**. An authenticated visitor asking for the login *form* belongs on
the home page, and a GET carries no credentials, so there is nothing to verify and
nothing to donate. (Make sure it `return`s — the framework's own `ShowLogin` did not, and
rendered the login form into a response that already carried the 301.)

A failed attempt deliberately changes nothing: an unknown username or a wrong password
leaves the session that was already there exactly as it was. Ending it would hand anybody
able to make a browser post the form a remote logout with no credentials at all, while a
caller who mistyped their own password loses nothing by typing it again.

### How to detect whether you are affected

```bash
# Your own calls to Login — every one of them needs a PublishSession
grep -rn '\.Login(' --include='*.go' . | grep -v '_test.go'

# The guard to delete, on POST handlers only
grep -rn -B 4 'IsLoggedIn(' --include='*.go' . | grep -v '_test.go'

# Clients or tests that read the first Set-Cookie rather than the last
grep -rn 'Cookies()\[0\]\|Set-Cookie' --include='*.go' --include='*.js' --include='*.ts' .
```

Runtime check, which is the one that matters:

```bash
# Log in, then immediately request something that requires a session, on the
# cookie the login response left behind. Before the fix this is a 401/redirect.
curl -s -c /tmp/j -X POST -d 'username=u&password=p' http://localhost:8080/login
curl -i -b /tmp/j http://localhost:8080/protected
```

### Mechanical or judgement?

**Mechanical** for the `PublishSession` call — one line per login handler. **Judgement**
for the guard: removing it means a `POST /login` with valid credentials switches the
signed-in user, which is a product decision as well as a security one. If your product
genuinely must refuse that, refuse it *after* verifying the credentials, not instead of.

---

## 26. `ISessionStorage`, `Logout` and the session interfaces

### What broke and why it had to

Session revocation was fail-open. Storage failures were discarded, so a logout that could
not delete the server-side session still expired the browser's cookie and reported
success — the user believes they are logged out, on a shared machine or after noticing
something wrong, while anybody holding a copy of the cookie stays authenticated. A login
whose row was never written reported success too. The mediator's cache decided whether a
session existed and was never revalidated, so a logout handled by one replica left the
session authenticating on every other replica indefinitely — no race and no error needed,
just two processes, which is the deployment database-backed sessions exist for.

Every method that changes stored state now reports failure, and the read distinguishes
"no such session" from "the store could not answer".

### Before / after — `core.ISessionStorage`

```go
// BEFORE
type ISessionStorage interface {
	ClearExpiredSessions()
	AddSession(session ISession)
	DeleteSession(session ISession)
	DeleteSessionById(id string)
	GetSessionById(id string) ISession
	SetSessionLifetime(lifetime time.Duration)
	GetSessionLifetime() time.Duration
	GetSessionRotationInterval() time.Duration
	GetSessionActivityTimeout() time.Duration
}

// AFTER — the last four are unchanged
type ISessionStorage interface {
	ClearExpiredSessions() error
	AddSession(session ISession) error
	DeleteSession(session ISession) error
	DeleteSessionById(id string) error
	GetSessionById(id string) (ISession, error)
	SetSessionLifetime(lifetime time.Duration)
	GetSessionLifetime() time.Duration
	GetSessionRotationInterval() time.Duration
	GetSessionActivityTimeout() time.Duration
}
```

**If you implement it:**

- Return the error from the four mutators instead of logging and swallowing it.
- **Deleting a session the store does not hold is not an error** — revocation is
  idempotent, so return `nil`.
- `GetSessionById` returns `(nil, nil)` when there is no such session and `(nil, err)`
  when the lookup itself failed. Do not collapse the second into the first: "the store is
  unreachable" and "this visitor has no cookie" lead to different decisions.
- **`AddSession` must refuse to recreate a session the store no longer holds.** An
  identifier that has been revoked and can still be written back is a revocation bypass.
  The framework's database storage checks the row; both shipped storages additionally
  keep a short-lived tombstone of the identifiers they revoked
  (`auth.SessionTombstoneRetention`, 25 h — one session lifetime plus a margin, which is
  as long as a tombstone can protect anything).

**If you call it:** handle the error. Where you cannot propagate it, fail closed — refuse
the request, or treat the session as absent — and report through `err.HandleError` rather
than discarding it.

### Before / after — `core.IAuthStrategy.Logout`

```go
// BEFORE
Logout(ctx context.Context)

// AFTER
Logout(ctx context.Context) error
```

**If you implement it:** return `nil` on success; return an error when the server-side
session could not be revoked, and **do not expire the session cookie in that case**. A
client that has thrown its cookie away cannot ask you to try again, so leaving it in
place is what lets the user retry. `JwtAuthStrategy.Logout` returns `nil` and does
nothing: a bearer token carries no server-side state to revoke.

**If you call it:** report the failure to the user. A handler that redirects to the login
page as though nothing happened is the fail-open behaviour this change exists to remove.

```go
// AFTER
if err := strategy.Logout(message.Context()); err != nil {
    grgerr.HandleError(err) // github.com/osbits/gorgany/v2/err
    message.RedirectWithFlash(loginUrl, http.StatusTemporaryRedirect, map[string]any{
        "error": "We could not end your session. You are still signed in; please try again.",
    })
    return
}
message.Response().Redirect(loginUrl, http.StatusTemporaryRedirect)
```

### `core.ISession` and `core.ISimpleStorage` are deliberately unchanged

This is the boundary, and an implementor needs to know where it is. `SetUserId`,
`SetExpiry`, `SetLastActivity` and the four `ISimpleStorage` methods (`GetItem`,
`SetItem`, `ClearItem`, `ClearItems`) **stay void**, even though a session backed by a
database persists on every write. Giving them errors would break every request scope,
view scope and test double in every downstream app for a signal almost no caller is in a
position to act on.

The consequence, if you implement a session whose write-through can fail: **remember the
failure and surface it at the next operation that can report one** — the storage call, or
`Login`/`Logout` — and expect callers of those to fail closed rather than assume the
write landed. The framework's own `DbSessionEntityWithMediator` reports through
`err.HandleError` from the setter and `StandardAuthStrategy.Login` re-persists through
the storage afterwards, which is both a reportable step and positive confirmation the
user id reached the store.

Also: **every field an implementation shares between concurrent requests must be
synchronised.** `GetUserId` in particular feeds authorization decisions, and the login
handler overwrites it on a session other requests are already authorizing against. Every
accessor on `auth.Session` and `auth.DbSessionEntity` now takes the session's mutex —
uniformly, rather than the subset somebody once saw a race on, which is how `GetUserId`
came to be unguarded while `GetExpiry` was locked.

### New: optional `core.ISessionRevoker`

```go
type ISessionRevoker interface {
	// RevokeSession deletes the session with this id and reports whether the store held it.
	RevokeSession(id string) (bool, error)
}
```

Optional and additive — an existing storage keeps compiling without it. It exists for
rotation, which carries the old session's user id onto a new identifier and must only do
that while the old session is still live: without the boolean, a request that loaded the
session just before the user logged out would mint a fresh durable session carrying the
logged-out user's identity and hand out its cookie. Plain `DeleteSessionById` cannot
express the difference, because deleting an absent session is deliberately not an error.
Both shipped storages implement it; one that does not gets the previous, unconditional
rotation behaviour.

### If you persist a session yourself

Hand the ORM a **detached copy**, never a session the mediator shares between requests.
`auth.DbSessionEntity.Snapshot()` is that copy, with its `Attributes` map deep-copied.
Locking the accessors does not make the live entity safe to persist: the ORM copies the
whole struct through `reflect` and the driver then marshals the *same* attribute map
inside the round trip, both outside anything the session's mutex guards. A Go map does
not merely tear under that — `json.Marshal` iterates it, the runtime notices the
concurrent write, and `fatal error: concurrent map iteration and map write` takes the
process down with every in-flight request. `AdoptPersistedMeta` copies back what the save
learned, so the next save knows whether its row exists.

### Other signature changes in the same area

```go
// auth.ISessionRepository
DeleteById(id string) error          // before
DeleteById(id string) (bool, error)  // after

// auth.DbSessionMediator
DeleteSession(id string) error          // before
DeleteSession(id string) (bool, error)  // after
```

`DbSessionRepository.DeleteById` is now a single `DELETE` rather than find-then-delete.
The old shape made an already-absent session an error — `FindById` returned `nil` and
`orm.Delete(nil)` answered `"domain cannot be nil"` — so a double-clicked logout, a
retried request or a row the sweep had already collected failed, and because the mediator
returned before purging its cache, the failure left the very entry the delete was
supposed to revoke still serving requests.

New constructor for building a storage outside the container:

```go
func NewDbSessionStorageWithRepository(
	sessionLifetime time.Duration, repository ISessionRepository) *DbSessionStorage
```

### How to detect whether you are affected

```bash
# Do you implement any of these?
grep -rn 'ISessionStorage\|IAuthStrategy\|ISessionRepository' --include='*.go' .

# Every call site of the reshaped methods
grep -rn 'GetSessionById\|AddSession\|DeleteSessionById\|DeleteSession(\|ClearExpiredSessions\|\.Logout(' \
  --include='*.go' .
```

### Mechanical or judgement?

**Mechanical** for the signatures — the compiler finds all of them. **Judgement** for what
each caller does with the error it now receives, and that is the whole point of the
change: an error handled by discarding it is the behaviour being removed. The one call
that must not be mechanical is `Logout` in a handler — see the snippet above.

### Cost

Database session storage is measurably chattier. Resolving a session is now a `SELECT`
per request rather than a map read after the first, and creating one costs
`SELECT` + `SELECT` + `INSERT` + `UPDATE`. That is the price of a logout on one replica
taking effect on the others. **It is not benchmarked**; if your session table is hot,
measure before you deploy.

---

## 27. `AccessCheckerMiddleware`, `HttpAccessCommand` and `HttpFilterCommand` are gone

### What broke and why it had to

`middleware.AccessCheckerMiddleware` was an authorization filter that enforced nothing.
The only code that could supply it an access decision was commented out, so the variable
was unconditionally `nil`, the nil branch always fired, and the middleware logged a
warning and called the next handler. A second fail-open sat behind it: even with a
decision available, a denial produced a `403` only when the `namespace` path parameter
was `api` — every other route fell through to the handler regardless.

`core.HttpAccessCommand` is deleted with it, including `IsAccessAllowed(ctx) bool` and
`FilterBuilder(ctx) IQueryBuilder`. It was already marked `Deprecated` and had no
remaining consumer.

`core.HttpFilterCommand` — `AllowFilterFields(ctx) []string` — is deleted for the same
reason. It had no implementations and no callers anywhere in the framework: nothing ever
invoked `AllowFilterFields`, so an application that implemented it in the belief that
filtering was restricted to the fields it named was filtering on anything a request asked
for. An extension point that reads as a control and enforces nothing is worse than no
extension point.

`core.IQueryBuilder` is unaffected.

### How to detect whether you are affected

```bash
grep -rn 'AccessCheckerMiddleware\|HttpAccessCommand\|HttpFilterCommand\|IsAccessAllowed\|AllowFilterFields' \
  --include='*.go' .
```

**No hits: nothing to do.** The framework never registered `AccessCheckerMiddleware` as a
default filter, so an app is affected only if it mounted it explicitly.

**If you mounted `AccessCheckerMiddleware`, read this carefully: those routes have had no
authorization on them.** Not weak authorization — none. The middleware could never obtain
a decision, so it allowed every request through, including unauthenticated ones, and said
so only in a log warning. If you mounted it over `/admin/**` or an equivalent and relied
on it, treat those routes as having been publicly reachable for as long as that mount
existed, and assess accordingly.

### Before / after

1. **Delete the mount.** Removing
   `WithMiddleware(middleware.AccessCheckerMiddleware{})` changes no runtime behaviour,
   because the middleware only ever called the next handler.
2. **Delete or repurpose your `HttpAccessCommand` implementations.** Any handler
   implementing `IsAccessAllowed` / `FilterBuilder` for this middleware's benefit was dead
   code. Keep `FilterBuilder` if your own code calls it directly; just drop the interface
   assertion.
3. **Put the authorization somewhere that runs.** There is no drop-in replacement,
   deliberately.

```go
// Option A — a real middleware
type AdminOnly struct{}

func (AdminOnly) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
    return func(message core.HttpMessage) {
        if !allowed(message.Context()) {
            // Every route, not just API ones. Write a response and do NOT call next.
            grghttp.WriteNegotiatedError(message, core.ForbiddenHttpStatus, "Forbidden")
            return
        }
        next(message)
    }
}

// Option B — in the handler, before touching any data.
// Per-field or per-record rules already have a home: model.RoleBasedAccessControl.
```

For `HttpFilterCommand`, delete the method or keep it — it is an ordinary method once the
interface is gone, and nothing called it before. If it named the fields you intended to be
filterable, **that restriction was never in effect.** Request-derived filters are
validated where they are built: `model.NewFilter` (`model/pagination.go`) rejects any
field for which `schema.LookUpField(field)` returns nil, which is the only path
query-string filters take. Restricting filtering beyond "must be a real column" is
`model.AccessControl.ValidateFilterAccess`, reached through `model.NewFilterWithAccess` —
express it there.

### Mechanical or judgement?

**Mechanical** to delete. **Judgement**, and possibly an incident review, for what
replaces it. Whichever route you take, add a test asserting that a request from an
unauthorized caller does not reach the handler. That assertion is what would have caught
the removed middleware.

---

## 28. `auth.jwt.secret` must be a real key (boot failure)

### What broke and why it had to

Nothing validated `auth.jwt.secret` anywhere, in any execution mode. An app could boot —
server or CLI, dev or prod — with the key absent from `config.yaml`, set to an empty
string, set to a YAML null, set to whitespace, left as an unresolved `${JWT_SECRET}`
literal, or set to something short enough to guess, with no error and no warning, and
then sign and verify tokens with it. `token.SignedString([]byte(""))` succeeds and
`jwt.Parse` verifies against `[]byte("")` just as happily, so `GenerateJwt(user, "")`
returned a working token and a `nil` error. Whoever knows the key mints a token naming any
user and role they like.

Four boundaries now refuse an unusable key:

- **Boot.** `AppProvider.Boot` calls `provider.ValidateJwtConfig()` immediately before it
  registers the JWT strategy, and **panics**. Both execution modes go through it, so the
  CLI is not a place a bad key is tolerated — migrations and jobs run under the same
  configuration as the server.
- **Config resolution.** `config.ResolveEnvPlaceholders` registers
  `auth.jwt.secret: ${JWT_SECRET}` as a default when the config declares an `auth.jwt`
  section but no secret. `viper.AllKeys()` never returned a key that appears nowhere in
  the file, so the absent-key case previously escaped the security-relevant placeholder
  guard entirely. It also means an app can keep the secret purely in the environment with
  no config line at all.
- **`JwtService`.** `GenerateJwt`, `ValidateJwt` and `ParseJwt` refuse. This is exported
  API an app can construct and call with a secret of its own choosing.
- **`JwtAuthStrategy.IsRequestMadeWithStrategy`.** It used to claim any request whose
  bearer token merely parsed. `AppProvider` registers the strategy under `"api"` for
  *every* app, and `AuthContext.ResolveAuthStrategyByContext` hands the request to the
  first strategy that claims it — so with an unusable key, any request carrying a
  self-signed bearer token captured strategy resolution, substituting the principal RBAC
  decides against, silently dropping session handling and turning `Logout` into a no-op.
  None of that required the app to have mounted `JwtMiddleware` or configured JWT at all.

**Is JWT in use?** The configuration answers it: an app whose config declares any
`auth.jwt.*` key has asked for token authentication and must configure it properly; an app
that declares none is not made to invent a secret in order to boot. The second half is
only safe because of the fourth boundary above.

### Before / after

```yaml
# BEFORE — booted clean, signed tokens with an empty key
auth:
  jwt:
    lifeTime: 3600

# AFTER — the boot fails naming auth.jwt.secret unless this resolves to >= 32 bytes
auth:
  jwt:
    secret: ${JWT_SECRET}
    lifeTime: 3600
```

```bash
openssl rand -base64 48
```

An app that does not use token authentication should have **no `auth.jwt` section at
all**.

The rules, all of them in `auth.ValidateJwtSecret`:

| Rejected | Why |
|---|---|
| empty or whitespace only | anyone can forge against it |
| a literal `${VAR}` | the variable was never substituted; a published string |
| shorter than `auth.MinJwtSecretLength` (32) | SHA-256's output size, RFC 2104's recommendation for an HMAC key, and the point past which lengthening buys nothing |
| 32+ bytes with fewer than 8 distinct bytes | `aaaa…` padded to length is long and has no entropy |

`jwt.Parse` is also pinned to HS256 at all three call sites. To be precise about what that
buys, because this option is usually described as the fix for algorithm confusion and
here it is not: golang-jwt/jwt v5 already refuses `alg: none`, its case variants and
every cross-family substitution. What was accepted before is substitution *within* the
HMAC family — a token the framework signed as HS256, re-signed as HS384 against the same
secret, verified.

### How to detect whether you are affected

```bash
# Every environment. This is a boot failure, so a missed one is an outage.
grep -rn 'JWT_SECRET' .env* deploy/ k8s/ docker-compose*.yml 2>/dev/null

# Length check, per environment
printf '%s' "$JWT_SECRET" | wc -c    # must be >= 32
```

The realistic ops mistake is `JWT_SECRET=` — an explicitly empty variable in a `.env`, a
compose file or a CI secret store. `os.LookupEnv` reports it as present, so substitution
writes it verbatim (deliberately: an empty string is a legitimate value for other keys)
and the result is an empty signing key. The boot now stops on it.

The boot error for an unresolved security-relevant placeholder also changed its advice. It
used to end "remove the placeholder so the framework's secure default applies" for every
key in `SecurityRelevantKeys`. That is true for `auth.session.cookie.secure`, which
defaults to `true`, and false for `auth.jwt.secret`, which has no default at all — so an
operator who followed the framework's own instruction converted a caught boot failure into
a silent authentication bypass. Keys in `config.KeysWithoutSecureFallback` now get advice
that fits them.

### Mechanical or judgement?

**Mechanical.** Generate a long random secret and set it everywhere. One thing to plan for:
**rotating the secret invalidates every outstanding token**, so expect clients to
re-authenticate. Tests that mint tokens with a short secret now get an error from
`GenerateJwt` and `false` from `ValidateJwt` — give them 32 bytes.

---

## 29. Request bodies are capped before they are read (behaviour)

### What broke and why it had to

Nothing capped a request body. The server was built with `ReadTimeout`, `WriteTimeout` and
`MaxHeaderBytes` and no body limit; `http.MaxBytesReader` appeared nowhere in the
framework; and the multipart parser read the whole form and consulted its size limits
afterwards. The argument to `ParseMultipartForm` is only the **in-memory** budget —
everything above it spills to temp files — so a single unauthenticated POST could write as
many bytes to the OS temp directory as it cared to send, and the per-file check ran once
they were already on disk. Only `Request().Body()` had a limit of its own, so
`BodyReader()`, `ParseForm` and any hand-rolled streaming had none.

Now:

- `app.MaxRequestBodyBytes()` resolves a ceiling, and `http.MaxBytesReader` applies it in
  two places: at the server boundary in `ServerApp.Run`, and per request in
  `Message.Init` before any middleware, handler or parser sees the body. The second is
  the one that protects an app mounting the router in a server it built itself.
- The ceiling **derives from the upload budget** rather than being an independent number,
  so the two cannot disagree and an app that raises its upload limit does not find the
  uploads refused elsewhere:

  ```
  max(32 MB, http.upload.maxMultipartSize) + 1 MB framing allowance
  ```

  The framing allowance is real, not padding: part boundaries, part headers and the
  trailing delimiter count against a reader limit but not against the upload budget, so a
  ceiling set to exactly the budget would reject an upload the upload limits allow.
- `ParseMultipartForm` is now called with the body already capped, so the total upload
  budget is enforced **while** the parse runs. The in-memory budget is the smaller of
  `http.security.body.maxSizeMB` (default 10 MB) and the total upload budget; `FormFile`
  and `GetFiles` used to pass `http.security.file.maxSizeMB`, default **100 MB**, per
  concurrent request.

### Before / after

```yaml
# Nothing to set unless you stream large bodies by hand or accept large uploads.
http:
  upload:
    maxMultipartSize: 104857600        # raises the derived ceiling with it
  security:
    body:
      maxRequestBytes: 209715200       # or override the derivation outright, in bytes
```

`app.ConfigMaxRequestBytes` is the key name; `app.DefaultMaxRequestBytes` is 32 MB.

If you need a per-route cap tighter than the global one:

```go
scope, ok := message.Request().(*grghttp.HTTPRequestScope)
if ok {
    scope.CapBody(1 << 20) // 1 MB, applied at most once per scope
}
```

### Also in this area

- **A malformed multipart body is a 400, not a panic.** `GetMultipartFormValues` returns
  nil when the form cannot be parsed, and `MultipartParser.Parse` ranged straight over
  `form.File` on that nil pointer — so any client could panic any multipart endpoint with
  a truncated body, unauthenticated, with no valid input needed. `Parse` now returns
  `err.InputBodyParseError` (400). **If you call `GetMultipartFormValues` yourself, check
  for nil**; nil means "malformed, or over a limit", and it is a client error.
- **`MultipartFile.Close()` no longer deletes a file that `Write` has published.** It used
  to resolve the file to delete through `getActualFilePath`, which returns the *public*
  path as soon as `Write` has run — and it closed the handle before asking for the path,
  so the `Stat` failed and the temp copy was in fact never removed either. `Close` now
  deletes the recorded temp path only, and is idempotent. **Use `Delete()` to remove a
  published file.**
- **Temp copies are released when the request ends.** `Message.RegisterCloser(io.Closer)`
  is new; `MultipartParser.Parse` and the hand-rolled `FormFile`/`GetFiles` paths both
  register with it. They cannot close earlier: the file is bound into the handler's DTO
  and `Write` reads from that very temp copy. `Message.Close` also calls
  `request.MultipartForm.RemoveAll()` — it used to loop over the parts opening each one
  and closing the *new* handle, which deletes nothing.

### How to detect whether you are affected

```bash
# Anything reading a body without a limit of its own
grep -rn 'BodyReader()\|io.ReadAll\|ParseForm\|GetMultipartFormValues' --include='*.go' .

# Your configured upload sizes — the ceiling derives from the first of these
grep -rn 'maxMultipartSize\|maxFileSize\|maxSizeMB' config/

# Anyone relying on Close() to delete a published file
grep -rn '\.Close()' --include='*.go' . | grep -i 'file\|upload'
```

Runtime check:

```bash
# Should be refused at the reader, not buffered then rejected
head -c 40000000 /dev/urandom | curl -i -X POST --data-binary @- \
  http://localhost:8080/<a-body-route>
```

### Mechanical or judgement?

**Judgement**, and it is one number: what is the largest body this app legitimately
accepts? Set `http.upload.maxMultipartSize` to it and let the ceiling follow, or set
`http.security.body.maxRequestBytes` directly if you stream something that is not an
upload.

---

## 30. Uploads are stored by sniffed content, not by filename (behaviour, security)

### What broke and why it had to

The stored filename kept whatever extension the client's filename carried:
`filepath.Ext(originalFileName)` was appended verbatim while only the base name was
sanitised. A part named `payload.html` was written to public storage as
`…-payload.html`, the public file server derived its `Content-Type` from that extension,
and the upload came back as `text/html` from the application's own origin — where script
in it is same-origin and can read every token the app publishes to its own pages. `.svg`
reached the same place through `image/svg+xml`.

The allowlist that was supposed to prevent this checked `fh.Header.Get("Content-Type")`,
a value the *uploading client* writes, so declaring `image/png` on a part full of HTML
satisfied it. And the DTO-binding path — `decoder/multipart.DecodeFiles`, a `core.IFile`
field on a DTO, which is the documented way to receive an upload — had **no content check
at all**.

Now `model.NewMultipartFile` sniffs the first 512 bytes with `http.DetectContentType`,
looks the resulting media type up in an allowlist, stores the file under the extension
that allowlist gives, and refuses a type with no entry. Every path into storage goes
through it, including the DTO binder. `FormFile` and `GetFiles` apply their mime allowlist
to `MultipartFile.MediaType()` — what the bytes actually sniffed as — rather than to the
client's header.

The type is settled **before** anything is written, so a refused upload leaves nothing on
disk. Sniffing consumes the front of a one-shot part reader, so the head is kept and put
back in front of the rest; an empty part is rejected rather than stored as a `.txt`
(`DetectContentType` answers `text/plain` for no bytes at all).

### The built-in allowlist

`image/png` `.png` · `image/jpeg` `.jpg` · `image/gif` `.gif` · `image/webp` `.webp` ·
`image/bmp` `.bmp` · `image/tiff` `.tiff` · `image/vnd.microsoft.icon` `.ico` ·
`application/pdf` `.pdf` · `application/zip` `.zip` · `application/x-gzip` `.gz` ·
`application/x-rar-compressed` `.rar` · `application/ogg` `.ogg` · `application/wasm`
`.wasm` · `application/font-woff` `.woff` · `application/x-font-ttf` `.ttf` ·
`audio/mpeg` `.mp3` · `audio/wave` `.wav` · `audio/aiff` `.aiff` · `audio/midi` `.mid` ·
`audio/basic` `.au` · `video/mp4` `.mp4` · `video/webm` `.webm` · `video/avi` `.avi` ·
`text/plain` `.txt` · `application/octet-stream` `.bin`

`text/html`, `image/svg+xml` and the XML types are absent **deliberately**: they are
script hosts, and there is no extension this framework can hand them that makes them safe
to serve back.

`application/octet-stream` → `.bin` is the entry that keeps this from breaking working
apps. `http.DetectContentType` answers with it for any binary format it has no signature
for, so an upload of some format nobody anticipated — a CAD drawing, a firmware image, a
proprietary container — is stored as `.bin` rather than rejected. `.bin` is inert:
[§31](#31-public-is-nosniff--attachment-and-svg-stops-rendering-behaviour-security)
serves it as a non-renderable attachment, which is the whole point.

Note what the sniffer *does* have a signature for. A `.docx`, `.xlsx`, `.pptx`, `.jar` or
`.odt` is a PKZip container, so `DetectContentType` answers `application/zip` for all of
them and the built-in table stores every one as **`.zip`** — accepted, but under an
extension that is not the one the user uploaded. The sniffer cannot tell the zip-based
formats apart, and neither can this allowlist.

### Before / after

```yaml
# Widen or replace the allowlist. Setting this key replaces the built-in table
# WHOLESALE — an app that needs one extra type must restate the ones it still wants.
http:
  upload:
    allowedTypes:
      image/png: .png
      image/jpeg: .jpg
      application/pdf: .pdf
      application/zip: .zip       # sniffed type on the left, stored extension on the right
```

A configured value that is not an extension — anything but a dot and ASCII alphanumerics,
or longer than 16 characters — is dropped rather than obeyed, because the extension is
joined onto both the temp and the public directory and an entry of `"../../pwned.html"`
would be a path. A configured map that resolves to nothing at all falls back to the
built-in table: an accidentally blank config value must not be read as "refuse every
upload".

```go
// A handler applying its own allowlist should use the sniffed type
if file.(*model.MultipartFile).MediaType() != "application/pdf" { /* refuse */ }

// A refusal from the binder is model.UploadTypeError, which carries the type but
// not the bytes, so it is safe to log and to render.
var typeErr *model.UploadTypeError
if errors.As(err, &typeErr) {
    // typeErr.MediaType, typeErr.FileName
}
```

### What changes for existing data and existing clients

- **Stored filenames change.** An accepted upload's name ends in the extension for its
  sniffed type, not the one the client sent. Files already on disk are untouched.
- **Some uploads that worked are now rejected.** Anything sniffing to `text/html`,
  `image/svg+xml` or an XML type. If your app legitimately accepts SVG from users, it
  needs a different design — an SVG served from your origin runs script on it — but you
  can add `image/svg+xml: .svg` to `allowedTypes` and accept that consequence knowingly.
- **A `.docx` is stored as `.zip`**, and so is every other zip-based format — `.xlsx`,
  `.pptx`, `.jar`, `.odt`. They sniff as `application/zip`, which the built-in table maps
  to `.zip`. They are *accepted*; only the extension changes. You can override the entry
  (`application/zip: .docx`), but that renames every zip-based upload to `.docx`,
  including an actual archive, so it is only worth doing in an app that accepts one such
  format and nothing else. If the extension has to be right for several of them, keep the
  client's filename in your own column and do not rely on the stored name.
- **The base name is bounded** at 96 characters and a name that sanitises down to nothing
  is stored as `upload<ext>`. Previously a very long filename made `os.Create` fail with
  "file name too long" and the upload was refused outright — an ordinary browser upload
  from someone verbose, not an attack.
- A `.` left in the base name becomes `-`, so `logo.php.png` cannot smuggle a second
  extension in.

### How to detect whether you are affected

```bash
# What do your handlers accept, and what do they do with the name?
grep -rn 'FormFile\|GetFiles\|core.IFile\|NewMultipartFile' --include='*.go' .

# Any existing allowlist config — the mime one is now applied to the sniffed type
grep -rn 'allowedMimes\|allowedTypes' config/

# Extensions already on disk that would no longer be produced
ls resource/public | sed 's/.*\.//' | sort -u
```

### Mechanical or judgement?

**Judgement.** Enumerate what your users actually upload, check each against the table
above, and configure `http.upload.allowedTypes` if the defaults do not cover it. The
strongest mitigation is one this change cannot make for you: **serve user uploads from a
separate origin.**

---

## 31. `/public/*` is nosniff + attachment, and SVG stops rendering (behaviour, security)

### What broke and why it had to

`PublicController` asked `mime.TypeByExtension` what the file's extension meant and sent
the answer verbatim. Combined with [§30](#30-uploads-are-stored-by-sniffed-content-not-by-filename-behaviour-security),
an upload stored as `…-payload.html` came back as `text/html; charset=utf-8` from the
application's own origin, where script in it is same-origin with the app, able to read the
CSRF token the session middleware publishes on every response and to call any endpoint the
visitor is authenticated for. `.svg` reached the same place through `image/svg+xml`, which
browsers also execute script in.

Every `/public/*` response now carries:

```
X-Content-Type-Options: nosniff
Content-Disposition: attachment; filename="…"
Content-Type: <render-safe, or application/octet-stream>
```

The render-safe set is `image/*` **except SVG**, `audio/*`, `video/*`, `font/*`,
`text/plain`, `text/css`, `text/csv`, `text/javascript`, `application/javascript`,
`application/json` and `application/pdf`. Everything else — `text/html` and
`image/svg+xml` included — is `application/octet-stream`.

> ### The SVG break
>
> `Content-Disposition` does **not** apply to a subresource load, so stylesheets,
> scripts, raster images and fonts referenced from a page keep loading normally. The
> **retyping does** apply to them, and SVG is the casualty: an SVG answered as
> `application/octet-stream` under `nosniff` is one the browser refuses to render.
>
> These all break for an SVG served from `/public/*`:
>
> ```html
> <img src="/public/logo.svg">
> <use href="/public/icons.svg#menu">
> ```
> ```css
> background-image: url(/public/pattern.svg);
> ```
>
> There is no way to keep them that does not also serve an *uploaded* SVG as
> `image/svg+xml`, which is a document that runs script on this origin. SVG icons are a
> common static asset, so this is the change most likely to be noticed.
>
> **What to do:** serve app-authored SVG from a route of your own, or from somewhere that
> is not also the upload root — a CDN, a dedicated static handler, or an inlined
> `<svg>` element. If you genuinely must serve user-uploaded SVG inline, put it on a
> separate origin, not behind an allowlist entry here.

A top-level navigation to any `/public/*` file now downloads rather than opening, which is
the other half of the change: a `.html` under the upload root cannot become a page on this
origin at all.

### Before / after

```go
// BEFORE — app-authored assets under the upload root
// <img src="/public/logo.svg">

// AFTER — option 1: serve app assets from a controller of your own
type AssetsController struct{}

func (AssetsController) GetRoutes() []core.IRouteConfig {
    // mount a handler over an `assets/` directory that is NOT resource/public,
    // and set your own Content-Type for the extensions you author
}

// AFTER — option 2: inline the icon
// <svg viewBox="0 0 24 24">…</svg>

// AFTER — option 3: a raster fallback, if the asset is decorative
// <img src="/public/logo.png">
```

`SpaController` is a separate controller with its own headers and is unaffected by this
change; a SPA's own hashed assets keep being served as before.

### How to detect whether you are affected

```bash
# SVG under the public root — every one of these stops rendering
find resource/public -name '*.svg'

# References to it, in templates, CSS and client code
grep -rn 'public/.*\.svg' --include='*.html' --include='*.amber' --include='*.css' \
  --include='*.js' --include='*.ts' --include='*.vue' .

# HTML under the public root — these now download instead of opening
find resource/public -name '*.html' -o -name '*.htm'
```

Runtime check:

```bash
curl -i http://localhost:8080/public/<some-file> | head -20
# Expect: X-Content-Type-Options: nosniff
#         Content-Disposition: attachment; filename="…"
#         Content-Type: application/octet-stream   (for anything not render-safe)
```

### Also fixed here

The containment check compared the resolved path against the resource root with a bare
string prefix, which a sibling directory whose name merely *starts* with the root —
`resources`, `resource-backups` — would have satisfied. The separator is now part of the
prefix. A filename containing a newline or a quote can no longer write a header of its own:
`Content-Disposition` is built with `mime.FormatMediaType`, which quotes, RFC 2231-encodes,
and returns `""` for anything it cannot express — in which case the header falls back to a
bare `attachment`.

### Mechanical or judgement?

**Mechanical** to find (the greps above are exhaustive). **Judgement** on where the
app-authored assets should live instead — and the answer is almost always "not in the
directory users can upload into", which is worth doing regardless of this change.

---

## 32. Mail refuses CRLF in a header value (behaviour, security)

### What broke and why it had to

Message headers were assembled with `fmt.Sprintf` and no CRLF filtering. A CR or LF in the
subject, a recipient, a CC entry, the sender or an attachment file name did not produce a
header containing a newline — it produced **extra headers** and, after a blank line, an
entire replacement body. Nothing downstream caught it: `smtp.SendMail`'s `validateLine`
guards only the envelope sender and recipients, and `textproto.DotWriter` guards only the
end of `DATA`. So the attacker could not add envelope recipients or speak SMTP, but a
subject of

```
hi\r\nReply-To: attacker@evil\r\nContent-Type: text/html\r\n\r\n<phishing>
```

sent a message they authored in full, from the application's authenticated SMTP identity,
with the application's own SPF and DKIM vouching for it. The vector is ordinary
application code: a contact form, a "new message from ⟨name⟩" notification, a password
reset that greets the user by name.

`MailService.buildBody` now **rejects** such a value with an error rather than stripping
it. A stripped newline still delivers a plausible-looking message, so the operator never
learns that someone is probing the mailer; a returned error reaches the app's error
handling and the log. The check covers the sender, every recipient, every CC entry, the
subject, every attachment file name and content id, and — in `Send` — every BCC entry.

### Before / after

No exported signature changed. `MailService.Send`, `MailService.buildBody` and
`mail.Attachment` are as they were. Two behaviour changes:

**1. `Send` can now fail for input it previously accepted.**

```go
// AFTER — Send returns:
//   mail: subject contains a line break: "hi\r\nReply-To: …"
// and no mail is sent.

// Sanitise at the boundary where you can tell the user, rather than letting the
// mailer refuse the send. Collapsing newlines to spaces is usually right.
subject = strings.Join(strings.Fields(userSuppliedSubject), " ")
```

Multi-line values were never delivered as intended anyway; they were delivered as extra
headers.

**2. The generated message uses CRLF throughout**, as RFC 5322 requires, including MIME
boundary delimiters — it used to mix line endings. Header text also goes through
`mime.QEncoding`, so a non-ASCII subject is emitted as an RFC 2047 encoded word instead of
raw UTF-8 bytes that only an SMTPUTF8-capable hop is allowed to carry. A plain
printable-ASCII subject is byte-for-byte what it was.

```go
// A test asserting on the exact bytes of a built message needs updating.
// For a non-ASCII subject, assert on the decoded value:
decoded, _ := (&mime.WordDecoder{}).DecodeHeader(header.Get("Subject"))
```

Encoded output is folded, because encoding is not length-neutral: a Cyrillic or CJK
subject grows roughly threefold, so 135 characters — an unremarkable subject — would come
out as a single header line of a thousand octets, past the 998 RFC 5322 allows and past
the point a receiving server may refuse it. Address lists fold after each comma for the
same reason.

### Also fixed here

- **Address headers go through `net/mail`**, so a display name is quoted and, when it is
  not ASCII, encoded. `Doe, John <j@example.com>` no longer reads as two addresses. A bare
  address is still written bare, and an address `net/mail` cannot parse is passed through
  unchanged — the header is cosmetic, delivery follows the envelope, and rewriting a value
  we failed to understand would be a guess.
- **Attachment `Content-Type` and `Content-Disposition` parameters are built with
  `mime.FormatMediaType`**, so a file name containing a space or a semicolon is quoted
  instead of truncating at the first delimiter — `Q3 invoice.pdf` used to reach the
  recipient as `Q3` — and a non-ASCII file name is RFC 2231 encoded.
- **A mail with neither a body nor an attachment no longer panics** on a nil MIME
  boundary; it produces a valid headers-only message.

### How to detect whether you are affected

```bash
# User input reaching a subject or an address
grep -rn 'GetSubject\|SetSubject\|Subject:' --include='*.go' .
grep -rn 'mail\.\|MailService\|IMail' --include='*.go' . | grep -v '_test.go'

# Golden files and byte-exact assertions on a built message
grep -rln 'MIME-version\|Content-Transfer-Encoding' --include='*_test.go' --include='*.golden' .
```

### Mechanical or judgement?

**Judgement**, in one place: where user input enters a subject line, decide whether to
sanitise it (collapse newlines) or reject it at the form. Everything else is mechanical
— updating tests that assert on raw bytes.

Known limit, unchanged: a subject a user typed to *look* like an encoded word
(`=?utf-8?B?…?=`) is still passed through as-is and will be decoded by the receiving
client. That is a display concern rather than a header-structure one, and the standard
library offers no way to force an encode of ASCII input. Reject such input at the
application boundary if it matters for your product.

---

## 33. `RoleBasedAccessControl` holds no per-request state (behaviour)

### What broke and why it had to

The user/role lookup was memoised in a plain `map[context.Context]*UserContextCache` field
on `RoleBasedAccessControl`, with **no mutex**. Three things were wrong at once:

- The key was the per-request `context.Context`, so no entry was ever reused by a later
  request. Every request was a miss and therefore a map **write**.
- An application naturally shares one configuration-driven access-control object across
  all of its requests, so two concurrent requests through any RBAC entry point raced on
  that map. Under `-race` this reports a DATA RACE; without it the Go runtime raises
  `fatal error: concurrent map writes`, which is a runtime fatal rather than a panic — so
  `RecoveryMiddleware` cannot contain it and the process dies together with every request
  in flight.
- Nothing pruned it on the normal path (`ClearUserCache` had no call site anywhere), so it
  grew by one entry per request for the lifetime of the process, holding each request's
  `context.Context` and the `core.Authenticable` behind it alive forever.

The identity is now resolved once per exported call and threaded through private helpers,
and memoised for the length of one request on the **request's own context** — the lifetime
the data actually has. It is thrown away with the request, reachable from nothing that
outlives it, and two requests have nowhere to meet.

### No API changes

`NewRoleBasedAccessControl`, every `AccessControl` method signature, `UserContextCache`,
and the documented `PersonService.GetAccessControl()` idiom are unchanged. Applications
need no code changes. Authorization decisions are unchanged. Four behavioural details are
worth knowing:

- **`ClearUserCache(ctx)` drops the identity memoised for that request**, so the next
  question about it resolves afresh. It is the escape hatch for an application that
  changes the principal mid-request in a way the memo key cannot observe — a custom
  strategy reading the identity from somewhere else, a role edit that must take effect
  before the response is written. On a context with no request behind it, it does nothing.
- **`ClearAllUserCache()` is a deliberate no-op**, kept so existing callers compile. There
  is no state spanning requests to clear. What it used to do — swap in a fresh
  instance-level map while other requests were reading the old one — was itself unsafe.
- **`GetCachedUserContext(ctx)` returns a snapshot of one request.** Treat it as valid for
  the current request only; holding it beyond the request holds a stale identity. Treat
  the returned value as immutable — several goroutines of a fanned-out request can be
  handed the same snapshot, and it is only safe to share because nothing writes to it
  after resolution.
- The `UserContextCache.isValid` field is gone. It was unexported, so nothing outside
  `model` could read it.

### The memo, and why it cannot go stale

Resolving the identity is not a cheap read: a session-backed strategy looks the session up
in its store and then loads the user, both database round trips in a real application. And
access control asks per row — `FieldFilteredDto.MarshalJSON` runs once per DTO — so
without a memo a hundred-row list performs a hundred session lookups and a hundred user
loads for one principal that cannot have changed in between.

The memo is keyed on what the identity was **derived from**: the identifier of the session
the request currently carries, plus the user id on it. The identifier moves when the
session is replaced (`Login` rotates it, [§25](#25-login-rotates-the-session-identifier-behaviour-security)),
and the user id moves when the same session object is re-pointed at another user; either
way the next lookup misses and resolves again. Both are reads of an object already in
memory. `http.PublishSession` and `StandardAuthStrategy.Logout` also drop the memo
outright on the two paths known to replace the principal. A stale identity here would be an
authorization decision made about the wrong user, which is worse than the repeated lookup
being avoided, so the invalidation is belt-and-braces on purpose.

A context with no per-request carrier — every CLI command, background job and unit test —
resolves every time, which is the previous behaviour.

### New optional contract, if you supply your own message context

```go
// app/core/http.go
type IRequestIdentityMemo interface {
	LoadIdentity(key string) (any, bool)
	StoreIdentity(key string, identity any)
	InvalidateIdentity()
}
```

Additive and optional. The framework's own message context implements it. If your
application supplies its own `core.IMessageContext`, implement these three to get the same
memoisation; a context that does not implement it simply resolves every time, as before.
The value is an `any` because `app/core` sits underneath the package that owns the resolved
identity type — a carrier stores it and hands it back for exactly the key it was stored
under, and never inspects it. **Implementations must be safe for concurrent use:** one
request can be served by more than one goroutine.

The framework's `messageContext.session` field is mutex-guarded for the same reason. It is
written in place by `Message.Context()` — which is how a session installed mid-request
reaches a `context.Context` a handler captured before the swap — and read by the
authentication strategy resolving the principal.

### How to detect whether you are affected

```bash
grep -rn 'ClearUserCache\|ClearAllUserCache\|GetCachedUserContext' --include='*.go' .
grep -rn 'IMessageContext' --include='*.go' . | grep -v '_test.go'
```

### Mechanical or judgement?

**Nothing to do** in almost every app. Delete any `ClearAllUserCache()` calls if you like
— they do nothing now — and stop storing a `*UserContextCache` past the end of a request
if you were.

---

## 34. Security headers are emitted by default (behaviour, security)

### What broke and why it had to

The framework emitted no security headers anywhere in the tree: no
`X-Content-Type-Options`, no `X-Frame-Options`, no `Referrer-Policy`, no
`Strict-Transport-Security`, no `Content-Security-Policy`. An app got whatever the
browser's most permissive interpretation of a response happened to be. `nosniff` is the
one whose absence was actively exploitable — it is what stops a browser from ignoring a
deliberately inert `Content-Type` and rendering uploaded bytes as HTML — but with none of
the others there was no second line of defence behind any of it either.

`middleware.SecurityHeadersMiddleware` is new and `RouteProvider` registers it as a `/**`
filter automatically, immediately after the recovery filter and ahead of the app's own —
it writes its headers before calling through, so they are on the response even when
something further down answers the request itself, and an app filter that wants a
different value still gets the last word.

| Header | Default | Notes |
|---|---|---|
| `X-Content-Type-Options` | `nosniff` | **not configurable** |
| `X-Frame-Options` | `SAMEORIGIN` | the one that can break a working page |
| `Referrer-Policy` | `strict-origin-when-cross-origin` | what current browsers do anyway |
| `Strict-Transport-Security` | `max-age=31536000`, **TLS responses only** | no `includeSubDomains`, no `preload` |
| `Content-Security-Policy` | **not sent** | see below |

`X-Frame-Options: SAMEORIGIN` is `SAMEORIGIN` rather than `DENY` because same-origin
framing is ordinary — previews, embedded editors, legacy admin screens — and a framework
default that silently breaks a working page is a default nobody keeps. **If a third party
embeds your app in a frame, this will break it**; set `FrameOptions: "off"` and manage
framing with a CSP `frame-ancestors` directive instead. If your app frames none of its own
pages, set `DENY`.

### There is no default CSP, on purpose

A policy worth having forbids inline script and inline style, and a server-rendered
application built with this framework's own view engines routinely has both. So any
default policy strict enough to be worth the bytes would break working pages on upgrade,
and one loose enough not to (`unsafe-inline`, `unsafe-eval`) buys close to nothing. A
policy has to be written against a particular app's pages; there is no safe-and-invisible
value to pick on its behalf.

```go
// Measure first: reported by the browser, never enforced.
routeProvider.ConfigureSecurityHeaders(middleware.SecurityHeadersOptions{
    ContentSecurityPolicyReportOnly: middleware.RecommendedContentSecurityPolicy,
})

// Then enforce, once the reports are clean.
routeProvider.ConfigureSecurityHeaders(middleware.SecurityHeadersOptions{
    ContentSecurityPolicy: middleware.RecommendedContentSecurityPolicy,
    FrameOptions:          "DENY",
    HSTSIncludeSubdomains: true,
})

// Or install your own filter entirely.
routeProvider.DisableSecurityHeadersMiddleware()
```

`middleware.RecommendedContentSecurityPolicy` is
`default-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'self';
form-action 'self'` — a starting point, not a recommendation for your app.

Any header other than `nosniff` is suppressed by setting its option to `"off"`
(`HSTSMaxAge: -1` for HSTS). HSTS is decided from the request — `r.TLS`, or
`X-Forwarded-Proto: https` for a proxy that terminates TLS, which is on by default because
that is the normal deployment. Set `TrustForwardedProto: false` for a server facing the
internet directly. Getting it wrong is not dangerous in the direction that matters: a
spoofed `X-Forwarded-Proto` makes the framework send HSTS on a response the browser then
ignores, because a browser applies the header only to responses it received over TLS
itself. `HSTSIncludeSubdomains` and `HSTSPreload` are off by default — the first commits
every subdomain of the host to HTTPS for `max-age`, including ones this app knows nothing
about, and the second is close to irreversible.

### One consequence of shipping `nosniff` everywhere

`HTTPViewScope.Render` now sets `Content-Type: text/html; charset=utf-8` when nothing has
chosen one. The rendered template used to go straight to the writer with no type at all,
leaving net/http's sniffer to guess from the first 512 bytes — survivable only because
browsers guessed as well. Now that every response carries `nosniff`, the browser honours
whatever the sniffer decided, and `http.DetectContentType` recognises only a fixed list of
opening tags: a fragment beginning `<ul>`, `<span>`, `<section>`, `<form>` or `<tr>` is
answered `text/plain` and shown to the visitor as source.

An app that renders a template which is **not** HTML — a sitemap, an RSS feed, a
plain-text mail body — must set the type before calling `Render`, and that choice wins:

```go
message.Response().SetHeader("Content-Type", "application/xml; charset=utf-8")
message.View().Render("sitemap", data)
```

### How to detect whether you are affected

```bash
# Is your app framed anywhere? Check your own embeds and any partner integration.
grep -rn '<iframe' --include='*.html' --include='*.amber' --include='*.vue' .

# Templates rendering something that is not HTML
grep -rn 'View().Render' --include='*.go' . | grep -v '_test.go'

# Do you already set these headers yourself? Yours still wins — it runs later.
grep -rn 'X-Frame-Options\|Content-Security-Policy\|Strict-Transport-Security' --include='*.go' .
```

Runtime check:

```bash
curl -i http://localhost:8080/ | grep -iE 'x-content-type|x-frame|referrer-policy|strict-transport'
```

### Mechanical or judgement?

**Nothing to do** for most apps — the defaults are chosen to be non-breaking. **Judgement**
on two: whether `SAMEORIGIN` breaks a partner embed, and when to adopt a CSP. The second is
real work and is worth scheduling separately from this upgrade.

---

## 35. `core.ISessionStorage` requires `RevokeSession` (source, security)

### What broke and why it had to

`RevokeSession(id) (bool, error)` was an optional interface the strategy asserted for. A
storage that did not implement it got a fallback: delete through `DeleteSessionById`, then
report `(true, nil)`. Deleting a session the store does not hold is deliberately *not* an
error, so that fallback answered "there was a live session here" for a delete that removed
nothing — and that is precisely the answer `RotateSession`'s guard reads as permission to
carry an identity onto a brand-new durable session. A third-party storage lost the guard
silently, and the only signal was its absence.

There is no correct value for the framework to guess. Both shipped storages already
implemented the method, so nothing changes at runtime; a third-party storage now gets a
compile error instead of a wrong answer.

### Before / after

```go
func (s *MyStorage) DeleteSessionById(id string) error { /* ... */ }

// after: add this.
//
// It must answer whether the store *held* the session, not whether the delete
// statement succeeded. If you genuinely cannot tell, return an error — rotation will
// fail closed, which is correct — rather than guessing true.
func (s *MyStorage) RevokeSession(id string) (bool, error) {
	held := s.has(id)
	if err := s.DeleteSessionById(id); err != nil {
		return false, err
	}
	return held, nil
}
```

### How to detect whether you are affected

`go build ./...`. If you implement `core.ISessionStorage`, it fails with
`missing method RevokeSession`.

### Mechanical or judgement?

Mechanical, unless your store cannot report liveness — then read the paragraph above.

---

## 36. The `sessions` table gains a `version` column (schema, behaviour)

### What broke and why it had to

Database-backed sessions are shared between replicas, and every write used to send the whole
row from whatever the writing replica last read. A write that changes the user id or the
attribute bag is now guarded on a version counter, so a replica writing from a copy another
replica has already superseded is refused and reconciles instead of silently winning. See
§37 for what the reconciliation does with your data.

Run the migration **before** deploying:

```bash
go run . db:migrate up
```

The new migration is `add_sessions_version_column`. `create_sessions_table` is deliberately
untouched — applied migrations are keyed by name and skipped, so amending it would change
nothing for any database that has already run it.

`version BIGINT NOT NULL DEFAULT 0` is metadata-only on PostgreSQL 11+ and MySQL 8, so it
does not rewrite the table however large it is. Existing rows read as `0`.

### The rolling-deploy caveat

An old binary's struct has no `Version` field, so its writes leave the column untouched and a
new replica's guard cannot see them. A mixed-version fleet therefore falls back to 2.0.0 write
semantics for the duration of the rollout. Migrate, then deploy; do not run the two versions
side by side for longer than you have to.

### How to detect whether you are affected

You are affected if `auth.session.storage` is `database`. Apps on the in-memory store need no
migration.

### Mechanical or judgement?

Mechanical, with an ordering constraint.

---

## 37. A session write that did not reach the store is reported (behaviour, security)

### What broke and why it had to

`core.ISession`'s setters are void and stay void — giving them errors would break every
request scope, view scope and test double downstream for a signal almost no caller can act
on. The contract on that interface already said what an implementation owes in exchange:
*"an implementation whose write-through can fail must remember the failure and surface it at
the next operation that can report one"*. It was written down and not implemented. The
database-backed session logged the error and forgot it.

Two paths reached a client with nothing able to report afterwards. `/csrf` — registered by
default, needing no authentication — handed out a token the store never received, so every
mutating request the client then made was refused by the CSRF check. And
`SessionMiddleware`'s per-request heartbeat vanished silently.

`GenerateCSRFToken`, `NewSessionWithoutUser` and `Login` now fail where they used to succeed
with unstored state. The heartbeat reports and continues, deliberately: refusing there would
turn an unreachable store into an outage on every page, and it hands the client nothing to be
wrong about.

### Before / after

Nothing to change. For a custom `core.ISession` whose writes can fail, implement the new
optional interface:

```go
func (s *MySession) PendingWriteError() error { return s.lastWriteErr }
```

Report, do not consume: the failure stays until a write succeeds, or which caller sees it
depends on call order.

### How to detect whether you are affected

Only if you relied on `/csrf` answering while the session store was unreachable. The token it
gave you was already useless.

### Mechanical or judgement?

Nothing to do.

---

## 38. A failed login writes no cookie; a stuck revocation is not papered over (behaviour, security)

### What broke and why it had to

**Login.** The session cookie was written inside `NewSessionWithoutUser`, and `CookieManager`
writes straight into the live `ResponseWriter` — so once it ran the header was on the wire.
`Login` then set the user id, minted a CSRF token and persisted, any of which could fail, and
a failure returned an error with no rollback. The store kept an authenticated row whose cookie
the browser already had, while the handler rendered "we could not sign you in". The next
request was signed in.

The cookie now goes out only after the final durable write, and any failure after the session
exists revokes it. `NewSessionWithoutUser` and `RotateSession` keep both their signatures and
their behaviour, so `SessionMiddleware` and `CsrfController` are untouched.

**Logout.** A revocation the store could not perform was recorded as a *tombstone* —
indistinguishable from "the row is gone". So the id stopped resolving, the next request minted
a replacement session and wrote its cookie over the client's only copy of the identifier that
still needed revoking; the user pressed "try again" and revoked the replacement while the
original stayed authenticated on every replica. It is now recorded as *pending* and retried on
the next request that presents the id, and no replacement is issued for that id until it
succeeds.

That refusal is per-identifier. A visitor with no cookie, or a different session, is
unaffected — an unreachable database does not become an outage for everybody.

### Also in this section

- `auth.SessionIdMaxLength` (128) caps an identifier before it is used as a map key or a
  database predicate. It is a length bound and not a format check, because `ISessionFactory`
  is a public extension point.
- A revocation is remembered until the revoked session's own expiry rather than a fixed 25
  hours, and both memory sets are bounded by cardinality as well as by time.
- `core.ISessionRevocationStatus` is the optional interface a storage implements to report a
  stuck revocation. A store whose delete cannot fail does not implement it.

### How to detect whether you are affected

A login that fails now leaves no cookie — if you had a test asserting one, it was asserting
the defect. `NewSessionWithoutUser` can now return `auth.ErrRevocationPending`;
`SessionMiddleware` already handles it by proceeding anonymously.

### Mechanical or judgement?

Nothing to do.

---

## 39. Guests are no longer exempt from `required_roles` (behaviour, security)

### What broke and why it had to

`CanAccessEntity` and `CanAccessEntityType` contained this:

```go
if len(domainConfig.RequiredRoles) > 0 {
	if userCache.isGuest && rbac.config.AllowGuestAccess {
		return true          // <- an anonymous caller satisfies any role requirement
	}
	return rbac.hasAnyRole(userCache.roles, domainConfig.RequiredRoles)
}
```

So `AddDomainOperation("read", true, "admin")` with `AllowGuestAccess` on granted read to an
anonymous visitor while correctly refusing an authenticated non-admin. That is an inversion,
not a loosening. The exemption was also absent from `CanReadField` and
`ValidateFieldAccess`, so the two halves of the surface disagreed about the same caller.

An admitted guest already carries the `guest` role, so a configuration that means to let
guests in lists it and passes the ordinary check. The short-circuit only ever fired when
`guest` was *absent* from the list — exactly the case the author wrote the list to exclude.

### Before / after

```yaml
# before — relied on the exemption
domain_operations:
  read: { allowed: true, required_roles: ["admin"] }
allow_guest_access: true

# after — say so
domain_operations:
  read: { allowed: true, required_roles: ["admin", "guest"] }
allow_guest_access: true
```

### How to detect whether you are affected

Grep your configuration for `allow_guest_access: true` together with any
`required_roles` that does not list `guest`. Anonymous callers were reaching those
operations and will now be refused.

### Mechanical or judgement?

**Judgement.** For each such operation, decide whether guests were supposed to reach it. If
they were, add `guest` to the list. If they were not, this was a vulnerability and the fix is
the whole point.

---

## 40. A failed identity lookup is a denial, not a guest (behaviour, security)

### What broke and why it had to

`CurrentUser` returns `(nil, nil)` for "there is no session" and `(nil, err)` for "the store
could not answer", and the comment on it says the two mean very different things. Access
control bound the error and never looked at it, so both became a guest. A session store or
user table that blinked demoted every authenticated caller to the guest role rather than
failing the request — and with `AllowGuestAccess` on, the guest role is a *different*
privilege set, not a smaller one, so the demotion granted as well as removed.

Identity is now tri-state. Resolution failure fails closed on every surface, and
`AllowGuestAccess` deliberately does not rescue it: it is a statement about callers we have
established carry no identity, and we have established nothing.

### Before / after

A handler that wants to answer 503 rather than 403:

```go
if reporter, ok := accessControl.(model.IdentityResolutionReporter); ok {
	if err := reporter.IdentityResolutionError(ctx); err != nil {
		// the request could not be decided, rather than decided against
		return serviceUnavailable(err)
	}
}
```

`IsGuest` still returns `true` for both, which is the safe label; the tri-state is what the
decisions use internally.

### How to detect whether you are affected

You are affected during an outage of your session store or user table, and not otherwise.
Previously those requests were served as guests.

### Mechanical or judgement?

Nothing to do unless you want the 503.

---

## 41. Ownership and access-level rules are role-checked, and can deny (behaviour, security)

### What broke and why it had to

`canAccessEntity` had two branches that read `domainConfig.Allowed` and never read
`domainConfig.RequiredRoles`, then returned `true` — bypassing the general rule and its role
check entirely. The second is the dangerous one: `core.OwnershipInfo.AccessLevel` is a
free-form string the *application's own entity* returns, and it is concatenated into a rule
name. An entity reporting `AccessLevel: "admin"` selected `read_admin` and was granted
whatever roles that rule declared.

### The precedence, in full

First row that matches ends the decision.

| # | Condition | Result |
|---|---|---|
| 0 | identity unresolved | **DENY** |
| 1 | anonymous and `!allow_guest_access` | **DENY** |
| 2 | `allowed_access_levels` set and the level is not in it | **DENY** |
| 3 | `IsOwner`, rule `<op>_owner` exists, `deny: true` | **DENY** |
| 4 | `IsOwner`, rule `<op>_owner` exists, allowed, roles satisfied | **ALLOW** |
| 5 | `AccessLevel` set, rule `<op>_<level>` exists, `deny: true` | **DENY** |
| 6 | `AccessLevel` set, rule `<op>_<level>` exists, allowed, roles satisfied | **ALLOW** |
| 7 | rule `<op>` missing, or denies | **DENY** |
| 8 | rule `<op>` allowed and roles satisfied | **ALLOW** |
| 9 | fallthrough | **DENY** |

Rows 4 and 6 grant-or-fall-through rather than grant-or-deny: the smaller break, closing the
bypass without revoking access from anyone whose owner rule grants *and* whose general rule
also would. Write `deny: true` for terminal refusal.

### Also in this section

- `AccessControlConfig.AllowedAccessLevels` bounds which rule an entity-supplied level may
  select. Empty means unbounded, which is the previous behaviour.
- `AccessControlConfig.OwnershipField` now actually restricts field reads. Both arms of the
  check used to return `true`, so it had no effect at all: a field that declares an `owner`
  operation is owner-*restricted* now, not owner-bonused.
- `isOwner` no longer infers ownership from an entity id that happens to equal the caller's —
  a grant on a coincidence for any type sharing an id space with users — and takes the
  request's context rather than `context.Background()`.
- `SetDefaultRoles` is deprecated and warns. It never had an effect; wiring it in would
  *grant* roles that are absent today, which is the wrong direction for a security release.

### How to detect whether you are affected

Grep for `_owner` and `_<level>` keys in `domain_operations` that declare `required_roles`.
Those roles were being ignored and now apply. Grep for `ownership_field` on a config whose
`field_access` declares an `owner` operation — that combination did nothing and now restricts.

### Mechanical or judgement?

**Judgement**, for the same reason as §39.

---

## 42. `GetAccessibleEntitiesWithFields` filters per entity (behaviour, security)

### What broke and why it had to

It made one type-level check and returned the input slice verbatim — the comment said "If
entity type access is granted, all entities are accessible" — so every per-entity ownership
decision was skipped. Its sibling `GetAccessibleEntities`, which a caller reasonably treats
as the same function plus a field list, decided each entity properly.

It now decides per entity, by the same predicate. The half of the optimisation that was real
is kept: the field list is still computed once for the collection. The type-level pre-check is
gone rather than kept as a short-circuit, because it cannot see the `_owner` rules and would
have refused an owner before the loop ran.

`GetReadableFields`, `GetReadableFieldsForCollection` and `GetInheritedFieldPermissions` also
apply the global guest gate *before* delegating to your entity's own field logic, which they
previously called with a nil user for a caller the configuration refuses outright.

### How to detect whether you are affected

If you call it and your entities implement `core.AccessibleEntity`, you were returning rows
the policy excludes. There is nothing to change.

### Mechanical or judgement?

Nothing to do.

---

## 43. `DBFilter.Field` is an identifier; raw SQL moves to `raw_sql` (source, **config format**, security)

### What broke and why it had to

This is the most serious finding in the release. `Field` was documented as being able to hold
raw SQL, the caller's attributes were substituted into it by string replacement, and `Field`
is emitted into the query as SQL syntax. An attacker's own username rewrote the authorization
predicate:

```
config:    field: "author_name = '{{user_username}}'"
username:  x' OR 'a'='a
rendered:  WHERE author_name = 'x' OR 'a'='a' = ?      args=[true]
```

Postgres reads that as `author_name = 'x' OR true`. One placeholder, one argument — nothing
downstream could notice — and because the WHERE clause is joined with bare `AND`/`OR`, the
injected `OR` neutralises sibling predicates too.

### Before / after

```yaml
# before — the vulnerable idiom
- field: "author_name = '{{user_username}}'"
  operator: "="
  value: true

# after, option 1: put the user context in `value`, which is bound
- field: "author_name"
  operator: "="
  value: "{{.Username}}"

# after, option 2: a predicate you vouch for, with its parameters bound.
# Placeholders are NOT expanded into raw_sql — user context reaches it only via raw_args.
- raw_sql: "author_name = ? AND published = true"
  raw_args: ["{{.Username}}"]
  logic: "AND"
```

A filter naming a custom processor uses `processor:` rather than putting the processor's name
in `field:`; `field:` is still accepted as a fallback when it names a registered processor, so
existing configurations keep working.

### Also in this section

- Role keys under `custom_filters:` are matched case-insensitively. They were not, and every
  shipped example keys them upper-case while the lookup lower-cased — so those filters
  matched nothing and the restrictions they describe were never applied. This is a fix, but
  it means filters that were dead may now start applying.
- An unknown `user_context_fields` name is an error. It used to default to the caller's user
  id, so a mistyped `tenant_id` compared the tenant column against a user id.
- An empty scoping attribute (`tenant_id`, `department_id`, `manager_id`) is an error rather
  than an empty predicate.
- A failed custom filter processor fails the whole call instead of logging and continuing.

### How to detect whether you are affected

```bash
grep -rnE 'field: *"[^"]*[ =<>()'"'"']' --include='*.yml' --include='*.yaml' .
grep -rn 'Field: *"[^"]*[ =<>()]' --include='*.go' .
```

Anything that matches is a `field:` that is not a column reference. `GenerateDBFilters` now
refuses it with a message naming `raw_sql`.

### Mechanical or judgement?

**Judgement.** Each one needs deciding: is the user context a *value* (option 1, almost
always) or is the predicate genuinely SQL (option 2)?

---

## 44. The filter appliers return errors (source)

### What broke and why it had to

`ApplyDBFiltersToQueryBuilder` and the `PaginationParams.Apply*` family silently dropped a
filter they could not emit — an unrecognised operator, an `in` whose value was not exactly
`[]interface{}` (which a `[]string` from YAML is not), a subquery operator outside the four
handled — each ending in a bare `return builder`.

These are *restrictions*. A filter that fails to apply does not deny, it stops denying, so
each of those silently widened the query to exactly the rows the policy meant to keep out.

### Before / after

```go
// before
builder = model.ApplyDBFiltersToQueryBuilder(builder, filters)

// after — the error means "do not run this query"
builder, err := model.ApplyDBFiltersToQueryBuilder(builder, filters)
if err != nil {
	return nil, err
}
```

`in`/`not in` now accept any slice, not only `[]interface{}`.

### How to detect whether you are affected

`go build ./...`.

### Mechanical or judgement?

Mechanical. Do not discard the error.

---

## 45. An expression in a condition's identifier slot must be `dbCore.Raw` (source)

### What broke and why it had to

`BinaryCondition.Left`, and the field slot of the `In`, `Between`, `Like` and `IsNull`
conditions, emitted a string verbatim into the SQL. That is what made §43 reach the query at
all, and it applies to anything else that puts caller-influenced text in those slots. A
column reference is emitted; anything else is now bound as a value, and the dialect refuses
outright so you are told rather than silently getting a comparison against a string.

Aggregates are the case this affects: `COUNT(*)` in a `HAVING` is not a column.

### Before / after

```go
// before
Having(&dbCore.BinaryCondition{Left: "COUNT(*)", Operator: ">", Right: 5})

// after
Having(&dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(*)"), Operator: ">", Right: 5})
```

The rendered SQL is identical — `dbCore.Raw` changes nothing but the fact that you said it.
`dbCore.Identifier` is the matching marker for a *value* slot that should hold a column, which
is how a join compares two columns rather than a column against the other column's name.

### How to detect whether you are affected

Your query build returns an error naming the slot and telling you to use `core.Raw`. Grep for
`Left:` and `Field:` values containing `(`.

### Mechanical or judgement?

Mechanical, and the error tells you where.

---

## 46. `/public/*` serves only `resource/public` (behaviour, security)

### What broke and why it had to

The controller joined the request path against `resource`, not `resource/public`. So an
unauthenticated `GET /public/temp/…` read in-flight and crash-orphaned uploads,
`/public/view/…` read template source, `/public/i18n/…` read translations, and
`/public/config/…` read whatever an app had put there. Containment was purely lexical, so a
symlink under the served root pointed wherever it liked.

It is anchored at `model.PublicStorage` now, contained with `os.OpenRoot` — which resolves
each path component against a held directory descriptor and refuses anything that escapes,
symlinks included — and streamed with `http.ServeContent` instead of buffering the whole
asset per request. Range requests, conditional requests and `HEAD` work as a result.

`MultipartFile.PublicPath()` returns a rooted single-segment URL (`/public/<path>/<name>`).
It used to be relative and one segment short of the route, so the value handed to your
templates was a 404 and the URL that actually worked had `public` in it twice.

### Before / after

```bash
# static assets move under the served root
git mv resource/assets resource/public/assets
```

`util.AssetPath` and `util.PublicPath` keep their signatures and their output strings; only
the directory they resolve to moves.

### How to detect whether you are affected

```bash
ls resource                      # anything here other than public/ was reachable and no longer is
grep -rn '/public/public/' --include='*.gohtml' --include='*.go' .
grep -rn 'AssetPath\|PublicPath' --include='*.gohtml' .
```

Nothing stored needs migrating: `File.Value` persists `path.Join(Path, Name)` and every URL is
computed at render time.

To keep serving another tree deliberately:

```go
controller.NewPublicControllerAt("/legacy/*", "resource/legacy")
```

### Mechanical or judgement?

Mechanical, plus one `git mv`.

---

## 47. `POST /login` and `POST /logout` require a same-origin request (behaviour, security)

### What broke and why it had to

The built-in browser login accepted an unprotected form POST. An attacker submits *their own*
valid credentials from a page the victim visits; the victim's browser sends it, the login
succeeds, and the victim spends the visit inside the attacker's account — typing into it,
uploading to it — while the attacker holds the other half of the session.

Mounting `CSRFMiddleware` was not available as the fix. The token could not exist: the
framework ships no login template, there was no view helper that could emit the hidden field,
and the token is published only as a response header. And `CSRFMiddleware` requires a
session, correctly, while `SessionMiddleware` is not in the default middleware set — so a
first-time visitor's login POST legitimately arrives without one and would be refused by
construction.

`middleware.SameOriginMiddleware` needs nothing of the template, the client or the session.

### The decision table

| Signal | Value | Result |
|---|---|---|
| method | GET / HEAD / OPTIONS / TRACE | allow |
| `Sec-Fetch-Site` | `same-origin` or `none` | allow |
| `Sec-Fetch-Site` | `cross-site` | **refuse** |
| `Sec-Fetch-Site` | `same-site` | **refuse** unless `AllowSameSite` |
| `Origin` | matches this origin | allow, else **refuse** |
| `Referer` | matches this origin | allow, else **refuse** |
| none of the three | — | allow, unless `RequireOriginHeader` |

`same-site` is refused by default because the session cookie is already `SameSite=Lax`: what
this adds over the cookie attribute is the sibling subdomain. All-absent is allowed by default
because a browser cannot be made to omit all three on a cross-site form POST, so that case is
a non-browser client.

### What this breaks

- A browser posting the login form from a sibling subdomain. Intentional; `AllowSameSite`
  opts back in.
- A reverse proxy that rewrites `Host` without setting `X-Forwarded-Proto`, on an app with
  `app.server.url` set to `https`. Set `TrustForwardedProto` — it is off by default because
  the header is client-settable unless a proxy overwrites it.
- A test suite posting `/login` with an explicit foreign `Origin`.

Configuration: `http.security.origin.allowSameSite`, `.required`, `.trustForwardedProto`.
`RouteProvider.EnableSameOriginProtection` applies it to every state-changing request — read
the note on the middleware first; it will also refuse a legitimate cross-origin browser fetch
that CORS is there to authorise.

### Moving up to token protection

Now possible, because the view helper exists:

```gohtml
<form method="post" action="{{ UrlByName "cp.login" }}">
  {{ csrf }}
  ...
</form>
```

then mount `middleware.NewCSRFMiddleware()` on the route. It additionally requires
`SessionMiddleware` to cover `/login`, or the request is refused with "No active session".

### Mechanical or judgement?

Judgement, if you are behind a proxy or post across your own subdomains.

---

## 48. Upload limits apply uniformly (behaviour)

### What broke and why it had to

The file-count limit counted map keys, so a hundred parts all named `file` counted as one
against a limit of a hundred — which is the shape an attacker sends, not the shape a form
does. And `FormFile`/`GetFiles` enforced neither the total-size nor the count cap that DTO
parsing enforces, while using a per-file limit ten times larger: the same upload was accepted
or refused depending on which API the handler happened to use, and the more permissive was on
the hand-rolled path.

`http.security.file.maxSizeMB` is now a *ceiling*, not the only limit: the effective per-file
cap is the smaller of it and `http.upload.maxFileSize`.

### How to detect whether you are affected

If your uploads sit between `http.upload.maxFileSize` (10 MB default) and
`http.security.file.maxSizeMB` (100 MB default) and you receive them through `FormFile` or
`GetFiles`, they start being rejected. Raise `http.upload.maxFileSize`.

### Mechanical or judgement?

One configuration value, if it affects you.

---

## 49. The chi import path is `github.com/go-chi/chi/v5` (source)

### What broke and why it had to

`github.com/go-chi/chi` without a suffix is the v1 module path, and it is dead — it receives
no fixes, so every advisory against it needs an exception rather than an upgrade. No gorgany
API changes: `Engine()` still returns `http.Handler`, and both chi fields on the router
adapter are unexported.

### Before / after

```bash
grep -rl '"github.com/go-chi/chi"' --include='*.go' . | xargs sed -i '' \
  's|"github.com/go-chi/chi"|"github.com/go-chi/chi/v5"|'
go get github.com/go-chi/chi/v5@latest && go mod tidy
```

### How to detect whether you are affected

Only if you import chi directly. Most apps reach for `message.Request().PathParam` instead.

### Note on the toolchain

`go.mod` moves to `toolchain go1.26.5`, and the `go` directive stays at `1.24.0`. The
directive is the consumer floor, so your app can still be built by Go 1.24; only the toolchain
that compiles a release moves.

### Mechanical or judgement?

Mechanical.

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

Then the checks for the security round (§24–§34). Every one of these fails silently if you
skip it — none is a build error and none is covered by a unit test in your app.

```bash
# 11. The session cookie is the prefixed one, and a login authenticates.
#     Two Set-Cookie headers for the session on a login is correct: take the LAST.
curl -si -X POST -d 'username=u&password=p' http://localhost:8080/login \
  | grep -i 'set-cookie'
# Expect: __Host-GRG_SESSION_ID=… ; Secure; HttpOnly; Path=/; SameSite=Lax
#         and NO Domain attribute. If you see the bare name, you have opted out —
#         check auth.session.cookie.secure and auth.session.cookie.domain.

# 12. The login actually took effect. This is the §25 check, and it is the one that
#     catches a login handler missing its PublishSession call.
curl -s -c /tmp/j -X POST -d 'username=u&password=p' http://localhost:8080/login >/dev/null
curl -i -b /tmp/j http://localhost:8080/<a-route-requiring-auth>
# Expect 200. A 401 or a redirect to /login means the session was rotated and
# never republished.

# 13. The old identifier is dead. Capture the pre-login cookie, log in on it, then
#     replay the OLD one — it must not authenticate.

# 14. Logout revokes. Log in, log out, then replay the cookie the login gave you.
#     It must not authenticate, and a logout that FAILS must not say it succeeded.

# 15. The CSRF token on the login response is the post-login one.
curl -si -c /tmp/j -X POST -d 'username=u&password=p' http://localhost:8080/login \
  | grep -i 'x-csrf-token'
# Then use exactly that token on the next mutating request. If your client caches
# the pre-login token instead, every mutating request is rejected until it re-reads.

# 16. Security headers are present on an ordinary route.
curl -sI http://localhost:8080/ \
  | grep -iE 'x-content-type-options|x-frame-options|referrer-policy'

# 17. /public/* is inert. Note the Content-Type for an SVG specifically.
curl -sI http://localhost:8080/public/<some-file> \
  | grep -iE 'content-type|content-disposition|x-content-type-options'

# 18. SVG under the public root — every hit here is a broken icon after the upgrade.
find resource/public -name '*.svg'
grep -rn 'public/.*\.svg' --include='*.html' --include='*.amber' --include='*.css' \
  --include='*.js' --include='*.ts' --include='*.vue' .

# 19. An oversize body is refused at the reader rather than buffered.
head -c 40000000 /dev/urandom | curl -si -X POST --data-binary @- \
  http://localhost:8080/<a-body-route> | head -1

# 20. An upload of each type your users actually send still succeeds, and check
#     the extension it was stored under — it now comes from the content, not the name.
ls -lt resource/public | head

# 21. A malformed multipart body is a 400, not a dropped connection.
curl -si -X POST -H 'Content-Type: multipart/form-data; boundary=x' \
  --data-binary '--x
' http://localhost:8080/<a-multipart-route> | head -1

# 22. The app boots with the JWT secret every environment actually has.
for f in .env .env.staging .env.production; do
  [ -f "$f" ] && printf '%s: ' "$f" && grep '^JWT_SECRET=' "$f" | cut -d= -f2- | tr -d '\n' | wc -c
done
# Anything under 32 is a boot panic.

# 23. A mail with a subject containing a newline is refused, and an ordinary
#     mail still sends. Exercise both against your real mailer, not a stub.
```

Two that need eyes rather than a command:

- **Announce the logout.** §24 ends every session at deploy time. There is nothing to
  verify afterwards; the work is telling people beforehand.
- **Run the whole app under `-race` for a while**, ideally with concurrent traffic. §33's
  defect was a runtime fatal, not a panic, so it never appeared in a log as anything but
  the process going away.

The framework's own `testsupport` package is the shortest way to write checks 5 and
10 as real tests — see [`docs/TESTING.md`](docs/TESTING.md).
