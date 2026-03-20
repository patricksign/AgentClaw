package api

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ─── Rate Limiter ─────────────────────────────────────────────────────────────
//
// Per-IP token bucket: 10 tokens, refilled at 10 tokens/minute.
// Each request consumes one token; requests that find the bucket empty are
// rejected with HTTP 429. The /ws path is excluded (WebSocket connections
// manage their own lifetime).
//
// Implementation is stdlib-only — no external dependencies.

const (
	rateLimitMax      = 10              // burst capacity (tokens)
	rateLimitInterval = time.Minute     // refill window
	cleanupInterval   = 5 * time.Minute // how often idle buckets are removed
	bucketIdleTTL     = 10 * time.Minute
)

// bucket holds the token count and last-seen timestamp for one IP.
type bucket struct {
	mu       sync.Mutex
	tokens   int
	lastSeen time.Time
}

// rateLimiter tracks per-IP buckets and periodically purges idle ones.
// Uses RWMutex: allow() acquires read-lock for existing IPs (hot path),
// write-lock only when creating a new bucket (cold path).
type rateLimiter struct {
	mu       sync.RWMutex
	buckets  map[string]*bucket
	stop     chan struct{}
	stopOnce sync.Once
}

func newRateLimiter() *rateLimiter {
	rl := &rateLimiter{
		buckets: make(map[string]*bucket),
		stop:    make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// allow returns true if the request from ip is within the rate limit.
// Hot path uses RLock for existing IPs; only promotes to Lock for new IPs.
func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.RLock()
	b, ok := rl.buckets[ip]
	rl.mu.RUnlock()

	if !ok {
		rl.mu.Lock()
		// Double-check after acquiring write lock.
		b, ok = rl.buckets[ip]
		if !ok {
			b = &bucket{tokens: rateLimitMax, lastSeen: time.Now()}
			rl.buckets[ip] = b
		}
		rl.mu.Unlock()
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastSeen)

	// Refill tokens proportional to elapsed time using integer arithmetic that
	// preserves the fractional remainder. Using elapsed*max/interval avoids
	// truncation-to-zero for sub-minute gaps (unlike elapsed/interval*max).
	refill := int(elapsed * time.Duration(rateLimitMax) / rateLimitInterval)
	if refill > 0 {
		b.tokens += refill
		if b.tokens > rateLimitMax {
			b.tokens = rateLimitMax
		}
		// Advance lastSeen by the consumed interval to preserve the fractional
		// remainder rather than discarding it by snapping to now.
		b.lastSeen = b.lastSeen.Add(time.Duration(refill) * rateLimitInterval / time.Duration(rateLimitMax))
	}

	if b.tokens == 0 {
		return false
	}
	b.tokens--
	return true
}

// cleanup removes buckets that have been idle for bucketIdleTTL.
// It snapshots candidate IPs under rl.mu, then checks lastSeen without
// holding the global lock to avoid stalling all allow() calls.
func (rl *rateLimiter) cleanup() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stop:
			return
		case <-ticker.C:
			// Collect a snapshot under read lock — does not block concurrent allow() calls.
			rl.mu.RLock()
			snapshot := make(map[string]*bucket, len(rl.buckets))
			for ip, b := range rl.buckets {
				snapshot[ip] = b
			}
			rl.mu.RUnlock()

			// Determine idle IPs without holding the global lock.
			var idle []string
			for ip, b := range snapshot {
				b.mu.Lock()
				isIdle := time.Since(b.lastSeen) > bucketIdleTTL
				b.mu.Unlock()
				if isIdle {
					idle = append(idle, ip)
				}
			}

			// Remove idle entries under the global lock.
			if len(idle) > 0 {
				rl.mu.Lock()
				for _, ip := range idle {
					delete(rl.buckets, ip)
				}
				rl.mu.Unlock()
			}
		}
	}
}

// Stop shuts down the background cleanup goroutine. Safe to call multiple times.
func (rl *rateLimiter) Stop() {
	rl.stopOnce.Do(func() { close(rl.stop) })
}

// ─── Middleware ───────────────────────────────────────────────────────────────

// withRateLimit wraps handler with per-IP rate limiting.
// Paths matching any entry in skip are passed through without rate limiting.
func withRateLimit(rl *rateLimiter, skip []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, prefix := range skip {
			if r.URL.Path == prefix {
				next.ServeHTTP(w, r)
				return
			}
		}

		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}

		if !rl.allow(ip) {
			log.Warn().Str("ip", ip).Str("path", r.URL.Path).Msg("rate limit exceeded")
			w.Header().Set("Retry-After", "60")
			errJSON(w, http.StatusTooManyRequests, "rate limit exceeded: 10 requests per minute")
			return
		}

		next.ServeHTTP(w, r)
	})
}
