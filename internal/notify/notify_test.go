package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/christianmeichtry/photinus/internal/quorum"
)

func TestElect(t *testing.T) {
	swarm := []string{"l1", "l2", "l3", "l4", "l5"}

	t.Run("deterministic and order independent", func(t *testing.T) {
		a := Elect("tcp db:5432", swarm)
		b := Elect("tcp db:5432", []string{"l5", "l3", "l1", "l4", "l2"})
		if a == "" || a != b {
			t.Errorf("winner depends on list order: %q vs %q", a, b)
		}
	})

	t.Run("different alerts spread across lanterns", func(t *testing.T) {
		winners := make(map[string]bool)
		alerts := []string{"tcp a:1", "tcp b:2", "disk:/ l1", "memory l2", "swap l3", "cpu l4", "skew l5", "tcp c:3"}
		for _, a := range alerts {
			winners[Elect(a, swarm)] = true
		}
		if len(winners) < 2 {
			t.Errorf("all %d alerts elected the same lantern, hashing is not spreading", len(alerts))
		}
	})

	t.Run("dead winner falls to the next, others keep their alerts", func(t *testing.T) {
		alert := "tcp db:5432"
		first := Elect(alert, swarm)
		var without []string
		for _, id := range swarm {
			if id != first {
				without = append(without, id)
			}
		}
		second := Elect(alert, without)
		if second == first || second == "" {
			t.Errorf("winner after death = %q, want a different live lantern", second)
		}
		// An alert the dead lantern did not own keeps its winner.
		for _, a := range []string{"tcp a:1", "tcp b:2", "cpu l4", "memory l2"} {
			if w := Elect(a, swarm); w != first {
				if again := Elect(a, without); again != w {
					t.Errorf("alert %q changed winner from %q to %q though its winner never died", a, w, again)
				}
			}
		}
	})

	t.Run("empty alive list elects nobody", func(t *testing.T) {
		if w := Elect("tcp db:5432", nil); w != "" {
			t.Errorf("elected %q from an empty swarm", w)
		}
	})
}

func TestTracker(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	warmup := 10 * time.Second
	afterWarmup := now.Add(warmup)

	dec := func(state string, voters int) quorum.Decision {
		return quorum.Decision{Target: "db:5432", Check: "tcp", State: state, Detail: "cannot connect: connection refused",
			Votes: voters, Voters: voters, Needed: 2, SwarmSize: 3}
	}

	// newTracker returns a tracker for `self` that already saw one quiet
	// evaluation at t=0, so warmup ends at afterWarmup.
	newTracker := func(self string) (*Tracker, *[]Event) {
		var delivered []Event
		tr := New(self, warmup, func(e Event) { delivered = append(delivered, e) }, nil)
		tr.Observe(nil, nil, now)
		return tr, &delivered
	}

	alive := []string{"l1", "l2", "l3"}
	winner := Elect("tcp db:5432", alive)
	var loser string
	for _, id := range alive {
		if id != winner {
			loser = id
			break
		}
	}

	t.Run("the elected lantern sends, exactly once, and recovery clears", func(t *testing.T) {
		tr, delivered := newTracker(winner)
		sent := tr.Observe([]quorum.Decision{dec(quorum.StateDown, 3)}, alive, afterWarmup)
		if len(sent) != 1 || sent[0].Kind != "down" {
			t.Fatalf("sent = %+v, want one down event", sent)
		}
		// Still down: silence.
		for i := 1; i <= 3; i++ {
			if again := tr.Observe([]quorum.Decision{dec(quorum.StateDown, 3)}, alive, afterWarmup.Add(time.Duration(i)*time.Second)); len(again) != 0 {
				t.Fatalf("flash %d re-sent an ongoing outage: %+v", i, again)
			}
		}
		// Recovery after down fires once, as recovered.
		rec := tr.Observe([]quorum.Decision{dec(quorum.StateUp, 3)}, alive, afterWarmup.Add(10*time.Second))
		if len(rec) != 1 || rec[0].Kind != "recovered" {
			t.Fatalf("recovery sent %+v, want one recovered event", rec)
		}
		if len(*delivered) != 2 {
			t.Errorf("sender saw %d events, want 2", len(*delivered))
		}
	})

	t.Run("a warning is announced as a warning, and clears as cleared", func(t *testing.T) {
		tr, delivered := newTracker(winner)
		sent := tr.Observe([]quorum.Decision{dec(quorum.StateWarn, 3)}, alive, afterWarmup)
		if len(sent) != 1 || sent[0].Kind != "warning" {
			t.Fatalf("sent = %+v, want one warning event", sent)
		}
		if !strings.Contains(sent[0].Detail, "warns") || !strings.Contains(sent[0].Detail, "connection refused") {
			t.Errorf("warning sentence = %q, want the complaint in it", sent[0].Detail)
		}
		// Escalation to down pages again, as down. The transitions sit
		// outside the flap window on purpose: rapid bouncing is damping's
		// business and tested there.
		esc := tr.Observe([]quorum.Decision{dec(quorum.StateDown, 3)}, alive, afterWarmup.Add(11*time.Minute))
		if len(esc) != 1 || esc[0].Kind != "down" {
			t.Fatalf("escalation sent %+v, want one down event", esc)
		}
		// Back to merely warning.
		deesc := tr.Observe([]quorum.Decision{dec(quorum.StateWarn, 3)}, alive, afterWarmup.Add(22*time.Minute))
		if len(deesc) != 1 || deesc[0].Kind != "warning" {
			t.Fatalf("de-escalation sent %+v, want one warning event", deesc)
		}
		// All the way up: cleared, not recovered, since it was only warning.
		clr := tr.Observe([]quorum.Decision{dec(quorum.StateUp, 3)}, alive, afterWarmup.Add(33*time.Minute))
		if len(clr) != 1 || clr[0].Kind != "cleared" {
			t.Fatalf("clear sent %+v, want one cleared event", clr)
		}
		if len(*delivered) != 4 {
			t.Errorf("sender saw %d events, want 4", len(*delivered))
		}
	})

	t.Run("a lantern that lost the election stays silent", func(t *testing.T) {
		tr, delivered := newTracker(loser)
		tr.Observe([]quorum.Decision{dec(quorum.StateDown, 3)}, alive, afterWarmup)
		tr.Observe([]quorum.Decision{dec(quorum.StateUp, 3)}, alive, afterWarmup.Add(time.Second))
		if len(*delivered) != 0 {
			t.Errorf("losing lantern sent %+v", *delivered)
		}
	})

	t.Run("no events during warmup", func(t *testing.T) {
		tr, delivered := newTracker(winner)
		if sent := tr.Observe([]quorum.Decision{dec(quorum.StateDown, 3)}, alive, now.Add(warmup/2)); len(sent) != 0 {
			t.Errorf("warmup sent %+v", sent)
		}
		if len(*delivered) != 0 {
			t.Errorf("sender saw %d events during warmup", len(*delivered))
		}
	})

	t.Run("unknown is not recovered", func(t *testing.T) {
		tr, delivered := newTracker(winner)
		tr.Observe([]quorum.Decision{dec(quorum.StateDown, 3)}, alive, afterWarmup)
		// Every observation went stale: voters 0 says unknown, not up.
		if sent := tr.Observe([]quorum.Decision{dec(quorum.StateUp, 0)}, alive, afterWarmup.Add(time.Second)); len(sent) != 0 {
			t.Fatalf("stale subject produced %+v, want silence", sent)
		}
		// Fresh observations agree it is still down: no second page either.
		if sent := tr.Observe([]quorum.Decision{dec(quorum.StateDown, 3)}, alive, afterWarmup.Add(2*time.Second)); len(sent) != 0 {
			t.Fatalf("state survived the gap but re-paged: %+v", sent)
		}
		if len(*delivered) != 1 {
			t.Errorf("sender saw %d events, want exactly the first page", len(*delivered))
		}
	})

	t.Run("the next winner takes over when the elected lantern dies", func(t *testing.T) {
		// Run trackers for every other lantern; kill the winner mid-outage.
		var survivors []string
		for _, id := range alive {
			if id != winner {
				survivors = append(survivors, id)
			}
		}
		next := Elect("tcp db:5432", survivors)

		took := 0
		for _, self := range survivors {
			tr, delivered := newTracker(self)
			tr.Observe([]quorum.Decision{dec(quorum.StateDown, 3)}, alive, afterWarmup)
			tr.Observe([]quorum.Decision{dec(quorum.StateDown, 2)}, survivors, afterWarmup.Add(5*time.Second))
			if len(*delivered) > 0 {
				took += len(*delivered)
				if self != next {
					t.Errorf("lantern %s took over though %s is the next winner", self, next)
				}
			}
		}
		if took != 1 {
			t.Errorf("takeover produced %d pages, want exactly 1", took)
		}
	})

	t.Run("membership growth does not re-page an ongoing outage", func(t *testing.T) {
		tr, delivered := newTracker(winner)
		tr.Observe([]quorum.Decision{dec(quorum.StateDown, 3)}, alive, afterWarmup)
		grown := append([]string{"l0", "l4", "l5"}, alive...)
		if sent := tr.Observe([]quorum.Decision{dec(quorum.StateDown, 3)}, grown, afterWarmup.Add(time.Second)); len(sent) != 0 {
			t.Errorf("new lanterns joining re-paged: %+v", sent)
		}
		if len(*delivered) != 1 {
			t.Errorf("sender saw %d events, want 1", len(*delivered))
		}
	})

	t.Run("authority warnings carry the host's own words", func(t *testing.T) {
		d := quorum.Decision{Target: "l1", Check: "swap", State: quorum.StateWarn,
			Detail: "swap is 84% used, 4.2 GiB of 5.0 GiB, threshold is 80%",
			Votes:  1, Voters: 1, Needed: 1, SwarmSize: 3, Authority: true}
		self := Elect("swap l1", alive)
		tr, delivered := newTracker(self)
		tr.Observe([]quorum.Decision{d}, alive, afterWarmup)
		if len(*delivered) != 1 {
			t.Fatalf("sender saw %d events, want 1", len(*delivered))
		}
		got := (*delivered)[0]
		if got.Kind != "warning" {
			t.Errorf("kind = %q, want warning", got.Kind)
		}
		if !strings.Contains(got.Detail, "swap on l1 warns") || !strings.Contains(got.Detail, "84% used") {
			t.Errorf("sentence = %q, want the host's own words in it", got.Detail)
		}
	})
}

func TestFlapDamping(t *testing.T) {
	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	alive := []string{"l1", "l2", "l3"}
	winner := Elect("tcp db:5432", alive)

	dec := func(state string) quorum.Decision {
		return quorum.Decision{Target: "db:5432", Check: "tcp", State: state, Votes: 3, Voters: 3, Needed: 2, SwarmSize: 3}
	}
	newTracker := func() (*Tracker, *[]Event) {
		var delivered []Event
		tr := New(winner, 0, func(e Event) { delivered = append(delivered, e) }, nil)
		tr.Observe(nil, nil, base.Add(-time.Hour))
		return tr, &delivered
	}

	t.Run("a storm collapses into one flapping page and one settled page", func(t *testing.T) {
		tr, delivered := newTracker()
		// Bounce every 30 seconds, Bespin style.
		state, last := quorum.StateDown, quorum.StateDown
		when := base
		for i := 0; i < 12; i++ {
			tr.Observe([]quorum.Decision{dec(state)}, alive, when)
			last = state
			if state == quorum.StateDown {
				state = quorum.StateUp
			} else {
				state = quorum.StateDown
			}
			when = when.Add(30 * time.Second)
		}
		kinds := []string{}
		for _, e := range *delivered {
			kinds = append(kinds, e.Kind)
		}
		// First bounces page normally (down, recovered, down), the fourth
		// transition inside the window trips the damper.
		flaps := 0
		for _, k := range kinds {
			if k == "flapping" {
				flaps++
			}
		}
		if flaps != 1 {
			t.Fatalf("kinds = %v, want exactly one flapping page", kinds)
		}
		if len(kinds) > 4 {
			t.Fatalf("kinds = %v: the storm was not damped", kinds)
		}
		// Now hold the last state long enough to settle.
		settled := when.Add(settleAfter + time.Minute)
		tr.Observe([]quorum.Decision{dec(last)}, alive, settled)
		kinds = nil
		for _, e := range *delivered {
			kinds = append(kinds, e.Kind)
		}
		if kinds[len(kinds)-1] != "settled" {
			t.Fatalf("kinds = %v, want a settled page at the end", kinds)
		}
	})

	t.Run("a fresh outage after settling pages normally again", func(t *testing.T) {
		tr, delivered := newTracker()
		when := base
		state := quorum.StateDown
		for i := 0; i < 6; i++ {
			tr.Observe([]quorum.Decision{dec(state)}, alive, when)
			if state == quorum.StateDown {
				state = quorum.StateUp
			} else {
				state = quorum.StateDown
			}
			when = when.Add(30 * time.Second)
		}
		// settle at up
		tr.Observe([]quorum.Decision{dec(quorum.StateUp)}, alive, when.Add(settleAfter+time.Minute))
		before := len(*delivered)
		// a real outage hours later must page immediately
		late := when.Add(2 * time.Hour)
		tr.Observe([]quorum.Decision{dec(quorum.StateDown)}, alive, late)
		if len(*delivered) != before+1 || (*delivered)[len(*delivered)-1].Kind != "down" {
			t.Fatalf("real outage after settling did not page: %+v", (*delivered)[before:])
		}
	})
}
