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
	// Kind is "down", "warning", "recovered" (up after down), or "cleared"
	// (up after a warning).
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
	state   map[string]string // subject -> last known state
	elected map[string]string // subject -> lantern elected to notify while not up
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
		state:   make(map[string]string),
		elected: make(map[string]string),
	}
}

// Observe takes the decisions computed after a flash and the lanterns
// currently believed alive. It returns the events this lantern actually
// sent, which is empty on every lantern except the elected one.
//
// A subject nobody has a live observation about is unknown, and unknown is
// neither recovered nor cleared: the state simply holds until somebody sees
// something.
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
		if d.Voters == 0 {
			// Unknown. Say nothing, keep the last known state.
			continue
		}
		prev, known := t.state[subject]
		if !known {
			prev = quorum.StateUp
		}
		cur := d.State

		if cur == prev {
			// Unchanged. If the subject is not healthy and the lantern
			// elected to notify has died since, the next winner owes the
			// operator that message: it cannot know whether the dead one
			// got it out, and a duplicate beats silence.
			if cur != quorum.StateUp {
				if e := t.elected[subject]; !slices.Contains(alive, e) {
					winner := Elect(subject, alive)
					t.elected[subject] = winner
					t.log.Printf("lantern %s went dark while %s is %s, lantern %s takes over the notification", e, subject, stateWord(cur), winner)
					if winner == t.self {
						ev := t.event(d, kindFor(prev, cur))
						t.send(ev)
						sent = append(sent, ev)
					}
				}
			}
			continue
		}

		t.state[subject] = cur
		kind := kindFor(prev, cur)
		winner := Elect(subject, alive)
		if cur == quorum.StateUp {
			delete(t.elected, subject)
		} else {
			t.elected[subject] = winner
		}
		t.log.Printf("%s is %s, lantern %s sends the %s notification", subject, stateWord(cur), winner, kind)
		if winner == t.self {
			ev := t.event(d, kind)
			t.send(ev)
			sent = append(sent, ev)
		}
	}
	return sent
}

// kindFor names the transition. Down always announces as down, a warning as
// warning, and the way back depends on how bad it was: recovered after an
// outage, cleared after a warning.
func kindFor(prev, cur string) string {
	switch cur {
	case quorum.StateDown:
		return "down"
	case quorum.StateWarn:
		return "warning"
	default:
		if prev == quorum.StateDown {
			return "recovered"
		}
		return "cleared"
	}
}

func stateWord(state string) string {
	switch state {
	case quorum.StateDown:
		return "down"
	case quorum.StateWarn:
		return "warning"
	default:
		return "up"
	}
}

func (t *Tracker) event(d quorum.Decision, kind string) Event {
	return Event{Kind: kind, Check: d.Check, Target: d.Target, Detail: sentence(d, kind)}
}

func sentence(d quorum.Decision, kind string) string {
	base := fmt.Sprintf("%s on %s", d.Check, d.Target)
	switch kind {
	case "down":
		how := fmt.Sprintf("%d of %d lanterns agree and quorum needed %d", d.Votes, d.Voters, d.Needed)
		if d.Authority {
			how = "its own lantern reports it"
		}
		s := fmt.Sprintf("%s is down, %s", base, how)
		if d.Detail != "" {
			s += ": " + d.Detail
		}
		return s
	case "warning":
		s := fmt.Sprintf("%s warns", base)
		if d.Detail != "" {
			s += ": " + d.Detail
		}
		return s
	case "recovered":
		return base + " recovered"
	default:
		return base + " cleared"
	}
}
