package lantern

import (
	"fmt"
	"time"

	"github.com/christianmeichtry/photinus/internal/quorum"
)

// The pulse check is a dead man's switch. A job (a backup cron, a certbot
// timer) pings any lantern over HTTP when it finishes; the ping becomes an
// up observation that rides the next flash, so the whole swarm learns the
// newest pulse time. When a declared pulse stays silent past its window,
// every lantern that declares it emits its own down observation, and quorum
// decides as usual.
//
// Silence must produce votes, not mere absence. An absent observation is
// unknown, and unknown never alarms; only an explicit down can. And it must
// be one vote per lantern, not one verdict: a single box with a wrong clock
// would see silence where there is none, and quorum is what keeps that box
// from paging alone.
//
// Rule 4 deliberately does not apply. The target is a job name, not a
// lantern id, so no observer is ever the authority on it and normal quorum
// runs. Do not name a pulse after a lantern; the authority rule would then
// mistake the receipt for the host's own word.

// defaultPulseTTL keeps a receipt for a pulse this lantern does not declare
// alive for 25 hours, the default window, so operators can ping first and
// declare after.
const defaultPulseTTL = 90000

// Pulse records one ping for the named pulse at time now. The receipt is
// stored as this lantern's own up observation and rides the next flash. It
// reports whether the pulse is declared on this lantern; an undeclared
// pulse is still recorded, but only declared lanterns evaluate its silence.
func (l *Lantern) Pulse(name string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	window, declared := l.pulses[name]
	ttl := defaultPulseTTL
	if declared && window >= time.Second {
		ttl = int(window / time.Second)
	}
	o := quorum.Observation{
		Observer: l.id,
		Target:   name,
		Check:    "pulse",
		State:    quorum.StateUp,
		Detail:   fmt.Sprintf("pulsed at %s", now.UTC().Format(time.RFC3339)),
		Seen:     now,
		TTL:      ttl,
	}
	l.store[storeKey(o)] = o
	if now.After(l.lastPulse[name]) {
		l.lastPulse[name] = now
	}
	return declared
}

// pulseObservations turns silence into down votes, one per declared pulse
// past its window. The baseline is the newest pulse heard from anywhere in
// the swarm, or this lantern's start when nothing has pulsed yet, so a
// fresh boot does not page about a job that simply has not come due.
// Callers hold l.mu.
func (l *Lantern) pulseObservations(now time.Time) []quorum.Observation {
	var obs []quorum.Observation
	for name, window := range l.pulses {
		baseline := l.start
		if t, ok := l.lastPulse[name]; ok && t.After(baseline) {
			baseline = t
		}
		age := now.Sub(baseline)
		if age <= window {
			continue
		}
		obs = append(obs, quorum.Observation{
			Observer: l.id,
			Target:   name,
			Check:    "pulse",
			State:    quorum.StateDown,
			Detail:   fmt.Sprintf("no pulse from %s for %s, window is %s", name, humanAge(age), window),
			Seen:     now,
		})
	}
	return obs
}

// humanAge rounds a duration to what an operator would say out loud: whole
// minutes past an hour, whole seconds past a minute.
func humanAge(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return d.Round(time.Minute).String()
	case d >= time.Minute:
		return d.Round(time.Second).String()
	}
	return d.Round(time.Millisecond).String()
}
