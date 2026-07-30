# CSRF protection

This document is the client contract. If you are writing the browser side of a Gorgany
app, everything you need is in the first two sections.

## The contract in three lines

1. **On boot**, `GET /csrf` and keep the `csrf_token` from the response body.
2. **After every response**, if it carried an `X-CSRF-Token` header, replace your stored
   token with it.
3. **On every mutating request** (anything that is not GET, HEAD or TRACE), send the
   stored token in the `X-CSRF-Token` header — or, for a classic form post, in a
   `csrf_token` form field.

That is the whole protocol. The rest of this document explains why each line is there
and what happens if you skip it.

## Getting the first token

```js
const res = await fetch('/csrf', { credentials: 'include' })
const { body } = await res.json()
let csrfToken = body.csrf_token       // the token
const csrfHeader = body.header        // "X-CSRF-Token"
```

The endpoint is `GET`, so it is on the middleware's safe-method list and needs no token
of its own — otherwise you could never obtain the first one.

It returns the token in **both** the body and the `X-CSRF-Token` response header. The
body is there because a cross-origin `fetch` cannot read a response header unless the
server lists it in `Access-Control-Expose-Headers`; the header is there so a same-origin
client can use one uniform code path for this response and every other one.

### It is mounted at the root, outside your middleware patterns

`GET /csrf` is registered at the root with no middleware of its own, so an app whose gates
are scoped to a pattern — an install gate on `/api/**`, a tenant gate, a maintenance gate —
does not apply them to it.

That is correct by design: a client needs a token *before* it can authenticate or pass most
gates, so a token endpoint behind them would be unreachable exactly when it is needed. But it
is the one route that appears in your app without you asking for it, so it is worth knowing
it is there and that it answers unconditionally. It returns nothing but a token bound to the
caller's own session.

If a gate genuinely must cover it — or you want it on a different path, or replaced with your
own handler — turn the framework's off and mount your own:

```go
routeProvider.DisableCsrfController()
// then mount controller.CsrfController yourself, with a different Path or inside a
// middleware pattern, or serve your own handler
```

## Keeping the token current

Every response to a request that carries a session publishes the current token:

```
X-CSRF-Token: p6Zc...==
```

Read it on the way past and store it. In practice this means you rarely need `/csrf`
after boot — the token arrives on the responses you were making anyway.

```js
async function api(url, options = {}) {
  const res = await fetch(url, {
    ...options,
    credentials: 'include',
    headers: {
      ...options.headers,
      ...(options.method && !['GET', 'HEAD'].includes(options.method)
        ? { 'X-CSRF-Token': csrfToken }
        : {}),
    },
  })

  const fresh = res.headers.get('X-CSRF-Token')
  if (fresh) csrfToken = fresh

  return res
}
```

The token does **not** rotate per request. Rotating on every response would invalidate
the token an in-flight request from another tab is already carrying, so a session keeps
one token until the session itself is replaced. That is also why step 2 above is cheap:
most of the time the header you read back is the token you already had.

## Sending the token

Either transport works:

| Transport | Where | Used by |
|-----------|-------|---------|
| Header | `X-CSRF-Token: <token>` | `fetch`/XHR clients |
| Form field | `csrf_token=<token>` | classic and multipart form posts |

The header is checked first. For a multipart post the field is read from
`MultipartForm` before `ParseForm` is called, because `ParseForm` consumes a multipart
body without populating `PostForm`.

## What the server enforces

`CSRFMiddleware` exempts **GET, HEAD and TRACE** — the methods that are safe by
definition. Everything else needs a valid token.

`OPTIONS` is deliberately *not* exempt. It is answered with `204` inside the middleware
and never reaches the handler, because the router registers every route under `OPTIONS`
as well as its declared method: exempting `OPTIONS` would have made every mutating
handler reachable without a token, and it would have run.

There is also no "no session, no check" shortcut. A request arriving without a session
is precisely the shape a cross-site forgery has, so the absence of a session can never
excuse the check.

Rejections use the framework's standard response envelope:

```json
{
  "status": 403,
  "status_code": "FORBIDDEN",
  "body": null,
  "errors": ["CSRF token missing"]
}
```

| `errors[0]` | What went wrong |
|-------------|-----------------|
| `CSRF token missing` | No `X-CSRF-Token` header and no `csrf_token` field |
| `No active session` | The request carried no session — you are probably missing `credentials: 'include'` |
| `CSRF protection not initialized` | The session exists but holds no token; call `GET /csrf` |
| `Invalid CSRF token` | The token does not match the session's |

Token comparison is constant-time in both `CSRFMiddleware` and
`CsrfService.ValidateCSRFToken`.

## Server-side rendering

For a server-rendered form, read the token out of the session and put it in a hidden
field:

```gohtml
<form method="post" action="/widgets">
  <input type="hidden" name="csrf_token" value="{{.CsrfToken}}">
  ...
</form>
```

Supply `CsrfToken` from your handler via `CsrfService.GetCSRFToken`, which returns the
session's existing token or mints one if there is none yet.

## History

Through v1.x there was no way for a client to obtain a token at all:

- `X-CSRF-Token` was set on exactly one response per session — the one that created it.
  A reload, a second tab, or a session that already existed meant the client never saw
  it.
- `CsrfService.GetCSRFToken` existed and had zero call sites.
- No route handed a token out.

This was survivable only because the middleware had two bypasses: `OPTIONS` was exempt
(and the router registers every route under `OPTIONS`), and the check was skipped
entirely when the request carried no session. v2.0 closed both — which, without token
delivery, would have meant the hardened middleware reliably rejecting mutating requests
from any client that missed that single header.

v2.0 therefore also publishes the token on **every** session-carrying response and ships
the `/csrf` endpoint, registered by default.
