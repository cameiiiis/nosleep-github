package app

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const attemptWindow = 15 * time.Minute

type loginWindow struct {
	count   int
	pending int
	until   time.Time
}
type rateLimiter struct {
	mu                           sync.Mutex
	accounts, ips, registrations map[string]loginWindow
	cleanupAt                    time.Time
}

func requestIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
func (l *rateLimiter) prepare(now time.Time) {
	if l.accounts == nil {
		l.accounts = map[string]loginWindow{}
		l.ips = map[string]loginWindow{}
		l.registrations = map[string]loginWindow{}
	}
	if !now.Before(l.cleanupAt) {
		for _, m := range []map[string]loginWindow{l.accounts, l.ips, l.registrations} {
			for key, v := range m {
				if !now.Before(v.until) && v.pending == 0 {
					delete(m, key)
				}
			}
		}
		l.cleanupAt = now.Add(time.Minute)
	}
}
func underLimit(m map[string]loginWindow, key string, limit, capacity int, now time.Time) bool {
	v, exists := m[key]
	if !now.Before(v.until) {
		v.count = 0
	}
	return (!exists && len(m) < capacity) || (exists && v.count+v.pending < limit)
}
func addAttempt(m map[string]loginWindow, key string, capacity int, now time.Time) {
	v, exists := m[key]
	if !exists && len(m) >= capacity {
		return
	}
	if !now.Before(v.until) {
		v = loginWindow{until: now.Add(attemptWindow), pending: v.pending}
	}
	v.count++
	m[key] = v
}
func (l *rateLimiter) allowLogin(email, ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prepare(now)
	key := hashToken(email)
	if !underLimit(l.accounts, key, 5, 4096, now) || !underLimit(l.ips, ip, 100, 1024, now) {
		return false
	}
	reserveAttempt(l.accounts, key, now)
	reserveAttempt(l.ips, ip, now)
	return true
}
func reserveAttempt(m map[string]loginWindow, key string, now time.Time) {
	v := m[key]
	if !now.Before(v.until) {
		v.count = 0
		v.until = now.Add(attemptWindow)
	}
	v.pending++
	m[key] = v
}
func releaseAttempt(m map[string]loginWindow, key string, clearFailures bool) {
	v := m[key]
	if v.pending > 0 {
		v.pending--
	}
	if clearFailures {
		v.count = 0
	}
	if v.count == 0 && v.pending == 0 {
		delete(m, key)
	} else {
		m[key] = v
	}
}
func (l *rateLimiter) loginResult(email, ip string, failed, succeeded bool, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prepare(now)
	if failed {
		addAttempt(l.accounts, hashToken(email), 4096, now)
		addAttempt(l.ips, ip, 1024, now)
	}
	releaseAttempt(l.accounts, hashToken(email), succeeded)
	releaseAttempt(l.ips, ip, false)
}
func (l *rateLimiter) allowRegistration(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prepare(now)
	if !underLimit(l.registrations, ip, 20, 1024, now) {
		return false
	}
	addAttempt(l.registrations, ip, 1024, now)
	return true
}
