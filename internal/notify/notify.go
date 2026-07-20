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

// Flapping bounds. Four transitions inside the window is a flap; one page
// says so and the subject then holds its tongue until it kept one state
// for the whole settle period.
const (
	flapWindow  = 10 * time.Minute
	flapCount   = 4
	settleAfter = 5 * time.Minute
)

// Tracker turns the stream of quorum decisions into events, firing only on
// state changes and only when this lantern wins the election for the alert.
type Tracker struct {
	self   string
	warmup time.Duration
	send   Sender
	log    *log.Logger

	start       time.Time
	holdDown    time.Duration          // how long a down must last before it pages
	state       map[string]string      // subject -> last known state
	elected     map[string]string      // subject -> lantern elected to notify while not up
	transitions map[string][]time.Time // subject -> recent state changes
	flapping    map[string]bool        // subject -> currently damped
	lastSeen    map[string]time.Time   // subject -> last time a decision mentioned it
	downSince   map[string]time.Time   // subject -> when an unconfirmed down began
}

// New builds a tracker. The warmup is how long after the first observation
// the tracker stays quiet: a lantern that just started is alone in a swarm
// of one and would otherwise reach quorum with only its own vote. holdDown
// is how long a subject must stay down before its first page fires; zero
// pages the instant quorum agrees.
func New(self string, warmup, holdDown time.Duration, send Sender, logger *log.Logger) *Tracker {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Tracker{
		self:        self,
		warmup:      warmup,
		holdDown:    holdDown,
		send:        send,
		log:         logger,
		state:       make(map[string]string),
		downSince:   make(map[string]time.Time),
		lastSeen:    make(map[string]time.Time),
		elected:     make(map[string]string),
		transitions: make(map[string][]time.Time),
		flapping:    make(map[string]bool),
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

	// A subject that no decision has mentioned for days is gone for good:
	// a removed watch, a renamed pulse, a decommissioned box. Its tracker
	// entries would otherwise live as long as the process; monitoring
	// state must not be the thing that grows forever.
	for _, d := range decisions {
		t.lastSeen[d.Check+" "+d.Target] = now
	}
	for subject := range t.state {
		if _, ok := t.lastSeen[subject]; !ok {
			t.lastSeen[subject] = now
		}
	}
	for subject, seen := range t.lastSeen {
		if now.Sub(seen) > 72*time.Hour {
			delete(t.state, subject)
			delete(t.elected, subject)
			delete(t.transitions, subject)
			delete(t.flapping, subject)
			delete(t.lastSeen, subject)
			delete(t.downSince, subject)
		}
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

		// A down that has not lasted the alert delay is not yet an alarm. A
		// brief unreachability, a home NAT blip, a scheduler pause, resolves
		// on its own, and the operator set this delay because that is not
		// worth a page. The stop-answering is still logged here and the
		// panel still shows the state live, so a brief outage is recorded,
		// just not pushed; a subject back before the delay never pages. A
		// delay of zero pages the instant quorum agrees.
		if t.holdDown > 0 {
			if cur != quorum.StateDown {
				delete(t.downSince, subject)
			} else if prev != quorum.StateDown {
				if t.downSince[subject].IsZero() {
					t.downSince[subject] = now
					t.log.Printf("%s went down, holding the alarm until it lasts %s", subject, t.holdDown)
				}
				if now.Sub(t.downSince[subject]) < t.holdDown {
					continue
				}
				delete(t.downSince, subject)
			}
		}

		if cur == prev {
			// Unchanged. A flapping subject that has now held one state
			// for the whole settle period gets its closing word; a
			// non-healthy subject whose elected sender died gets a
			// takeover page, since survivors cannot know the original
			// page got out, and a duplicate beats silence.
			if t.flapping[subject] {
				hist := t.transitions[subject]
				if len(hist) > 0 && now.Sub(hist[len(hist)-1]) >= settleAfter {
					delete(t.flapping, subject)
					delete(t.transitions, subject)
					winner := Elect(subject, alive)
					t.log.Printf("%s settled at %s after flapping, lantern %s sends the settled notification", subject, stateWord(cur), winner)
					if winner == t.self {
						ev := Event{Kind: "settled", Check: d.Check, Target: d.Target,
							Detail: fmt.Sprintf("%s on %s settled: %s, after flapping", d.Check, d.Target, stateWord(cur))}
						t.send(ev)
						sent = append(sent, ev)
					}
				}
				continue
			}
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

		// A transition. Record it and check for flapping before speaking:
		// fifty pages that each say still bouncing carry less information
		// than one page that says so.
		t.state[subject] = cur
		hist := append(t.transitions[subject], now)
		for len(hist) > 0 && now.Sub(hist[0]) > flapWindow {
			hist = hist[1:]
		}
		t.transitions[subject] = hist

		if t.flapping[subject] {
			continue
		}
		if len(hist) >= flapCount {
			t.flapping[subject] = true
			winner := Elect(subject, alive)
			t.elected[subject] = winner
			t.log.Printf("%s is flapping, %d changes in %s, lantern %s sends the flapping notification", subject, len(hist), flapWindow, winner)
			if winner == t.self {
				ev := Event{Kind: "flapping", Check: d.Check, Target: d.Target,
					Detail: fmt.Sprintf("%s on %s is flapping: %d state changes in %s, holding pages until it settles", d.Check, d.Target, len(hist), flapWindow)}
				t.send(ev)
				sent = append(sent, ev)
			}
			continue
		}

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
