package lantern

import (
	"testing"
	"time"

	"github.com/christianmeichtry/photinus/internal/quorum"
)

func TestSkew(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	newLantern := func(skewMax time.Duration) *Lantern {
		return New(Config{ID: "l1", Interval: 2 * time.Second, SkewMax: skewMax})
	}
	find := func(obs []quorum.Observation, peer string) *quorum.Observation {
		for i := range obs {
			if obs[i].Target == peer {
				return &obs[i]
			}
		}
		return nil
	}

	t.Run("gossip delay does not look like skew, the minimum wins", func(t *testing.T) {
		l := newLantern(5 * time.Second)
		// Three flashes from l2 whose clock is in step: arrival lags the
		// stamp only by transit delay of varying size.
		l.observeClock("l2", now.Add(-3*time.Second), now)                                        // relayed late, +3s
		l.observeClock("l2", now.Add(2*time.Second-80*time.Millisecond), now.Add(2*time.Second))  // +80ms
		l.observeClock("l2", now.Add(4*time.Second-900*time.Millisecond), now.Add(4*time.Second)) // +900ms
		obs := l.skewObservations(now.Add(4 * time.Second))
		o := find(obs, "l2")
		if o == nil {
			t.Fatal("no skew observation about l2")
		}
		if o.State != quorum.StateUp {
			t.Errorf("l2 reported %s (%s), want up: minimum sample was 80ms", o.State, o.Detail)
		}
	})

	t.Run("a peer clock far behind trips the check", func(t *testing.T) {
		l := newLantern(5 * time.Second)
		// l2 stamps its flashes 40s in the past: its clock runs behind.
		l.observeClock("l2", now.Add(-40*time.Second), now)
		l.observeClock("l2", now.Add(2*time.Second-40*time.Second), now.Add(2*time.Second))
		obs := l.skewObservations(now.Add(2 * time.Second))
		o := find(obs, "l2")
		if o == nil {
			t.Fatal("no skew observation about l2")
		}
		if o.State == quorum.StateUp {
			t.Errorf("l2 reported up (%s), want a warning of about 40s", o.Detail)
		}
	})

	t.Run("a peer clock ahead trips the check too", func(t *testing.T) {
		l := newLantern(5 * time.Second)
		l.observeClock("l2", now.Add(30*time.Second), now)
		obs := l.skewObservations(now)
		o := find(obs, "l2")
		if o == nil {
			t.Fatal("no skew observation about l2")
		}
		if o.State == quorum.StateUp {
			t.Errorf("l2 reported up (%s), want a warning: stamps from the future", o.Detail)
		}
	})

	t.Run("a silent peer stops being measured, then is forgotten", func(t *testing.T) {
		l := newLantern(5 * time.Second)
		l.observeClock("l2", now, now)
		quiet := now.Add(l.skewWindow() + time.Second)
		if obs := l.skewObservations(quiet); find(obs, "l2") != nil {
			t.Error("still emitting skew about a peer silent past the window")
		}
		gone := now.Add(4*l.skewWindow() + time.Second)
		l.skewObservations(gone)
		if _, ok := l.clocks["l2"]; ok {
			t.Error("silent peer was never pruned from the clock table")
		}
	})

	t.Run("disabled skew emits nothing", func(t *testing.T) {
		l := newLantern(0)
		l.observeClock("l2", now.Add(-40*time.Second), now)
		if obs := l.skewObservations(now); len(obs) != 0 {
			t.Errorf("skew disabled but %d observations emitted", len(obs))
		}
	})

	t.Run("observations carry the right shape for quorum", func(t *testing.T) {
		l := newLantern(5 * time.Second)
		l.observeClock("l2", now.Add(-40*time.Second), now)
		obs := l.skewObservations(now)
		o := find(obs, "l2")
		if o == nil {
			t.Fatal("no skew observation about l2")
		}
		if o.Observer != "l1" || o.Check != "skew" {
			t.Errorf("observation is %s/%s about %s, want l1/skew about l2", o.Observer, o.Check, o.Target)
		}
		if o.Observer == o.Target {
			t.Error("skew observation must never look authoritative")
		}
	})
}
