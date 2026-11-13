package web

import (
	"container/heap"
	"crypto/sha256"
	"sync"
	"time"
)

// limiter is a fixed-window counter with bounded storage.
type limiter struct {
	mu          sync.Mutex
	max         int
	window      time.Duration
	maxKeys     int
	now         func() time.Time
	entries     map[string]*entry
	expirations expirationHeap
}

type entry struct {
	count   int
	pending int
	resetAt time.Time
	key     string
	index   int
}

type expirationHeap []*entry

func (h expirationHeap) Len() int           { return len(h) }
func (h expirationHeap) Less(i, j int) bool { return h[i].resetAt.Before(h[j].resetAt) }
func (h expirationHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *expirationHeap) Push(x any) {
	e := x.(*entry)
	e.index = len(*h)
	*h = append(*h, e)
}
func (h *expirationHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	old[len(old)-1] = nil
	e.index = -1
	*h = old[:len(old)-1]
	return e
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
	if ok && !now.Before(e.resetAt) {
		heap.Remove(&l.expirations, e.index)
		delete(l.entries, key)
		ok = false
	}
	if !ok {
		if len(l.entries) >= l.maxKeys {
			l.purge(now)
		}
		if len(l.entries) >= l.maxKeys {
			return nil
		}
		e = &entry{resetAt: now.Add(l.window), key: key}
		l.entries[key] = e
		heap.Push(&l.expirations, e)
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

func (l *limiter) finishCounted(e *entry) {
	l.finish(e, true, false)
}

func (l *limiter) purge(now time.Time) {
	for len(l.expirations) > 0 && !now.Before(l.expirations[0].resetAt) {
		e := heap.Pop(&l.expirations).(*entry)
		delete(l.entries, e.key)
	}
}
