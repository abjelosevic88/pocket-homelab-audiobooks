package server

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type shareAttempts struct {
	mu      sync.Mutex
	entries map[string]shareAttempt
}
type shareAttempt struct {
	start time.Time
	count int
}

// shareAttemptsMax bounds the addresses remembered at once.
const shareAttemptsMax = 4096

func (a *shareAttempts) permit(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if a.entries == nil {
		a.entries = map[string]shareAttempt{}
	}
	v, known := a.entries[key]
	if !known && len(a.entries) >= shareAttemptsMax {
		// Full: drop expired windows, then the oldest, so a flood of addresses
		// costs its own entries rather than locking everyone else out.
		oldest, oldestAt := "", now
		for k, e := range a.entries {
			if now.Sub(e.start) >= time.Minute {
				delete(a.entries, k)
			} else if e.start.Before(oldestAt) {
				oldest, oldestAt = k, e.start
			}
		}
		if len(a.entries) >= shareAttemptsMax && oldest != "" {
			delete(a.entries, oldest)
		}
	}
	if now.Sub(v.start) >= time.Minute {
		v = shareAttempt{start: now}
	}
	if v.count >= 12 {
		return false
	}
	v.count++
	a.entries[key] = v
	return true
}
func (a *shareAttempts) reset(key string) { a.mu.Lock(); delete(a.entries, key); a.mu.Unlock() }
func sharePeer(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

var sharePasswordSlots = make(chan struct{}, 4)
