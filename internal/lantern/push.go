package lantern

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/christianmeichtry/photinus/internal/notify"
)

// A phone registers for pushes at any door; the registration then belongs
// to the whole swarm, because the lantern elected to send a page is rarely
// the one the phone happened to reach. Registrations ride their own small
// envelope on every flash and the anti-entropy sync, merge newest-first
// like observations, and age out when the phone stops renewing.

// RegisterPush records one phone's device token. Re-registering the same
// token just refreshes its clock.
func (l *Lantern) RegisterPush(token, env string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cur, ok := l.pushRegs[token]; !ok || now.After(cur.Seen) {
		l.pushRegs[token] = notify.PushRegistration{Token: token, Env: env, Seen: now}
	}
}

// PushRegistrations answers the current tokens, stably ordered. The APNs
// sender calls this at page time, so a token learned seconds ago pages.
func (l *Lantern) PushRegistrations() []notify.PushRegistration {
	l.mu.Lock()
	defer l.mu.Unlock()
	regs := make([]notify.PushRegistration, 0, len(l.pushRegs))
	for _, r := range l.pushRegs {
		regs = append(regs, r)
	}
	sort.Slice(regs, func(i, j int) bool { return regs[i].Token < regs[j].Token })
	return regs
}

// mergePush folds a peer's registrations in, newest word per token
// winning. Callers hold l.mu.
func (l *Lantern) mergePush(regs []notify.PushRegistration) {
	for _, r := range regs {
		if r.Token == "" {
			continue
		}
		if cur, ok := l.pushRegs[r.Token]; !ok || r.Seen.After(cur.Seen) {
			l.pushRegs[r.Token] = r
		}
	}
}

// pushPayload wraps the registrations in their own envelope, or nil when
// there are none. Registrations are a few dozen bytes and phones are few;
// if a fleet of phones ever outgrows the packet, the log will say so.
func (l *Lantern) pushPayload() []byte {
	l.mu.Lock()
	regs := make([]notify.PushRegistration, 0, len(l.pushRegs))
	for _, r := range l.pushRegs {
		regs = append(regs, r)
	}
	l.mu.Unlock()
	if len(regs) == 0 {
		return nil
	}
	sort.Slice(regs, func(i, j int) bool { return regs[i].Token < regs[j].Token })
	payload, err := json.Marshal(envelope{V: flashV, Push: regs})
	if err != nil {
		return nil
	}
	if len(payload) > 1000 {
		l.log.Printf("push registrations outgrew a gossip packet (%d bytes, %d phones) and were not gossiped", len(payload), len(regs))
		return nil
	}
	return payload
}

// prunePush forgets registrations no phone has renewed within PushTTL.
// Callers hold l.mu.
func (l *Lantern) prunePush(now time.Time) {
	for token, r := range l.pushRegs {
		if now.Sub(r.Seen) > notify.PushTTL {
			delete(l.pushRegs, token)
		}
	}
}
