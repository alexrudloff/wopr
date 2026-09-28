package subagent

import (
	"context"
	"sync"
)

// Limiter bounds concurrent subagents overall and per provider, so parallel
// briefs never pile onto a single-slot local server.
type Limiter struct {
	mu          sync.Mutex
	max         int
	perProvider map[string]int
	running     int
	byProvider  map[string]int
	changed     chan struct{}
}

// NewLimiter allows limit concurrent tasks (at least 1) and perProvider[p]
// concurrent tasks on provider p; providers not listed are bounded by limit.
func NewLimiter(limit int, perProvider map[string]int) *Limiter {
	return &Limiter{max: max(1, limit), perProvider: perProvider, byProvider: map[string]int{}, changed: make(chan struct{})}
}

// Saturated reports whether provider has no free slot.
func (l *Limiter) Saturated(provider string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	limit, ok := l.perProvider[provider]
	return ok && l.byProvider[provider] >= limit
}

// Acquire waits for a slot on provider and returns its release function.
func (l *Limiter) Acquire(ctx context.Context, provider string) (func(), error) {
	for {
		l.mu.Lock()
		limit, limited := l.perProvider[provider]
		if l.running < l.max && (!limited || l.byProvider[provider] < limit) {
			l.running++
			l.byProvider[provider]++
			l.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { l.release(provider) }) }, nil
		}
		wait := l.changed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

func (l *Limiter) release(provider string) {
	l.mu.Lock()
	l.running--
	l.byProvider[provider]--
	close(l.changed)
	l.changed = make(chan struct{})
	l.mu.Unlock()
}
