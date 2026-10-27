package netx

import (
	"context"
	"strings"
	"sync"
	"time"
)

// HostLimiter spaces out requests to the same host across every job running in
// this process, so two customers crawling one site cannot overload it together.
type HostLimiter struct {
	mu        sync.Mutex
	defaultIv time.Duration
	hosts     map[string]*hostSlot
	lastSweep time.Time
}

type hostSlot struct {
	next    time.Time
	blocked time.Time // set from Retry-After / 429 responses
}

// NewHostLimiter creates a limiter allowing ratePerSec requests per second per
// host by default. ratePerSec <= 0 disables the default limit.
func NewHostLimiter(ratePerSec float64) *HostLimiter {
	return &HostLimiter{defaultIv: rateToInterval(ratePerSec), hosts: map[string]*hostSlot{}}
}

func rateToInterval(ratePerSec float64) time.Duration {
	if ratePerSec <= 0 {
		return 0
	}
	return time.Duration(float64(time.Second) / ratePerSec)
}

// Wait blocks until a request to host may be sent. ratePerSec overrides the
// default rate when > 0 (a job may ask for a slower, never a faster, pace than
// the global default).
func (l *HostLimiter) Wait(ctx context.Context, host string, ratePerSec float64) error {
	if l == nil {
		return nil
	}
	iv := l.defaultIv
	if jobIv := rateToInterval(ratePerSec); jobIv > iv {
		iv = jobIv
	}
	host = strings.ToLower(host)

	l.mu.Lock()
	now := time.Now()
	l.sweepLocked(now)
	slot := l.hosts[host]
	if slot == nil {
		slot = &hostSlot{}
		l.hosts[host] = slot
	}
	at := now
	if slot.next.After(at) {
		at = slot.next
	}
	if slot.blocked.After(at) {
		at = slot.blocked
	}
	if iv == 0 && !slot.blocked.After(now) {
		l.mu.Unlock()
		return nil
	}
	slot.next = at.Add(iv)
	l.mu.Unlock()

	d := time.Until(at)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Penalize stops requests to host for d (used for 429 / Retry-After).
func (l *HostLimiter) Penalize(host string, d time.Duration) {
	if l == nil || d <= 0 {
		return
	}
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	host = strings.ToLower(host)
	l.mu.Lock()
	defer l.mu.Unlock()
	slot := l.hosts[host]
	if slot == nil {
		slot = &hostSlot{}
		l.hosts[host] = slot
	}
	until := time.Now().Add(d)
	if until.After(slot.blocked) {
		slot.blocked = until
	}
}

// sweepLocked drops idle hosts so the map does not grow without bound.
func (l *HostLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	for h, s := range l.hosts {
		if now.Sub(s.next) > time.Minute && now.After(s.blocked) {
			delete(l.hosts, h)
		}
	}
}
