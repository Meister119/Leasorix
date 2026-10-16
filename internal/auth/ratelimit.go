package auth

import (
	"sync"
	"time"
)

// Limiter counts failures per key in a sliding window (login rate limit).
type Limiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	now    func() time.Time
	fails  map[string][]time.Time
}

// NewLimiter allows at most max failures per key within window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{window: window, max: max, now: time.Now, fails: map[string][]time.Time{}}
}

func (l *Limiter) prune(key string, now time.Time) []time.Time {
	ts := l.fails[key]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= l.window {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = ts
	}
	return ts
}

// Blocked reports whether key exceeded the limit and how long until the
// next attempt is allowed.
func (l *Limiter) Blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	ts := l.prune(key, now)
	if len(ts) < l.max {
		return false, 0
	}
	return true, l.window - now.Sub(ts[0])
}

// Fail records a failure for key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(key, now)
	l.fails[key] = append(l.fails[key], now)
	// Bound memory against key spraying.
	if len(l.fails) > 100000 {
		for k := range l.fails {
			delete(l.fails, k)
			if len(l.fails) <= 50000 {
				break
			}
		}
	}
}

// Reset clears the failures of key.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}
