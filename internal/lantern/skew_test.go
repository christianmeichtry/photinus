package lantern

import (
	"encoding/json"
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

// TestSkewReadsTheSendTimeNotTheObservations pins the fix for a real fleet
// incident: relighting a lantern made the whole swarm report its clock as
// 33 hours adrift, because the first word heard back from it was a pulse
// receipt stamped with the job's ping time from the previous Monday.
//
// Farewell clears what is known about a departed peer's clock, so on its
// return any observation could open the measurement window, and an
// observation's timestamp is when the thing was seen, not when it was sent.
// The send time now rides the envelope and is the only thing measured.
func TestSkewReadsTheSendTimeNotTheObservations(t *testing.T) {
	now := time.Now().UTC()

	flash := func(from string, sent time.Time, obs []quorum.Observation) []byte {
		env := envelope{V: flashV, From: from, Obs: obs}
		if !sent.IsZero() {
			env.Sent = &sent
		}
		payload, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshalling the flash: %v", err)
		}
		return payload
	}
	// The receipt from the incident: ewok's weekly cert cron pinged days ago,
	// and the fact deliberately outlives its receiver's farewell.
	staleReceipt := func(observer string) []quorum.Observation {
		return []quorum.Observation{{
			Observer: observer, Target: "ewok-certs", Check: "pulse",
			State: quorum.StateUp, Detail: "pulsed at " + now.Add(-33*time.Hour).Format(time.RFC3339),
			Seen: now.Add(-33 * time.Hour), TTL: 3600,
		}}
	}
	skewOf := func(l *Lantern, peer string) *quorum.Observation {
		l.mu.Lock()
		defer l.mu.Unlock()
		obs := l.skewObservations(time.Now().UTC())
		for i := range obs {
			if obs[i].Target == peer {
				return &obs[i]
			}
		}
		return nil
	}

	t.Run("a stale receipt from a returning lantern is not a clock sample", func(t *testing.T) {
		l := New(Config{ID: "scarif", Interval: 2 * time.Second, SkewMax: 5 * time.Second})
		l.forget("ewok") // the graceful farewell, which clears the clock table
		l.ReceiveFlash(flash("ewok", time.Now().UTC(), staleReceipt("ewok")))
		o := skewOf(l, "ewok")
		if o == nil {
			t.Fatal("no skew observation about a lantern that just flashed")
		}
		if o.State != quorum.StateUp {
			t.Errorf("a returning lantern's stale receipt reported its clock as %s: %q", o.State, o.Detail)
		}
	})

	t.Run("a paced check observed minutes ago is not a clock sample", func(t *testing.T) {
		l := New(Config{ID: "scarif", Interval: 2 * time.Second, SkewMax: 5 * time.Second})
		old := []quorum.Observation{{
			Observer: "drongar", Target: "https://photinus.dev", Check: "http",
			State: quorum.StateUp, Detail: "200 OK in 42ms", Seen: now.Add(-5 * time.Minute), TTL: 1500,
		}}
		l.ReceiveFlash(flash("drongar", time.Now().UTC(), old))
		o := skewOf(l, "drongar")
		if o == nil {
			t.Fatal("no skew observation about a lantern that just flashed")
		}
		if o.State != quorum.StateUp {
			t.Errorf("a five-minute-old http verdict reported the sender's clock as %s: %q", o.State, o.Detail)
		}
	})

	t.Run("a genuinely wrong clock is still caught", func(t *testing.T) {
		l := New(Config{ID: "scarif", Interval: 2 * time.Second, SkewMax: 5 * time.Second})
		l.ReceiveFlash(flash("jawa", time.Now().UTC().Add(-90*time.Second), nil))
		o := skewOf(l, "jawa")
		if o == nil {
			t.Fatal("no skew observation about a lantern that just flashed")
		}
		if o.State != quorum.StateWarn {
			t.Errorf("a 90 second offset read as %s: %q", o.State, o.Detail)
		}
	})

	t.Run("a lantern too old to send its clock is left unmeasured, not guessed", func(t *testing.T) {
		l := New(Config{ID: "scarif", Interval: 2 * time.Second, SkewMax: 5 * time.Second})
		l.ReceiveFlash(flash("", time.Time{}, staleReceipt("ewok")))
		if o := skewOf(l, "ewok"); o != nil {
			t.Errorf("an envelope without a send time produced a skew verdict anyway: %s %q", o.State, o.Detail)
		}
	})

	t.Run("a lantern never measures its own clock", func(t *testing.T) {
		l := New(Config{ID: "scarif", Interval: 2 * time.Second, SkewMax: 5 * time.Second})
		l.ReceiveFlash(flash("scarif", time.Now().UTC().Add(-90*time.Second), nil))
		if o := skewOf(l, "scarif"); o != nil {
			t.Errorf("a lantern measured itself: %s %q", o.State, o.Detail)
		}
	})
}
