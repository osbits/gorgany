# Changelog

All notable changes to the gorgany framework.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [2.0.0] — 2026-08-02

> **The `v2.0.0` tag predates this entry and must be re-cut.**
>
> A `v2.0.0` tag was created locally on `fd0d31e` and never pushed — `git ls-remote
> --tags` shows `v1.5.2` and earlier and no `v2.0.0`. Everything in the security round
> below landed *after* that tag, so the tag does not point at the tree this entry
> describes. Delete it and re-tag the release commit. Because the tag was never
> published, no consumer can have resolved `v2.0.0` to the older tree, and this stays
> one 2.0.0 release rather than becoming a 2.0.1: there is nothing to be compatible
> with. `constants.go` still reads `FrameworkVersion = "2.0.0"` and is correct.

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

The security round added a second wave of source breaks, all in the session and auth
layer: every `core.ISessionStorage` mutator returns an `error`, `GetSessionById`
returns `(ISession, error)`, `core.IAuthStrategy.Logout` returns an `error`,
`core.SessionCookieName` is gone, and `core.HttpAccessCommand` /
`core.HttpFilterCommand` / `middleware.AccessCheckerMiddleware` are deleted. See
**Security** below and MIGRATION_v2.md §24–§34.

There are also five **behaviour** changes that no compiler will catch — a memoized
query builder, `json:"-"`, a role-mismatch status code, `OPTIONS` routing, and
connection-pool limits that start being enforced. Those are the ones most likely to
surprise, and they are why [`MIGRATION_v2.md`](MIGRATION_v2.md) leads with them.

`e2e/fixture-app`, the only v1.5.1-compatible sample app in this repo, needed **no
source changes** for the database and routing work, which was a useful signal about
that part's blast radius. The security round changed that: the fixture needed three
edits — `grghttp.PublishSession` after `Login`, a `Logout` that reports the error it
now receives, and a `JWT_SECRET` long enough to boot with. Those three are a fair
sample of what a real app has to do. An app that calls `builder.ToSQL()` directly,
implements `SQLDialect`, implements `core.ISessionStorage`, has its own login
handler, or relies on any of the behaviour changes will need work.

### Security

This section is first because it is the reason the tag has to be re-cut, and because
most of what is in it also breaks source or behaviour — the entries below are the ones
to read before the `Breaking` list, not after it.

A pre-release audit (`SECURITY_RELEASE_BLOCKERS.md`, rev 2) produced eight
release-gating findings. All eight are fixed here, together with the defects the fix
work itself turned up: a login handler that donated accounts, a second dead
authorization interface, a session middleware that never told the request its session
had been rotated, a CSRF regression introduced by the first version of the rotation
fix, and an identity-resolution amplification introduced by the first version of the
RBAC fix. `SECURITY_RELEASE_BLOCKERS.md` carries the per-finding status and the
regression test that fences each one.

Two of these are, by a distance, the most likely to be noticed on upgrade:

1. **The session cookie is renamed, which logs every signed-in user out.** See below.
2. **`/public/*` no longer renders SVG.** An `<img src="/public/logo.svg">` breaks.
   See below.

#### The session cookie is `__Host-` prefixed by default, logging every session out

`core.SessionCookieName` (the constant, `"GRG_SESSION_ID"`) is **gone**. The cookie is
now named `__Host-GRG_SESSION_ID` unless the app opts out. A browser holding the old
cookie sends a name the framework no longer reads, so on the first request after the
upgrade every signed-in user starts a fresh anonymous session. There is deliberately no
fallback to the old name: reading the unprefixed cookie when the prefixed one is absent
reopens exactly the hole the prefix closes.

The prefix is the load-bearing part of the session-fixation fix. An unprefixed name is a
token *any* host under the registrable domain can write, even when the app's own cookie
is host-only: `sibling.example.com` sends
`Set-Cookie: GRG_SESSION_ID=chosen; Domain=example.com; Path=/login`, RFC 6265
serialises the more specific path first, `http.Request.Cookie` returns the first match,
and the framework reads the identifier the sibling chose. A browser refuses to let any
other host set a `__Host-` cookie for yours.

| Configuration | Cookie name | Why |
|---|---|---|
| default (nothing set) | `__Host-GRG_SESSION_ID` | Secure, `Path=/`, no `Domain` |
| `auth.session.cookie.secure: false` | `GRG_SESSION_ID` | a `__Host-` cookie is only accepted when Secure |
| `auth.session.cookie.domain: example.com` | `GRG_SESSION_ID` | a `__Host-` cookie may not carry a `Domain` |

Both fallbacks are explicit opt-outs, keep the pre-upgrade cookie name, log nobody out —
and keep the exposure. The form in use is logged once, with its reason, on the first
resolution. New API: `core.SessionCookieBaseName`,
`core.HostPrefixedSessionCookieName`, `auth.SessionCookieName()`,
`auth.SessionCookieDomain()`, `auth.ConfigSessionCookieDomain` and
`auth.NewSessionCookie(value, maxAge, sameSite)` — the only correct way to build the
cookie, since the name, `Secure` and `Domain` are one decision and a `__Host-` name
paired with a `Domain` is a cookie no browser keeps. The old constant was **not** kept
as a deprecated alias: code reading the cookie under a hard-coded name would still
compile and would silently read the wrong cookie. See MIGRATION_v2.md §24.

#### `Login` rotates the session identifier, and callers must republish

`StandardAuthStrategy.Login` used to assign the user id to whatever session the client
presented. A party who can plant a session cookie in someone's browser therefore held a
live cookie for the victim's authenticated session from the moment the victim signed in;
neither existing rotation trigger helped, because both fire on idleness and age and the
victim's own page loads keep the activity stamp fresh. `Login` now revokes the presented
session, mints a fresh identifier, carries over only the user id and last-activity
stamp, and issues a **new** CSRF token. It fails closed: if the old session cannot be
revoked or the new one cannot be persisted, it returns an error and no session.

Because the identifier changes, the session the request resolved on the way in no longer
exists when `Login` returns, and two places cache it for the life of the request — the
message's session scope, and the message context, which the strategy consults *before*
the cookie. New helper `http.PublishSession(message, session)` updates both, drops the
per-request identity memo, and rewrites `X-CSRF-Token` on the response. **Any app with
its own login handler has to call it.** See MIGRATION_v2.md §25.

Two consequences worth knowing: a login response now carries two `Set-Cookie` headers
for the session when the request arrived without one (middleware starts a session, login
rotates it) — a hand-written client that takes the *first* match picks up an identifier
that has just been revoked; and every login creates and immediately revokes one extra
session.

#### Session revocation fails closed, and works across replicas

Revocation was fail-open and raceable. Storage failures were discarded, so a logout that
could not delete the server-side session still expired the browser's cookie and reported
success. The mediator's cache decided whether a session existed and was never
revalidated, so a logout handled by one replica left the session authenticating on every
other replica indefinitely. A concurrent request could re-publish a session a logout had
just removed, and rotation then laundered it into a new durable row. The attribute map
was marshalled for the database under no lock, which is `fatal error: concurrent map
iteration and map write` — a runtime fatal `RecoveryMiddleware` cannot contain.

- `core.ISessionStorage`'s four mutators return `error`; `GetSessionById` returns
  `(ISession, error)`, distinguishing "no such session" from "the store could not
  answer". `core.IAuthStrategy.Logout` returns `error`.
- `core.ISession` and `core.ISimpleStorage` are **deliberately unchanged and stay
  void.** See MIGRATION_v2.md §26 for what that means if you implement one.
- New optional `core.ISessionRevoker` (`RevokeSession(id) (bool, error)`), so rotation
  can refuse to carry a user id over from a session somebody already revoked.
- `DbSessionMediator.GetSession` consults the row on every call. The cache still
  guarantees one session object per identifier per process, but no longer decides
  whether the session exists.
- Every accessor on `auth.Session` and `auth.DbSessionEntity` takes the session's mutex,
  `GetUserId` included. The mediator persists a detached `Snapshot()` rather than the
  live entity.
- `auth.ISessionRepository.DeleteById` returns `(bool, error)`;
  `DbSessionRepository.DeleteById` is one `DELETE` rather than find-then-delete, so
  revoking twice is no longer an error that left the cache entry alive.
- `LoginController.Logout` redirects with a flash naming the failure instead of
  pretending the logout happened.
- `StandardAuthStrategy.Logout` invalidates the request's identity memo. Without it the
  memo key (session id plus user id) is unchanged across a logout and the revoked
  session is still on the message context, so anything access-controlled rendered later
  in the same request would be filtered for the user who just logged out. A logout that
  *failed* to revoke deliberately keeps the memo, because the caller is still
  authenticated.

See MIGRATION_v2.md §26.

#### `POST /login` always verifies the credentials it is given

`LoginController.Login` answered an already-authenticated request with a redirect home,
so the posted credentials were never compared to anything. Turned around, that is an
account donation: a party who can plant a cookie plants their own **signed-in** session,
the victim's login bounces off the guard, and the victim spends the visit inside the
planted account while the planter holds a live cookie for it. The guard is removed.
Authenticating over a live session is safe precisely because `Login` now replaces the
session rather than reusing it. `GET /login` keeps its redirect — a GET carries no
credentials. A failed attempt deliberately changes nothing: ending the live session on a
failed attempt would be a remote logout for anyone able to make a browser post the form.
Both handlers now `return` after redirecting; `ShowLogin` used to render the login form
into a response that already carried the 301.

#### JWT configuration fails closed

Nothing validated `auth.jwt.secret` anywhere. An app could boot — server or CLI, dev or
prod — with the key absent, empty, a YAML null, whitespace, an unresolved
`${JWT_SECRET}` literal, or short enough to guess, and then sign and accept tokens with
it. `GenerateJwt(user, "")` returned a working token and a `nil` error. Four boundaries
now refuse an unusable key: `AppProvider.Boot` (panics, via `provider.ValidateJwtConfig`),
`config.ResolveEnvPlaceholders` (registers the placeholder default so an absent key
becomes visible to the security-relevant guard at all), the exported `JwtService`
methods, and `JwtAuthStrategy.IsRequestMadeWithStrategy` — which matters most, because
the strategy is registered under `"api"` for *every* app, so an unusable key let any
request carrying a self-signed bearer token capture strategy resolution even in an app
that only ever configured session auth. `auth.MinJwtSecretLength` is 32 bytes, and a
value that reaches the length with fewer than eight distinct bytes is rejected as
padding. An app that declares no `auth.jwt` section is not made to invent a secret. See
MIGRATION_v2.md §28.

`jwt.Parse` is pinned to HS256 at all three call sites. To be precise about what that
buys: golang-jwt/jwt v5 already refuses `alg: none`, its case variants and cross-family
substitution. What was accepted before is substitution *within* the HMAC family — a
token re-signed as HS384 or HS512 against the same secret verified.

The boot error for an unresolved security-relevant placeholder no longer gives dangerous
advice. It used to end "remove the placeholder so the framework's secure default
applies" for every key in `SecurityRelevantKeys`. True for
`auth.session.cookie.secure`; false for `auth.jwt.secret`, which has no default at all,
so an operator following the framework's own instruction converted a caught boot failure
into a silent authentication bypass. Keys in the new `config.KeysWithoutSecureFallback`
get advice that fits them.

#### Uploads are stored by sniffed content, not by the client's filename

Three behaviours combined into same-origin script execution:

- **The stored extension now comes from the content.** `model.NewMultipartFile` sniffs
  the first 512 bytes with `http.DetectContentType`, looks the media type up in an
  allowlist, and stores the file under the extension that allowlist gives. A type with
  no entry is refused. Previously `filepath.Ext(originalFileName)` was appended verbatim
  while only the base name was sanitised, so a part named `payload.html` was stored as
  `…-payload.html`. `text/html`, `image/svg+xml` and the XML types are deliberately
  absent from the allowlist — they are script hosts, and there is no extension that
  makes them safe to serve back. `application/octet-stream` **is** present, mapped to
  `.bin`, so a binary format Go has no signature for is stored rather than rejected.
- **The allowlist is applied to the sniffed type, on every path.** `FormFile` and
  `GetFiles` checked `fh.Header.Get("Content-Type")` — a value the uploading client
  writes — so declaring `image/png` on a part full of HTML satisfied it. The
  DTO-binding path (`decoder/multipart.DecodeFiles`, a `core.IFile` field on a DTO, the
  documented way to receive an upload) had **no** content check at all.
- **`PublicController` never names a script-capable type.** See the next entry.

New config key `http.upload.allowedTypes`, a map from sniffed media type to stored
extension. Setting it **replaces the built-in table wholesale**, so an app that needs one
extra type has to restate the ones it still wants. See MIGRATION_v2.md §30.

The strongest mitigation is one this change cannot make for you: serve user uploads from
a separate origin.

#### `/public/*` is `nosniff` + `attachment`, and SVG stops rendering

Every `/public/*` response now carries `X-Content-Type-Options: nosniff` and
`Content-Disposition: attachment`, and the `Content-Type` is passed through only for a
render-safe allowlist: `image/*` **except SVG**, `audio/*`, `video/*`, `font/*`,
`text/plain`, `text/css`, `text/csv`, JavaScript, `application/json` and
`application/pdf`. Everything else — `text/html` and `image/svg+xml` included — is
served as `application/octet-stream`.

**The SVG change is the breakage most likely to be noticed.** `Content-Disposition`
does not apply to a subresource load, so stylesheets, scripts, raster images and fonts
served from `/public/*` keep working. The retyping *does* apply to them, and SVG is the
casualty: an SVG answered as `application/octet-stream` under `nosniff` is one the
browser refuses to render, so `<img src="/public/logo.svg">`, an SVG `background-image`
and an SVG `<use>` reference all break. There is no way to keep them that does not also
serve an *uploaded* SVG as `image/svg+xml`, which is a document that runs script on this
origin. SVG icons are a common static asset, so check for them before deploying — serve
them from a route of your own, or from somewhere that is not also the upload root. See
MIGRATION_v2.md §31.

Also fixed here: the containment check compared against the resource root with a bare
string prefix, which a sibling directory named `resources` or `resource-backups` would
have satisfied.

#### Security headers are emitted by default

`middleware.SecurityHeadersMiddleware` is new, and `RouteProvider` registers it as a
`/**` filter immediately after the recovery filter. The framework emitted no security
headers anywhere before.

| Header | Default | Notes |
|---|---|---|
| `X-Content-Type-Options` | `nosniff` | not configurable |
| `X-Frame-Options` | `SAMEORIGIN` | `DENY` is stronger; set it if the app frames none of its own pages |
| `Referrer-Policy` | `strict-origin-when-cross-origin` | |
| `Strict-Transport-Security` | `max-age=31536000`, **TLS responses only** | no `includeSubDomains`, no `preload` |
| `Content-Security-Policy` | **not sent** | see below |

**There is no default CSP, on purpose.** A policy worth having forbids inline script and
inline style, and a server-rendered app built on this framework's view engines routinely
has both — so a default strict enough to be worth the bytes would break working pages on
upgrade, and one loose enough not to (`unsafe-inline`, `unsafe-eval`) buys close to
nothing. Adopt one explicitly, measuring with `ContentSecurityPolicyReportOnly` first;
`middleware.RecommendedContentSecurityPolicy` is a starting point. HSTS is decided from
the request (`r.TLS`, or `X-Forwarded-Proto: https` for a TLS-terminating proxy; set
`TrustForwardedProto: false` for a server facing the internet directly). Any header
other than `nosniff` can be suppressed with `"off"` (`HSTSMaxAge: -1` for HSTS), and the
whole filter with `RouteProvider.DisableSecurityHeadersMiddleware()`.

One consequence of shipping `nosniff` everywhere: `HTTPViewScope.Render` now sets
`Content-Type: text/html; charset=utf-8` when nothing has chosen one. It used to write
the rendered template with no type at all and let net/http's sniffer guess, which
browsers forgave; under `nosniff` they do not, and `http.DetectContentType` recognises
only a fixed list of opening tags, so a fragment beginning `<ul>`, `<span>`, `<section>`,
`<form>` or `<tr>` was answered `text/plain` and shown as source. An app that renders
something that is not HTML — a sitemap, a feed, a plain-text mail body — sets the type
before calling `Render` and that choice still wins.

#### Request bodies are capped before they are read

Nothing capped a request body before. The server was built with `ReadTimeout`,
`WriteTimeout` and `MaxHeaderBytes` and no body limit, `http.MaxBytesReader` appeared
nowhere in the tree, and the multipart parser read the whole form and consulted its size
limits afterwards — the argument to `ParseMultipartForm` is only the *in-memory* budget,
so a single unauthenticated POST could write as many bytes to the OS temp directory as
it cared to send and the 10 MB per-file check ran once they were on disk.

- `app.MaxRequestBodyBytes()` resolves a ceiling; `http.MaxBytesReader` applies it at the
  server boundary in `ServerApp.Run` **and** per request in `Message.Init`, before any
  middleware, handler or parser sees the body. The second is what protects an app that
  mounts the router in a server of its own.
- The ceiling **derives from the upload budget** so the two cannot disagree:
  `max(32 MB, http.upload.maxMultipartSize) + 1 MB` of framing allowance. Override it
  outright with the new `http.security.body.maxRequestBytes` (bytes).
- `ParseMultipartForm` is now called with the body already capped at
  `http.upload.maxMultipartSize` plus the framing allowance, so the total upload budget
  is enforced *while* the parse runs. The in-memory budget is the smaller of
  `http.security.body.maxSizeMB` (default 10 MB) and the total upload budget; `FormFile`
  and `GetFiles` used to pass `http.security.file.maxSizeMB`, default **100 MB**, per
  concurrent request.

**Breaking:** a body larger than the resolved ceiling now fails at the reader. An app
that streams large bodies by hand must raise `http.security.body.maxRequestBytes`. See
MIGRATION_v2.md §29.

#### A malformed multipart body is a 400, not a panic

`GetMultipartFormValues` returns nil when the form cannot be parsed, and
`MultipartParser.Parse` ranged straight over `form.File` on that nil pointer — so any
client could panic any multipart endpoint with a truncated body, unauthenticated, with
no valid input needed. `Parse` now returns `err.InputBodyParseError` (400) and
`validateFormSize` nil-guards.

#### Email header injection via `Subject`, `To`, `Cc` and attachment names

Message headers were assembled with `fmt.Sprintf` and no CRLF filtering, so a CR or LF
in the subject, a recipient, a CC entry, the sender or an attachment file name did not
produce a header containing a newline — it produced *extra headers* and, after a blank
line, an entire replacement body. `smtp.SendMail` validates only the envelope, so the
attacker could not add envelope recipients or speak SMTP, but a subject of
`hi\r\nReply-To: attacker@evil\r\nContent-Type: text/html\r\n\r\n<phishing>` sent a
message they authored in full from the application's authenticated SMTP identity, with
the application's own SPF and DKIM vouching for it. Any app that puts user-supplied text
in a subject — a contact form, a "new message from ⟨name⟩" notification, a password
reset that greets the user by name — was a vector.

`MailService.buildBody` now **rejects** such a value with an error rather than stripping
it, so `Send` fails before the SMTP conversation and the operator sees the probe in the
log instead of a plausible-looking delivered message. The check covers the sender, every
recipient, every CC entry, the subject, every attachment file name and content id, and
(in `Send`) every BCC entry. See MIGRATION_v2.md §32.

#### `AccessCheckerMiddleware` and two dead authorization interfaces are deleted

`middleware.AccessCheckerMiddleware` was an authorization filter that enforced nothing.
The only code that could supply it an access decision was commented out, so its "no
decision available" branch fired on every request: it logged a warning and called the
next handler. **Any route it was mounted on had no authorization applied** — not weak
authorization, none, including for unauthenticated requests. A second fail-open sat
behind it: even with a decision, a denial produced a `403` only when the `namespace`
path parameter was `api`.

`core.HttpAccessCommand` is deleted with it. `core.HttpFilterCommand` — its twin,
`AllowFilterFields(ctx) []string` — is deleted too: it had no implementations and no
callers anywhere, so nothing ever invoked `AllowFilterFields` and an app that
implemented it in the belief filtering was restricted to the fields it named was
filtering on anything a request asked for. Removing it changes no enforcement;
request-derived filters are validated in `model.NewFilter`, and restricting them beyond
"must be a real column" is `model.AccessControl.ValidateFilterAccess`, reached through
`model.NewFilterWithAccess`. See MIGRATION_v2.md §27.

#### `RoleBasedAccessControl` is safe to share across requests

The user/role lookup was memoised in a plain `map[context.Context]*UserContextCache`
field with **no mutex**. Because the key was the per-request `context.Context`, no entry
was ever reused: every request was a miss and therefore a map *write*. An app naturally
shares one access-control object across every request, so two concurrent requests
through any RBAC entry point raced on that map — a DATA RACE under `-race`, and without
it `fatal error: concurrent map writes`, a runtime fatal `RecoveryMiddleware` cannot
contain, taking the process down together with every request in flight. The same map was
an unbounded leak: nothing pruned it on the normal path (`ClearUserCache` had no call
site), so it grew by one entry per request served and held each request's
`context.Context` and the `core.Authenticable` behind it alive forever.

The identity is now resolved once per exported call, threaded through private helpers,
and memoised for the length of one request on the **request's own context** — see
`core.IRequestIdentityMemo` under Added. Authorization decisions are unchanged. No API
signature changed; `ClearUserCache(ctx)` drops the request's memo, and
`ClearAllUserCache()` is a deliberate no-op because there is no longer any state
spanning requests. See MIGRATION_v2.md §33.

#### What these fixes cost, and what is still open

None of this is free, and an app author deciding when to upgrade should have the bill.

- **Database session storage is chattier.** The mediator's cache no longer decides
  whether a session exists, so resolving a session is a `SELECT` on every request rather
  than a map read after the first. Creating one costs `SELECT` (uniqueness check) +
  `SELECT` (does the store already hold it) + `INSERT` + `UPDATE` (the CSRF token,
  written through `SetItem`). That is the price of a logout on one replica taking effect
  on the others, and there is no version of cross-process revocation that reads only
  local memory. **It is not benchmarked.** If your session table is hot, measure before
  you deploy.
- **Every login creates and immediately revokes one extra session** — one extra `INSERT`
  and `DELETE` on database storage — because rotation mints the new identifier through
  the same `NewSessionWithoutUser` path everything else uses.
- **`db/orm/fields.go` still copies an entity through `reflect` under no lock.** Any
  entity shared between goroutines can therefore race inside the ORM, whatever locking
  its own accessors do. The session layer is safe only because
  `DbSessionMediator.persist` hands the ORM a detached `Snapshot()`; anything else in an
  app that persists a shared entity has the same problem and needs the same treatment.
  This is a known open defect, not a closed one.
- **The live e2e suite has not been run against the fixed tree.** The Docker registry was
  unreachable in the audit environment, so `sh e2e/run.sh` could not execute — which is
  also how the harness's own "green means nothing" faults were found by reading rather
  than by running. Everything above is verified by `go build ./...`, `go vet ./...`,
  `go test ./...` and `go test -race ./auth ./event ./http/... ./job ./model
  ./provider`; the live cross-engine exit criterion is **unverified, not passed.**
- **`RB-10` … `RB-15` from the audit are untouched** and remain open follow-ups:
  `RecoveryMiddleware` echoing the panic value in production, `AllowedProtectedFields()`
  collapsing the protected-field boundary, filter-pattern matching normalising
  differently from chi routing, RBAC `DBFilter.Field` taking user-controlled
  substitution into documented raw SQL, `buildSubquerySQL` rendering `ORDER BY` without
  the dialect hardening, and `MemoryRateLimitStore` sweeping at most every ten minutes.
  So is RB-2 (dependency and toolchain floor).

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
- **The duplicate condition family in `db/sql/gorm/postgres/v2` is removed.**
  `SimpleCondition`, `InCondition`, `BetweenCondition`, `RawCondition`,
  `CompositeCondition` and the `Equal` / `NotEqual` / `GreaterThan` / `LessThan` /
  `In` / `Between` / `And` / `Or` / `Raw` constructors were an engine-agnostic copy
  of `db/sql/core` — `?` placeholders, no quoting — left behind when the builder
  moved out of the Postgres package. Use the `dbCore` types directly. They are
  **not** drop-in replacements: field names differ (`BetweenCondition.Start`/`End`
  → `Lower`/`Upper`, `SimpleCondition` → `BinaryCondition{Left, Right}`) and there are
  no constructor helpers. See MIGRATION_v2.md §10.
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
  with `databases.<name>.allow_unfaithful_upsert: true`. `DO NOTHING` is unaffected.
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
- **The module path is `github.com/osbits/gorgany/v2`.** Go requires a `/vN`
  suffix for major version 2 and above, so `require github.com/osbits/gorgany
  v2.0.0` failed with *"version v2.0.0 invalid: should be v0 or v1, not v2"* —
  v2.0.0 could not be required as v2.0.0 at all. `+incompatible` does not apply,
  because the module has a `go.mod`. Every import path gains `/v2`; see
  MIGRATION_v2.md §20.
- **`DbProvider` no longer registers a database driver.** It blank-imported
  `db/sql/driver/builtin`, which registers both engines, so a Postgres-only app
  linked `gorm.io/driver/mysql`, `go-sql-driver/mysql` and
  `filippo.io/edwards25519` with no way to opt out. Add one blank import —
  `_ ".../db/sql/driver/postgres"`, `.../mysql`, or `.../builtin` for both.
  Compile-clean, boot-time break; `driver.New`'s message names the import. See
  MIGRATION_v2.md §21.
- **A DTO validation failure is `422` for an API client and `303` for a browser**,
  not a `301` redirect for everyone. **Behaviour change, no compiler error.** This
  is what makes the reshaped payload above observable; see MIGRATION_v2.md §22.
- **`err.InputBodyParseError.Error()` no longer contains the request body.** The
  body is still on the `Body` field. **Behaviour change**, and a security fix: the
  framework's own handler logs the error, so every malformed body was written
  verbatim to the log at Error level.
- **`omitempty` now uses `encoding/json`'s emptiness rule.** A non-nil empty slice
  or map is omitted where it used to be emitted; a zero `time.Time` or nested
  struct is emitted where it used to be omitted. **Behaviour change, no compiler
  error** — response shapes move in both directions. See MIGRATION_v2.md §23.
- **An embed/outer wire-name collision resolves to the outer field**, regardless
  of declaration order. **Behaviour change, no compiler error.**
- **An unresolved `${VAR}` is blanked rather than left as the literal.** A
  correction to the first version of this fix: an unresolved
  `auth.session.cookie.domain` became `Domain: "${COOKIE_DOMAIN}"`, an invalid
  cookie attribute, so the browser dropped `Set-Cookie` and login failed
  silently. `config.KeepUnresolvedLiterals()` opts out. **Behaviour change, no
  compiler error.**

The security round's breaks, each detailed under **Security** above:

- **`core.SessionCookieName` is deleted** and the session cookie is renamed to
  `__Host-GRG_SESSION_ID`. **This logs every existing session out on upgrade.**
  MIGRATION_v2.md §24.
- **`core.ISessionStorage` reshaped.** `ClearExpiredSessions`, `AddSession`,
  `DeleteSession` and `DeleteSessionById` return `error`; `GetSessionById` returns
  `(ISession, error)`. `core.ISession` and `core.ISimpleStorage` are unchanged.
  MIGRATION_v2.md §26.
- **`core.IAuthStrategy.Logout` returns `error`.** MIGRATION_v2.md §26.
- **`auth.ISessionRepository.DeleteById` returns `(bool, error)`**, and
  `auth.DbSessionMediator.DeleteSession` with it.
- **`StandardAuthStrategy.Login` rotates the session identifier and mints a fresh CSRF
  token**, so a caller must republish with `http.PublishSession`. **Behaviour change
  for the framework's own `LoginController`, compile-clean break for an app's own login
  handler** — it will build and quietly log nobody in. MIGRATION_v2.md §25.
- **`LoginController.Login` no longer short-circuits an authenticated request**, so a
  `POST /login` with valid credentials switches user instead of being ignored.
  **Behaviour change, no compiler error.**
- **`middleware.AccessCheckerMiddleware`, `core.HttpAccessCommand` and
  `core.HttpFilterCommand` are deleted.** MIGRATION_v2.md §27.
- **Boot fails for an absent, empty, placeholder or weak `auth.jwt.secret`** (32-byte
  floor), and the exported `JwtService` methods refuse an unusable key. Compile-clean,
  boot-time break. MIGRATION_v2.md §28.
- **Request bodies are capped by `MaxBytesReader`.** New
  `http.security.body.maxRequestBytes`. **Behaviour change, no compiler error.**
  MIGRATION_v2.md §29.
- **An upload is stored under the extension its *content* sniffs to, and a type outside
  the allowlist is refused.** New `http.upload.allowedTypes` replaces the built-in table
  wholesale. **Behaviour change, no compiler error.** MIGRATION_v2.md §30.
- **`/public/*` sends `nosniff` and `Content-Disposition: attachment`, and retypes
  everything outside a render-safe allowlist to `application/octet-stream` —
  including SVG.** An `<img src="/public/logo.svg">` stops rendering. **Behaviour
  change, no compiler error.** MIGRATION_v2.md §31.
- **`MultipartFile.Close()` no longer deletes a file `Write` has published.** Use
  `Delete()`. It also became idempotent, and now deletes the recorded *temp* path rather
  than resolving through the public one. **Behaviour change, no compiler error.**
- **`MailService.Send` fails for a value it previously accepted**: a CR or LF in the
  sender, subject, a recipient, a CC or BCC entry, or an attachment name or content id.
  The generated message also uses CRLF throughout and RFC 2047-encodes non-ASCII header
  text. **Behaviour change, no compiler error.** MIGRATION_v2.md §32.
- **`mail`'s internal `bodyBuilder.String()` is `Build() ([]byte, error)`**, and
  `boundaryContent.FormatEmail` returns `(*bytes.Buffer, error)`. Unexported; listed
  because a fork or a vendored copy will see it.
- **Security headers are on by default** as a `/**` filter. `X-Frame-Options:
  SAMEORIGIN` is the one that can break a working page (an app framed by a third party).
  `RouteProvider.DisableSecurityHeadersMiddleware()` opts out.
- **`HTTPViewScope.Render` sets `Content-Type: text/html; charset=utf-8`** when nothing
  has chosen one. **Behaviour change, no compiler error.**
- **`CsrfController.Token` no longer writes to session storage.** The
  `SessionStorage` field is still injected and is now unused.

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
- **`db/sql/driver/postgres` and `db/sql/driver/mysql`**: single-engine
  registration packages, so an app links only the engine it uses. `driver/builtin`
  still registers both.
- **`dbCore.CompositeCondition`**, moved from the Postgres driver package where it
  was keeping `gorm.io/driver/postgres` on `model`'s dependency path.
- **`model.IsEmptyValue`**: `encoding/json`'s emptiness rule, beside the tag
  parsing that reads `omitempty`.
- **`config.KeepUnresolvedLiterals()`** and `config.ParseWithOptions`.
- **`err.InputBodyParseError.Body`** is documented as deliberately excluded from
  `Error()`, since an error string reaches a log by default.
- Test coverage went from 14 `_test.go` files to 47. Measured with no database
  running: `db/sql/builder` 96.6%, MySQL dialect 95.3%, Postgres dialect 96.6%.
  A `livedb`-tagged suite additionally verifies the Tier-1 fixes against real
  MySQL 8 and Postgres 16.

From the security round:

- **`http.PublishSession(message core.HttpMessage, session core.ISession)`**: installs a
  session as the one the rest of the request sees — the session scope, the message
  context, the identity memo and the `X-CSRF-Token` header, which the open-coded
  `message.Session().(core.IEditableSessionScope).Set(...)` dance did not cover.
- **`core.IRequestIdentityMemo`** (`LoadIdentity`, `StoreIdentity`,
  `InvalidateIdentity`): an optional interface a per-request context may implement so
  the caller's identity is resolved once per request rather than once per question. The
  framework's own message context implements it. An app that supplies its own message
  context can implement it and get the same memoisation; a context that does not — a
  CLI command, a background job, a unit test — resolves every time, as before. The
  memoised value is an `any` because `app/core` sits underneath the package that owns
  the resolved type; a carrier stores it and hands it back, keyed, and never inspects
  it.
- **`core.ISessionRevoker`** (`RevokeSession(id) (bool, error)`), optional and additive.
  Both shipped storages implement it; one that does not gets the previous
  (unconditional) rotation behaviour.
- **`middleware.SecurityHeadersMiddleware`**, `middleware.SecurityHeadersOptions`,
  `middleware.RecommendedContentSecurityPolicy`,
  `middleware.DefaultReferrerPolicy` / `DefaultFrameOptions` / `DefaultHSTSMaxAge`, and
  `RouteProvider.ConfigureSecurityHeaders` / `DisableSecurityHeadersMiddleware`.
- **`core.SessionCookieBaseName`**, `core.HostPrefixedSessionCookieName`,
  `auth.SessionCookieName()`, `auth.SessionCookieDomain()`,
  `auth.ConfigSessionCookieDomain` and `auth.NewSessionCookie`.
- **`auth.ValidateJwtSecret`**, `auth.MinJwtSecretLength`, `provider.ValidateJwtConfig`,
  `config.JwtIsConfigured`, `config.JwtSecretKey` and
  `config.KeysWithoutSecureFallback`.
- **`auth.NewDbSessionStorageWithRepository`**: builds a storage over a repository given
  directly. `NewDbSessionStorage` leaves the mediator to be injected, so nothing outside
  the container could build a working storage — including a test that wants to prove a
  revoked session stops authenticating, or an app whose sessions live in a table its own
  repository owns.
- **`auth.DbSessionEntity.Snapshot` / `AdoptPersistedMeta` / `IsPersisted`**, and
  `auth.SessionTombstoneRetention` (25 h).
- **`orm.UpdateExisting` and `orm.ErrRowGone`.** `Save`, `Create`, `Update` and `Delete`
  are unchanged, and `Update` still does not treat zero matched rows as an error:
  Postgres reports matched rows, but MySQL reports *changed* rows unless the DSN carries
  `clientFoundRows`, so a no-op update of a live row legitimately reports zero there.
  `UpdateExisting` resolves the ambiguity by checking for the row only when the statement
  matched none. `updateEntity` now records the statement result on
  `EntityMeta.QueryResult` unconditionally.
- **`model.MultipartFile.MediaType()`**, `model.UploadTypePolicy`,
  `model.ResolveUploadTypePolicy`, `model.DetectUploadMediaType`,
  `model.UploadTypeError` and `model.ConfigAllowedUploadTypes`.
- **`Message.RegisterCloser(io.Closer)`** and `HTTPRequestScope.CapBody(int64)`.
- **`app.MaxRequestBodyBytes()`**, `app.ConfigMaxRequestBytes`,
  `app.DefaultMaxRequestBytes`.
- **Config keys**: `http.upload.allowedTypes` and
  `http.security.body.maxRequestBytes` are new.
  `auth.session.cookie.domain` is not new but now has a named constant, a resolver and a
  documented consequence — setting it opts the app out of the `__Host-` cookie name.
- Another 29 test files, all of them regression tests for the audit findings; they are
  named per finding in `SECURITY_RELEASE_BLOCKERS.md`.

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

#### Query builder and dialect (continued)

- **`dbCore.BetweenCondition` interpolated a string bound into the SQL text** instead
  of binding it, which made it the only member of the condition family to read a
  string in a *value* position as SQL. `Builder.Between` passes its arguments straight
  through, so an app filtering a date range from the query string produced
  `created_at BETWEEN 1 OR 1=1 -- AND 2` — SQL injection through the framework's own
  builder. Every sibling already bound: `BinaryCondition.Right`, `InCondition.Values`
  and `LikeCondition.Pattern` all parameterise a string. A `*Query` bound still renders
  as a subquery; use a `RawCondition` for an identifier in a bound.

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
- **A malformed request body was written verbatim to the log.**
  `InputBodyParseError.Error()` included it and `processBodyParsingError` logs the
  error, so a truncated `POST /auth/login` put a cleartext password in `docker logs`
  and any aggregator. New exposure from the same release that introduced the error
  type. The log now carries the request line instead — `URL.Path`, not
  `RequestURI`, because a query string routinely carries a token.
- **Validation errors never reached an API client.** `processValidationErrors` was
  the one default handler the negotiation work missed, and it answered every caller
  with a `301` to the `Referer`. Two further defects in the same four lines: a `301`
  is permanently cacheable and browsers rewrite it to a `GET`, and an empty
  `Referer` produced `Location: ""`.
- **Two more unchecked type assertions**, in `processValidationErrors` and
  `processValidationError`. `Catch` dispatches on the error's *bare type name*, so a
  type called `ValidationErrors` from any package reached them and panicked.
- **Query-string and multipart parse errors lost the field name.**
  `checkAndAddIfValidationError` guarded on `errors.Is(err, &err.ValidationError{})`,
  which is always false — no `Is` method, freshly allocated target — and tested the
  singular type where the parsers produce the plural one; the assertion behind it
  would have panicked had the branch been reachable. Both parsers also wrapped with
  `fmt.Errorf("failed to process field %s: %w", …)`, putting the name in prose at the
  one place it was known. So every such failure came back as `field: "GeneralError"`,
  and the reshaped payload above did not reach two of the three input paths.
- **The framework's own JWT login endpoint crashed on bad input.** `panic(err)` on a
  malformed body (a 500 where every other body path is a 400), a discarded body-read
  error, an unread error from `Login` followed by a nil dereference, and a nil
  dereference when no `jwt` strategy is registered. It also served a Forbidden-coded
  envelope under HTTP 200, and put its error message in the envelope's `body` rather
  than `errors` — **response shape change** for anyone using it.

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

#### The event bus

- **`Publish` read the subscriber map with no lock held**, while every write took one.
  The race detector reports it. On a Go map that can escalate to `fatal error:
  concurrent map read and map write` — a runtime fatal, so `RecoveryMiddleware` cannot
  catch it and it takes the process down rather than the request. The lock is now an
  RWMutex and is released before the subscriber runs, so a slow handler cannot
  serialise other publishes and a handler may subscribe from inside `Handle`.
- **`Subscribe` and `SubscribeAsync` returned while holding the mutex** on the
  non-pointer path, so one bad subscriber wedged every later `Subscribe`,
  `SubscribeAsync` and `Unsubscribe` permanently. Validation now happens before the
  lock is taken.
- **`Subscribe(event, nil)` panicked**: `reflect.TypeOf(nil)` is nil and `Kind()` was
  called on it.
- `doPublishAsync` called `waitGroup.Done()` before recovering, so `WaitAsync` could
  return while a panicking handler was still unwinding.

  Note the contract, since "bus" invites the opposite assumption: there is **one
  subscriber per event name** and subscribing twice replaces the first. Documented and
  pinned by a test rather than changed.

#### The IoC container

- **Rebinding a core interface deadlocked the process.** `bind` took the write lock
  with a deferred unlock and then logged the rebind warning while still holding it;
  `log.Log()` goes through the factory `LoggerProvider` installs, which resolves
  `core.Logger` back out of the same container, and `sync.RWMutex` is not reentrant.
  Unconditional, and triggered by the ordering the warning itself recommends. A real
  app's boot printed four warnings and then hung forever.
- The container's own diagnostics can no longer re-enter it. Releasing the lock
  stops the deadlock but not the recursion: the logger factory's fallback branch
  *binds* a logger when resolution fails, so a warning from `bind` could reach `bind`
  again.

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

#### Sessions and uploads (from the security round)

Bugs found alongside the security work, none of them exploitable on their own:

- **A request that rotated its session was anonymous.** `CurrentSession` rotates once a
  session is idle past the activity timeout or older than the rotation interval, and the
  rotation deletes the identifier the request resolved on — but `SessionMiddleware` never
  told the request. Both of the request's caches named the deleted identifier, so
  `IsLoggedIn` and `CurrentUser` found nothing: a returning visitor was bounced through
  the login page once per idle period, on a request carrying a perfectly good cookie. It
  also orphaned a row — a login arriving past the activity timeout took `Login`'s "no
  existing session" branch and minted a third session while the live one was never
  revoked, leaving three `Set-Cookie` headers, two rows, and an `X-CSRF-Token` bound to
  neither. `SessionMiddleware` now publishes the session it resolved; when nothing
  rotated it is a no-op.
- **A CSRF token now survives an idle or age rotation** and is deliberately *replaced* by
  a login. Rotation used to mint a fresh token and leave the client holding one the next
  request rejects — a spurious 403 at the activity threshold and the rotation interval,
  on a request the client did not ask to rotate anything on, which it has no way to
  learn about. `docs/CSRF.md` is corrected: a session keeps one token for as long as it
  is the same session **and the same principal**, and re-reading `X-CSRF-Token` from
  every response is required rather than merely tidy.
- **`Request().Body()`'s "body too large" error reported the wrong limit** —
  `maxFileBytes`, a much larger number than the `maxBodyBytes` actually applied, so the
  message told an operator the body was under the limit it had just been rejected for.
- **Temp copies of uploads were never removed.** The cleanup sat commented out behind a
  `// todo: need to fix the closing (removing) of temp files`, so every upload received
  through a DTO stayed in `resource/temp` for good. The todo was warranted:
  `MultipartFile.Close` resolved the file to delete through the *public* path as soon as
  `Write` had published the upload, so a request-scoped cleanup would have deleted the
  stored file — and it closed the handle before asking for the path, so the temp copy
  was in fact never removed either. Whoever wrote the cleanup had to choose between
  leaking every upload and destroying every upload. `Close` now deletes the recorded temp
  path only and is idempotent; `Message.RegisterCloser` releases the copies when the
  request ends.
- **`Message.Close` deleted no spilled multipart parts.** It looped over the parts
  opening each one and closing the *new* handle, which removes nothing, so every part the
  parser spilled past its in-memory budget stayed in the OS temp directory for the life
  of the process. It calls `request.MultipartForm.RemoveAll()` now.
- **`decoder/multipart.DecodeFiles` discarded the error from `NewMultipartFile`** and
  bound the nil result onto the DTO anyway, producing a non-nil `core.IFile` holding a
  nil pointer, so the handler's first call on it panicked somewhere unrelated to the
  upload.
- **`MultipartFile.Write(path, nil)` on a released file dereferenced a nil reader**
  inside `io.Copy` instead of reporting that the upload was gone.
- **An upload with a very long filename was refused** with "file name too long" from
  `os.Create` — an ordinary browser upload from someone with a verbose filename, not an
  attack. The base name is now bounded, and one that sanitises down to nothing is stored
  as `upload<ext>` rather than with an empty name component.
- **`DbSessionRepository.Save` called an update against a row that was gone a success.**
  An `UPDATE` keyed on a primary key that no longer exists matches nothing and succeeds,
  so a session whose row had been deleted was written "successfully" and every caller
  above believed the state was persisted. It goes through `orm.UpdateExisting` now.
- **Six dropped errors on `DbSessionEntityWithMediator`'s setters.** Each mutates the
  entity and writes it back, and each called `UpdateSession` as a bare statement, so a
  session whose store was unreachable behaved exactly like one that had been saved.
- **`session:gc` and `ClearExpiredSessionsJob` reported success for a sweep that
  failed.** The storage swallowed the error; both callers now report it, and the command
  prints "expired sessions were NOT cleared" rather than "cleared".
- **A mail with neither a body nor an attachment panicked** on a nil MIME boundary. It
  now produces a valid headers-only message.
- **A `.svg` served from `/public/*` was `image/svg+xml`** and a `.html` was
  `text/html; charset=utf-8`, both from the application's own origin. See Security.

#### The e2e harness reported success for runs that executed nothing

Three independent faults, all of which made a green harness meaningless:

- **`e2e/run.sh` exited 0 no matter what failed.** Its `EXIT` trap ends in `|| true` —
  it has to, since tearing down a stack that never came up is not an error — and on any
  shell that predates POSIX settling on `$?` being preserved across a trap, the script's
  status becomes the trap's. That was observed, not feared: a base image would not pull,
  no test executed, and the script reported success. `cleanup` now reads `$?` on its
  first line and `exit`s with it on its last, dumps `ps -a` and the container logs before
  `down` deletes them, and the `up` steps use `--wait` so a service that starts and
  immediately dies is caught rather than walked past.
- **Every case in the live suite skipped itself when its dependency was unreachable**,
  and `go test` reports a run of nothing but skips as `ok` with exit 0 — so twelve skips
  read exactly like twelve passes to anything downstream of the exit code. Skipping is
  the right default for a developer running `go test ./...`; it is the wrong default for
  the harness. `E2E_REQUIRE_LIVE=1`, set by the compose file, turns every gate fatal, and
  a `TestMain` fails a run that executed **zero** live cases at all — which is what
  catches a `-run` pattern that matched nothing or a build tag that left the file out of
  the binary.
- **MySQL was never started, and could not have been reached if it had been.** The
  harness brought up only `postgres` and ran `go test` without the `livedb` tag, so the
  whole of the MySQL coverage was compiled out; and the live suite hardcoded
  `127.0.0.1:3307`, which from inside the runner container is the runner itself. The
  compose file now starts `postgres-live` and `mysql-live` alongside the fixture's own
  database, the runner depends on all of them being healthy, the test command carries
  `-tags=livedb`, and `E2E_PG_HOST` / `E2E_PG_PORT` / `E2E_MYSQL_HOST` /
  `E2E_MYSQL_PORT` redirect the suite at the compose network. The cross-engine case
  refuses to fall back to a second Postgres database under `E2E_REQUIRE_LIVE`.
- `e2e/fixture-app/.env` carried `JWT_SECRET=fixture-secret`, 14 bytes, which the new
  validation rejects — so the fixture app panicked at boot and took the whole suite with
  it. It is a 42-byte value now. Worth naming because it is exactly the upgrade failure a
  real app will hit.

### What a real migration found

A separate round of work (`FINDINGS_v2_FROM_FLOW8.md`) came from migrating a real
application — ~150 controllers, Postgres-only, SPA frontend — to `2.0.0-edge`. Every
one of its eleven findings held up on inspection, and all of them survived the
framework's own green suites: `go test ./...`, `go test -tags=livedb ./...` and
`sh e2e/run.sh`.

That is the part worth keeping. The gaps were where the tests structurally could not
reach:

- `service/container_warning_test.go` exercises the exact line that deadlocked and
  passes, because its logger factory returns a logger directly rather than resolving
  through the container. One line's difference from the real factory.
- `http/error_negotiation_test.go` covered four of the five default error handlers,
  and the one it omitted was the broken one — not a coincidence, since it covered
  the four the negotiation work had changed.
- `e2e/fixture-app` overrode all five handlers, so the e2e harness could never reach
  a default. That override is now removed.

Also documented rather than changed:

- The `OPTIONS` preflight responder is registered without the route middleware
  chain, which is what makes it side-effect-free — but it also means route-scoped
  auth and audit middleware no longer apply, so a session-less `OPTIONS` on a
  protected route returns `204` with an `Allow` header naming the methods. It has to
  stay that way: browsers send preflight uncredentialed, so applying auth would
  break CORS entirely.
- `GET /csrf` is registered at the root with no middleware, so an app whose gates
  are scoped to `/api/**` does not apply them to it. Correct by design — a client
  needs a token before it can authenticate. Being outside those patterns also puts it
  outside the *session* middleware, which is why it now starts a session itself; see
  the section below.

### Features that were shipped unusable

A review of the migration plan found five cases where a v2 feature was documented, tested
and reachable by nobody — each one a default that could not work, an opt-in with no route
to it, or a silent skip with no diagnostic. They are grouped because they share a shape:
the tests asserted the behaviour the code had, so being green proved nothing about being
usable.

- **`GET /csrf` answered `403` forever.** No session on the request meant "the CSRF
  endpoint must be covered by the session middleware" — but the framework registers it
  at the root with no middleware, and an app's session middleware is scoped to the
  namespace it protects, so the default registration could not satisfy its own
  precondition. The first call any client makes carries no session, which is exactly
  the call the endpoint exists to answer. It now starts one itself, via the same
  `NewSessionWithoutUser` call `SessionMiddleware` makes, `Set-Cookie` included, and
  persists the token through `ISessionStorage` (`SetItem` alone does not survive the
  response on a DB-backed store). A bearer-token request gets `400` naming the reason
  instead of a `500`. `DisableCsrfController()` plus a hand-registered copy is no longer
  the only way to get a token.
- **A mounted SPA shadowed the negotiated `404`.** `SpaController` registers a `/*`
  catch-all, and chi matches a catch-all in preference to falling through to `NotFound`,
  so mounting the SPA replaced the router's negotiated `404` for every GET the app had no
  route for: `GET /api/v1/widgetz` with `Accept: application/json` returned **`200` and an
  HTML document**, while the same path under DELETE still returned the `405` envelope.
  All of `SpaController`'s refusals now go through the same negotiated writer the router
  uses (`http.WriteNegotiatedError`, moved out of `http/router` for the fourth caller), so
  an app's `404` no longer depends on whether a SPA is mounted.
- **The MySQL upsert opt-in could not be taken.** `MySQLDialect.AllowUnfaithfulUpsert`
  was documented in four places and settable from none of them by an app using the ORM:
  `Dialect()` returned a hard-coded `&MySQLDialect{}`, and `session.Query()` and
  `Transaction()` build every builder from it, so the only route was constructing a
  builder by hand with `NewBuilderWithDialect` and bypassing the ORM. The refusal message
  named a field the caller could not reach. New config key
  **`databases.<name>.allow_unfaithful_upsert`**, threaded through to the dialect; the
  message now names it. MySQL-only, ignored by Postgres, which expresses the construct
  exactly.
- **Nothing swept the `sessions` table.** `ClearExpiredSessionsJob`'s own doc comment said
  it was "registered by the standard setup for apps using `auth.session.storage:
  database`", and no such registration existed anywhere in the framework — while
  `DbProvider` adds the sessions migration unconditionally. Every database-backed app got
  the table and no sweep, and the symptom (a table that only grows) is indistinguishable
  from a sweep that runs and finds nothing. A2 had fixed the *other* half, the scheduler
  that could not have ticked it. `JobProvider.Boot` now adds the job for `database`
  storage, with `DisableSessionGc()` to opt out; an app that already added the job by hand
  keeps its own registration rather than failing to boot on the duplicate. Plus a sixth
  built-in command, **`session:gc`**, for a web tier that does not run the scheduler or for
  wall-clock scheduling that interval-only `core.JobSchedule` cannot express.

  Scheduling the sweep exposed three defects in the code it newly reaches, all fixed here:

  - `MemorySession.ClearExpiredSessions` ranged over the session map with **no lock
    held**, taking the lock only around each individual delete, while every other method
    on the type locks correctly. A concurrent map iteration and write is a runtime *fatal*
    error — `RecoveryMiddleware` cannot catch it, so it takes the process down rather than
    the request. Same failure mode as the event bus race.
  - `Session.expiry` and `DbSessionEntity.Expiry` were written unguarded by `SetExpiry`,
    which `SessionMiddleware` calls on **every request**, and read unguarded by the
    sweep's `IsExpired`. A `time.Time` is three words, so a torn read could expire a live
    session or keep a dead one. `DbSessionMediator` caches one entity pointer per session
    and hands it to every concurrent request for that session.
  - `DbSessionStorage.ClearExpiredSessions` discarded its error with `_ = err`, alone in a
    file where every other method reports through `HandleError`. A sweep failing every
    time looked exactly like one that worked.
  - `DbSessionRepository.DeleteExpired` was one unbatched
    `DELETE FROM sessions WHERE expiry < NOW()`. On an app that had been leaking for months
    the first sweep would take a long lock on the whole backlog, emit a WAL burst
    proportional to it, and leave bloat needing `VACUUM` — so the fix for the leak would
    have hit hardest exactly the apps that had leaked most. It now deletes in batches of
    `auth.SessionSweepBatchSize` (1000), each committing on its own, capped per sweep so it
    cannot run forever if rows arrive as fast as they go. `DELETE ... LIMIT` is MySQL-only
    and Postgres has no equivalent, so the statement wraps its subquery in a derived table —
    which is also what MySQL needs to avoid error 1093 on its own DELETE target. Verified
    against live Postgres 16 and MySQL 8.4.
- **Memory session storage never reclaimed anything.** `SessionMiddleware` creates a session
  for every non-OPTIONS request that arrives without one, so on a public app the store grows
  per crawler hit, scanner probe and health check. Those clients never come back, so the
  read-through eviction in `SessionMiddleware` cannot reach their sessions — and the scheduled
  sweep above was registered for `database` storage only, on the reasoning that memory sessions
  are collected when the process exits. That is not a bound for a server that runs for weeks,
  and it left `MemorySession.ClearExpiredSessions` with no caller at all: the same defect one
  entry up, on the **default** backend, since `AppProvider` treats anything that is not
  `database` as memory. `MemorySession` now evicts expired entries inside `AddSession` — the
  only method that grows the map — at most once per `auth.MemorySweepInterval`. Deliberately
  not by registering the job for memory storage: that would make an in-process bound depend on
  an app wiring `JobProvider`, and an app that does not would still leak. Note what remains
  inherent: a memory store holds every session created within one lifetime, so a long
  `auth.session.lifeTime` plus public session creation is a large heap by construction, and
  `database` storage is the answer to that rather than a shorter sweep.
- **A validator that silently ignored your `validation.*` translations.** `validator.message`
  skips the i18n lookup when no manager is installed, so every message comes back as the
  framework's English. The skip is deliberate — a CLI app validates its command DTOs without
  booting i18n, and `GetManager()` panics when none is installed, so a bad flag would become
  a crash — but nothing distinguished it from the case that looks identical and is a bug: a
  server app that ships the keys and never registers `I18nProvider`. Its catalog has no
  visible effect and there is nothing to grep for. Now warned, once per process (a validation
  failure is request-driven, so a line per rejected field would be a floodable log), naming
  the keys and the provider. Gated on `i18n` being present in the config, so an app that is
  not using the feature is not told about it.

### Corrections to the briefs

Six claims across `IMPROVEMENT_v2.md`, `IMPROVEMENT_v2.1.md` and this work did not
survive verification:

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
6. **A5's fix was wrong in its remedy as well as its reasoning.** The v2.1 round
   already recorded that "leave the key alone so its default applies" cannot work.
   What it did not follow through on is that leaving the literal in place is
   *actively worse* than blanking for an optional key, because a literal `${VAR}` is
   a value no consumer accepts. Corrected in this round; see the Breaking entry.

Four more from the security round, against `SECURITY_RELEASE_BLOCKERS.md` rev 2 and
against the fix work's own first attempts:

7. **RB-5's scope was one interface too small.** It called for deleting
   `core.HttpAccessCommand` and left `core.HttpFilterCommand` alone. The twin has the
   same defect for the same reason — no implementations, no callers, and a name that
   reads as a control — so it is deleted too. `core.IQueryBuilder` is genuinely
   unaffected.
8. **RB-4's API break did not have to reach `core.ISession`.** Giving the setters errors
   would break every request scope, view scope and test double downstream for a signal
   almost no caller can act on. They stay void; the failure surfaces at the next
   operation that *can* report one — the storage call, or `Login`/`Logout` — and
   `StandardAuthStrategy.Login` now persists through the storage after setting the user
   id, which is both a reportable step and positive confirmation the write landed.
9. **Carrying the CSRF token across a rotation was right, and then wrong.** The first
   version of the RB-3 fix carried the token onto every new identifier, including the
   one a login mints. That is a complete CSRF bypass for an app that opted out of the
   `__Host-` name: a sibling host plants a session, reads its token from `/csrf`, the
   login moves the victim onto an identifier the sibling cannot guess — and the sibling,
   being same-site, still gets the victim's `SameSite=Lax` cookie sent on its POSTs and
   presents the token it already knows. `RotateSession` still carries the token, because
   the idle and age rotations happen on requests the client did not ask to rotate
   anything on; `Login` replaces it, because a login is a request the client made and
   reads the response to.
10. **Resolving the RBAC identity per call was a real regression.** The first version of
    the RB-6 fix removed the shared map and resolved on every exported call, which for a
    session-backed strategy is a session lookup plus a user load — once per row, because
    `FieldFilteredDto.MarshalJSON` runs per DTO. A hundred-row list performed a hundred
    of each. The memo is back, on the request's own context where the data's lifetime
    actually is, keyed on the session id and user id so a login mid-request misses it.

---

## [1.5.1] and earlier

See git history.
