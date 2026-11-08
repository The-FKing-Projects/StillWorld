package web

import (
	"sync"
	"time"
)

// limiter is a fixed-window counter keyed by string. It bounds memory by
// discarding expired entries whenever it grows past maxKeys.
type limiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	maxKeys int
	now     func() time.Time
	entries map[string]*entry
}

type entry struct {
	count   int
	resetAt time.Time
}

func newLimiter(max int, window time.Duration, now func() time.Time) *limiter {
	return &limiter{max: max, window: window, maxKeys: 100_000, now: now, entries: map[string]*entry{}}
}

// blocked reports whether key has already used up its allowance.
func (l *limiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok || !l.now().Before(e.resetAt) {
		return false
	}
	return e.count >= l.max
}

// add records one event for key.
func (l *limiter) add(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.entries[key]
	if !ok || !now.Before(e.resetAt) {
		if len(l.entries) >= l.maxKeys {
			l.purge(now)
		}
		if len(l.entries) >= l.maxKeys {
			// Still full of live entries: refuse to grow, which fails closed
			// only for keys already tracked, never for the whole server.
			return
		}
		e = &entry{resetAt: now.Add(l.window)}
		l.entries[key] = e
	}
	e.count++
}

// reset forgets key, for example after a successful login.
func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

func (l *limiter) purge(now time.Time) {
	for k, e := range l.entries {
		if !now.Before(e.resetAt) {
			delete(l.entries, k)
		}
	}
}
