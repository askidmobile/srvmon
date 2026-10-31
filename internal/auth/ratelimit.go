package auth

import (
	"sync"
	"time"
)

// Limiter blocks an IP after too many failed login attempts within a window.
// It complements fail2ban on the host: fail2ban bans at the firewall,
// the limiter protects even when fail2ban is not installed.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
	now    func() time.Time
}

// NewLimiter allows max failures per window per key.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, fails: map[string][]time.Time{}, now: time.Now}
}

// Blocked reports whether key is currently blocked and for how long.
func (l *Limiter) Blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.prune(key)
	if len(f) < l.max {
		return false, 0
	}
	return true, f[0].Add(l.window).Sub(l.now())
}

// Reserve atomically counts an attempt before it is checked, so concurrent
// requests cannot all slip past the limit while slow hashing runs. It returns
// false (and the remaining block time) when the key is already blocked. Call
// Release when the attempt turns out to be successful.
func (l *Limiter) Reserve(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.prune(key)
	if len(f) >= l.max {
		return false, f[0].Add(l.window).Sub(l.now())
	}
	l.fails[key] = append(f, l.now())
	return true, 0
}

// Release takes back the most recent reserved attempt.
func (l *Limiter) Release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.fails[key]
	if len(f) <= 1 {
		delete(l.fails, key)
		return
	}
	l.fails[key] = f[:len(f)-1]
}

// Fail records a failed attempt.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key] = append(l.prune(key), l.now())
}

// Reset clears failures after a successful login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

func (l *Limiter) prune(key string) []time.Time {
	f := l.fails[key]
	cut := l.now().Add(-l.window)
	i := 0
	for i < len(f) && f[i].Before(cut) {
		i++
	}
	f = f[i:]
	if len(f) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = f
	return f
}
