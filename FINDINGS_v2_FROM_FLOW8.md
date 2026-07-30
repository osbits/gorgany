# Framework findings from migrating flow8-be to v2.0.0-edge

Found while migrating a real application (`flow8-be`, ~150 controllers, Postgres-only,
SPA frontend) from v1.5.1 to `2.0.0-edge`. Framework at `0246e02`.

Everything below is **framework-side**. App-side migration work is not listed. Each item
states how it was verified; where a claim rests on a probe, the probe is described so it
can be re-run. Two claims I initially got wrong are marked as such — the corrected
version is what is recorded.

The framework's own suites are green: `go build`, `go vet`, `go test ./...` (18/18
packages), `go test -tags=livedb ./e2e/tests/` (11 tests against real Postgres 16 and
MySQL 8), and `sh e2e/run.sh` (7/7, dockerised fixture app). Every issue below survives
that, which is the point worth taking from this document: the gaps are in places the
existing tests structurally cannot reach.

---

## 1. BLOCKER — `Container.bind` deadlocks the process on any core-interface rebind

`service/container.go:131` takes `c.mu.Lock()` with a deferred unlock, and then at
`:140` calls `log.Log().Warnf(...)` — the new rebind warning — **while still holding the
write lock**. `log.Log()` goes through the factory `provider/logger_provider.go:18-35`
installs, which calls `c.Resolve` / `c.NamedResolve` → `Container.resolve` →
`c.mu.RLock()` on the same mutex. `sync.RWMutex` is not reentrant, so this is an
unconditional self-deadlock.

**Trigger:** any rebinding of a `github.com/osbits/gorgany/app/core` interface that
happens *after* `LoggerProvider.Register` has installed the container-backed factory.

**Repro — framework only, no app involved:**

```go
c := service.NewContainer()
(&provider.LoggerProvider{}).Register(c)
_ = c.SingletonLazy(func() core.IValidator { return nil })   // returns
_ = c.SingletonLazy(func() core.IValidator { return nil })   // never returns
```

**Observed in flow8-be:** boot printed four rebind warnings and then hung forever. The
four that printed come from a provider registered *before* `LoggerProvider`, so
`log.Log()` still used the pre-factory default logger. The fifth — `core.IDataContext`,
rebound inside `DbProvider.Register` — deadlocked. Goroutine 1:

```
main.main → ConsoleApp.Run → Bootstrapper.Bootstrap
  → ud/pkg/provider.DbProvider.Register → gorgany/provider.DbProvider.Register
  → Container.SingletonLazy → Container.bind
  → log.Log → LoggerProvider.Register.func2
  → Container.NamedResolve → Container.resolve → sync.RWMutex.RLock
```

**Why the test suite misses it:** `service/container_rebind_test.go` builds bare
`NewContainer()` values and never installs `LoggerProvider` or calls
`SetLoggerFactory` (verified: no such reference in the file). So `log.Log()` resolves to
the default logger, which never touches the container. The test asserts the warning is
emitted; it cannot observe that emitting it deadlocks a real boot.

**Suggested fix:** decide the warning under the lock, emit it after unlocking. Something
like collecting a `replaced bool` before `c.mu.Unlock()` and logging after, rather than
`defer`-unlocking around the log call. Add a regression test that installs
`LoggerProvider` first.

---

## 2. BLOCKER — v2.0.0 cannot be required as v2.0.0: the module path has no `/v2`

`go.mod:1` is `module github.com/osbits/gorgany`. Go requires a `/vN` suffix for major
version ≥ 2:

```
require github.com/osbits/gorgany v2.0.0-edge
→ go: errors parsing go.mod: require github.com/osbits/gorgany:
  version "v2.0.0-edge" invalid: should be v0 or v1, not v2
```

`+incompatible` does not apply (the module has a `go.mod`). So
`MIGRATE_TO_V2_PROMPT.md` Phase 5's instruction —

```bash
go get github.com/osbits/gorgany@v2.0.0
```

— cannot succeed for any consumer. Grepping `CHANGELOG.md`, `MIGRATION_v2.md`,
`MIGRATE_TO_V2_PROMPT.md` and `README.md` for the module path, `/v2` or
`+incompatible` returns nothing, so this is unacknowledged rather than a known
trade-off.

**Options:** rename the module path to `github.com/osbits/gorgany/v2` and update every
internal import; or release as `v1.6.0` and accept that source-breaking changes ship in a
minor; or document that every consumer must use a `replace` directive. The first is the
only one consistent with the CHANGELOG's own "Why 2.0.0 and not 1.6.0" section.

**Workaround used to run this migration** (verified — `FrameworkVersion` reports
`2.0.0-edge`):

```
require github.com/osbits/gorgany v1.5.1
replace github.com/osbits/gorgany => /path/to/gorgany
```

---

## 3. BLOCKER (security) — a malformed request body is written verbatim to the log

`err/errors.go:128-130`:

```go
func (thiz InputBodyParseError) Error() string {
	return fmt.Sprintf("Unable to convert body from %s. Error: %v\nBody: %s",
		thiz.Type, thiz.RawError, thiz.Body)
}
```

The raw body is part of `Error()`. `http/error.go:132` `processBodyParsingError` opens
with `error2.PrintError(err)`, and `err/errors.go:19-21` is
`log.Log("").Errorf("%s\n%s", err, GetStacktrace())` — so the body lands in the log at
Error level, on stderr, and from there in `docker logs` and any aggregator.

The HTTP **response** is fine: `http/error.go:135-137` deliberately uses
`parseError.RawError.Error()`. Only the log leaks.

**This is new exposure.** Before this wave the parse path produced an empty
`ValidationErrors` and `processValidationErrors` logged nothing, so no body ever reached
a log. The five construction sites that attach `string(body)` are new
(`http/json_parser.go`).

**Measured, on a real app.** A malformed `POST` to a JSON-DTO route:

```
ERROR [err/errors.go:20] Unable to convert body from application/json. Error: invalid JSON syntax at byte 24: unexpected end of JSON input
Body: {"this is not valid json
```

A truncated or oversized `POST /auth/login` body puts a cleartext password there. Note
that `MIGRATE_TO_V2_PROMPT.md` Phase 4c already warns an app about exactly this hazard
("a body that failed to parse is exactly the kind that might carry a password halfway
through") — while the framework's own default handler does it.

**Suggested fix:** do not log `err.Error()` for this type. Log `RawError` plus method and
path; `RawError` only ever carries offsets, sizes and limits, never body bytes. Keep the
body on the struct for a caller who explicitly opts in, or drop it. An app can work
around it today by registering its own `InputBodyParseError` handler, which is what
flow8-be now does — but the default should be safe.

---

## 4. `omitempty` does not match `encoding/json`

`model/dto_api_wrapper.go:225` gates on `reflect.Value.IsZero()`. `encoding/json` uses
its own `isEmptyValue`: `Len() == 0` for slice/map/array/string, `IsNil()` for
pointer/interface, and a struct is **never** empty. Measured against the real code:

| field with `,omitempty`           | `encoding/json` | gorgany v2  |
|-----------------------------------|-----------------|-------------|
| `[]string{}` (non-nil, len 0)     | omitted         | **emitted** |
| `map[string]string{}` (len 0)     | omitted         | **emitted** |
| `time.Time{}` (zero struct)       | **emitted**     | omitted     |
| zero-valued nested struct         | **emitted**     | omitted     |
| nil slice / map / ptr, `""`, `0`, `false` | omitted | omitted (match) |

The `time.Time` row is the dangerous one: the key silently disappears where the stated
goal is parity. `json:"-"` suppression and anonymous-embed inlining are both correct —
this is specifically the `omitempty` predicate.

**Suggested fix:** replicate `encoding/json`'s `isEmptyValue` rather than using
`IsZero()`. Both directions matter, but the struct case is the one that changes a
response shape clients already depend on.

---

## 5. Embed/outer name collision precedence depends on declaration order

*(I first recorded this as "the embedded value always wins", which is wrong. Corrected
by probe.)*

When an inlined embed and an outer field share a wire name, `encoding/json` always
prefers the shallower (outer) field, regardless of declaration order. gorgany's result
depends on order, because `buildBodyElement` walks fields in order and the embed is
merged with `util.MergeMaps` while the outer field is a plain map assignment:

| declaration order            | `encoding/json` | gorgany v2      |
|------------------------------|-----------------|-----------------|
| embed first, outer field second | `FROM-OUTER` | `FROM-OUTER` (match) |
| outer field first, embed second | `FROM-OUTER` | **`FROM-EMBED`** |

So the divergence bites only when the outer field is declared before the embed. Narrow,
but silent, and it makes a struct's wire output depend on field order in a way
`encoding/json` never does.

**Suggested fix:** resolve collisions by depth before writing, or skip an inlined
embedded key that the outer struct also declares.

---

## 6. "leaving the key unset so its default applies" cannot happen

`config/viper_parser.go` leaves an unresolved `${VAR}` in place and warns:

```
config: <key> references ${VAR}, which is not set; leaving the key unset so its default applies
```

The key is not unset and the default does not apply. `ResolveEnvPlaceholders` iterates
`viper.AllKeys()`, so **every key it can reach is by construction already in viper's
config layer**, which outranks `SetDefault`. Probed:

```
before: IsSet=true  GetString="${DEFINITELY_UNSET_VAR}"
after : IsSet=true  GetString="${DEFINITELY_UNSET_VAR}"
RESULT: default did NOT apply; the key holds the literal
```

`config/viper_parser_test.go`'s `TestUnsetPlaceholderIsLeftVisibleNotBlanked` asserts the
literal, and its own doc comment concedes the wording does not hold — so the behaviour is
intended and only the message is wrong. But the consequence is not cosmetic. For an
**optional** key, the literal is worse than empty:

- `auth.session.cookie.domain: ${COOKIE_DOMAIN}` unset → `Domain: "${COOKIE_DOMAIN}"` on
  the session cookie (`auth/standard_auth_strategy.go:72,188`). That is an invalid Domain
  attribute, so the browser drops `Set-Cookie` entirely and login fails silently — the
  same class of failure the secure-cookie work in this release set out to eliminate.
- A URL key used directly in `Redirect()` becomes a redirect to `${FRONTEND_URL}`.
- An OAuth client id becomes "configured with garbage" rather than "not configured".

flow8-be had 10 such keys. It now blanks leftover literals itself after calling the
framework resolver.

**Suggested fix:** at minimum correct the message. Better: distinguish keys where a
literal aids diagnosis (a DB host — a connection error naming `${DB_HOST}` genuinely
helps) from keys where empty is a legitimate value, or offer
`ResolveEnvPlaceholders` an option to blank rather than retain. The current one-size rule
turns every optional key into a landmine.

---

## 7. Validation errors are still not delivered to a client

`B2` set out to "make validation errors usable by a client", and the new payload is a
real improvement — wire name instead of the Go field name, a readable message, plus
`rule` / `param` / `path`. But `http/error.go:82-86` is unchanged:

```go
func processValidationErrors(error error, message core.HttpMessage) {
	concreteError := error.(*error2.ValidationErrors)
	req := message.Request().RawRequest()
	message.RedirectWithFlash(req.Referer(), 301, map[string]any{"validation": concreteError})
}
```

Every DTO validation failure is a **301 redirect to the Referer**, for API clients too.
Measured against a real endpoint:

```
POST /api/install/run  {}   →  HTTP/1.1 301 Moved Permanently
                              Location: http://example.invalid/page
```

So a JSON client never receives the reshaped payload on this path. Two knock-on problems:

- `MIGRATE_TO_V2_PROMPT.md` Phase 4c states "a body that *parses* but holds a value a
  field rejects is still `422` with `ValidationErrors`". It is a 301, not a 422.
  `docs/VALIDATION.md` advertises the same 422.
- The `C2` work content-negotiated 404, 405, `processInputParsingError`,
  `processJwtAuthError` and `processDefaultError` — but not this handler, which is the
  one most likely to be hit by a normal client.

**Suggested fix:** negotiate it like the others — `WantsJSON(message)` → `422` with the
envelope, otherwise keep the redirect-with-flash for a server-rendered form. That is a
behaviour change and belongs in the CHANGELOG, but the current state means the headline
improvement of B2 is unobservable to the clients it was for.

---

## 8. Strict unknown-key rejection breaks app-owned sibling keys, undocumented

`db/sql/config/config.go` `knownKeys` rejects any key under `databases.<name>` that the
framework does not recognise. flow8-be carried an app-owned block there —

```yaml
databases:
  default:
    driver: postgres_gorm
    ...
    pool:                      # read by the app itself via viper
      maxOpen: 25
```

— specifically *because* the framework's own `properties` path was dead pre-v2 (the
camelCase lookups viper's lowercasing could never match). v2 fixes `properties` **and**
makes the workaround fatal:

```
panic: database 'default': datasource config: unknown key(s) 'pool'
```

It compiles, vets and tests clean — the panic is at `Register` time — so the prompt's
Phase 5 verification (`go build ./... && go vet ./...`) passes and the app dies on first
boot. `MIGRATE_TO_V2_PROMPT.md` frames unknown keys only as "a typo that used to be
ignored will fail at boot"; grepping the four migration docs for "unknown key" returns
nothing. Nothing warns that an app-owned sibling key under a datasource is now illegal.

**Suggested fix:** a line in the migration prompt under Phase 5b, and consider whether
rejection should be a boot warning for keys that are not near-misses of a known key
(a typo like `databse` is worth failing on; a deliberate namespace is not).

---

## 9. A Postgres-only app now links the MySQL driver

`provider/db_provider.go:17` blank-imports `db/sql/driver/builtin`, and
`db/sql/driver/builtin/builtin.go:18-19` imports both `mysql/v2` and `postgres/v2`. So
every app that uses `DbProvider` links both engines. For flow8-be, `go mod tidy` added:

```
gorm.io/driver/mysql v1.6.0
github.com/go-sql-driver/mysql v1.8.1
filippo.io/edwards25519 v1.1.0
```

Not a defect, but it is a new dependency and attack surface for an app that will never
speak MySQL, and it is not in the CHANGELOG's Added section.

**Suggested fix:** either note it, or split the registration so an app opts into engines
(`_ ".../driver/postgres"`), with `builtin` kept as the convenience import.

---

## 10. The `viper.Set` sibling-wipe hazard is fixed inside the framework but still trips apps

The CHANGELOG documents this well: writing substitutions with `viper.Set` puts them in
viper's override layer, and a map fetch resolves against the highest layer holding the
key without deep-merging below it — so after substituting `databases.default.host`,
`GetStringMap("databases")` returns only that key and `driver`, `log` and `properties`
are gone.

`ResolveEnvPlaceholders` now uses `MergeConfigMap` and is safe. But **any app that also
substitutes placeholders reintroduces the bug**, and that is not a hypothetical: flow8-be
had its own copy of the loop (needed because its own `MergeInConfig` re-read the file and
restored the raw `${...}` strings), and on v2 it booted as

```
panic: database 'default': datasource config: 'driver' is required
      (registered drivers: [mysql_gorm postgres_gorm])
```

which is the exact symptom the CHANGELOG attributes to the framework's own former bug.
The fix was to call the exported `config.ResolveEnvPlaceholders()` instead — which is
presumably why it was exported, but nothing points a migrating app at it.

**Suggested fix:** add a grep and an edit to the migration prompt — "if your app
substitutes `${VAR}` itself, replace it with `config.ResolveEnvPlaceholders()`; a
local `viper.Set` loop will wipe your datasource config" — since the failure mode is a
boot panic that names an unrelated key.

---

## 11. Notes, lower severity

**Unauthenticated `OPTIONS` enumerates methods on protected routes.** The preflight
responder is registered without the route middleware chain
(`r.engine.Options(pattern, fn)` with no `With(mws...)`), which is what makes it
side-effect-free — but it also means route-scoped auth no longer applies. A session-less
`OPTIONS /api/users/1` returned a JSON 401 on v1.5.1 and now returns
`204` with `Allow: DELETE, GET, OPTIONS`. Measured. Also: OPTIONS requests no longer
reach an audit-logging route middleware, so they stop being audited. Both are reasonable
consequences of the security fix; neither is in the CHANGELOG.

**`GET /csrf` sits outside app middleware patterns.** It is registered at the root, so an
app whose gates are scoped to `/api/**` (an install gate, a tenant gate) does not apply
them to it. Correct by design — a client needs a token before authenticating — but worth
a sentence in `docs/CSRF.md`, because it is the one route that appears without the app
asking for it.

---

## What was verified working

To keep the list above in proportion — these were checked against a real app and behaved
exactly as documented:

- `OPTIONS` on a mutating route → `204` + `Allow`, handler not invoked, no side effect.
- `405` now answered with the standard envelope, naming the rejected method.
- `404` still routes to the app's own `SetNotFoundHandler`.
- Malformed body → `400` with a readable reason and **no raw body in the response**.
- `GET /csrf` returns a token that is stable per session; `X-CSRF-Token` set on every
  session-carrying response.
- CORS preflight from a configured origin → `200` with credentials; the wildcard +
  credentials refusal fires correctly (and catches an *empty* origin list, which is the
  case a config-driven policy actually hits).
- **Scheduled jobs run.** `scheduler: started with 3 job(s)`, a `RunAtStartup` job fired
  immediately, and a 1-minute job fired at exactly +60s with its `container:"inject"`
  dependencies filled. This was the single most valuable fix in the release for this app:
  three jobs had silently never run.
- `db:migrate-checked` applied 14 migrations on a fresh database, including the rewritten
  `create_sessions_table` through GORM's Migrator.
- `properties` pool settings now actually apply — `sql.DB.Stats().MaxOpenConnections`
  reads back the configured 25.
- `dsconfig.Parse` accepts a hand-built map with a string port, and reports unknown keys
  by their original spelling.
