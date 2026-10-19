package api

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// limiter is a per-client token bucket for /api/ requests.
type limiter struct {
	rate    float64 // tokens per second
	burst   float64
	trusted []netip.Prefix

	mu      sync.Mutex
	buckets map[netip.Addr]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(perMinute, burst int, trusted []netip.Prefix) *limiter {
	return &limiter{
		rate: float64(perMinute) / 60, burst: float64(burst), trusted: trusted,
		buckets: map[netip.Addr]*bucket{},
	}
}

// ParsePrefixes reads a comma-separated list of CIDRs or bare addresses.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		if !strings.Contains(f, "/") {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return nil, err
			}
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// clientIP uses the last X-Forwarded-For hop only when the direct peer is a
// trusted proxy; otherwise the peer address itself.
func (l *limiter) clientIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	peer = peer.Unmap()
	if !l.isTrusted(peer) {
		return peer
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if a = a.Unmap(); !l.isTrusted(a) {
			return a
		}
	}
	return peer
}

func (l *limiter) isTrusted(a netip.Addr) bool {
	for _, p := range l.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// allow takes a token for addr. It returns how long to wait when empty.
func (l *limiter) allow(addr netip.Addr, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.swept) > time.Minute {
		full := time.Duration(l.burst / l.rate * float64(time.Second))
		for a, b := range l.buckets {
			if now.Sub(b.last) > full {
				delete(l.buckets, a)
			}
		}
		l.swept = now
	}
	b, ok := l.buckets[addr]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[addr] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

func (l *limiter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if ok, wait := l.allow(l.clientIP(r), time.Now()); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// secure adds headers suitable for a public read-only site.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
