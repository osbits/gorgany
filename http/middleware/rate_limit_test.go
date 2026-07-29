package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/osbits/gorgany/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// C1: the framework shipped session auth, JWT auth, OTP-capable user services and CSRF,
// and no way to stop a login or OTP endpoint being brute-forced.

// ------------------------------------------------------------------- helpers

// rateLimitMessage builds a message with a real RemoteAddr, since the default key is
// IP plus route.
func rateLimitMessage(method, target, remoteAddr string, headers map[string]string) *fakeMessage {
	raw := httptest.NewRequest(method, target, nil)
	raw.RemoteAddr = remoteAddr
	for k, v := range headers {
		raw.Header.Set(k, v)
	}

	recorded := &recordedResponse{}
	return &fakeMessage{
		req:      &fakeRequest{raw: raw, pathParams: map[string]string{}},
		res:      &fakeResponse{recorded: recorded},
		ctx:      raw.Context(),
		recorded: recorded,
	}
}

// drive sends n requests through the middleware and reports how many reached the handler.
func drive(mw *RateLimitMiddleware, n int, build func() *fakeMessage) (allowed int, last *fakeMessage) {
	handler := mw.Handle(func(core.HttpMessage) {})

	for i := 0; i < n; i++ {
		message := build()
		before := message.recorded.Written
		handler(message)
		if message.recorded.Written == before {
			allowed++
		}
		last = message
	}
	return allowed, last
}

// -------------------------------------------------------------- the basic limit

// TestABurstIsAllowedThenRejected is the headline: a fixed number through, the rest 429.
func TestABurstIsAllowedThenRejected(t *testing.T) {
	mw := NewRateLimitMiddleware(PerMinute(5))

	allowed, last := drive(mw, 8, func() *fakeMessage {
		return rateLimitMessage(http.MethodPost, "/session/login", "10.0.0.1:1234", nil)
	})

	assert.Equal(t, 5, allowed, "exactly Burst requests get through")
	assert.Equal(t, http.StatusTooManyRequests, last.recorded.Status)
}

// TestTheRejectionIsANegotiatedEnvelope, matching every other framework error path (C2).
func TestTheRejectionIsANegotiatedEnvelope(t *testing.T) {
	mw := NewRateLimitMiddleware(RateLimit{Burst: 1, Window: time.Minute})
	handler := mw.Handle(func(core.HttpMessage) {})

	build := func() *fakeMessage {
		return rateLimitMessage(http.MethodPost, "/api/v1/login", "10.0.0.2:1", map[string]string{
			"Accept": "application/json",
		})
	}

	handler(build())
	rejected := build()
	handler(rejected)

	require.Equal(t, http.StatusTooManyRequests, rejected.recorded.Status)

	raw, err := json.Marshal(rejected.recorded.Body)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))

	assert.Equal(t, float64(429), body["status"])
	assert.Equal(t, "TOO_MANY_REQUESTS", body["status_code"])
	assert.NotEmpty(t, body["errors"])
}

// TestTheRejectionIsTextForABrowser, same rule.
func TestTheRejectionIsTextForABrowser(t *testing.T) {
	mw := NewRateLimitMiddleware(RateLimit{Burst: 1, Window: time.Minute})
	handler := mw.Handle(func(core.HttpMessage) {})

	build := func() *fakeMessage {
		return rateLimitMessage(http.MethodPost, "/session/login", "10.0.0.3:1", map[string]string{
			"Accept": "text/html,*/*;q=0.8",
		})
	}

	handler(build())
	rejected := build()
	handler(rejected)

	assert.Equal(t, http.StatusTooManyRequests, rejected.recorded.Status)
	assert.Nil(t, rejected.recorded.Body)
	assert.NotEmpty(t, rejected.recorded.Text)
}

// TestRetryAfterIsNeverZero. Retry-After is in whole seconds, so rounding a 400ms wait
// down would tell the client to retry immediately, straight into the same rejection.
func TestRetryAfterIsNeverZero(t *testing.T) {
	mw := NewRateLimitMiddleware(PerSecond(1))
	handler := mw.Handle(func(core.HttpMessage) {})

	build := func() *fakeMessage {
		return rateLimitMessage(http.MethodGet, "/widgets", "10.0.0.4:1", nil)
	}

	handler(build())
	rejected := build()
	handler(rejected)

	retryAfter := rejected.recorded.Headers.Get(RetryAfterHeader)
	require.NotEmpty(t, retryAfter)

	seconds, err := strconv.Atoi(retryAfter)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, seconds, 1, "retry-after must never instruct an immediate retry")
}

// TestBudgetHeadersAreOnEveryResponse, so a well-behaved client can back off before
// being rejected.
func TestBudgetHeadersAreOnEveryResponse(t *testing.T) {
	mw := NewRateLimitMiddleware(PerMinute(3))
	handler := mw.Handle(func(core.HttpMessage) {})

	for expectedRemaining := 2; expectedRemaining >= 0; expectedRemaining-- {
		message := rateLimitMessage(http.MethodGet, "/widgets", "10.0.0.5:1", nil)
		handler(message)

		assert.Equal(t, "3", message.recorded.Headers.Get(RateLimitLimitHeader))
		assert.Equal(t, strconv.Itoa(expectedRemaining),
			message.recorded.Headers.Get(RateLimitRemainingHeader))
	}
}

// ------------------------------------------------------------------- the key

// TestDifferentIPsHaveIndependentBudgets. Sharing one bucket would let a single client
// lock out everyone else.
func TestDifferentIPsHaveIndependentBudgets(t *testing.T) {
	mw := NewRateLimitMiddleware(PerMinute(2))

	first, _ := drive(mw, 4, func() *fakeMessage {
		return rateLimitMessage(http.MethodPost, "/session/login", "10.0.0.6:1", nil)
	})
	second, _ := drive(mw, 4, func() *fakeMessage {
		return rateLimitMessage(http.MethodPost, "/session/login", "10.0.0.7:1", nil)
	})

	assert.Equal(t, 2, first)
	assert.Equal(t, 2, second, "a second client's budget is untouched by the first")
}

// TestDifferentRoutesHaveIndependentBudgets: exhausting the login endpoint must not lock
// a client out of the whole app.
func TestDifferentRoutesHaveIndependentBudgets(t *testing.T) {
	mw := NewRateLimitMiddleware(PerMinute(2))

	login, _ := drive(mw, 4, func() *fakeMessage {
		return rateLimitMessage(http.MethodPost, "/session/login", "10.0.0.8:1", nil)
	})
	widgets, _ := drive(mw, 4, func() *fakeMessage {
		return rateLimitMessage(http.MethodGet, "/widgets", "10.0.0.8:1", nil)
	})

	assert.Equal(t, 2, login)
	assert.Equal(t, 2, widgets)
}

// TestTheMethodIsPartOfTheKey: GET and POST on one path are different operations.
func TestTheMethodIsPartOfTheKey(t *testing.T) {
	mw := NewRateLimitMiddleware(PerMinute(1))

	get, _ := drive(mw, 2, func() *fakeMessage {
		return rateLimitMessage(http.MethodGet, "/widgets", "10.0.0.9:1", nil)
	})
	post, _ := drive(mw, 2, func() *fakeMessage {
		return rateLimitMessage(http.MethodPost, "/widgets", "10.0.0.9:1", nil)
	})

	assert.Equal(t, 1, get)
	assert.Equal(t, 1, post)
}

// TestIdentifierSegmentsCollapseToOneBucket. Keying on the raw path would give
// /widgets/1, /widgets/2, /widgets/3 their own budgets — so walking ids would bypass the
// limit entirely.
func TestIdentifierSegmentsCollapseToOneBucket(t *testing.T) {
	mw := NewRateLimitMiddleware(PerMinute(3))

	allowed := 0
	handler := mw.Handle(func(core.HttpMessage) {})
	for i := 1; i <= 6; i++ {
		message := rateLimitMessage(http.MethodGet, fmt.Sprintf("/widgets/%d", i), "10.0.1.1:1", nil)
		handler(message)
		if !message.recorded.Written {
			allowed++
		}
	}

	assert.Equal(t, 3, allowed, "a numeric path segment must not create a fresh bucket")
}

// TestUuidSegmentsCollapseToo, for an app keyed on UUIDs rather than integers.
func TestUuidSegmentsCollapseToo(t *testing.T) {
	uuids := []string{
		"3f2504e0-4f89-11d3-9a0c-0305e82c3301",
		"3f2504e0-4f89-11d3-9a0c-0305e82c3302",
		"3f2504e0-4f89-11d3-9a0c-0305e82c3303",
		"3f2504e0-4f89-11d3-9a0c-0305e82c3304",
	}

	mw := NewRateLimitMiddleware(PerMinute(2))
	handler := mw.Handle(func(core.HttpMessage) {})

	allowed := 0
	for _, id := range uuids {
		message := rateLimitMessage(http.MethodGet, "/widgets/"+id, "10.0.1.2:1", nil)
		handler(message)
		if !message.recorded.Written {
			allowed++
		}
	}

	assert.Equal(t, 2, allowed)
}

// TestThePortIsNotPartOfTheKey. RemoteAddr is host:port, and a fresh port per connection
// would give every request its own bucket — the same as no limit at all.
func TestThePortIsNotPartOfTheKey(t *testing.T) {
	mw := NewRateLimitMiddleware(PerMinute(2))
	handler := mw.Handle(func(core.HttpMessage) {})

	allowed := 0
	for port := 1000; port < 1006; port++ {
		message := rateLimitMessage(http.MethodPost, "/session/login",
			fmt.Sprintf("10.0.1.3:%d", port), nil)
		handler(message)
		if !message.recorded.Written {
			allowed++
		}
	}

	assert.Equal(t, 2, allowed)
}

// TestForwardedForIsIgnoredByDefault is the security-relevant default. The header is
// caller-supplied, so trusting it when the app is not behind a proxy lets any client pick
// its own bucket and rotate through an unlimited number of them.
func TestForwardedForIsIgnoredByDefault(t *testing.T) {
	mw := NewRateLimitMiddleware(PerMinute(2))
	handler := mw.Handle(func(core.HttpMessage) {})

	allowed := 0
	for i := 0; i < 6; i++ {
		message := rateLimitMessage(http.MethodPost, "/session/login", "10.0.1.4:1",
			map[string]string{"X-Forwarded-For": fmt.Sprintf("203.0.113.%d", i)})
		handler(message)
		if !message.recorded.Written {
			allowed++
		}
	}

	assert.Equal(t, 2, allowed, "a spoofed X-Forwarded-For must not mint a fresh bucket")
}

// TestForwardedForIsHonouredWhenTrusted, for an app actually behind a proxy.
func TestForwardedForIsHonouredWhenTrusted(t *testing.T) {
	mw := NewRateLimitMiddleware(PerMinute(2))
	mw.TrustForwardedFor = true
	handler := mw.Handle(func(core.HttpMessage) {})

	// Same RemoteAddr (the proxy), different real clients.
	for _, client := range []string{"203.0.113.1", "203.0.113.2"} {
		allowed := 0
		for i := 0; i < 4; i++ {
			message := rateLimitMessage(http.MethodPost, "/session/login", "10.0.1.5:1",
				map[string]string{"X-Forwarded-For": client})
			handler(message)
			if !message.recorded.Written {
				allowed++
			}
		}
		assert.Equalf(t, 2, allowed, "client %s", client)
	}
}

// TestTheLeftmostForwardedForEntryIsTheClient: the rest of the list is proxies.
func TestTheLeftmostForwardedForEntryIsTheClient(t *testing.T) {
	mw := NewRateLimitMiddleware(PerMinute(10))
	mw.TrustForwardedFor = true

	message := rateLimitMessage(http.MethodGet, "/widgets", "10.0.1.6:1", map[string]string{
		"X-Forwarded-For": "203.0.113.9, 10.1.1.1, 10.2.2.2",
	})

	assert.Equal(t, "203.0.113.9", mw.ClientIP(message))
}

// TestARealIpHeaderIsTheFallback, for a proxy that sets only that one.
func TestARealIpHeaderIsTheFallback(t *testing.T) {
	mw := NewRateLimitMiddleware(PerMinute(10))
	mw.TrustForwardedFor = true

	message := rateLimitMessage(http.MethodGet, "/widgets", "10.0.1.7:1", map[string]string{
		"X-Real-IP": "203.0.113.8",
	})

	assert.Equal(t, "203.0.113.8", mw.ClientIP(message))
}

// TestACustomKeyFuncTakesOver, for keying on a tenant or an API key.
func TestACustomKeyFuncTakesOver(t *testing.T) {
	mw := NewRateLimitMiddleware(PerMinute(2))
	mw.KeyFunc = func(message core.HttpMessage) string {
		return message.Request().Header().Get("X-Api-Key")
	}

	// Same IP and route, two different keys: two independent budgets.
	for _, key := range []string{"key-a", "key-b"} {
		allowed, _ := drive(mw, 4, func() *fakeMessage {
			return rateLimitMessage(http.MethodGet, "/widgets", "10.0.1.8:1",
				map[string]string{"X-Api-Key": key})
		})
		assert.Equalf(t, 2, allowed, "key %s", key)
	}
}

// ------------------------------------------------------------------- the store

// TestTokensRefillOverTime. The bucket has to recover, or the first burst would ban a
// client permanently.
func TestTokensRefillOverTime(t *testing.T) {
	store := NewMemoryRateLimitStore()
	rate := RateLimit{Burst: 2, Window: time.Second}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 2; i++ {
		allowed, _, _ := store.Allow("k", rate, start)
		require.True(t, allowed)
	}

	allowed, _, retryAfter := store.Allow("k", rate, start)
	require.False(t, allowed)
	assert.Greater(t, retryAfter, time.Duration(0))

	// Half a window buys back one token.
	allowed, _, _ = store.Allow("k", rate, start.Add(500*time.Millisecond))
	assert.True(t, allowed, "a partial window must refill proportionally")

	allowed, _, _ = store.Allow("k", rate, start.Add(500*time.Millisecond))
	assert.False(t, allowed, "but only one token's worth")
}

// TestAPartialRefillIsNotLostToTruncation. Integer tokens at 10-per-minute would mean no
// refill at all until six full seconds had passed, so a client polling every five seconds
// would be stuck at zero forever.
func TestAPartialRefillIsNotLostToTruncation(t *testing.T) {
	store := NewMemoryRateLimitStore()
	rate := PerMinute(10)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 10; i++ {
		allowed, _, _ := store.Allow("k", rate, now)
		require.True(t, allowed)
	}

	// Five one-second steps accumulate to more than a token even though no single step
	// is worth one.
	granted := false
	for i := 1; i <= 7; i++ {
		if allowed, _, _ := store.Allow("k", rate, now.Add(time.Duration(i)*time.Second)); allowed {
			granted = true
			break
		}
	}
	assert.True(t, granted, "fractional refill must accumulate across calls")
}

// TestTheBucketNeverExceedsTheBurst: an idle client must not bank unlimited credit.
func TestTheBucketNeverExceedsTheBurst(t *testing.T) {
	store := NewMemoryRateLimitStore()
	rate := PerMinute(3)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// One call to create the bucket, then a very long idle period.
	_, _, _ = store.Allow("k", rate, now)
	later := now.Add(24 * time.Hour)

	allowed := 0
	for i := 0; i < 10; i++ {
		if ok, _, _ := store.Allow("k", rate, later); ok {
			allowed++
		}
	}

	assert.Equal(t, 3, allowed, "an idle bucket refills to Burst, not beyond")
}

// TestIdleBucketsAreSwept. Without a sweep the map is an unbounded, caller-controlled
// allocation — a slow memory exhaustion reachable by anyone sending requests from many
// addresses.
func TestIdleBucketsAreSwept(t *testing.T) {
	store := NewMemoryRateLimitStore()
	store.sweepInterval = time.Minute
	rate := PerMinute(5)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 100; i++ {
		store.Allow(fmt.Sprintf("client-%d", i), rate, now)
	}
	require.Equal(t, 100, store.Len())

	// Well past both the sweep interval and two windows.
	store.Allow("fresh", rate, now.Add(time.Hour))

	assert.Equal(t, 1, store.Len(),
		"only the bucket touched after the sweep survives")
}

// TestAnActiveBucketSurvivesTheSweep: dropping a bucket mid-window would hand its owner a
// full allowance back.
func TestAnActiveBucketSurvivesTheSweep(t *testing.T) {
	store := NewMemoryRateLimitStore()
	store.sweepInterval = time.Second
	rate := PerHour(2)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.True(t, mustAllow(store, "active", rate, now))
	require.True(t, mustAllow(store, "active", rate, now.Add(time.Second)))

	// A sweep runs, but the bucket was touched a second ago and its window is an hour.
	store.Allow("other", rate, now.Add(2*time.Second))

	allowed, _, _ := store.Allow("active", rate, now.Add(3*time.Second))
	assert.False(t, allowed, "the exhausted bucket must not have been reset by the sweep")
}

func mustAllow(store *MemoryRateLimitStore, key string, rate RateLimit, now time.Time) bool {
	allowed, _, _ := store.Allow(key, rate, now)
	return allowed
}

// TestAnInvalidRateDeniesRatherThanAllows: an invalid rate must not become an open door.
func TestAnInvalidRateDeniesRatherThanAllows(t *testing.T) {
	store := NewMemoryRateLimitStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, rate := range []RateLimit{
		{Burst: 0, Window: time.Minute},
		{Burst: -1, Window: time.Minute},
		{Burst: 5, Window: 0},
		{Burst: 5, Window: -time.Minute},
		{},
	} {
		allowed, _, _ := store.Allow("k", rate, now)
		assert.Falsef(t, allowed, "rate %+v must deny", rate)
	}
}

// TestTheStoreIsConcurrencySafe. Every request goes through it, so a data race here is a
// crash under load.
func TestTheStoreIsConcurrencySafe(t *testing.T) {
	store := NewMemoryRateLimitStore()
	rate := PerMinute(1000)
	now := time.Now()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				store.Allow(fmt.Sprintf("client-%d", i%5), rate, now)
			}
		}(i)
	}
	wg.Wait()

	assert.LessOrEqual(t, store.Len(), 5)
}

// TestTheMiddlewareIsConcurrencySafe covers the lazy store initialisation too, which is a
// classic place for a race.
func TestTheMiddlewareIsConcurrencySafe(t *testing.T) {
	mw := NewRateLimitMiddleware(PerMinute(1000))
	handler := mw.Handle(func(core.HttpMessage) {})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			handler(rateLimitMessage(http.MethodGet, "/widgets", fmt.Sprintf("10.1.%d.1:1", i), nil))
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------- construction

// TestAnInvalidRateIsRefusedAtConstruction: a Burst of 0 would reject every request and a
// Window of 0 would allow every one. Both are misconfigurations with no valid reading.
func TestAnInvalidRateIsRefusedAtConstruction(t *testing.T) {
	for _, rate := range []RateLimit{
		{Burst: 0, Window: time.Minute},
		{Burst: 5, Window: 0},
		{},
	} {
		assert.Panicsf(t, func() { NewRateLimitMiddleware(rate) }, "rate %+v", rate)
	}
}

// TestAZeroValueMiddlewareFailsLoudly rather than silently allowing everything: a
// middleware installed to limit requests that limits nothing is worse than none.
func TestAZeroValueMiddlewareFailsLoudly(t *testing.T) {
	mw := &RateLimitMiddleware{}
	handler := mw.Handle(func(core.HttpMessage) {})

	assert.Panics(t, func() {
		handler(rateLimitMessage(http.MethodGet, "/widgets", "10.1.2.3:1", nil))
	})
}

// TestTheRateHelpersAreWhatTheySay.
func TestTheRateHelpersAreWhatTheySay(t *testing.T) {
	assert.Equal(t, RateLimit{Burst: 5, Window: time.Second}, PerSecond(5))
	assert.Equal(t, RateLimit{Burst: 5, Window: time.Minute}, PerMinute(5))
	assert.Equal(t, RateLimit{Burst: 5, Window: time.Hour}, PerHour(5))
}

// TestACustomStoreIsUsed, which is the seam a distributed backend plugs into.
func TestACustomStoreIsUsed(t *testing.T) {
	spy := &spyStore{}
	mw := NewRateLimitMiddleware(PerMinute(5))
	mw.Store = spy

	handler := mw.Handle(func(core.HttpMessage) {})
	handler(rateLimitMessage(http.MethodGet, "/widgets", "10.1.3.1:1", nil))

	assert.Equal(t, 1, spy.calls, "the configured store must be consulted")
	assert.NotEmpty(t, spy.lastKey)
}

type spyStore struct {
	calls   int
	lastKey string
}

func (s *spyStore) Allow(key string, rate RateLimit, now time.Time) (bool, int, time.Duration) {
	s.calls++
	s.lastKey = key
	return true, rate.Burst - 1, 0
}
