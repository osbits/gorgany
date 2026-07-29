# Rate limiting

Nothing in the framework rate-limited anything before v2.0. It shipped session auth, JWT
auth, OTP-capable user services and CSRF, and no way to stop a login or OTP endpoint
being brute-forced.

## Using it

Install it as a filter over the pattern you want to protect:

```go
routeProvider.AddMiddleware(
    http.NewMiddlewareConfigBuilder().
        WithPattern("/session/login").
        AsFilter().
        WithMiddleware(middleware.NewRateLimitMiddleware(middleware.PerMinute(5))).
        Build(),
)
```

`PerSecond(n)`, `PerMinute(n)` and `PerHour(n)` cover the common cases; `RateLimit{Burst,
Window}` is the general form.

The rate is a token bucket: `Burst` requests are allowed at once, and the bucket refills
at `Burst` per `Window`. So `PerMinute(10)` means a client may spend all ten immediately
after a quiet period and then gets one back every six seconds — which is usually what
"10 per minute" is meant to convey.

## What a client sees

Every response carries the client's budget:

```
X-RateLimit-Limit: 5
X-RateLimit-Remaining: 3
```

A rejected request gets `429` with the standard envelope (or plain text for a browser —
the same negotiation rule as every other framework error), plus:

```
Retry-After: 12
```

`Retry-After` is in whole seconds and is never `0`: rounding a 400ms wait down would tell
the client to retry immediately, straight back into the rejection.

## The bucket key

By default: **client IP plus route**. Exhausting the budget on the login endpoint leaves
the rest of the app usable.

Two details of the route half that matter:

- The method is part of the key, so `GET /widgets` and `POST /widgets` have separate
  budgets.
- Numeric and UUID-shaped path segments collapse to `*`, so `/widgets/1`, `/widgets/2`
  and `/widgets/3` share one bucket. Without that, walking ids would bypass the limit
  entirely.

  The chi route *pattern* would be the ideal key, but it is not reachable from the message
  scope. This is the closest approximation available.

To key on something else — a tenant, an API key, a username from the body — set
`KeyFunc`:

```go
limiter := middleware.NewRateLimitMiddleware(middleware.PerMinute(5))
limiter.KeyFunc = func(message core.HttpMessage) string {
    return limiter.ClientIP(message) + "|" + message.Request().Header().Get("X-Api-Key")
}
```

Keep the IP in the key unless you have a reason not to. Keying on a username alone lets
one attacker lock out every account by guessing against each in turn.

## Behind a proxy

`X-Forwarded-For` and `X-Real-IP` are **ignored by default**, and that default is the
security-relevant one: those headers are caller-supplied, so trusting them when the app is
not actually behind a proxy lets any client pick its own bucket and rotate through an
unlimited number of them — which is the same as not rate-limiting at all.

Turn it on only when a trusted proxy sets the header:

```go
limiter.TrustForwardedFor = true
```

The left-most `X-Forwarded-For` entry is taken as the client; the rest are proxies.

## Multiple instances

The default store, `MemoryRateLimitStore`, holds buckets in process memory. **Limits are
therefore per-instance**: an app behind three replicas allows three times the configured
rate.

That is a deliberate first move — there is no Redis dependency in this repo and adding one
for rate limiting is not worth it — but the seam is there for when it matters:

```go
type RateLimitStore interface {
    Allow(key string, rate RateLimit, now time.Time) (allowed bool, remaining int, retryAfter time.Duration)
}
```

`Allow` receives everything a shared backend needs to decide on its own. Set
`limiter.Store` to your implementation. It must be safe for concurrent use.

The in-memory store sweeps idle buckets periodically (`DefaultRateLimitSweepInterval`, 10
minutes). Without that the map is an unbounded, caller-controlled allocation — a slow
memory exhaustion reachable by anyone who can send requests from many addresses. A bucket
idle for two full windows has refilled completely, so discarding it is equivalent to
keeping it.

## Misconfiguration

`NewRateLimitMiddleware` panics on a `Burst` of 0 (which would reject every request) or a
`Window` of 0 (which would allow every one). Both are misconfigurations with no valid
reading, and boot is where a developer sees them.

A zero-value `RateLimitMiddleware{}` built by hand also panics on first use, rather than
silently allowing everything: a middleware installed to limit requests that limits nothing
is worse than no middleware at all.
