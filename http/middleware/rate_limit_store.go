package middleware

import (
	"sync"
	"time"
)

// MemoryRateLimitStore is a token-bucket store held in process memory.
//
// It is the framework's default, and the right first move: there is no Redis dependency
// in this repo and adding one for rate limiting is not worth it. The limits it applies
// are per-instance, so an app behind N replicas allows N times the configured rate —
// implement RateLimitStore against a shared backend when that matters.
//
// Buckets are created on demand and swept periodically, so a long-running server does
// not accumulate one entry per IP that has ever connected. Without the sweep the map is
// an unbounded, caller-controlled allocation — a slow memory exhaustion reachable by
// anyone who can send requests from many addresses.
type MemoryRateLimitStore struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket

	// lastSweep is when idle buckets were last discarded.
	lastSweep time.Time
	// sweepInterval is how often to sweep. Zero means the default.
	sweepInterval time.Duration
}

// tokenBucket is one key's allowance.
//
// tokens is fractional so a partial refill is not lost to truncation: at 10 per minute,
// integer tokens would mean no refill at all until six full seconds had passed, and a
// client polling every five seconds would be permanently stuck at zero.
type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
	lastSeen   time.Time
}

// DefaultRateLimitSweepInterval is how often MemoryRateLimitStore discards idle buckets.
const DefaultRateLimitSweepInterval = 10 * time.Minute

func NewMemoryRateLimitStore() *MemoryRateLimitStore {
	return &MemoryRateLimitStore{buckets: make(map[string]*tokenBucket)}
}

var _ RateLimitStore = (*MemoryRateLimitStore)(nil)

// Allow consumes one token for key if one is available.
func (s *MemoryRateLimitStore) Allow(key string, rate RateLimit, now time.Time) (bool, int, time.Duration) {
	if err := rate.Validate(); err != nil {
		// A store cannot report this, and the middleware validates before calling. Deny
		// rather than allow: an invalid rate must not become an open door.
		return false, 0, rate.Window
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.buckets == nil {
		s.buckets = make(map[string]*tokenBucket)
	}

	s.sweepLocked(now, rate.Window)

	bucket, exists := s.buckets[key]
	if !exists {
		bucket = &tokenBucket{tokens: float64(rate.Burst), lastRefill: now}
		s.buckets[key] = bucket
	}

	// Refill for the elapsed time, capped at the bucket size.
	refillPerSecond := float64(rate.Burst) / rate.Window.Seconds()
	if elapsed := now.Sub(bucket.lastRefill); elapsed > 0 {
		bucket.tokens += elapsed.Seconds() * refillPerSecond
		if bucket.tokens > float64(rate.Burst) {
			bucket.tokens = float64(rate.Burst)
		}
		bucket.lastRefill = now
	}
	bucket.lastSeen = now

	if bucket.tokens < 1 {
		// How long until one whole token exists.
		missing := 1 - bucket.tokens
		retryAfter := time.Duration(missing / refillPerSecond * float64(time.Second))
		return false, 0, retryAfter
	}

	bucket.tokens--
	return true, int(bucket.tokens), 0
}

// sweepLocked discards buckets nothing has touched for long enough that they are
// certainly full, and so indistinguishable from a fresh one.
//
// The caller must hold the mutex.
func (s *MemoryRateLimitStore) sweepLocked(now time.Time, window time.Duration) {
	interval := s.sweepInterval
	if interval <= 0 {
		interval = DefaultRateLimitSweepInterval
	}

	if !s.lastSweep.IsZero() && now.Sub(s.lastSweep) < interval {
		return
	}
	s.lastSweep = now

	// A bucket idle for a full window has refilled completely, so dropping it is
	// equivalent to keeping it — and keeping it is what grows the map without bound.
	// Two windows, so a client just inside its window is never handed a fresh bucket
	// because of clock granularity.
	idleCutoff := now.Add(-2 * window)
	for key, bucket := range s.buckets {
		if bucket.lastSeen.Before(idleCutoff) {
			delete(s.buckets, key)
		}
	}
}

// Len reports how many buckets are held. Exported for tests and for an app that wants to
// gauge the store's footprint.
func (s *MemoryRateLimitStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buckets)
}

// Reset discards every bucket. Useful between tests.
func (s *MemoryRateLimitStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buckets = make(map[string]*tokenBucket)
	s.lastSweep = time.Time{}
}
