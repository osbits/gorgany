# Prompt: upgrade an application from gorgany v1.5.1 to v2.0.0

Copy everything below the line into a coding agent **running in your application's
repository** — not in the framework repo. It is self-contained; it assumes no
prior conversation.

---

You are upgrading a Go application that depends on the web framework
`github.com/osbits/gorgany` from **v1.5.1 to v2.0.0**.

The target of this work is **the application repository you are in now**, not the
framework. Do not clone or modify the framework. Everything below describes what
changed in the framework and what you must change in the app.

First, confirm you are in the right place:

```bash
grep -n 'github.com/osbits/gorgany' go.mod
```

If that finds nothing, stop and say so — this prompt does not apply to this repo.

## Ground rules

1. Work through the phases in order. Phases 1–4 are behaviour changes that compile
   cleanly; Phases 5–8 are signature changes the compiler will find for you. Doing
   the behaviour work first means you are not making judgement calls while chasing
   build errors.
2. **Do not `go get` the new version until Phase 5.** Phases 1–4 are audits of the
   existing code, and you want it building while you do them.
3. Commit per phase, so a mistake is easy to isolate.
4. If a grep in this document returns nothing, say so and move on — several of
   these changes affect no app at all.
5. Do not "fix" anything this document does not ask you to. If you find an
   unrelated bug, note it and leave it.

---

## Phase 0 — Baseline

Record a known-good starting point.

```bash
go build ./... && go vet ./... && go test ./... -count=1
git rev-parse HEAD
```

If the suite is already failing, note which tests and treat them as pre-existing.
Do not try to fix them as part of this upgrade.

---

## Phase 1 — `session.Query()` no longer memoizes (behaviour, no compiler error)

**What changed.** `session.Query()` and `transaction.Query()` used to memoize one
builder per session and hand the same instance back every time, so a second
`Query()` call arrived already carrying the first query's `WHERE`, `ORDER BY`,
`LIMIT` and `FROM`. They now return a fresh builder on every call.

**Why it matters.** If any code relied on that shared state, the query it produces
now has *fewer* predicates than before. No error, no panic — different rows.

**Find the candidates:**

```bash
grep -rn '\.Query()' --include='*.go' . | grep -v '_test.go'
```

**For each hit, check two things:**

1. Is `Query()` called more than once on the same session value? If so, read both
   queries and decide whether the second was meant to inherit the first's clauses.
2. Is the result of a builder call **discarded**? This is the strongest tell:

```bash
# A statement that calls a builder method and throws the result away.
grep -rnE '^\s+[a-zA-Z_][a-zA-Z0-9_.]*\.(Eq|Where|From|Select|OrderBy|Limit|Offset|GroupBy|Join)\(' \
  --include='*.go' . | grep -v '=' | grep -v 'return' | grep -v '_test.go'
```

The builder has been copy-on-write for every method all along, so a discarded
result only ever did anything *because* of the memoization.

**The edit.** Make the sharing explicit by deriving from a base builder:

```go
// Before — relied on Query() returning the same builder.
session.Query().Eq("tenant_id", tenantID)
orders := session.Query().Select("*").From("orders")

// After — one base, explicitly derived. Each derivation is independent.
base := session.Query().Eq("tenant_id", tenantID)
orders := base.Select("*").From("orders")
widgets := base.Select("*").From("widgets")
```

**How to test it.** For any repository method you changed, assert on the generated
SQL of the *second* query:

```go
func TestSecondQueryDoesNotInheritTheFirst(t *testing.T) {
    session := newTestSession(t)

    _, _, err := session.Query().Select("id").From("orders").Eq("tenant_id", 42).ToSQL()
    require.NoError(t, err)

    sql, args, err := session.Query().Select("id").From("widgets").ToSQL()
    require.NoError(t, err)

    assert.Equal(t, "SELECT id FROM widgets", sql)
    assert.Empty(t, args)
}
```

Commit: `Make shared query-builder state explicit for gorgany v2`.

---

## Phase 2 — `json:"-"`, `omitempty` and embedded structs in API responses (behaviour, security)

**What changed.** `dto.ReturnObject` bodies are not marshalled by
`encoding/json`; the framework reflects over the struct field by field. Its tag
parser used to treat `json:"-"` exactly like an absent tag, so a field marked
`json:"-"` was **serialised anyway under its Go name**. `omitempty` was ignored,
and an anonymous embedded struct was written under its Go *type* name instead of
being inlined.

v2 matches `encoding/json` for all three.

**Why it matters.** This is the change most likely to alter a response shape your
clients parse. It is also a security fix: if a DTO carried a `json:"-"` secret, it
was on the wire.

**Step 2a — find suppressed fields that were leaking.**

```bash
grep -rn 'json:"-"' --include='*.go' .
```

For each, decide: was any client depending on receiving this field? Almost
certainly not — the tag says it should never have been sent. Note each one; these
fields **disappear** from responses now.

**Step 2b — find `omitempty` fields.**

```bash
grep -rn 'omitempty' --include='*.go' .
```

These keys now **disappear when zero**. If a client does
`if (body.nickname === "")`, it must become `if (!body.nickname)`.

**Step 2c — find embedded structs in DTOs. This is the important one.**

```bash
# Types that reach the response envelope
grep -rln 'dto.ReturnObject' --include='*.go' .

# Anonymous embedded fields: a line inside a struct with a type and no field name
grep -rnE '^\s+\*?[A-Z][A-Za-z0-9_]*(Dto|DTO|Response|Model)\s*(`[^`]*`)?\s*$' \
  --include='*.go' .
```

For every DTO with an anonymous embed, the response shape changed:

```jsonc
// v1.5.1
{ "OwnerCardDto": { "owner_id": "o1", "owner_name": "Ann" }, "card_id": "c1" }

// v2.0.0 — inlined, matching encoding/json
{ "owner_id": "o1", "owner_name": "Ann", "card_id": "c1" }
```

**The edit.** Usually none — the new shape is what the json tags always said. If a
client genuinely needs the old nested shape, name the embed, which suppresses
inlining in v2 and in `encoding/json` alike:

```go
type CardDto struct {
    OwnerCardDto `json:"OwnerCardDto"`   // keeps the v1.5.1 shape
    CardID       string `json:"card_id"`
}
```

One boundary to know: an embedded field whose **type is unexported** is skipped
entirely (the framework reads values via `reflect.Value.Interface()`, which panics
on anything reached through an unexported field). v1.5.1 skipped these too, so
nothing regressed — but if you embed an unexported type and want its fields in the
response, **export the type**.

**How to test it.** For each DTO, compare the envelope against `encoding/json`:

```go
func TestDtoResponseShape(t *testing.T) {
    payload := UserDto{ID: "u1", Email: "a@b.c", PasswordHash: "secret"}

    raw, err := json.Marshal(dto.ReturnObject(payload, core.SuccessHttpStatus, nil))
    require.NoError(t, err)
    assert.NotContains(t, string(raw), "secret", "a json:\"-\" field must not ship")

    var got struct{ Body map[string]any `json:"body"` }
    require.NoError(t, json.Unmarshal(raw, &got))

    reference, _ := json.Marshal(payload)
    var want map[string]any
    require.NoError(t, json.Unmarshal(reference, &want))

    assert.Equal(t, want, got.Body)
}
```

Write one per DTO that reaches the API. Update any client fixture or contract test
that pins the old shape.

Commit: `Update DTO response-shape expectations for gorgany v2`.

---

## Phase 3 — A role mismatch is 403, not 401 (behaviour)

**What changed.** `AuthMiddleware` returned 401 for a *role* mismatch, telling an
authenticated user with the wrong role that they were unauthenticated. It is now
403. 401 is reserved for a missing or invalid session.

Also, for a non-JSON (browser) request: a 401 still redirects to
`auth.login.formUrl`, but a 403 now renders `Forbidden` with status 403 instead of
redirecting — a redirect to login cannot fix a role problem and would loop.

And: the JSON-vs-HTML decision now also honours the `Accept` header and an `/api/`
path prefix, not just `Content-Type` and an `api` namespace path param. A GET
carries no `Content-Type`, so `Accept: application/json` on a GET now correctly
gets a JSON envelope where it previously got a redirect.

**Find the code that cares:**

```bash
grep -rn 'StatusUnauthorized\|== 401\|NotAuthorizedHttpStatus' --include='*.go' .
```

Then your frontend / API clients:

```bash
grep -rn '401' --include='*.ts' --include='*.tsx' --include='*.js' --include='*.vue' . 2>/dev/null
```

**The edit.** Split the two cases wherever they were conflated:

```go
// Before
if resp.StatusCode == http.StatusUnauthorized {
    redirectToLogin()
}

// After
switch resp.StatusCode {
case http.StatusUnauthorized:
    redirectToLogin()
case http.StatusForbidden:
    showPermissionDenied()
}
```

**How to test it.** Two HTTP tests against a role-guarded route: a logged-out
caller must get 401, and a logged-in caller with the wrong role must get 403.

Commit: `Handle 403 separately from 401 for gorgany v2`.

---

## Phase 4 — `OPTIONS` no longer reaches your handlers (behaviour, security)

**What changed.** The framework's router used to register every route under
`OPTIONS` **with the route's own handler**, while `CSRFMiddleware` exempted
`OPTIONS` from token checking. Together that was a token-free path to every
mutating endpoint: `OPTIONS /widgets/1` ran the `DELETE` handler and deleted the
row.

v2 registers a preflight responder that answers `204` with an `Allow` header and
never calls the route handler. An explicitly declared `OPTIONS` route still wins.

**Find whether you depend on it:**

```bash
grep -rn 'MethodOptions\|"OPTIONS"' --include='*.go' .
grep -rn 'Cors\|CORS' --include='*.go' .
```

**The edit.**

- If you relied on the implicit `OPTIONS` route for **CORS preflight**: the
  `204` + `Allow` response is what you want. Make sure your CORS headers are set
  by a `/**` **filter** middleware (`AsFilter()`), which runs before the
  responder, not by a route middleware.
- If you genuinely need `OPTIONS` to reach a handler, declare it as a route:

```go
router.RouteConfig{
    Path:    "/widgets/{id}",
    Method:  core.Method(http.MethodOptions),
    Name:    "widgets.options",
    Handler: c.Options,
}
```

**How to test it — this is the regression test worth keeping:**

```go
func TestOptionsHasNoSideEffect(t *testing.T) {
    before := countWidgets(t)

    rec := httptest.NewRecorder()
    app.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/widgets/1", nil))

    assert.Equal(t, http.StatusNoContent, rec.Code)
    assert.NotEmpty(t, rec.Header().Get("Allow"))
    assert.Equal(t, before, countWidgets(t), "OPTIONS must not mutate anything")
}
```

Commit: `Assert OPTIONS has no side effects for gorgany v2`.

---

## Phase 5 — Bump the dependency and let the compiler drive

```bash
go get github.com/osbits/gorgany@v2.0.0
go mod tidy
go build ./... 2>&1 | tee /tmp/gorgany-v2-build.log
```

Work through the errors. Expect these three shapes.

### 5a. `assignment mismatch: 2 variables but ToSQL returns 3 values`

`IQueryBuilder.ToSQL()` now returns `(string, []any, error)`. The error is how a
dialect refuses a construct its engine cannot express instead of emitting SQL the
server will reject.

```go
// Before
sql, args := builder.Select("id").From("users").Eq("id", 1).ToSQL()

// After
sql, args, err := builder.Select("id").From("users").Eq("id", 1).ToSQL()
if err != nil {
    return fmt.Errorf("cannot render user query: %w", err)
}
```

Do **not** discard the error with `_`. It is the only signal that the query is
unrepresentable.

```bash
grep -rn '\.ToSQL()' --include='*.go' .
```

`Condition.ToSQL()` — `BinaryCondition`, `RawCondition`, etc. — is **unchanged**
and still returns two values. Only `IQueryBuilder.ToSQL()` grew an error.

If you only ever go through `Executor.Exec` / `Find` / `Count`, there is nothing
to change: they check it and surface it as `QueryResult.Error`.

### 5b. `multiple-value v2.NewDataSource(...) in single-value context`

`NewDataSource(map[string]any)` returns `(IDataSource, error)` instead of
panicking on a missing or mistyped key.

```go
// Before
dbProvider.AddConnection("reports", func() dbCore.IDataSource {
    return v2.NewDataSource(map[string]any{
        "host": "localhost", "port": 5432, "db": "reports",
        "ssl": "disable", "log": false, "prefer_simple_protocol": false,
    })
})

// After — prefer the typed form
dbProvider.AddConnectionE("reports", func() (dbCore.IDataSource, error) {
    return v2.NewDataSourceWithConfig(dsconfig.DataSource{
        Host: "localhost", Port: 5432, Database: "reports", SSL: "disable",
    })
})
```

with `dsconfig "github.com/osbits/gorgany/db/sql/config"`.

`log` and `prefer_simple_protocol` are now optional. An **unknown** key is now
reported, so a typo that used to be ignored will fail at boot — read the message,
it names the key.

### 5c. A custom `SQLDialect` implementation

```bash
grep -rn 'SQLDialect\|FormatQuery\|FormatGroupBy' --include='*.go' .
```

Almost certainly nothing. Before v2 there was no way to inject a dialect, so you
could not have used one without forking the builder. If you *did* fork it, read
`docs/DIALECTS.md` in the framework repo — the fork is now unnecessary.

Then:

```bash
go build ./... && go vet ./...
```

Commit: `Adopt gorgany v2 API signatures`.

---

## Phase 6 — Multi-database apps only

Skip this phase entirely if you configure exactly one database and it is named
`default`.

```bash
grep -rn -A3 'databases:' config/*.yaml config/*.yml 2>/dev/null
```

### 6a. Bare `dbCore.ISession` injection now means `default`

Those bindings used to be re-registered once per connection, and the container
overwrites, so a bare injected session resolved to whichever connection registered
**last** — nondeterministically, because the registration loop walked a Go map.
They now come from `default` only.

```bash
grep -rn 'dbCore.ISession\|dbCore.IQueryExecutor\|dbCore.IQueryBuilder' --include='*.go' . \
  | grep 'container:"inject"'
```

For each hit, work out which database it actually meant — the question the old code
answered at random. To reach a non-`default` one, resolve by name:

```go
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

If you have **no** connection named `default`, the bare bindings are not registered
at all and injection now fails to resolve with a clear container error rather than
panicking later. Either name one `default` or switch every site to named
resolution. The framework logs a warning at boot naming your connections.

### 6b. Declare each migration's target datasource

`db:migrate`, `db:seed` and `db:diff` used to hard-code `default`, so a migration
written for a second database executed against the first. They now accept
`--datasource=<name>`.

```bash
ls db/migration/
grep -rln 'DataSourceName' db/migration/
```

Every migration **not** in that second list is treated as targeting `default`,
which preserves current behaviour. Add a declaration to the ones that belong
elsewhere:

```go
// DataSourceName implements db.DatasourceScoped.
func (m *ReportsIndexMigration) DataSourceName() string { return "reports" }
```

Rules: declaring nothing means `default`; declaring a different *configured*
datasource means the migration is skipped with a log line; declaring one that is
**not configured** fails the command loudly, naming the migration.

Then run each database's migrations separately:

```bash
go run cmd/cli.go db:migrate up
go run cmd/cli.go db:migrate up --datasource=reports
```

Commit: `Scope datasource injection and migrations for gorgany v2`.

---

## Phase 7 — Config and provider adjustments

### 7a. Local development over plain HTTP

The session cookie's `Secure` attribute used to be hard-coded `true`, so login
silently failed over `http://` — the browser accepted `Set-Cookie` and then
declined to send the cookie back, making every request after login look
logged-out. If you hit that in local dev, add to your **dev** config only:

```yaml
auth:
  session:
    cookie:
      secure: false   # local dev over http:// ONLY — never in production
```

The default is `true`. Verify it is not set in your production config:

```bash
grep -rn 'secure' config/ 2>/dev/null
```

### 7b. Upload limits are configurable

They were compile-time constants (32 MB total / 100 files / 10 MB per file). If
you worked around them by reading `Request().BodyReader` by hand, you can drop
that:

```yaml
http:
  upload:
    maxMultipartSize: 67108864   # 64 MB
    maxFiles: 20
    maxFileSize: 11534336        # 11 MB
```

Defaults are unchanged, so configuring nothing keeps today's behaviour. A
configured `0` falls back to the default rather than disabling the limit.

```bash
grep -rn 'BodyReader' --include='*.go' .
```

### 7c. Recovery middleware is now automatic

`RouteProvider` registers `RecoveryMiddleware` as the first `/**` filter. If your
app installs its own recovery filter, either delete yours or opt out:

```go
routeProvider.DisableRecoveryMiddleware()
```

```bash
grep -rn 'recover()' --include='*.go' . | grep -v '_test.go'
```

The upside: a registered `JwtAuthError` handler now actually fires.
`JwtMiddleware` reports failure by panicking, and previously nothing recovered to
re-dispatch it — so that handler was dead code.

### 7d. `EventProvider` now works at all

`EventProvider.Boot` returned an `error`, which does not satisfy
`core.IProvider`, so `*EventProvider` could never be passed to
`AddProvider` and the events subsystem was unreachable. If you built an adapter
to work around that, delete it:

```go
eventProvider := provider.NewEventProvider()
eventProvider.RegisterSubscriber("user.created", func() core.ISubscriber {
    return &WelcomeEmailSubscriber{}
})
bootstrapper.AddProvider(eventProvider)   // now compiles
```

### 7e. Register override providers last

The container still lets you overwrite a framework binding — that is how you
override `core.IValidator` or `core.IDataContext` — but it now **warns** when a
core interface is rebound, because only the last registration wins. Move any
override provider to the end of your `AddProvider` sequence, and watch the boot
log for the warning.

### 7f. Route middleware no longer leaks across methods

Route-scoped middleware used to be re-attached to any later route with a matching
pattern, so registering `GET /x` then `PUT /x` fired a side-effecting middleware
**twice** on one request and leaked GET's middleware onto PUT. If you relied on
that to share middleware between methods of one path, declare it on each route or
register it globally with a pattern:

```go
routeProvider.AddMiddleware(
    grghttp.NewMiddlewareConfigBuilder().
        WithPattern("/widgets/**").
        WithMiddleware(middleware.NewAuditMiddleware()).
        Build(),
)
```

Commit: `Adjust providers and config for gorgany v2`.

---

## Phase 8 — Optional: adopt what v2 added

Only if useful to you.

- **`search_path` for schema-per-test isolation** (Postgres). Previously
  impossible, which forced `CREATE DATABASE` per test run plus
  truncate-between-tests:

  ```yaml
  databases:
    test:
      driver: postgres_gorm
      host: localhost
      db: app_test
      search_path: tenant_a
  ```

- **MySQL.** `driver: mysql_gorm`. Read `docs/DIALECTS.md` in the framework repo
  for the table of what MySQL refuses — `RETURNING`, `DISTINCT ON`,
  `FULL OUTER JOIN`, `CUBE`, `GROUPING SETS` — each of which now returns an
  explicit error rather than invalid SQL.

- **`db:migrate down --steps=N`.** It was previously an empty stub that reported
  success and did nothing, so verify your `Down()` methods actually work before
  relying on it.

- **`ROLLUP` / `CUBE` / `GROUPING SETS`** now render. They were silently dropped
  before — and with no plain `GroupBy()` fields the framework emitted a bare
  `GROUP BY `, which is a syntax error. If you worked around that with raw SQL,
  you can drop the workaround.

---

## Verification

Run all of this and report the output.

```bash
go build ./... && go vet ./... && go test ./... -count=1
```

App-level smoke checks that no compiler covers:

```bash
# 1. Login works, and the session survives the next request.
#    (Over http:// locally this needs auth.session.cookie.secure: false.)

# 2. OPTIONS on a mutating route: 204, an Allow header, and NO side effect.
curl -i -X OPTIONS http://localhost:8080/api/<a-mutating-route>

# 3. No suppressed field appears in any response body.
curl -s http://localhost:8080/api/<a-dto-route> \
  | grep -iE 'password|hash|secret|token|internal'

# 4. A logged-out caller gets 401; a wrong-role caller gets 403.
curl -i http://localhost:8080/api/<a-role-guarded-route>

# 5. A CSRF-protected form POST still succeeds with a valid token, and is
#    rejected with 403 (standard envelope, not a bare {"error": ...}) without one.

# 6. A file upload of your largest expected size succeeds.

# 7. Every response shape a client parses still matches — diff against a
#    v1.5.1 capture if you have one.
```

If you run more than one database:

```bash
go run cmd/cli.go db:migrate up
go run cmd/cli.go db:migrate up --datasource=<other>
```

Boot the app **ten times** and confirm every datasource resolves by name on every
boot. The pre-v2 registration was nondeterministic, so one successful boot proves
nothing.

## Report

When you finish, report:

1. Which phases required changes and which were no-ops.
2. Every response shape that changed, and whether a client depends on it.
3. Anything you could not resolve, with the exact error.
4. Any test you added.
5. Anything you deliberately left alone, and why.
