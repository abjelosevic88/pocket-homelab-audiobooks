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

func (a *shareAttempts) permit(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if a.entries == nil {
		a.entries = map[string]shareAttempt{}
	}
	if len(a.entries) >= 4096 {
		for k, v := range a.entries {
			if now.Sub(v.start) >= time.Minute {
				delete(a.entries, k)
			}
		}
		if len(a.entries) >= 4096 {
			return false
		}
	}
	v := a.entries[key]
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
