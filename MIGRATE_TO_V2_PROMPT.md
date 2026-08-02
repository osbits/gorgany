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

1. Work through the phases in order. Phases 1–4m are behaviour changes that compile
   cleanly; Phases 5–8 are signature changes the compiler will find for you. Doing
   the behaviour work first means you are not making judgement calls while chasing
   build errors.
2. **Do not change the dependency until Phase 5.** Phases 1–4m are audits of the
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
7. Three changes compile *and vet* cleanly and fail only when the app boots: the
   driver import (5i), an app-owned key under `databases.<name>` (5j), and an
   absent or weak `auth.jwt.secret` (5n). **Boot the app** before you call the
   migration done — `go build ./... && go vet ./...` passing means nothing for any
   of them.
8. **One change compiles, vets, boots and then quietly logs nobody in: Phase 4h.**
   `Login` now rotates the session identifier, and a login handler that does not
   call `http.PublishSession` leaves the rest of the request looking at a session
   that has been deleted. There is no error anywhere. Log in with `curl` and make a
   second request on the cookie you were given; that is the only check that catches
   it.
9. **Two changes are visible to users on the first request after the deploy, and
   neither is a code change you can make.** The session cookie is renamed, so every
   signed-in user is logged out (Phase 4g); and `/public/*` no longer renders SVG,
   so every `<img src="/public/….svg">` breaks (Phase 4i). Report both to whoever
   owns the deploy before you finish.

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

**The re-read is mandatory, not an optimisation.** The token does not rotate per request,
but the session is replaced more often than you would guess and one of those replacements
replaces the token:

- The session's own rotations — past the idle timeout, and past the rotation interval —
  **carry the token over**. They happen on a request the client did not ask to rotate
  anything on, so minting a new token there would reject a form that was already rendered.
- **Logging in replaces it.** `Login` rotates the session and mints a new token, because
  every secret a pre-login session held was chosen by whoever presented that session. The
  login response carries the replacement in `X-CSRF-Token`.

So a session keeps one token for as long as it is the same session **and the same
principal**. A client that caches its pre-login token has every mutating request rejected
with `Invalid CSRF token` until it calls `GET /csrf` again — check specifically that yours
re-reads the header on the **login** response, not only on ordinary ones.

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

Note the 400-vs-422 split: a body that *parses* but holds a value a field rejects is a `422`
with `ValidationErrors` for an API client, and a `303` back to the `Referer` for a browser
form — see Phase 4e. Only unparseable bodies are `400`.

---

## Phase 4d — Check every `${VAR}` is actually set

This is a fix rather than a break, but the old behaviour was the opposite of what the
config sample implies, and it was security-relevant.

`${VAR}` substitution used `os.Getenv`, which cannot tell an unset variable from an empty
one, and wrote the result into viper's highest-precedence layer. So a placeholder for a
**missing** variable produced a key that was present, empty, and unbeatable by any default.
With the documented `secure: ${SESSION_COOKIE_SECURE}` and the variable unset, the session
cookie shipped **without** `Secure` and nothing said so.

An unresolved placeholder is now **blanked**, and the two security-relevant keys stop the
boot. It is not left as the literal `${VAR}`: a literal is a value no consumer accepts, and
the failure that matters is the silent one — an unresolved `auth.session.cookie.domain`
becomes an invalid cookie `Domain` attribute, so the browser drops `Set-Cookie` and login
fails with nothing in any log. `config.KeepUnresolvedLiterals()` opts back in if you would
rather see `${DB_HOST}` in a connection error.

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

Any other unresolved placeholder is blanked, with a warning naming the key and the variable.
Note what the warning also tells you and what the old one got wrong: the key stays *present*,
so a `SetDefault` for it will not apply. Set the variable, or remove the placeholder to fall
back to the framework's default.

**If your app substitutes `${VAR}` itself**, replace that with a call to the framework's
resolver:

```bash
grep -rn 'viper.Set\|os.Getenv\|LookupEnv' --include="*.go" . | grep -v "_test"
```

```go
// BEFORE — a local substitution loop
for _, k := range viper.AllKeys() { ... viper.Set(k, os.Getenv(name)) ... }

// AFTER
if err := config.ResolveEnvPlaceholders(); err != nil {
    return err
}
```

This matters more than it looks. `viper.Set` writes the *override* layer, and a map fetch
resolves against the highest layer holding the key without deep-merging the ones below — so
substituting `databases.default.host` with `viper.Set` makes `GetStringMap("databases")`
return only that key, and `driver`, `log` and `properties` vanish. The symptom is a boot
panic naming an unrelated key:

```
panic: database 'default': datasource config: 'driver' is required
```

The framework's resolver uses `MergeConfigMap`, which deep-merges and does not do this.


---

## Phase 4e — Validation failures now reach an API client (behaviour)

A DTO that failed validation used to produce a **301 redirect to the `Referer`** for every
caller, API clients included. So the reshaped payload from Phase 4a was unobservable through
the framework's own handler — the only way to get a `422` was to register your own handler.

Now: `422` with the envelope for an API client, `303 See Other` back to the `Referer` for a
browser form, and `422` when there is no `Referer` to go back to.

```bash
# Did you register a ValidationErrors handler? If it exists only to produce a 422, you can
# delete it — compare it against the framework's first, the shapes are the same.
grep -rn '"ValidationErrors"\|"ValidationError"' --include="*.go" .

# Anything client-side that treats a 301 from a POST as meaningful
grep -rn "301\|StatusMovedPermanently" \
  --include="*.js" --include="*.ts" --include="*.vue" --include="*.go" . \
  | grep -vi redirect || echo "no matches"
```

Two details if you keep a browser flow:

- The redirect is `303`, not `301`. A `301` is permanently cacheable and browsers rewrite it
  to a `GET`, so a browser could cache "POST this URL → GET that one" indefinitely. If you
  asserted on `301` in a test, that is the change.
- The errors are still flashed into the session under the same key, so a server-rendered form
  renders them exactly as before.

Verify against a real endpoint:

```bash
# API client → 422 with the payload
curl -i -X POST -H 'Content-Type: application/json' --data '{}' \
  http://localhost:8080/api/<a-validated-route>

# Browser form with a Referer → 303
curl -i -X POST -H 'Referer: http://localhost:8080/form' \
  -d 'field=' http://localhost:8080/<a-form-route>

# No Referer → 422 rather than a redirect to nowhere
curl -i -X POST -d 'field=' http://localhost:8080/<a-form-route>
```

---

## Phase 4f — Response shapes: `omitempty` and embedded collisions (behaviour)

The API envelope now applies `encoding/json`'s emptiness rule rather than
`reflect.Value.IsZero()`, and resolves an embed/outer wire-name collision by depth rather
than by declaration order. Both move response shapes.

| field with `,omitempty` | before | after (= `encoding/json`) |
|---|---|---|
| `[]string{}` (non-nil, len 0) | emitted | **omitted** |
| `map[string]string{}` (len 0) | emitted | **omitted** |
| `time.Time{}` | omitted | **emitted** as `"0001-01-01T00:00:00Z"` |
| zero nested struct | omitted | **emitted** |

```bash
# The fields that can move
grep -rn 'omitempty' --include="*.go" . | grep -iE 'time\.|\[\]|map\['

# DTOs where an embed and an outer field might share a wire name
grep -rn -B 3 -A 10 'json:"' --include="*.go" . | grep -B 6 -A 6 '^\S*-\s*[A-Z][A-Za-z]*Dto$'
```

The `time.Time` row is the one to check. A `CreatedAt time.Time` tagged
`json:"created_at,omitempty"` on a not-yet-persisted record used to vanish and now appears as
the zero time. If a client treats key-absence as meaningful, that is a real change — drop
`,omitempty` from the field if the old shape is what you want.

The reliable check is a diff:

```bash
# Capture the same responses before and after the upgrade and compare
curl -s http://localhost:8080/api/<a-dto-route> | jq -S . > after.json
diff before.json after.json
```

## Phase 4g — The session cookie is renamed: every user is logged out (behaviour, security)

**What changed.** The session cookie is now `__Host-GRG_SESSION_ID`, not
`GRG_SESSION_ID`. The constant `core.SessionCookieName` is deleted (that part is Phase 5l).

**Why it matters.** A browser holding the old cookie sends a name the framework no longer
reads, so **on the first request after the deploy every signed-in user is logged out** and
starts a fresh anonymous session. There is deliberately no fallback to the old name. This
is not a code change you can make; it is a deploy you have to plan.

The prefix is what stops cookie tossing: an unprefixed name is a token any host under the
registrable domain can also write, even when the app's own cookie is host-only, and the
framework then reads the identifier that host chose. A browser refuses to let any other
host set a `__Host-` cookie for yours.

**Find anything that names the cookie outside Go:**

```bash
grep -rn 'GRG_SESSION_ID' --include='*.js' --include='*.ts' --include='*.vue' \
  --include='*.yaml' --include='*.yml' --include='*.conf' --include='*.tf' \
  --include='Dockerfile*' .
```

Check three things that produce no compile error and no test failure:

1. A reverse proxy, CDN or WAF rule keyed on the cookie name — cache-bypass rules, sticky
   sessions, exemptions. An exact match on `GRG_SESSION_ID` will not match the new name.
2. A synthetic monitor or smoke test that sets the cookie by name.
3. Your own config:

```bash
grep -rn 'auth.session.cookie' config/
```

`auth.session.cookie.secure: false` (the documented local-HTTP setting) and
`auth.session.cookie.domain: <anything>` each keep the **old** name, because a `__Host-`
cookie must be Secure and may not carry a Domain. If either is set in production you keep
the old name, log nobody out, and keep the exposure. **Do not set the domain purely to
avoid the logout** — set it only if you genuinely share a session across subdomains.

Note that your local development cookie name will differ from production if you use
`secure: false` locally. That is correct: a `__Host-` cookie that is not Secure is
discarded by the browser silently, which would make every local request look logged-out
with nothing reporting an error.

**Report:** whether you are on the prefixed or the unprefixed name in each environment,
and the list of infrastructure rules that need the new name. Then tell whoever owns the
deploy that it ends every session.

---

## Phase 4h — Your login handler must republish the session (compile-clean, security)

**What changed.** `StandardAuthStrategy.Login` now revokes the session the client
presented, mints a fresh identifier, and issues a new CSRF token. It used to assign the
user id to whatever session arrived.

**Why it matters.** This is the most dangerous item in this document, because **it
compiles.** The session your request resolved on the way in no longer exists when `Login`
returns, and two places cache it for the life of the request: the message's session scope,
and the message context — which `ResolveSessionId` consults *before* the cookie. A handler
that does not republish leaves `message.Session().Get()` and `IsLoggedIn` looking at a
deleted session, so a login that fully succeeded is indistinguishable from one that failed.
Your app builds, your tests may well pass, and nobody can log in.

**Find every login handler:**

```bash
grep -rn '\.Login(' --include='*.go' . | grep -v '_test.go'
```

For each, add one line:

```go
// BEFORE
_, err := strategy.Login(user, message.Context())
if err != nil { /* ... */ }
message.Response().Redirect(homeUrl, 301)

// AFTER
session, err := strategy.Login(user, message.Context())
if err != nil { /* ... */ }

grghttp.PublishSession(message, session) // github.com/osbits/gorgany/v2/http

message.Response().Redirect(homeUrl, 301)
```

`http.PublishSession` installs the session on the session scope **and** the message
context, drops the request's memoised identity, and rewrites `X-CSRF-Token` from the new
session's token. It replaces any hand-rolled
`message.Session().(core.IEditableSessionScope).Set(...)`, which covered only the first of
those.

**Then delete the already-authenticated guard from your POST handler:**

```bash
grep -rn -B 4 -A 4 'IsLoggedIn(' --include='*.go' . | grep -v '_test.go'
```

```go
// DELETE this from the POST handler
if strategy.IsLoggedIn(message.Context()) {
    message.Response().Redirect(homeUrl, 301)
    return
}
```

A login POST answered with a redirect is a login POST whose credentials were never
compared to anything: whoever the session already belonged to keeps it and the poster is
handed that identity. Turned around it is an attack — a party who can plant the session
cookie plants their own *signed-in* session, the victim's login bounces off the guard,
their password is never checked, and they browse inside the planted account while the
planter holds a live cookie for the same session. It is safe to remove now precisely
because `Login` replaces the session rather than reusing it.

**Keep the guard on the GET handler**, and make sure it `return`s after redirecting. A GET
carries no credentials, so there is nothing to verify and nothing to donate.

**Do not** end the session on a failed login attempt. That would hand anybody able to make
a browser post the form a remote logout with no credentials at all.

**One more thing to check — clients and tests that read `Set-Cookie`:**

```bash
grep -rn 'Cookies()\[0\]\|Set-Cookie' --include='*.go' --include='*.js' --include='*.ts' .
```

A login response now carries **two** `Set-Cookie` headers for the session when the request
arrived without one (the middleware starts a session, the login rotates it). Browsers and
real cookie jars keep the last. Anything taking the **first** picks up an identifier that
has just been revoked.

**Verify at runtime, not by building:**

```bash
curl -s -c /tmp/j -X POST -d 'username=u&password=p' http://localhost:8080/login >/dev/null
curl -i -b /tmp/j http://localhost:8080/<a-route-requiring-auth>
# 200 = correct. 401 or a redirect to /login = you are missing PublishSession.
```

---

## Phase 4i — SVG and HTML under `resource/public` (behaviour, security)

**What changed.** Every `/public/*` response now carries `X-Content-Type-Options: nosniff`
and `Content-Disposition: attachment`, and the `Content-Type` is passed through only for a
render-safe allowlist: `image/*` **except SVG**, `audio/*`, `video/*`, `font/*`,
`text/plain`, `text/css`, `text/csv`, JavaScript, `application/json`, `application/pdf`.
Everything else — `text/html` and `image/svg+xml` included — is served as
`application/octet-stream`.

**Why it matters.** `Content-Disposition` does not apply to a subresource load, so
stylesheets, scripts, raster images and fonts keep loading. The retyping *does* apply, and
**SVG is the casualty**: an SVG answered as `application/octet-stream` under `nosniff` is
one the browser refuses to render.

**Find every broken icon:**

```bash
find resource/public -name '*.svg'

grep -rn 'public/.*\.svg' --include='*.html' --include='*.amber' --include='*.tmpl' \
  --include='*.css' --include='*.scss' --include='*.js' --include='*.ts' --include='*.vue' .
```

Every hit is an `<img>`, a `<use href>` or a CSS `url()` that stops rendering. Three ways
out, in order of preference:

1. **Move app-authored assets out of the upload root.** Serve them from a controller of
   your own over an `assets/` directory, or from a CDN. This is the right answer
   regardless of this change: the directory users can upload into is not where your logo
   belongs.
2. **Inline the SVG** as an `<svg>` element in the template.
3. **Use a raster fallback** if the asset is decorative.

Adding `image/svg+xml` back is not on the list. There is no way to serve your SVG as
`image/svg+xml` from this endpoint that does not also serve an *uploaded* SVG that way,
and an SVG served from your origin runs script on it.

**Also find HTML under the public root:**

```bash
find resource/public -name '*.html' -o -name '*.htm'
```

These now download rather than opening. Serve app-authored HTML through a view or a
dedicated route.

**Verify:**

```bash
curl -sI http://localhost:8080/public/<some-file> \
  | grep -iE 'content-type|content-disposition|x-content-type-options'
```

---

## Phase 4j — Upload types and stored extensions (behaviour, security)

**What changed.** An upload's stored extension now comes from what its bytes sniff to
(`http.DetectContentType` on the first 512 bytes), looked up in an allowlist, and a type
with no entry is **refused**. The extension used to be `filepath.Ext(clientFilename)`
appended verbatim, and the mime allowlist was applied to `Content-Type` on the part — a
value the uploading client writes. The DTO-binding path (a `core.IFile` field on a DTO) had
no content check at all.

**Why it matters.** Uploads that used to be accepted can now be rejected, and accepted ones
are stored under a different name.

**Find what your app accepts:**

```bash
grep -rn 'FormFile\|GetFiles\|core.IFile\|NewMultipartFile' --include='*.go' .
grep -rn 'allowedMimes\|allowedTypes' config/

# What extensions are actually on disk today
ls resource/public | sed 's/.*\.//' | sort | uniq -c | sort -rn
```

The built-in table covers PNG, JPEG, GIF, WebP, BMP, TIFF, ICO, PDF, ZIP, GZ, RAR, OGG,
WASM, WOFF, TTF, MP3, WAV, AIFF, MIDI, AU, MP4, WebM, AVI and `text/plain`, plus
`application/octet-stream` → `.bin`. That last entry is the one that keeps working apps
working: any binary format Go has no signature for is stored as `.bin` rather than
rejected, and `.bin` is inert.

**What will surprise you:**

- A `.docx`, `.xlsx`, `.pptx` or `.jar` sniffs as `application/zip` and is stored as
  `.zip` — accepted, but under an extension that is not the one the user uploaded. The
  sniffer cannot tell zip-based formats apart.
- Anything sniffing to `text/html`, `image/svg+xml` or an XML type is **rejected**.
- The stored extension no longer matches the uploaded one for anything the sniffer
  disagrees with the client about.

**If you need more types**, configure them — and note that setting this key **replaces the
built-in table wholesale**, so restate everything you still want:

```yaml
http:
  upload:
    allowedTypes:
      image/png: .png
      image/jpeg: .jpg
      application/pdf: .pdf
      application/zip: .zip
```

A configured value that is not an extension (anything but a dot and ASCII alphanumerics,
or over 16 characters) is dropped rather than obeyed — the extension is joined onto a
filesystem path.

Overriding `application/zip` to `.docx` is possible, but it renames *every* zip-based
upload to `.docx` — the sniffer cannot distinguish docx, xlsx, pptx, jar and a plain
archive. If several of them matter, store the client's filename in your own column and do
not rely on the stored name.

**If your own code applies an allowlist**, switch it to the sniffed type:

```go
// BEFORE — the client wrote this
if fh.Header.Get("Content-Type") != "application/pdf" { /* refuse */ }

// AFTER — this is what the bytes are
if file.(*model.MultipartFile).MediaType() != "application/pdf" { /* refuse */ }
```

**One API change to look for:**

```bash
grep -rn '\.Close()' --include='*.go' . | grep -i 'file\|upload'
```

`MultipartFile.Close()` no longer deletes a file that `Write` has published — use
`Delete()` for that. (It never actually deleted the temp copy either, so nothing you
relied on is being taken away; both halves were broken.) Temp copies are now released
automatically when the request ends.

**Verify:** upload one of every type your users actually send, and look at the resulting
filename.

---

## Phase 4k — Request bodies are capped (behaviour)

**What changed.** `http.MaxBytesReader` now caps every request body, at the server
boundary and again per request before any middleware, handler or parser reads it. Nothing
capped a body before — only `Request().Body()` had a limit of its own, so `BodyReader()`,
`ParseForm` and any hand-rolled streaming had none, and the multipart parser spilled
unbounded bytes to the OS temp directory before checking any size.

The ceiling derives from your upload budget so the two cannot disagree:

```
max(32 MB, http.upload.maxMultipartSize) + 1 MB framing allowance
```

**Find code that reads a large body:**

```bash
grep -rn 'BodyReader()\|io.ReadAll\|ParseForm\|GetMultipartFormValues' --include='*.go' .
grep -rn 'maxMultipartSize\|maxFileSize\|maxSizeMB' config/
```

If you accept uploads larger than 32 MB, or stream something large that is not an upload,
raise the limit:

```yaml
http:
  upload:
    maxMultipartSize: 104857600      # raises the derived ceiling with it
  security:
    body:
      maxRequestBytes: 209715200     # or override the derivation outright, in bytes
```

**If you call `GetMultipartFormValues` yourself, check for nil.** Nil now means "malformed,
or over a limit" and must be treated as a client error. The framework's own caller ranged
straight over `form.File` on that nil pointer, which is why any client could panic any
multipart endpoint with a truncated body.

**Verify:**

```bash
head -c 40000000 /dev/urandom | curl -si -X POST --data-binary @- \
  http://localhost:8080/<a-body-route> | head -1
```

---

## Phase 4l — Mail rejects CR/LF in a header value (behaviour, security)

**What changed.** `MailService.buildBody` rejects a CR or LF in the sender, the subject,
any recipient, any CC or BCC entry, and any attachment file name or content id, and `Send`
fails rather than delivering. The generated message also uses CRLF throughout and RFC
2047-encodes non-ASCII header text.

**Why it matters.** Headers were assembled with `fmt.Sprintf` and no filtering, so a
newline in a subject produced *extra headers* and, after a blank line, an entire
replacement body — sent from your authenticated SMTP identity with your SPF and DKIM
vouching for it. Any place user input reaches a subject was a vector.

**Find where user input reaches a header:**

```bash
grep -rn 'GetSubject\|SetSubject\|Subject' --include='*.go' . | grep -v '_test.go'
grep -rn 'mail\.\|MailService\|core.IMail' --include='*.go' . | grep -v '_test.go'
```

For each, sanitise at the boundary where you can report it to the user, rather than
letting the mailer refuse the send:

```go
// BEFORE
subject := fmt.Sprintf("New message from %s", form.Name)

// AFTER
subject := fmt.Sprintf("New message from %s", strings.Join(strings.Fields(form.Name), " "))
```

Collapsing newlines to spaces is usually right. Multi-line values were never delivered as
intended anyway — they were delivered as extra headers.

**Then find tests that assert on raw message bytes:**

```bash
grep -rln 'MIME-version\|Content-Transfer-Encoding' --include='*_test.go' --include='*.golden' .
```

Line endings are CRLF now, and a non-ASCII subject appears as `=?utf-8?q?…?=`. Assert on
the decoded value with `mime.WordDecoder.DecodeHeader` rather than on the raw header.

---

## Phase 4m — Security headers and framing (behaviour)

**What changed.** `middleware.SecurityHeadersMiddleware` is registered automatically as a
`/**` filter. The framework emitted no security headers at all before.

| Header | Default |
|---|---|
| `X-Content-Type-Options` | `nosniff` (not configurable) |
| `X-Frame-Options` | `SAMEORIGIN` |
| `Referrer-Policy` | `strict-origin-when-cross-origin` |
| `Strict-Transport-Security` | `max-age=31536000`, TLS responses only |
| `Content-Security-Policy` | **not sent** |

**The one that can break a working page is `X-Frame-Options: SAMEORIGIN`.**

```bash
grep -rn '<iframe' --include='*.html' --include='*.amber' --include='*.vue' .
```

If a third party embeds your app in a frame, set `FrameOptions: "off"` and manage framing
with a CSP `frame-ancestors` directive instead:

```go
routeProvider.ConfigureSecurityHeaders(middleware.SecurityHeadersOptions{
    FrameOptions:          "off",
    ContentSecurityPolicy: "frame-ancestors 'self' https://partner.example.com",
})
```

**The second thing to check is templates that render something other than HTML.**
`HTTPViewScope.Render` now sets `Content-Type: text/html; charset=utf-8` when nothing has
chosen one — it used to send no type at all and let the browser guess, which `nosniff` now
forbids.

```bash
grep -rn 'View().Render' --include='*.go' . | grep -v '_test.go'
```

A sitemap, an RSS feed or a plain-text body rendered through a template needs its type set
first, and that choice still wins:

```go
message.Response().SetHeader("Content-Type", "application/xml; charset=utf-8")
message.View().Render("sitemap", data)
```

**Do not adopt a CSP as part of this upgrade.** There is deliberately no default one — a
policy worth having forbids inline script and style, and a server-rendered app routinely
has both. Measure first, in a separate piece of work:

```go
routeProvider.ConfigureSecurityHeaders(middleware.SecurityHeadersOptions{
    ContentSecurityPolicyReportOnly: middleware.RecommendedContentSecurityPolicy,
})
```

**Verify:**

```bash
curl -sI http://localhost:8080/ \
  | grep -iE 'x-content-type-options|x-frame-options|referrer-policy'
```

---

## Phase 5 — Bump the dependency and let the compiler drive

```bash
# The module path gained a /v2 suffix: Go requires one for major version 2 and above, so
# `require github.com/osbits/gorgany v2.0.0` fails outright with
# "version v2.0.0 invalid: should be v0 or v1, not v2".
#
# Rewrite every import first, then swap the requirement.
#
# Guard: the next line must print nothing. The rewrite is not idempotent — running it on a
# tree already on /v2 produces github.com/osbits/gorgany/v2/v2.
grep -rn '"github.com/osbits/gorgany/v2' --include="*.go" .

grep -rl '"github.com/osbits/gorgany' --include="*.go" . \
  | xargs sed -i '' \
      -e 's|"github.com/osbits/gorgany/|"github.com/osbits/gorgany/v2/|g' \
      -e 's|"github.com/osbits/gorgany"|"github.com/osbits/gorgany/v2"|g'
gofmt -w .

go mod edit -droprequire=github.com/osbits/gorgany
go get github.com/osbits/gorgany/v2@v2.0.0
go mod tidy

# On GNU sed, drop the '' after -i. Then check for the path outside Go files:
grep -rn "osbits/gorgany" --include="*.yml" --include="*.yaml" --include="Dockerfile*" \
  --include="Makefile" . | grep -v "/v2"
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

with `dsconfig "github.com/osbits/gorgany/v2/db/sql/config"`.

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

```yaml
databases:
  main:
    driver: mysql_gorm
    allow_unfaithful_upsert: true
```

or use `DoNothing()` (unaffected) or an explicit read-then-write in a transaction.

Use the config key, not `mysqlv2.MySQLDialect{AllowUnfaithfulUpsert: true}` — the struct
field only affects a builder you construct yourself, and the datasource builds its own
dialect.

### 5i. `no datasource drivers are registered` at boot

`provider.DbProvider` used to blank-import `db/sql/driver/builtin`, which registers both
engines — so every app on the standard bootstrap linked `gorm.io/driver/mysql`,
`go-sql-driver/mysql` and `filippo.io/edwards25519` whether or not it would ever speak
MySQL. It now registers nothing and you choose.

**This compiles and vets cleanly.** It fails on the first boot:

```
datasource config: no datasource drivers are registered, so "postgres_gorm" cannot be
resolved. Import the engine you use for its side effects — ...
```

Add one blank import next to your own provider package's imports:

```go
_ "github.com/osbits/gorgany/v2/db/sql/driver/postgres"   // Postgres only
_ "github.com/osbits/gorgany/v2/db/sql/driver/mysql"      // MySQL only
_ "github.com/osbits/gorgany/v2/db/sql/driver/builtin"    // both, exactly as before
```

Which one:

```bash
grep -rn "driver:" config/
```

Then confirm the other engine is actually gone, which is the point:

```bash
go mod tidy
go list -deps ./cmd/server | grep -E "mysql|postgres"
```

A Postgres-only app should show `gorm.io/driver/postgres` and no `mysql` line. Expect
`go mod tidy` to *remove* `gorm.io/driver/mysql`, `github.com/go-sql-driver/mysql` and
`filippo.io/edwards25519` from your `go.mod`.

### 5j. An app-owned key under `databases.<name>` is now rejected

`db/sql/config` rejects any key it does not recognise under a datasource, which is what
turns a typo like `databse` into a boot failure instead of a silently ignored setting. A
deliberate app-owned key is rejected too:

```yaml
databases:
  default:
    driver: postgres_gorm
    pool:                 # read by the app itself via viper
      maxOpen: 25
```

```
panic: database 'default': datasource config: unknown key(s) 'pool' under this database
  — did you mean 'properties' instead of 'pool'? — recognised keys are ...
```

This is worth knowing about if the framework's `properties` block never worked for you
before — it did not, on any version up to v1.5.1, because the camelCase lookups could never
match viper's lowercased keys — and you worked around it with a sibling block.

```bash
# Every key under every datasource. Anything not in the recognised list will now fail.
grep -rn -A 20 "^databases:" config/
```

Two ways out, and the error message names both: move the block under `properties`, whose
contents are passed through untouched, or out from under `databases.<name>` entirely.

This also **compiles and vets cleanly** — it is a `Register`-time panic — so it is another
boot check rather than a build one.

### 5k. `undefined: core.SessionCookieName`

The constant is gone. Every read and every write of the session cookie has to go through
the resolver, because which of two names is in use depends on configuration — see Phase 4g.

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

Use `auth.NewSessionCookie` rather than building the struct yourself. The name, `Secure`
and `Domain` are one decision, not three: a `__Host-` name paired with a `Domain` produces
a cookie no browser keeps, with no diagnostic anywhere. `maxAge < 0` expires it.

The constant was deliberately not left as a deprecated alias — code reading the cookie
under a hard-coded name would still compile and would silently read the wrong cookie.

`core.SessionCookieBaseName` (`"GRG_SESSION_ID"`) and
`core.HostPrefixedSessionCookieName` exist if you need to name the two forms, for example
in a test. Do not use either to read or write the cookie.

### 5l. `ISessionStorage` and `Logout` return errors

If you implement `core.ISessionStorage`, four mutators gained an `error` and the read
gained one:

```go
// BEFORE
ClearExpiredSessions()
AddSession(session ISession)
DeleteSession(session ISession)
DeleteSessionById(id string)
GetSessionById(id string) ISession

// AFTER — the four lifetime/timeout methods are unchanged
ClearExpiredSessions() error
AddSession(session ISession) error
DeleteSession(session ISession) error
DeleteSessionById(id string) error
GetSessionById(id string) (ISession, error)
```

Three rules the compiler will not enforce:

- **Deleting a session the store does not hold is not an error.** Revocation is
  idempotent; return `nil`.
- **`GetSessionById` returns `(nil, nil)` for "no such session" and `(nil, err)` for "the
  lookup failed".** Do not collapse the second into the first — "the store is unreachable"
  and "this visitor has no cookie" lead to different decisions.
- **`AddSession` must refuse to recreate a session the store no longer holds.** An
  identifier that has been revoked and can still be written back is a revocation bypass.

`core.IAuthStrategy.Logout` gained an error too:

```go
// BEFORE
Logout(ctx context.Context)
// AFTER
Logout(ctx context.Context) error
```

If you implement it: return an error when the server-side session could not be revoked,
and **do not expire the session cookie in that case** — the session is still live, and a
client that has thrown its cookie away cannot ask you to try again.

If you call it, the caller must say so:

```go
// BEFORE — fail-open: tells the user they are logged out of a live session
strategy.Logout(message.Context())
message.Response().Redirect(loginUrl, http.StatusTemporaryRedirect)

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

**`core.ISession` and `core.ISimpleStorage` are deliberately unchanged.** `SetUserId`,
`SetExpiry`, `SetLastActivity`, `GetItem`, `SetItem`, `ClearItem` and `ClearItems` stay
void, so your request scopes, view scopes and test doubles keep compiling. If you
implement a session whose write-through can fail, remember the failure and surface it at
the next operation that *can* report one — the storage call, or `Login`/`Logout`. And
synchronise every field two requests can touch: `GetUserId` feeds authorization decisions,
and a login overwrites it on a session other requests are already authorizing against.

Two smaller signature changes in the same area, if you touch them:

```go
// auth.ISessionRepository
DeleteById(id string) (bool, error)     // was: error
// auth.DbSessionMediator
DeleteSession(id string) (bool, error)  // was: error
```

Optional and additive, worth implementing if you have your own storage:

```go
type ISessionRevoker interface {
	RevokeSession(id string) (bool, error)  // (false, nil) = there was nothing to revoke
}
```

Session rotation uses it to refuse to carry a user id over from a session somebody already
revoked. A storage that does not implement it gets the previous, unconditional rotation
behaviour.

Finally, if your app persists a session entity itself: hand the ORM a **detached copy**
(`auth.DbSessionEntity.Snapshot()`), never a shared one. Locking the accessors is not
enough — the ORM copies the struct through `reflect` and the driver marshals the same
attribute map inside the round trip, both outside anything the session's mutex guards, and
a concurrent map iteration is a runtime fatal that takes the process down.

### 5m. `undefined: middleware.AccessCheckerMiddleware` / `core.HttpAccessCommand` / `core.HttpFilterCommand`

All three are deleted.

```bash
grep -rn 'AccessCheckerMiddleware\|HttpAccessCommand\|HttpFilterCommand\|IsAccessAllowed\|AllowFilterFields' \
  --include='*.go' .
```

**If you mounted `AccessCheckerMiddleware`, stop and read this.** It was an authorization
filter that enforced nothing: the only code that could supply it a decision was commented
out, so it logged a warning and called the next handler on **every** request, including
unauthenticated ones. Those routes have had no authorization on them for as long as the
mount existed. Deleting the mount changes no runtime behaviour. Report this to whoever
owns the application before you carry on — it may need an incident review, not just a
code change.

**If you implemented `core.HttpFilterCommand`**, its `AllowFilterFields` was never called
by anything. If it named the fields you intended to be filterable, that restriction was
never in effect and query-string filters accepted any real column. Express it through
`model.AccessControl.ValidateFilterAccess`, reached via `model.NewFilterWithAccess`, which
*is* consulted.

To migrate:

1. Delete the mount and the interface assertions.
2. Delete or repurpose the implementations — they were dead code. Keep a `FilterBuilder`
   method if your own code calls it directly; just drop the `core.HttpAccessCommand`
   assertion.
3. Put the authorization somewhere that runs. There is no drop-in replacement,
   deliberately:

```go
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
```

Add a test asserting that a request from an unauthorized caller does not reach the
handler. That assertion is what would have caught the removed middleware.

`core.IQueryBuilder` is unaffected.

### 5n. The app panics at boot: `refusing to boot: … auth.jwt.secret`

Compiles and vets cleanly; fails at boot, in **every** execution mode including the CLI.

Nothing validated `auth.jwt.secret` before. An app could boot with the key absent, empty,
a YAML null, whitespace, an unresolved `${JWT_SECRET}` literal, or short enough to guess,
and then sign and verify tokens with it — so whoever knew the key could mint a token
naming any user and role.

```bash
# Every environment. A missed one is an outage, not a warning.
grep -rn 'JWT_SECRET' .env* deploy/ k8s/ docker-compose*.yml 2>/dev/null
printf '%s' "$JWT_SECRET" | wc -c    # must be >= 32
```

The rules: at least 32 bytes, not whitespace, not a literal `${VAR}`, and not padding (a
32-byte value with fewer than 8 distinct bytes is rejected). Generate one:

```bash
openssl rand -base64 48
```

```yaml
auth:
  jwt:
    secret: ${JWT_SECRET}
    lifeTime: 3600
```

Two things to know:

- **An app that does not use token authentication should have no `auth.jwt` section at
  all.** The configuration is what answers "is JWT in use here", and an app declaring no
  `auth.jwt.*` key is not made to invent a secret in order to boot. It is safe without one
  because every JWT entry point refuses an unusable key at the point of use — including
  `IsRequestMadeWithStrategy`, which is how an app that never configured JWT could
  otherwise be dragged into authenticating a bearer token the caller signed themselves.
- **Rotating the secret invalidates every outstanding token.** Expect clients to
  re-authenticate.

Tests that mint tokens with a short secret now get an error from `GenerateJwt` and `false`
from `ValidateJwt`. Give them 32 bytes.

The boot error for an unresolved `${VAR}` on a security-relevant key also changed its
advice. It used to say "remove the placeholder so the framework's secure default applies",
which is true for `auth.session.cookie.secure` and **actively dangerous** for
`auth.jwt.secret`, which has no default — following it converted a caught boot failure
into a silent authentication bypass. If you ever acted on that advice, check the key.

Then:

```bash
go build ./... && go vet ./...
```

Commit: `Adopt gorgany v2 API signatures`.

Then **boot the app**, because 5i, 5j and 5n all pass `build` and `vet` and fail only when
the process starts.

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

- **`session:gc`, and an automatic session sweep.** If `auth.session.storage` is
  `database`, your `sessions` table has been growing without bound: the framework's
  `ClearExpiredSessionsJob` claimed to be "registered by the standard setup" and was
  registered by nothing, while `DbProvider` adds the sessions migration unconditionally.
  `JobProvider` now adds the job itself, so a scheduler-running process sweeps without
  being asked. If you wrote your own cleanup job you can delete it — and if you keep it,
  the framework skips its own copy rather than failing to boot on a duplicate.

  For a web tier that does not run the scheduler, or wall-clock scheduling that
  interval-only `core.JobSchedule` cannot express, use the command from cron instead and
  turn the job off:

  ```bash
  go run cmd/cli.go session:gc
  ```

  ```go
  jobProvider.DisableSessionGc()
  ```

  The sweep deletes in batches of 1000 (`auth.SessionSweepBatchSize`), so the first run on a
  table that has been growing since deployment does not go in one statement. Check the row
  count anyway — it tells you how long the backlog will take to clear, and each batch commits
  on its own so an interrupted sweep keeps the work it did.

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

# 13. Every ${VAR} in config/ is set in this environment. An unset one is now blanked
#     rather than left as a literal, and the two security-relevant keys fail the boot.
grep -rn '\${' config/

# 14. Validation reaches an API client as a 422 payload, not a redirect.
curl -s -X POST -H 'Content-Type: application/json' --data '{}' \
  http://localhost:8080/api/<a-validated-route>

# 15. The app BOOTS. Three changes compile and vet cleanly and fail only here: the
#     driver import (5i), an app-owned key under databases.<name> (5j), and the
#     JWT secret (5n).
go run cmd/server.go   # or however you start it — read the first 20 lines of output
```

The security round (Phases 4g–4m and 5k–5n). None of these is a build error; several
change what a user sees on the first request after the deploy.

```bash
# 17. A login authenticates, and the NEXT request is still authenticated.
#     This is the Phase 4h check and the single most important one in this list:
#     a missing PublishSession builds, boots, and logs nobody in.
curl -s -c /tmp/j -X POST -d 'username=u&password=p' http://localhost:8080/login >/dev/null
curl -i -b /tmp/j http://localhost:8080/<a-route-requiring-auth>
# 200 = correct. 401 or a redirect to /login = PublishSession is missing.

# 18. The session cookie is the prefixed one — or you know why it is not.
curl -si -X POST -d 'username=u&password=p' http://localhost:8080/login | grep -i 'set-cookie'
# Expect __Host-GRG_SESSION_ID, Secure, HttpOnly, Path=/, and NO Domain.
# Two Set-Cookie headers for the session is correct; a client must take the LAST.

# 19. The pre-login identifier is dead, and logout revokes.
#     Capture the cookie before logging in, log in, then replay the old one.
#     Then log out and replay the post-login one. Neither may authenticate.

# 20. The CSRF token on the login response is the post-login one, and the client
#     re-reads it. A client that keeps its pre-login token has every mutating
#     request rejected with "Invalid CSRF token".
curl -si -c /tmp/j -X POST -d 'username=u&password=p' http://localhost:8080/login \
  | grep -i 'x-csrf-token'

# 21. Security headers are on an ordinary response.
curl -sI http://localhost:8080/ \
  | grep -iE 'x-content-type-options|x-frame-options|referrer-policy'

# 22. /public/* is inert, and no SVG is referenced from it.
curl -sI http://localhost:8080/public/<some-file> \
  | grep -iE 'content-type|content-disposition|x-content-type-options'
find resource/public -name '*.svg'
grep -rn 'public/.*\.svg' --include='*.html' --include='*.amber' --include='*.css' \
  --include='*.js' --include='*.ts' --include='*.vue' .

# 23. An upload of every type your users actually send still succeeds, and check
#     the extension it was stored under — it now comes from the content.
ls -lt resource/public | head

# 24. An oversize body is refused, and a malformed multipart body is a 400 rather
#     than a dropped connection.
head -c 40000000 /dev/urandom | curl -si -X POST --data-binary @- \
  http://localhost:8080/<a-body-route> | head -1

# 25. A mail with an ordinary subject still sends, against the real mailer.
```

If you use MySQL:

```bash
# 16. orm.Create inserts a row and the entity comes back with its id.
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
8. Whether the app **booted**, and with which driver import. `go build` and `go vet`
   passing is not evidence for either of the two boot-only changes.
9. Every response shape that moved because of `omitempty` or an embed collision
   (4f) — in particular any `time.Time` field tagged `omitempty`, whose key
   reappears.
10. **Whether you observed a login working end to end** (4h) — logged in with
    `curl`, then made a second authenticated request on the cookie you were given.
    Say plainly if you did not. A green build proves nothing here.
11. **The deploy-time consequences, addressed to whoever owns the deploy**, not
    buried in a code summary:
    - Every signed-in user is logged out on the first request after the deploy
      (4g), unless you found `auth.session.cookie.secure: false` or
      `auth.session.cookie.domain` set in that environment — say which.
    - Every SVG served from `/public/*` stops rendering (4i). List the files and
      the references you found, and what you did about each.
    - The infrastructure rules that name `GRG_SESSION_ID` and need the new name.
12. Whether `auth.jwt.secret` is at least 32 bytes **in every environment** (5n),
    and that rotating it will force every outstanding token to be re-issued.
13. Whether you found `AccessCheckerMiddleware` mounted anywhere (5m). If you did,
    say so prominently and separately: those routes had no authorization on them
    for as long as the mount existed, which is a finding about the past, not a
    migration task.
14. Whether any upload type your users send is now rejected or stored under a
    different extension (4j), and whether you configured
    `http.upload.allowedTypes`.
