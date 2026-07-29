package middleware

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/osbits/gorgany/app/core"
	grghttp "github.com/osbits/gorgany/http"
	"github.com/osbits/gorgany/service/dto"
)

// RateLimitStore is the seam a distributed backend plugs into.
//
// The framework ships one implementation, MemoryRateLimitStore, which is the right
// first move: there is no Redis dependency in this repo and adding one for this is not
// worth it. But a token bucket in process memory is per-instance, so an app behind three
// replicas gets three times the configured rate — which is why the store is an interface
// rather than a struct field, and why Allow carries everything a shared backend needs to
// make the decision on its own.
//
// An implementation must be safe for concurrent use.
type RateLimitStore interface {
	// Allow reports whether one token is available for key under the given rate, and
	// consumes it if so. retryAfter is how long the caller should wait before the next
	// attempt, and is only meaningful when allowed is false.
	Allow(key string, rate RateLimit, now time.Time) (allowed bool, remaining int, retryAfter time.Duration)
}

// RateLimit is a token-bucket rate: Burst tokens, refilled at Burst per Window.
//
// Burst is both the bucket size and the refill amount, which is what makes the rate
// readable as "10 requests per minute" while still letting a client spend all ten at
// once after a quiet period.
type RateLimit struct {
	// Burst is the maximum number of requests in a window. Must be positive.
	Burst int
	// Window is the period over which Burst requests are allowed. Must be positive.
	Window time.Duration
}

// Validate rejects a rate that would let everything through or nothing at all.
func (r RateLimit) Validate() error {
	if r.Burst <= 0 {
		return fmt.Errorf("rate limit: Burst must be positive, got %d", r.Burst)
	}
	if r.Window <= 0 {
		return fmt.Errorf("rate limit: Window must be positive, got %s", r.Window)
	}
	return nil
}

// PerMinute is a rate of n requests per minute.
func PerMinute(n int) RateLimit {
	return RateLimit{Burst: n, Window: time.Minute}
}

// PerSecond is a rate of n requests per second.
func PerSecond(n int) RateLimit {
	return RateLimit{Burst: n, Window: time.Second}
}

// PerHour is a rate of n requests per hour.
func PerHour(n int) RateLimit {
	return RateLimit{Burst: n, Window: time.Hour}
}

// RateLimitMiddleware limits how often one client may call the routes it covers.
//
// Nothing in the framework rate-limited anything before v2.0: it shipped session auth,
// JWT auth, OTP-capable user services and CSRF, and no way to stop a login or OTP
// endpoint being brute-forced. This is the missing piece, not a complete answer — see
// RateLimitStore on what a multi-instance deployment needs.
//
// Use it as a filter over a pattern, the same way as any other middleware:
//
//	http.NewMiddlewareConfigBuilder().
//	    WithPattern("/session/login").
//	    AsFilter().
//	    WithMiddleware(middleware.NewRateLimitMiddleware(middleware.PerMinute(5))).
//	    Build()
//
// The key is IP plus route by default, so a client exhausting its budget on the login
// endpoint can still use the rest of the app. Set KeyFunc to key on something else — a
// username from the body, an API key header, a tenant id.
type RateLimitMiddleware struct {
	// Rate is the limit applied to each key.
	Rate RateLimit

	// Store holds the buckets. Nil means a MemoryRateLimitStore created on first use.
	Store RateLimitStore

	// KeyFunc derives the bucket key from the request. Nil means IP plus route.
	KeyFunc func(core.HttpMessage) string

	// TrustForwardedFor makes the client IP come from X-Forwarded-For or X-Real-IP.
	//
	// Off by default, and that default matters: those headers are caller-supplied, so
	// trusting them when the app is *not* behind a proxy lets any client pick its own
	// bucket and rotate through an unlimited number of them — which is the same as not
	// rate-limiting at all. Turn it on only when a trusted proxy sets the header.
	TrustForwardedFor bool

	once          sync.Once
	resolvedStore RateLimitStore
}

// NewRateLimitMiddleware creates a rate limiter at the given rate.
//
// It panics on an invalid rate. A Burst of 0 would reject every request and a Window of
// 0 would allow every one; both are misconfigurations with no valid reading, and boot is
// where a developer sees them.
func NewRateLimitMiddleware(rate RateLimit) *RateLimitMiddleware {
	if err := rate.Validate(); err != nil {
		panic(err)
	}
	return &RateLimitMiddleware{Rate: rate}
}

var _ core.IMiddleware = (*RateLimitMiddleware)(nil)

// Rate-limit response headers, as the de-facto convention has them.
const (
	RateLimitLimitHeader     = "X-RateLimit-Limit"
	RateLimitRemainingHeader = "X-RateLimit-Remaining"
	RateLimitResetHeader     = "X-RateLimit-Reset"
	RetryAfterHeader         = "Retry-After"
)

// TooManyRequestsHttpStatus is the status a rejected request gets.
var TooManyRequestsHttpStatus = core.HttpStatus{Status: http.StatusTooManyRequests, Code: "TOO_MANY_REQUESTS"}

func (thiz *RateLimitMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		rate := thiz.Rate
		if err := rate.Validate(); err != nil {
			// A zero-value middleware constructed by hand rather than through
			// NewRateLimitMiddleware. Refusing every request would be worse than
			// saying so, and letting every request through silently would defeat the
			// point of installing it.
			panic(err)
		}

		key := thiz.key(message)
		allowed, remaining, retryAfter := thiz.store().Allow(key, rate, time.Now())

		thiz.setHeaders(message, rate, remaining, retryAfter)

		if !allowed {
			thiz.reject(message, retryAfter)
			return
		}

		next(message)
	}
}

// store returns the configured store, defaulting to an in-memory one.
func (thiz *RateLimitMiddleware) store() RateLimitStore {
	thiz.once.Do(func() {
		if thiz.Store != nil {
			thiz.resolvedStore = thiz.Store
			return
		}
		thiz.resolvedStore = NewMemoryRateLimitStore()
	})
	return thiz.resolvedStore
}

// key derives the bucket key for this request.
func (thiz *RateLimitMiddleware) key(message core.HttpMessage) string {
	if thiz.KeyFunc != nil {
		return thiz.KeyFunc(message)
	}

	return thiz.ClientIP(message) + "\x00" + routeKey(message)
}

// ClientIP resolves the address to key on.
//
// Exported because a custom KeyFunc almost always wants it — keying on a username alone
// lets one attacker lock out every account by guessing against each in turn.
func (thiz *RateLimitMiddleware) ClientIP(message core.HttpMessage) string {
	req := message.Request()
	if req == nil {
		return "unknown"
	}

	if thiz.TrustForwardedFor {
		if header := req.Header(); header != nil {
			// The left-most entry is the original client; the rest are proxies.
			if forwarded := header.Get("X-Forwarded-For"); forwarded != "" {
				if first, _, found := strings.Cut(forwarded, ","); found {
					return strings.TrimSpace(first)
				}
				return strings.TrimSpace(forwarded)
			}
			if real := strings.TrimSpace(header.Get("X-Real-IP")); real != "" {
				return real
			}
		}
	}

	raw := req.RawRequest()
	if raw == nil || raw.RemoteAddr == "" {
		return "unknown"
	}

	// RemoteAddr is host:port. Keying on it with the port would give every connection
	// its own bucket, which is the same as no limit at all.
	if host, _, err := net.SplitHostPort(raw.RemoteAddr); err == nil {
		return host
	}
	return raw.RemoteAddr
}

// routeKey identifies the route, so exhausting the budget on one endpoint does not lock
// a client out of the whole app.
//
// The chi route *pattern* would be the ideal key — /widgets/{id} rather than
// /widgets/17 — but it is not reachable from the message scope, and using the raw path
// would give every id its own bucket. Method plus path with numeric and UUID-shaped
// segments collapsed is the closest approximation available here.
func routeKey(message core.HttpMessage) string {
	req := message.Request()
	if req == nil {
		return "unknown"
	}

	raw := req.RawRequest()
	if raw == nil || raw.URL == nil {
		return "unknown"
	}

	segments := strings.Split(raw.URL.Path, "/")
	for i, segment := range segments {
		if looksLikeAnIdentifier(segment) {
			segments[i] = "*"
		}
	}

	return raw.Method + " " + strings.Join(segments, "/")
}

// looksLikeAnIdentifier reports whether a path segment is probably a parameter value
// rather than a fixed part of the route.
func looksLikeAnIdentifier(segment string) bool {
	if segment == "" {
		return false
	}

	if _, err := strconv.Atoi(segment); err == nil {
		return true
	}

	// A UUID, or anything else long and hex-with-dashes.
	if len(segment) >= 32 {
		for _, r := range segment {
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex && r != '-' {
				return false
			}
		}
		return true
	}

	return false
}

// setHeaders publishes the client's budget, so a well-behaved client can back off before
// being rejected.
func (thiz *RateLimitMiddleware) setHeaders(message core.HttpMessage, rate RateLimit, remaining int, retryAfter time.Duration) {
	response := message.Response()
	if response == nil {
		return
	}

	header := response.Header()
	if header == nil {
		return
	}

	header.Set(RateLimitLimitHeader, strconv.Itoa(rate.Burst))
	header.Set(RateLimitRemainingHeader, strconv.Itoa(remaining))
	if retryAfter > 0 {
		header.Set(RateLimitResetHeader, strconv.Itoa(int(secondsCeil(retryAfter))))
	}
}

func (thiz *RateLimitMiddleware) reject(message core.HttpMessage, retryAfter time.Duration) {
	seconds := secondsCeil(retryAfter)

	if response := message.Response(); response != nil {
		if header := response.Header(); header != nil {
			header.Set(RetryAfterHeader, strconv.FormatInt(seconds, 10))
		}
	}

	reason := fmt.Sprintf("Too many requests. Retry in %d second(s).", seconds)

	if grghttp.WantsJSON(message) {
		message.Response().JSON(
			dto.ReturnObject(nil, TooManyRequestsHttpStatus, reason),
			TooManyRequestsHttpStatus.Status)
		return
	}

	message.Response().Text(reason, TooManyRequestsHttpStatus.Status)
}

// secondsCeil rounds a duration up to whole seconds, with a floor of 1.
//
// Retry-After is expressed in seconds, so rounding *down* a 400ms wait would produce
// "retry in 0 seconds" — an instruction to retry immediately, into the same rejection.
func secondsCeil(d time.Duration) int64 {
	if d <= 0 {
		return 1
	}
	seconds := int64(d / time.Second)
	if d%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}
	return seconds
}
