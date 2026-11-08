package web

import (
	"crypto/sha256"
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
	pending int
	resetAt time.Time
}

func newLimiter(max int, window time.Duration, now func() time.Time) *limiter {
	return &limiter{max: max, window: window, maxKeys: 100_000, now: now, entries: map[string]*entry{}}
}

// reserve counts in-flight requests so concurrent attempts cannot exceed a quota.
func (l *limiter) reserve(key string) *entry {
	digest := sha256.Sum256([]byte(key))
	key = string(digest[:])
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.entries[key]
	if !ok || !now.Before(e.resetAt) {
		if len(l.entries) >= l.maxKeys {
			l.purge(now)
		}
		if len(l.entries) >= l.maxKeys {
			return nil
		}
		e = &entry{resetAt: now.Add(l.window)}
		l.entries[key] = e
	}
	if e.count+e.pending >= l.max {
		return nil
	}
	e.pending++
	return e
}

// finish releases an admission and records a failure or clears prior failures.
func (l *limiter) finish(e *entry, failed, reset bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.pending--
	if reset {
		e.count = 0
	} else if failed {
		e.count++
	}
}

func (l *limiter) purge(now time.Time) {
	for k, e := range l.entries {
		if !now.Before(e.resetAt) {
			delete(l.entries, k)
		}
	}
}
