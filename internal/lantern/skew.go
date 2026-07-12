package lantern

import (
	"fmt"
	"time"

	"github.com/christianmeichtry/photinus/internal/quorum"
)

// Skew is the one relational check: it measures clock drift between this
// lantern and its peers from the timestamps their flashes carry. It cannot
// live in internal/check because it produces one observation per peer, not
// one about a single target, and it needs the received flashes to do it.
//
// The estimate: a flash stamped S by the sender arrives at local time A, so
// A - S equals the true clock offset plus transit delay. Transit delay is
// always positive, so the minimum of A - S over a window of fresh flashes
// approaches the true offset. Gossip delay is milliseconds to seconds;
// the drift worth alerting on is seconds to minutes, so the noise floor is
// acceptable.
//
// Aggregation does the diagnosis. A peer whose clock is wrong is seen
// skewed by every lantern, and quorum agrees on that peer. A lantern whose
// own clock is wrong sees everyone skewed, convinces nobody, and is itself
// seen skewed by the whole swarm.
type peerClock struct {
	// offset is the smallest arrival-minus-stamp seen this window.
	offset time.Duration
	// window is when the current minimum started counting.
	window time.Time
	// sampled is when a fresh flash last updated the estimate.
	sampled time.Time
}

// skewWindow bounds how long a minimum is trusted before re-measuring, and
// how long a silent peer keeps a skew observation.
func (l *Lantern) skewWindow() time.Duration { return 15 * l.interval }

// observeClock feeds one fresh flash timestamp from a peer into the
// estimate. Callers hold l.mu.
func (l *Lantern) observeClock(peer string, stamped time.Time, now time.Time) {
	sample := now.Sub(stamped)
	c, ok := l.clocks[peer]
	if !ok {
		c = &peerClock{offset: sample, window: now}
		l.clocks[peer] = c
	}
	if now.Sub(c.window) > l.skewWindow() {
		c.offset = sample
		c.window = now
	} else if sample < c.offset {
		c.offset = sample
	}
	c.sampled = now
}

// skewObservations turns the current estimates into this lantern's own
// observations, one per recently heard peer. Callers hold l.mu.
func (l *Lantern) skewObservations(now time.Time) []quorum.Observation {
	if l.skewMax <= 0 {
		return nil
	}
	var obs []quorum.Observation
	for peer, c := range l.clocks {
		if now.Sub(c.sampled) > 4*l.skewWindow() {
			// The peer has been silent too long; stop measuring and forget.
			delete(l.clocks, peer)
			continue
		}
		if now.Sub(c.sampled) > l.skewWindow() {
			continue
		}
		off := c.offset
		abs := off
		if abs < 0 {
			abs = -abs
		}
		state := quorum.StateUp
		var detail string
		switch {
		case abs <= l.skewMax:
			detail = fmt.Sprintf("clock of %s is in step with mine, within %s", peer, l.skewMax)
		case off > 0:
			state = quorum.StateWarn
			detail = fmt.Sprintf("clock of %s runs about %s behind mine", peer, abs.Round(100*time.Millisecond))
		default:
			state = quorum.StateWarn
			detail = fmt.Sprintf("clock of %s runs about %s ahead of mine", peer, abs.Round(100*time.Millisecond))
		}
		obs = append(obs, quorum.Observation{
			Observer: l.id,
			Target:   peer,
			Check:    "skew",
			State:    state,
			Detail:   detail,
			Seen:     now,
		})
	}
	return obs
}
