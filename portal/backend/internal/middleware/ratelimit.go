package middleware

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimitConfig sets the per-user token-bucket. RPS <= 0 disables limiting
// entirely (Wrap becomes a pass-through), which is the local-PoC default.
type RateLimitConfig struct {
	RPS   float64
	Burst int
}

// RateLimiter throttles mutating requests per authenticated user (keyed by the
// email recorded via SetUser). It is single-instance state — adequate for the
// hardened single-instance deployment; a shared store (Redis/Vault) would be the
// scale-out replacement. Idle limiters are evicted so the map can't grow without
// bound.
type RateLimiter struct {
	cfg     RateLimitConfig
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

const rlIdleTTL = 10 * time.Minute

// NewRateLimiter returns a limiter for cfg.
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	return &RateLimiter{cfg: cfg, buckets: map[string]*bucket{}}
}

// Wrap applies the limit to next. With limiting disabled it returns next
// unchanged. Requests without a recorded user fall back to a shared "anonymous"
// bucket (these are pre-auth, so this is just a backstop).
func (rl *RateLimiter) Wrap(next http.Handler) http.Handler {
	if rl.cfg.RPS <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := userOf(r.Context())
		if key == "" {
			key = "anonymous"
		}
		if !rl.limiterFor(key).Allow() {
			w.Header().Set("Retry-After", "1")
			writeJSONError(w, r, http.StatusTooManyRequests, "rate limit exceeded, slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) limiterFor(key string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	if b, ok := rl.buckets[key]; ok {
		b.seen = now
		return b.lim
	}
	rl.evictLocked(now)
	b := &bucket{lim: rate.NewLimiter(rate.Limit(rl.cfg.RPS), rl.cfg.Burst), seen: now}
	rl.buckets[key] = b
	return b.lim
}

// evictLocked drops buckets idle longer than rlIdleTTL. Called under rl.mu while
// adding a new key, so cleanup amortizes over creation and needs no goroutine.
func (rl *RateLimiter) evictLocked(now time.Time) {
	for k, b := range rl.buckets {
		if now.Sub(b.seen) > rlIdleTTL {
			delete(rl.buckets, k)
		}
	}
}
