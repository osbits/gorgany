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

1. Work through the phases in order. Phases 1–4d are behaviour changes that compile
   cleanly; Phases 5–8 are signature changes the compiler will find for you. Doing
   the behaviour work first means you are not making judgement calls while chasing
   build errors.
2. **Do not `go get` the new version until Phase 5.** Phases 1–4d are audits of the
   existing code, and you want it building while you do them.
3. Commit per phase, so a mistake is easy to isolate.
4. If a grep in this document returns nothing, say so and move on — several of
   these changes affect no app at all.
5. Do not "fix" anything this document does not ask you to. If you find an
   unrelated bug, note it and leave it.
6. Three of these changes are things that **never worked**, so a green build and a
   passing suite prove nothing about them. You have to watch them run: a scheduled
   job firing (5d), `orm.Create` inserting a row if you use MySQL (Phase 6), and a
   CSRF-protected request succeeding with a token obtained from `/csrf` (4b).

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

---

## Phase 4a — Validation error shape (behaviour, no compiler error)

`err.ValidationError` used to carry the Go struct field name and go-playground's raw
sentence:

```json
{"field": "MobilePhone", "err": "Key: 'Dto.MobilePhone' Error:Field validation for 'MobilePhone' failed on the 'required' tag"}
```

It now carries the wire name, a readable message, and three new `omitempty` keys:

```json
{"field": "mobile_phone", "err": "mobile_phone is required", "rule": "required", "path": "mobile_phone"}
```

Find what depends on the old shape:

```bash
# Client code matching on a Go field name or parsing the raw message
grep -rn "Error:Field validation\|Key: '" \
  --include="*.js" --include="*.ts" --include="*.jsx" --include="*.tsx" \
  --include="*.vue" --include="*.svelte" --include="*.html" . || echo "no client-side matches"

# A custom validator that exists only to rename fields and translate messages
grep -rn "core.IValidator" --include="*.go" . | grep -v "_test"

# Templates rendering a validation error's Field directly
grep -rn "\.Field" --include="*.gohtml" --include="*.amber" --include="*.tmpl" . || echo "no template matches"
```

For each client-side match, key off `rule` instead of parsing `err`:

```js
// BEFORE
if (e.field === 'MobilePhone' && e.err.includes('required')) { ... }
// AFTER
if (e.field === 'mobile_phone' && e.rule === 'required') { ... }
```

If you replaced `core.IValidator` **only** to rename fields or translate messages, delete
it and move your wording into translation files instead:

```yaml
# resource/i18n/en.yaml
validation:
  required: "Please provide {:field}"
  email: "{:field} does not look like an email address"
  min: "{:field} must be at least {:param}"
```

Then check for a DTO with two fields on one wire name, which is now an error rather than
half-working:

```bash
# Two json tags with the same name inside one struct. Review the hits by hand;
# `go vet` also reports this as a structtag error.
go vet ./... 2>&1 | grep "repeats json tag" || echo "no duplicate wire names"
```

---

## Phase 4b — CSRF token delivery (behaviour, security)

Two things changed, and together they close a gap that made the hardened CSRF middleware
reject every mutating request from a client that had not caught one particular header:

- `X-CSRF-Token` is now set on **every** response to a request carrying a session, not
  only on the response that created it.
- `GET /csrf` is registered automatically.

First check for a collision, because a route already at `/csrf` fails the duplicate-route
check at boot:

```bash
grep -rn '"/csrf"' --include="*.go" . || echo "no collision"
```

If there is one, either move the framework's endpoint or drop it:

```go
// Move it
csrf := controller.NewCsrfController()
csrf.Path = "/api/v1/csrf"
routeProvider.DisableCsrfController()
routeProvider.AddController(csrf)

// Or keep your own only
routeProvider.DisableCsrfController()
```

Then audit the client side. This is the work that matters: an app that never had a working
CSRF story now needs one.

```bash
grep -rn "X-CSRF-Token\|csrf" \
  --include="*.js" --include="*.ts" --include="*.jsx" --include="*.tsx" \
  --include="*.vue" --include="*.svelte" . || echo "no client-side CSRF handling at all"
```

The contract is three lines — fetch on boot, re-read the header from every response, send
on every mutating request:

```js
const { body } = await (await fetch('/csrf', { credentials: 'include' })).json()
let csrfToken = body.csrf_token

async function api(url, options = {}) {
  const res = await fetch(url, { ...options, credentials: 'include',
    headers: { ...options.headers,
      ...(options.method && !['GET','HEAD'].includes(options.method)
        ? { 'X-CSRF-Token': csrfToken } : {}) } })
  const fresh = res.headers.get('X-CSRF-Token')
  if (fresh) csrfToken = fresh
  return res
}
```

For a server-rendered form, put the token in a hidden `csrf_token` field, sourced from
`CsrfService.GetCSRFToken`.

If your SPA is on a different origin, expose the header or it cannot read it:

```bash
grep -rn "ExposedHeaders" --include="*.go" .
```

```go
ExposedHeaders: []string{core.CSRFTokenHeader},
```

---

## Phase 4c — Malformed bodies are 400, not a 301 (behaviour)

A body that could not be parsed used to produce an empty `ValidationErrors`, which the
validation handler answered with a **301 redirect to the `Referer`**. It is now a `400`
carrying `err.InputBodyParseError`.

```bash
# A registered handler for this error now actually fires — it never did before
grep -rn "InputBodyParseError" --include="*.go" . || echo "no handler registered"

# Anything treating a 301 from a POST as meaningful
grep -rn "301\|StatusMovedPermanently" \
  --include="*.js" --include="*.ts" --include="*.go" . | grep -vi redirect || echo "no matches"
```

If you have a handler, make sure it reports the *reason* and not `err.Error()`:
`InputBodyParseError.Error()` includes the raw body for the log's benefit, and a body that
failed to parse is exactly the kind that might carry a password halfway through.

```go
func inputErrorHandler(err error, message core.HttpMessage) {
    reason := err.Error()
    if parseError, ok := err.(*error2.InputBodyParseError); ok {
        reason = "the request body could not be parsed"
        if parseError.RawError != nil {
            reason = parseError.RawError.Error()
        }
    }
    message.Response().JSON(dto.ReturnObject(nil, core.BadRequestHttpStatus, reason), 400)
}
```

Note the 400-vs-422 split: a body that *parses* but holds a value a field rejects is still
`422` with `ValidationErrors`. Only unparseable bodies are `400`.

---

## Phase 4d — Check every `${VAR}` is actually set

This is a fix rather than a break, but the old behaviour was the opposite of what the
config sample implies, and it was security-relevant.

`${VAR}` substitution used `os.Getenv`, which cannot tell an unset variable from an empty
one, and wrote the result into viper's highest-precedence layer. So a placeholder for a
**missing** variable produced a key that was present, empty, and unbeatable by any default.
With the documented `secure: ${SESSION_COOKIE_SECURE}` and the variable unset, the session
cookie shipped **without** `Secure` and nothing said so.

```bash
# Every placeholder in your config
grep -rn '\${' config/
```

**For each one, confirm the variable is set in every environment the app runs in** — local,
CI, staging, production. Until now a missing one silently degraded rather than failing.

The two security-relevant keys now stop the boot when unresolved:

- `auth.jwt.secret`
- `auth.session.cookie.secure`

```
config: security-relevant key(s) reference environment variables that are not set:
auth.session.cookie.secure (${SESSION_COOKIE_SECURE}). Set them, or remove the
placeholder so the framework's secure default applies — an unset placeholder must
never silently weaken security
```

Removing the placeholder is a valid fix: the framework's default is `secure: true`.

Any other unresolved placeholder is left as the literal `${VAR}` with a warning, so a
connection error naming `${DB_HOST}` is now diagnosable where an empty host gave no clue.


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

### 5d. `CleanupJob does not implement core.IJob`

Scheduled jobs never ran, on any version: `JobProvider.Boot` field-injected a zero-value
scheduler instead of resolving the registered one, so every job was registered against one
object and a different, empty one was started. Fixing that required a scheduler the
framework controls, and `gocron` is now out of the module entirely.

```bash
grep -rn "gocron\|GetInterval()\|GetUnit()\|GetJob()" --include="*.go" . || echo "no jobs"
```

Convert each job:

```go
// BEFORE
func (j CleanupJob) GetInterval() uint64   { return 1 }
func (j CleanupJob) GetUnit() gocron.Unit  { return gocron.Hours }
func (j CleanupJob) GetJob() (any, []any)  { return func() { j.do() }, nil }

// AFTER — note the pointer receiver
func (j *CleanupJob) Schedule() core.JobSchedule {
    return core.JobSchedule{
        Every:        time.Hour,
        RunAtStartup: true,   // decide per job; not expressible before
        AllowOverlap: false,  // decide per job; not expressible before
    }
}

func (j *CleanupJob) Run(ctx context.Context) error {
    j.do()
    return nil
}
```

Unit conversion:

| Before | After |
|--------|-------|
| `30, gocron.Seconds` | `Every: 30 * time.Second` |
| `5, gocron.Minutes` | `Every: 5 * time.Minute` |
| `1, gocron.Hours` | `Every: time.Hour` |
| `1, gocron.Days` | `Every: 24 * time.Hour` |

Register jobs as **pointers**. A job registered by value that carries
`container:"inject"` tags is now a loud error rather than a silently unfilled struct.

```bash
grep -rn "AddJob(" --include="*.go" .
```

```go
jobProvider.AddJob(&CleanupJob{})   // not CleanupJob{}
```

Then remove the dependency:

```bash
go mod tidy && grep -n gocron go.mod || echo "gocron gone"
```

Cron expressions are not supported. If you had one, schedule at the finest interval you
care about and check the clock inside `Run`.

**Then actually watch a job fire.** Nothing in the framework's own repo noticed that jobs
never ran, so a green build proves nothing here. Set one to `Every: 5 * time.Second`
temporarily and confirm it logs.

### 5e. `Make(...)` now returns an error where it used to return nil

`Container.Make` on a pointer-to-struct fills its `container:"inject"` fields; it does not
hand back the registered singleton. Both used to return `nil`, so a caller expecting the
singleton got a zero value and no error — which is how the framework's own job scheduler
came to tick empty.

```bash
grep -rn "\.Make(&" --include="*.go" .
```

For each hit, decide which you meant:

```go
// You wanted the registered instance
var scheduler *job.Scheduler
if err := c.Resolve(&scheduler); err != nil { return err }

// You wanted this struct's dependencies filled — keep Make
resolver := &MyThing{}
if err := c.Make(resolver); err != nil { return err }
```

This is a **runtime** error, not a compile error, and it only fires when a binding exists
for that type. The message names the method you wanted. Boot the app and read the log.

### 5f. `NewCorsMiddleware` panics on wildcard-plus-credentials

Browsers reject `Access-Control-Allow-Origin: *` together with
`Access-Control-Allow-Credentials: true`, so that pair was a silent failure with no
diagnostic. It is now refused at construction.

```bash
grep -rn "AllowCredentials" --include="*.go" . -B 5 -A 2
```

Any hit where `AllowCredentials: true` sits with `AllowedOrigins: []string{"*"}` — **or
with no `AllowedOrigins` at all**, which also means "all origins":

```go
// BEFORE — constructed fine, failed in every browser
AllowedOrigins: []string{"*"}, AllowCredentials: true

// AFTER
AllowedOrigins: []string{"https://app.example.com"}, AllowCredentials: true
// A wildcard *within* an origin is still fine:
AllowedOrigins: []string{"https://*.example.com"}, AllowCredentials: true
```

For a policy built from configuration you do not control, use
`NewCorsMiddlewareChecked(options) (*Cors, error)`.

Nothing that worked stops working: if the pair was configured, the credentialed requests
were already failing.

### 5g. `core.MongoDb` is gone

```bash
grep -rn "core.MongoDb\|MongoDb\b" --include="*.go" . || echo "not used"
```

It was a `DbType` constant with no driver behind it, so `driver: mongo` failed at boot
while the exported constant advertised support. If you referenced it, you were not
connecting to Mongo through this framework.

### 5h. MySQL only: `ON CONFLICT … DO UPDATE` now errors

```bash
grep -rn "DoUpdate(" --include="*.go" .
```

Postgres is unaffected. On MySQL the translation to `ON DUPLICATE KEY UPDATE` silently
dropped the conflict-target columns, and MySQL keys off *any* unique index — so on a table
with more than one, the row that got updated was not yours to control. Either opt in, having
confirmed the table has exactly one unique index:

```go
dialect := mysqlv2.MySQLDialect{AllowUnfaithfulUpsert: true}
```

or use `DoNothing()` (unaffected) or an explicit read-then-write in a transaction.

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

### 7f. A route whose DTO has no body parser now fails at boot

A handler taking a DTO whose `ContentType()` returns something no parser handles used to
reach the request path and panic there. It is now rejected at route registration, with the
route name and the offending type in the message. If the app stops booting with

```
route POST /widgets (widgets.create): DTO app.BrokenDto returns an empty ContentType(),
so no body parser can be selected; return one of application/json, multipart/form-data, query
```

fix the DTO's `ContentType()`. This is a bug the framework was previously serving 500s for.

### 7g. 404, 405 and error handlers now negotiate

An unknown route used to return an empty body; `405` was chi's bare default with nothing
registered. Both now return the standard envelope to an API client and plain text to a
browser, and `405` names the method it rejected. `processInputParsingError` and
`processJwtAuthError` used to write literally empty bodies and now say what happened.

```bash
# Tests asserting on an empty 404/405/401 body
grep -rn "StatusNotFound\|StatusMethodNotAllowed\|StatusUnauthorized" --include="*_test.go" .
```

Status codes are unchanged. Your own `SetNotFound` handler still wins, so if you registered
one, nothing changes for 404.

### 7h. Route middleware no longer leaks across methods

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

- **`testsupport`** — a database test harness. If you hand-rolled one (connect, wait for
  the engine, migrate, truncate between tests, skip when nothing is running), you can
  probably delete it:

  ```go
  func TestMain(m *testing.M) {
      testsupport.AddMigration(migrations.All()...)
      testsupport.Main(m)
  }

  func TestSavingAWidget(t *testing.T) {
      db := testsupport.RequireDatabase(t)
      // ... db.Session(), db.CountRows(t, "widgets"), db.Exec(t, ...)
  }
  ```

  `EachDatabase` runs the same test against Postgres and MySQL as subtests. Read
  `docs/TESTING.md` in the framework repo — in particular the note that
  `IsolateByRollback` only works if the code under test uses the harness's session.

- **Rate limiting.** There was none, anywhere, on any version — including on login and
  OTP endpoints. If you wrote your own, compare:

  ```go
  routeProvider.AddMiddleware(
      http.NewMiddlewareConfigBuilder().
          WithPattern("/session/login").
          AsFilter().
          WithMiddleware(middleware.NewRateLimitMiddleware(middleware.PerMinute(5))).
          Build(),
  )
  ```

  Note the two things worth knowing before relying on it: limits are **per-instance**
  (the default store is in process memory, so N replicas allow N times the rate), and
  `X-Forwarded-For` is **ignored** unless you set `TrustForwardedFor` — trusting it when
  the app is not behind a proxy lets any client pick its own bucket. `docs/RATE_LIMITING.md`.

- **Serving a built SPA.** `PublicController` serves `/public/*` and returns 400 for
  anything else, so a deep link could not work at all. `controller.NewSpaController("web/dist")`
  serves real files with an `index.html` fallback, `no-store` on the entry document and
  immutable caching on hashed assets. Mount it **last** — it claims a catch-all.
  `docs/SPA.md`.

- **A `role` claim on generated JWTs**, readable with `auth.RoleFromClaims`. It is
  informational: `JwtMiddleware` still resolves the user, because a signed token is frozen
  for its lifetime and a role baked into one would outlive a demotion. Use the claim for UI,
  not for authorisation.

- **Localised validation messages** under `validation.<rule>` in your translation files,
  and `validator.DefaultMessages()` to see what you are overriding. `docs/VALIDATION.md`.

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

# 8. A scheduled job actually fires. Set one to Every: 5 * time.Second and watch
#    the log. Jobs never ran on any prior version and nothing noticed, so a green
#    build proves nothing here.

# 9. A CSRF token can be obtained and is then accepted.
TOKEN=$(curl -s -c /tmp/gj http://localhost:8080/csrf \
  | sed 's/.*"csrf_token":"\([^"]*\)".*/\1/')
curl -i -b /tmp/gj -X POST -H "X-CSRF-Token: $TOKEN" \
  http://localhost:8080/<a-mutating-route>

# 10. A malformed body is a 400 envelope, not a 301 to the Referer.
curl -i -X POST -H 'Content-Type: application/json' --data '{"a":' \
  http://localhost:8080/api/<a-body-route>

# 11. An unknown route returns the envelope to an API client.
curl -i -H 'Accept: application/json' \
  http://localhost:8080/api/definitely-not-a-route

# 12. Validation errors carry the wire field name and a readable message.
curl -s -X POST -H 'Content-Type: application/json' --data '{}' \
  http://localhost:8080/api/<a-validated-route>

# 13. Every ${VAR} in config/ is set in this environment.
grep -rn '\${' config/
```

If you use MySQL:

```bash
# 14. orm.Create inserts a row and the entity comes back with its id.
#     The ORM ignored the session's dialect entirely before this release, so every
#     ORM query against MySQL emitted Postgres SQL. This one needs a real insert
#     against a real server — no unit test in the framework caught it.
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
6. For each of the three things that never worked — a job firing (5d), `orm.Create`
   on MySQL (Phase 6), a CSRF-protected request with a token from `/csrf` (4b) —
   state whether you actually observed it working, or say plainly that you did not.
   A green build is not evidence for any of them.
7. Every `${VAR}` in `config/` that you could not confirm is set, and in which
   environment.
