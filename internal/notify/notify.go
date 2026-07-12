// Package notify sends the one notification the swarm owes the operator.
// Every lantern detects the same outages, so the problem is not sending a
// page, it is sending exactly one. The election is deterministic hashing:
// every lantern computes the same winner from the same facts, so there is
// no election protocol to run and nothing to break when a lantern dies.
package notify

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"slices"
	"time"

	"github.com/christianmeichtry/photinus/internal/quorum"
)

// Event is one state change worth telling an operator about.
type Event struct {
	// Kind is "down" or "recovered".
	Kind   string
	Check  string
	Target string
	// Detail is a plain sentence for a human.
	Detail string
}

// Sender delivers one event to the operator. It must not block; a slow
// transport should do its waiting in a goroutine.
type Sender func(Event)

// Elect picks the lantern that sends the notification for one alert, by
// rendezvous hashing: every alive lantern gets a score from hashing its ID
// together with the alert, and the highest score wins. Everyone computes
// the same winner with no coordination, and when the winner is dead it is
// simply absent from the alive list, so the next score takes over by the
// same arithmetic.
func Elect(alertID string, alive []string) string {
	var winner string
	var best uint64
	for _, id := range alive {
		h := sha256.Sum256([]byte(id + "\x00" + alertID))
		score := binary.BigEndian.Uint64(h[:8])
		if winner == "" || score > best || (score == best && id > winner) {
			winner, best = id, score
		}
	}
	return winner
}

// Tracker turns the stream of quorum decisions into events, firing only on
// state changes and only when this lantern wins the election for the alert.
type Tracker struct {
	self   string
	warmup time.Duration
	send   Sender
	log    *log.Logger

	start   time.Time
	down    map[string]bool   // subject -> currently considered down
	elected map[string]string // subject -> lantern elected to page while down
}

// New builds a tracker. The warmup is how long after the first observation
// the tracker stays quiet: a lantern that just started is alone in a swarm
// of one and would otherwise reach quorum with only its own vote.
func New(self string, warmup time.Duration, send Sender, logger *log.Logger) *Tracker {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Tracker{
		self:    self,
		warmup:  warmup,
		send:    send,
		log:     logger,
		down:    make(map[string]bool),
		elected: make(map[string]string),
	}
}

// Observe takes the decisions computed after a flash and the lanterns
// currently believed alive. It returns the events this lantern actually
// sent, which is empty on every lantern except the elected one.
//
// A subject nobody has a live observation about is unknown, and unknown is
// not recovered: the state simply holds until somebody sees something.
func (t *Tracker) Observe(decisions []quorum.Decision, alive []string, now time.Time) []Event {
	if t.start.IsZero() {
		t.start = now
	}
	if now.Sub(t.start) < t.warmup {
		return nil
	}

	var sent []Event
	for _, d := range decisions {
		subject := d.Check + " " + d.Target
		wasDown := t.down[subject]
		switch {
		case d.Voters == 0:
			// Unknown. Say nothing, keep the last known state.
		case d.Down && !wasDown:
			t.down[subject] = true
			winner := Elect(subject, alive)
			t.elected[subject] = winner
			t.log.Printf("%s is down, lantern %s sends the notification", subject, winner)
			if winner == t.self {
				ev := Event{Kind: "down", Check: d.Check, Target: d.Target, Detail: downDetail(d)}
				t.send(ev)
				sent = append(sent, ev)
			}
		case d.Down && wasDown:
			// Still down. If the lantern elected to page has died since,
			// the next winner owes the operator that page: it cannot know
			// whether the dead one got it out, and a duplicate page beats
			// a silent outage.
			if prev := t.elected[subject]; !slices.Contains(alive, prev) {
				winner := Elect(subject, alive)
				t.elected[subject] = winner
				t.log.Printf("lantern %s went dark while %s is down, lantern %s takes over the notification", prev, subject, winner)
				if winner == t.self {
					ev := Event{Kind: "down", Check: d.Check, Target: d.Target, Detail: downDetail(d)}
					t.send(ev)
					sent = append(sent, ev)
				}
			}
		case !d.Down && wasDown:
			t.down[subject] = false
			delete(t.elected, subject)
			winner := Elect(subject, alive)
			t.log.Printf("%s recovered, lantern %s sends the notification", subject, winner)
			if winner == t.self {
				ev := Event{Kind: "recovered", Check: d.Check, Target: d.Target,
					Detail: fmt.Sprintf("%s on %s recovered", d.Check, d.Target)}
				t.send(ev)
				sent = append(sent, ev)
			}
		}
	}
	return sent
}

func downDetail(d quorum.Decision) string {
	if d.Authority {
		return fmt.Sprintf("%s on %s is down, its own lantern reports it", d.Check, d.Target)
	}
	return fmt.Sprintf("%s on %s is down, %d of %d lanterns agree and quorum needed %d",
		d.Check, d.Target, d.Votes, d.Voters, d.Needed)
}
