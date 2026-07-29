# gorgany v2.1 — what the v2 pass left behind

Follow-up to `IMPROVEMENT_v2.md`. Same repo, same rules. Read that file first for
context; this one only covers what is **still open**.

State when this brief was written: `FrameworkVersion = "2.0.0-edge"` (`constants.go:5`),
HEAD `c92a0b7`, `go build ./... && go vet ./... && go test ./... -count=1` green across
18 test packages, working tree clean.

## What v2 completed — do not redo

All 18 v2 items landed, across commits `55c4485`…`ffbdc7e`, and all four deliverables
exist (`CHANGELOG.md`, `MIGRATION_v2.md`, `MIGRATE_TO_V2_PROMPT.md`, `docs/DIALECTS.md`).
Test files went from 14 to 45. Confirmed present: `db/sql/driver/` (registry),
`db/sql/config/` (typed config), `db/sql/builder/` (extracted, dialect-injectable),
`db/sql/gorm/mysql/v2/` (MySQL datasource + dialect + error tests),
`http/middleware/recovery_middleware.go`, `http/upload_limits_test.go`,
`auth/cookie_config.go`, `command/db/datasource_test.go`,
`service/container_rebind_test.go`.

**But v2 is not a pure win.** Three of the items below are worse than the changelog
suggests, and two of them are release blockers: `orm.Create` cannot insert on MySQL (A1),
so the headline v2 feature is unusable for writes; scheduled jobs have never fired (A2);
and v2's own secure-cookie default fails open when an env var is missing (A5). Treat the
release notes as a claim to verify, not a record.

Two things to know before you start:

- **The version is unreleased** (`2.0.0-edge`). Breaking changes below fold into the
  **same v2.0.0 release** — extend `MIGRATION_v2.md` and `MIGRATE_TO_V2_PROMPT.md`
  in place. Do **not** create a `MIGRATION_v2.1.md`; a consumer upgrading from v1.5.x
  should read exactly one migration document.
- **Upstream moved in parallel.** `c92a0b7` merged `github/develop`, which carried its
  own `v1.5.2` bump (`10676c9`). Check for drift in anything you touch, and re-read
  cited lines before editing.

Everything below was verified against this HEAD.

---

## Tier A — Subsystems that are broken and say nothing

The v2 pass covered the request path. These five sit outside it and fail silently.
Ordered by severity: **A1 and A2 are release blockers** — A1 makes the headline v2
feature (MySQL) unusable for writes, and A2 means a whole subsystem has never worked.

### A1 — `orm.Create` cannot work on MySQL. The v2 MySQL deliverable is unusable for inserts.

T2.1 made the builder dialect-injectable and T2.3 fixed `session.Query()`. **The ORM
never uses either.** `db/orm/saver.go:189`:

```go
var builder dbCore.IQueryBuilder
builder = v2.NewBuilder()          // the Postgres builder, unconditionally
```

and then `:216`:

```go
builder = builder.Returning(returningCols...)   // Postgres-only clause
```

`orm.New[T EntityWithMeta](db dbCore.ISession)` (`db/orm/orm.go:23`) is *handed the
session* — the dialect is right there — and discards it. So every `orm.New[T](sess).Create`
against MySQL emits `... RETURNING id` and dies with **MySQL error 1064**.

The many-to-many path is broken the same way. `db/orm/relation.go:219-224`:

```go
joinBuilder := v2.NewBuilder().
    Insert(joinTable).
    Columns(ownerFKCol, relatedFKCol).
    Values(ownerPKValue.Interface(), relPK).
    OnConflict(ownerFKCol, relatedFKCol).
    DoNothing()
```

Note what that means: the MySQL dialect *has* an `ON CONFLICT` → `ON DUPLICATE KEY UPDATE`
translation (Tier D below), but the ORM never reaches the MySQL dialect at all, so the
translation is bypassed and raw Postgres `ON CONFLICT` goes to the server. 1064 again.

This is not confined to those two sites. `v2.NewBuilder()` is hard-coded at **13 places**
outside the builder package:

| File | Lines |
|---|---|
| `db/orm/relation.go` | 219, 233, 550, 710, 846, 1010, 1192, 1287, 1379 |
| `db/orm/saver.go` | 189 |
| `db/orm/orm.go` | 300 |
| `model/pagination.go` | 386, 456 |

Reads, updates and deletes happen to survive today, because `PostgresDialect` emits **bare
unquoted identifiers** (`dialect.go:39-44`, and `quoteIdentifier` at `:136-138` is only
used inside the package) and both engines accept `LIMIT n OFFSET m`. Do not take comfort
in that: it is luck, not design. Every one of the 13 sites is a latent break the moment
any dialect-specific emission is added — quoting, upsert, `RETURNING`, pagination syntax.

Fix: thread the session's dialect through. `ORM[T]` already holds the session, so
`o.db.DataSource()` can supply the dialect; the builder constructor from T2.1
(`NewBuilderWithDialect`) is the seam. Replace **all 13** call sites, not just the two that
currently fail — a partial fix leaves the same trap for the next dialect. Then delete or
`//lint:ignore`-guard `v2.NewBuilder()` so a new hard-coded call cannot be added silently;
better still, make the ORM's construction path the only way to obtain a builder.

Test it on a live MySQL engine: `Create` on an auto-increment PK entity, and a
many-to-many association save. The v2 live suite (`e2e/tests/live_db_test.go`) is where
these belong. Neither is covered today, which is how a shipped MySQL driver passed its
whole test suite while being unable to insert a row.

### A2 — Scheduled jobs never run. The framework's only shipped job never runs either.

`core.IJob` (`app/core/job.go:5-8`) is:

```go
type IJob interface {
    InitSchedule() *gocron.Job
    Handle()
}
```

`gocron.Every(n)` is `scheduler.go:196-197` in `jasonlvhit/gocron@v0.0.1`:

```go
func Every(interval uint64) *Job {
    return defaultScheduler.Every(interval)   // package-level global
}
```

So `ClearExpiredSessionsJob.InitSchedule()` (`job/clear_expired_sessions_job.go:12-14`)
registers on gocron's **package-level `defaultScheduler`**. Meanwhile `JobProvider.Boot`
(`provider/job_provider.go:37-40, 71`):

```go
sched := &gocron.Scheduler{}
if err := c.Make(sched); err != nil { ... }
...
schedule := job.InitSchedule()   // → defaultScheduler
schedule.Do(handler)             // → registered on defaultScheduler
...
go sched.Start()                 // → ticks a zero-value scheduler
```

The decisive detail is what `c.Make(sched)` does — and it is **not** resolution.
`Container.Make` (`service/container.go:187-209`) branches on the target's kind:

```go
if v.Elem().Kind() == reflect.Struct {
    ...
    return c.fill(v.Interface(), newResolutionState())   // field injection
}
// interface pointer: map to resolveInternal
return c.namedResolveInternal(target, "")
```

`*gocron.Scheduler` is a pointer-to-**struct**, so it takes the `fill` branch: the
container performs `container:"inject"` field injection on gocron's own struct, which has
no such tags, and returns nil. `sched` is left as the **zero value**. The
`SingletonLazy(func() *gocron.Scheduler { return gocron.NewScheduler() })` binding at
`:26-28` is never resolved by anything, and `go sched.Start()` ticks an empty,
uninitialised scheduler.

Nothing ever calls `gocron.Start()` / `RunPending()` on the default scheduler either, so
**no scheduled job executes**, in any gorgany app, ever. The panic-recovery wrapper at
`:59-66` is dead code. `ClearExpiredSessionsJob` is the framework's own session GC, so
every app using `auth.session.storage: database` has a `sessions` table that grows without
bound — which is also why nobody has noticed: the symptom is a slow leak, not an error.

Note the root cause is one level down, and it is not job-specific: **`Container.Make`
silently field-injects whenever the caller passes a pointer-to-struct expecting
resolution.** Wrong result, nil error. Fix that too — see B4.

Prove it before you fix it: register a job whose `Handle()` increments a counter, boot,
wait past its interval, assert the counter is still zero. Then make that test assert the
opposite.

Fix, in this order:

1. Make the provider and the jobs use **one** scheduler. The interface currently forces
   the global, so this cannot be fixed inside `JobProvider` alone.
2. **Get `gocron` out of `app/core`.** `InitSchedule() *gocron.Job` leaks a third-party
   type into the framework's core interface, so every app's job files import
   `jasonlvhit/gocron` directly and the dependency can never be replaced without breaking
   all of them. Replace it with a schedule descriptor gorgany owns (interval/cron
   expression + optional timezone) that the provider translates. **Breaking** — migration
   entry with before/after for a job file.
3. `jasonlvhit/gocron@v0.0.1` is unmaintained (the maintained successor is
   `go-co-op/gocron`); it has no context support, so a job cannot be cancelled at
   shutdown, and no sub-minute cron syntax. Once step 2 hides the dependency, swapping it
   is an internal change. Do the swap if it is low-risk; if you defer it, say so and
   record why.
4. Give jobs what an operator needs: a run at startup vs. first-interval choice,
   overlap protection (a long job must not re-enter), and a `context.Context` carrying
   cancellation on shutdown.

### A3 — Two guaranteed panics in the input resolver, one of them a check that does nothing

`http/input_resolver.go:57-63`:

```go
parser := resolveBodyParser(httpCommand, thiz.Message)

if parser == nil {
    log.Log().Warnf("Body parser could not be resolved!")
}

err := parser.Parse(arg)   // nil dereference — the check above does not return
```

`resolveBodyParser` (`:103-113`) returns `nil` for any `ContentType()` that is not
`ApplicationJson`, `MultipartFormData` or `Query`. So a DTO returning anything else — or
the zero value, if someone forgets to set it — logs a warning and then panics on the very
next line. The `if` block has no `return`.

Same function, `:53-56`:

```go
httpCommand, ok := arg.(core.HttpCommand)
if !ok {
    log.Log().Warnf("Argument of %s handler is not core.HttpCommand instance", ...)
    continue
}
```

`continue` skips the `args = append(...)` at `:77`, so the handler is later invoked
through `reflect.Call` with **too few arguments** — a second panic, with a message that
points at reflection rather than at the DTO that is missing `ContentType()`.

v2's `RecoveryMiddleware` now converts both into a 500 instead of a dropped connection,
which is strictly better and also why these are easy to miss. But a developer error in a
DTO declaration should not be a runtime 500 discovered in production.

Fix: return a descriptive error from both paths — naming the DTO type and the
`ContentType` it returned. Better still, catch it at registration: when a route is
registered, walk the handler's parameters and fail at **boot** on any `core.HttpCommand`
whose `ContentType()` has no parser. A misdeclared DTO should stop the server starting,
not serve 500s. Note that `e2e/fixture-app` and the `MIGRATE_TO_V2_PROMPT.md` guidance
both suggest a `var _ = []core.HttpCommand{…}` compile-time list as the app-side guard —
this replaces that workaround with a framework-side check.

### A4 — There is no way for a client to obtain a CSRF token

`SessionMiddleware.Handle` (`http/middleware/session_middleware.go`) sets the token
header at `:98` — but only on the code path that creates a **brand-new** session. The
valid-session branch at `:60-70` calls `next(message)` and returns without ever setting
it. `CsrfService.GetCSRFToken` (`auth/csrf_service.go:53-54`) exists and is **called from
nowhere**; `core.CSRFTokenHeader` (`app/core/const.go:178`) is set in exactly that one
place; and there is no route or controller that hands a token out.

So a JS client gets one shot at reading `X-CSRF-Token` — the first response that
happens to create a session — and if it misses it (a page reload, a second tab, a session
that already existed, a rotation) it can never recover it.

**v2 made this worse, and that is the point of raising it here.** T3.2 correctly stopped
the CSRF middleware skipping the check when no session cookie was present, and correctly
stopped exempting `OPTIONS`. Both were real bypasses. But the token-delivery gap means the
hardened middleware now *reliably rejects* mutating requests from any client that did not
catch that single header. Closing a bypass without providing the token is a half-landed
change.

Fix, both halves:

1. Set `X-CSRF-Token` on **every** response for a request carrying a session, not just on
   creation — the valid-session branch at `:60-70` needs it too.
2. Ship a token endpoint (`GET /csrf` or equivalent) backed by the existing
   `CsrfService.GetCSRFToken`, and register it in the standard setup. This is what a SPA
   calls on boot.

Then extend `docs/` with the client contract: fetch on boot, re-read on every response,
send on every mutating request. Test that a request with a pre-existing session receives
a usable token, and that the token it receives passes the middleware.

### A5 — An unset `${VAR}` silently becomes `""` and counts as set — which ships an insecure cookie

`config/viper_parser.go:20-29` resolves `${VAR}` placeholders after merging the config
files:

```go
for _, k := range viper.AllKeys() {
    value := viper.Get(k)
    val, ok := value.(string)
    if ok && strings.HasPrefix(val, "${") && strings.HasSuffix(val, "}") {
        envValue := os.Getenv(strings.TrimSuffix(strings.TrimPrefix(val, "${"), "}"))
        viper.Set(k, envValue)     // "" when the var is unset — written unconditionally
    } else {
        viper.Set(k, value)
    }
}
```

Two defects, one of them security-relevant:

**It cannot distinguish "unset" from "set to empty".** `os.Getenv` returns `""` for both
(`os.LookupEnv` is the call that distinguishes them), and the result is written with
`viper.Set` — viper's **override** layer, the highest precedence there is. So the key is
now present, empty, and unbeatable by any default.

That defeats v2's own T3.6 fix. `auth/cookie_config.go:29-33`:

```go
func SessionCookieSecure() bool {
    if !viper.IsSet(ConfigSessionCookieSecure) {
        return true          // secure by default — the intent is correct
    }
    return viper.GetBool(ConfigSessionCookieSecure)
}
```

With the documented convention `secure: ${SESSION_COOKIE_SECURE}` in config.yaml and the
variable unset — a fresh checkout, a CI runner, a container missing one env line —
`IsSet` returns **true**, the secure-by-default guard is skipped, `GetBool("")` returns
**false**, and the session cookie ships **without `Secure`**. The one guard v2 added to
keep cookies safe by default fails open, and it fails open along the exact path the config
sample tells people to use.

**The `else` branch promotes every config-file key into the override layer.** After this
loop, `viper.IsSet` is true for literally every key in config.yaml and `viper.SetDefault`
is inert for all of them. Any future `IsSet`-guarded default is born broken.
`auth/cookie_config.go:30` is the only such guard today, which bounds today's blast radius
to that one cookie — but it is a trap laid for every default added from here on.

Fix:

1. Use `os.LookupEnv`. If the variable is absent, **leave the key alone** so defaults and
   `IsSet` behave — do not write `""`. Decide and document what an explicitly-empty
   variable means (an empty string is a legitimate value for some keys).
2. Stop rewriting untouched keys through the override layer — only substituted keys need
   writing, and prefer a mechanism that does not clobber precedence.
3. Fail loudly on a placeholder that resolves to nothing for a key the framework treats as
   security-relevant. A missing `JWT_SECRET` or `SESSION_COOKIE_SECURE` should stop boot,
   not quietly degrade.
4. Make `SessionCookieSecure()` robust regardless: treat an empty or unparseable value as
   "unset" and return `true`. Secure-by-default must not depend on the config layer
   getting it right.

Tests: an unset `${VAR}` leaves the default in force; `SessionCookieSecure()` returns true
when the variable is missing; an explicitly-empty variable behaves as documented.

---

## Tier B — Systemic robustness

### B1 — 42 unchecked type assertions remain, several on untrusted input

T1.3 fixed the datasource config path. The pattern it was an instance of is untouched
elsewhere. Non-test, non-e2e count at this HEAD: **42**. Reachable from untrusted input,
in priority order:

| Site | Assertion | Reached by |
|---|---|---|
| `auth/jwt_service.go:64` | `claims["username"].(string)` | a signed token whose `username` claim is absent or non-string → panic |
| `decoder/query.go:71-72` | `value.(string)` (twice) | query-string shapes; the line already carries a `// todo: Test it` |
| `http/message.go:504` | `oneTimeParams.Values[key].([]any)` | session-stored flash data |
| `model/dto_api_wrapper.go:192` | `nestedElement.(map[string]any)` | any response DTO with an embedded struct |
| `view/view.go:64`, `view/native.go:55` | `opts["fn"].(map[string]any)`, `funcs.(map[string]any)` | template options |
| `util/reflect.go:171` | `arg.(float64)` | primitive resolution |

Fix: comma-ok everywhere, returning a descriptive error. Start with `jwt_service.go` and
`decoder/query.go` — those two are reachable from data the caller controls. Enumerate the
full list first (`grep -rnE '\.\((bool|int|string|float64|\[\]any|map\[string\]any)\)'`
minus tests and `, ok`), fix them, and report the count you closed versus the ones you
deliberately left as invariants that genuinely cannot fail.

### B2 — Validation errors are unusable by any client

`validator/validator.go:74-75`:

```go
Field: e.Field(),   // the Go struct field name, e.g. "MobilePhone"
Err:   e.Error(),   // go-playground's raw English sentence
```

`e.Error()` produces `Key: 'Dto.Email' Error:Field validation for 'Email' failed on the
'email' tag` — a string no UI can display and no client can map to a form field, since
`Field` is the Go name rather than the `json:`/`scheme:` wire name the client sent.

Every serious consumer therefore replaces `core.IValidator` wholesale just to rename
fields and translate messages. That is a framework gap, not an app concern.

Fix: resolve `Field` through the same `json:`/`scheme:` tag the parsers use (the tag
handling from T3.5 in `model/dto_api_wrapper.go` is the precedent to reuse — do not write
a second tag parser), and give `Err` a per-rule message catalog with a default English set,
wired to the existing `i18n/` manager so an app can supply another language without
reimplementing the validator. Keep every current rule's semantics identical.

While you are in there: `getOverriddenFields` (`validator/validator.go:87-120`) means a
DTO field that shadows an embedded field's name is silently dropped from validation.
Either make that safe or fail loudly on the collision.

### B3 — A dead error path with live handlers registered for it

`err.NewInputBodyParseError` (`err/errors.go:94`) has **zero call sites** — nothing in the
framework ever constructs it. Yet `http/error.go:15` registers
`"InputBodyParseError": processBodyParsingError`, and `e2e/fixture-app` registers a
handler for it too (`pkg/provider/provider.go:40`), so app authors reasonably believe it
is the hook for malformed bodies. It is not: JSON syntax errors surface as
`*err.ValidationErrors` from the parser instead.

Fix: pick one. Either construct `InputBodyParseError` where a body genuinely fails to
parse — which is the more useful shape, since a 400-class parse failure is not the same
thing as a 422-class validation failure — or delete the type and its handlers and say in
the docs that parse failures arrive as `ValidationErrors`. Do not leave both. Update
`fixture-app` to match whichever you choose.

### B4 — `Container.Make` silently field-injects when the caller wanted resolution

The root cause of A2, worth fixing in its own right. `Container.Make`
(`service/container.go:187-209`) dispatches on the target's kind: pointer-to-struct goes to
`c.fill` (field injection), pointer-to-interface goes to `namedResolveInternal`
(resolution). Both return `nil` on success, so a caller who passes `&SomeStruct{}` expecting
to receive the registered singleton gets a zero value and **no error** — which is exactly
how `JobProvider.Boot` came to tick an empty scheduler for however long this has been
shipping.

Fix: make the two intents distinguishable. Either give resolution its own method for
concrete types (`Resolve` already exists — route struct pointers through it when a binding
for that type exists), or return an error when `Make` is handed a pointer-to-struct that
has a registered binding, telling the caller which method they wanted. Silence is the bug;
picking a convention matters less than making the wrong call loud.

Then audit for other victims: `grep -rn "\.Make(&" --include="*.go" .` and check each
pointer-to-struct call site for the same mistaken expectation.

---

## Tier C — Surface every app is currently forced to build

Each of these is something a real consumer had to write from scratch. They are ranked by
how badly its absence hurts.

**C1 — No rate limiting, anywhere.** Zero hits for rate-limit of any kind in the repo. The
framework ships session auth, JWT auth, OTP-capable user services and CSRF, and no way to
stop a login or OTP endpoint being brute-forced. Ship an in-memory token-bucket middleware
keyed by IP+route, configurable per route pattern, usable as a filter. In-memory is the
right first move — there is no Redis dependency and adding one for this is not worth it —
but design the seam so a distributed backend can be dropped in.

**C2 — 404 and 405 are not content-negotiated.** `http/router/gorgany.go:81-86` responds
`Bytes(nil, 404)`; `http/error.go:83` uses `Text("", 404)`. An API client hitting an
unknown route gets an empty non-JSON body and never the standard envelope. T3.4 fixed
negotiation for `AuthMiddleware` only — apply the same `Accept` / `/api/` prefix /
Content-Type rule to the router's 404, its 405, and the generic error path, reusing the
helper T3.4 introduced rather than writing a second one.

**C3 — No way to serve a built SPA.** `PublicController`
(`http/controller/public_controller.go:20-40`) serves `/public/*` and returns 400 for
anything else, so client-side routing (a deep link the server has no route for) cannot
work. Ship a static-with-fallback controller: `index.html` for unmatched non-API paths,
`no-store` on the HTML, immutable caching on hashed assets, and a path-traversal guard
(reuse the `filepath.Clean` + `..` check already in `public_controller.go:26-30`).

**C4 — No test-support package.** `ISession.Transaction` (`db/sql/core/interfaces.go:61`)
means a transaction-per-test seam is *expressible*, but nothing ships to use it, so every
app hand-rolls a database harness. v2 added `e2e/tests/live_db_test.go` behind a build tag
— generalise that into a supported `testsupport` package: throwaway database per run,
migrate, truncate-between-tests or transaction-rollback-per-test, both engines. This is
the single biggest multiplier for consumers on the list.

**C5 — CORS allows `*` together with credentials.** `cors_middleware.go:271-272` and
`:321-322` emit `Access-Control-Allow-Origin: *` when origins are wildcarded, while
`:286` / `:330` independently emit `Access-Control-Allow-Credentials: true`. Browsers
reject that pair, so a developer who configures both gets silent failure with no
diagnostic. `Vary: Origin` is set correctly (`:245-250`, `:301-302`), so the fix is
narrow: when `AllowCredentials` is true, reflect the specific request origin instead of
`*` — or refuse the combination at construction with an explanatory error. Prefer
refusing; a security-relevant misconfiguration should not be silently papered over.

**C6 — JWT carries no role claim.** `auth/jwt_service.go:26-27` sets only `exp` and
`username`, so `jwt_middleware`'s role check costs a user lookup on every request. Add a
role claim, keeping the lookup as the authority for revocation (a stale role in a signed
token must not outlive a role change — say which way you resolved that and test it).

**C7 — `core.MongoDb`** (`app/core/db.go:19`) is a `DbType` constant with no driver
behind it. With v2's driver registry in place, either implement it or remove the constant.
Low priority; just do not leave a name that promises support.

---

## Tier D — One v2 rule not yet honoured

v2's governing rule for the dialect was: *every construct MySQL cannot express returns an
explicit error rather than emitting invalid SQL.* `ON CONFLICT` is the one case where that
rule was answered with documentation instead of an error.

`db/sql/gorm/mysql/v2/dialect.go:360, 415-432` translates `ON CONFLICT (cols) DO UPDATE`
into `ON DUPLICATE KEY UPDATE`, silently dropping the conflict-target column list.
Commit `2922933` documented why this is not faithful (`docs/DIALECTS.md:309-322`): MySQL
keys off *any* unique index, and on a table with more than one unique index MySQL's own
manual advises against the clause entirely because which row gets updated is not the
caller's to control.

That is *valid but wrong* SQL — the failure mode the rule exists to prevent, and the one
the v2 brief called out as worse than invalid SQL. A doc warning does not stop it
executing.

Fix: error by default, with an explicit opt-in (a dialect option such as
`AllowUnfaithfulUpsert`, or a distinct builder method that names what it does) for a
caller who has read the caveat and knows their table has exactly one unique index. Keep
`ON CONFLICT DO NOTHING` as-is — the self-assignment no-op at `docs/DIALECTS.md:296` is
faithful. Update the doc to describe the new default, and keep the existing warning for
anyone who opts in.

---

## Deliverables

1. **Extend `CHANGELOG.md`** under the existing v2.0.0 entry — not a new version section.
2. **Extend `MIGRATION_v2.md`** with the new breaking changes, in the format already
   established there: what broke, before/after, how to detect, mechanical or judgement.
   At minimum: the `IJob` interface change (A2), and any signature change from B1/B2/B4.
   Follow the existing file's lead in separating behaviour-only breaks from compile
   breaks — the CSRF token change (A4) and the validation error shape (B2) are
   behaviour-only, and a client will notice them before a compiler does.
3. **Extend `MIGRATE_TO_V2_PROMPT.md`** with a grep and an edit for each new break. It has
   to stay runnable standalone. Add a note for A5: an app carrying
   `secure: ${SESSION_COOKIE_SECURE}` in its config must check that the variable is
   actually set in every environment, because until now a missing one silently disabled
   the flag.
4. **Tests for every item**, same standard as v2. Five specifically — each one is a test
   whose absence let the bug ship:
   - `orm.Create` inserts a row on **live MySQL**, and a many-to-many save writes its join
     row (A1);
   - a job actually fires on schedule (A2);
   - a DTO with an unparseable `ContentType` fails at **boot**, not with a 500 (A3);
   - a request with a pre-existing session receives a CSRF token the middleware then
     accepts (A4);
   - `SessionCookieSecure()` returns true when `${SESSION_COOKIE_SECURE}` is unset (A5).
5. **`docs/`**: the SPA-serving contract (C3), the CSRF client contract (A4), the
   `testsupport` usage (C4), and — for A5 — what `${VAR}` substitution does with a missing
   variable, since the current behaviour is the opposite of what the config sample implies.

## Verification

The v2 verification block still applies in full. On top of it:

```bash
cd /Users/anton/Documents/projects/gorgany/framework/gorgany

go build ./... && go vet ./... && go test ./... -count=1
(cd e2e/fixture-app && go build ./... && go test ./... -count=1)

# A1: no hard-coded Postgres builder may remain outside the builder package
grep -rn "v2.NewBuilder()" --include="*.go" . \
  | grep -v "_test\|/e2e/\|db/sql/builder/\|db/sql/gorm/.*/v2/" | wc -l   # 13 at the start

# B1: the unchecked-assertion count must go down, and you must say by how much
grep -rnE '\.\((bool|int|string|float64|\[\]any|map\[string\]any)\)' --include="*.go" . \
  | grep -v "_test\|/e2e/\|, ok\|,ok" | wc -l    # 42 at the start of this brief
```

Two things no unit test in this repo can prove, and both are release blockers:

- **Insert a row through `orm.Create` against a live MySQL server** (A1). The MySQL driver
  shipped in v2 with a green test suite while being unable to insert anything, because
  every dialect test asserts strings and no test drives the ORM against a real engine.
- **Boot an app with a job scheduled at a short interval and assert it runs** (A2). The
  scheduler has never worked and nothing in the repo notices.

Report at the end: what you fixed, what you found wrong in this brief, what you left
alone and why. If A2's step 3 (replacing gocron) is deferred, state that explicitly
rather than leaving it implied.
