// Package quorum decides when the swarm agrees that something is broken.
// One lantern seeing a host as down is a fact about that lantern. Quorum is
// the line between opinion and outage.
package quorum

import (
	"sort"
	"time"
)

// The three states a subject can be in. A warning is not an outage: the
// thing still works but is heading somewhere bad, and the words must never
// confuse the two.
const (
	StateUp   = "up"
	StateWarn = "warn"
	StateDown = "down"
)

// An Observation is one lantern's opinion about one check on one host.
type Observation struct {
	Observer string `json:"observer"`
	Target   string `json:"target"`
	Check    string `json:"check"`
	// State is up, warn, or down.
	State  string    `json:"state"`
	Detail string    `json:"detail,omitempty"`
	Seen   time.Time `json:"seen"`
	// TTL, in seconds, is how long this observation stays a valid vote.
	// Zero means the caller's default aging window. Slow-paced checks set
	// it so their votes survive between runs. Additive since wire v1.
	TTL int `json:"ttl,omitempty"`
}

// Subject names what an observation is about, regardless of who observed it.
func (o Observation) Subject() string { return o.Check + " " + o.Target }

// Decision is the swarm's verdict on one subject.
type Decision struct {
	Target string `json:"target"`
	Check  string `json:"check"`
	// State is up, warn, or down. Warn and down require quorum unless the
	// subject is a lantern's own local check.
	State string `json:"state"`
	// Detail is the most telling observation's detail line: the authority's
	// word for a local check, the first complaint otherwise.
	Detail string `json:"detail,omitempty"`
	// Authority is true when the subject is a lantern's own local check, so
	// that lantern's word decided alone and no agreement was needed.
	Authority bool `json:"authority,omitempty"`
	// Votes counts lanterns currently reporting the subject warn or down.
	Votes int `json:"votes"`
	// Voters counts lanterns with a live observation on the subject.
	Voters int `json:"voters"`
	// Needed is how many votes quorum requires.
	Needed int `json:"needed"`
	// SwarmSize is the last known swarm size the quorum was computed against.
	SwarmSize int `json:"swarm_size"`
}

// Decide applies the last-known-size rule: quorum is a majority of the last
// known swarm size, not of the lanterns currently reachable. A minority
// partition can therefore never reach quorum on its own count. It goes quiet
// instead of screaming.
//
// Only the newest observation per observer counts, and observations older
// than maxAge are ignored entirely (zero maxAge disables the age cut).
//
// One exception, and it is rule 4: an observation whose observer is the
// target itself is a lantern reporting on its own local checks. That lantern
// is the sole authority on those, so its word decides alone and hearsay
// about the same subject never mixes with it.
func Decide(target, checkName string, obs []Observation, lastKnownSize int, maxAge time.Duration, now time.Time) Decision {
	d := Decision{
		Target:    target,
		Check:     checkName,
		State:     StateUp,
		Needed:    lastKnownSize/2 + 1,
		SwarmSize: lastKnownSize,
	}

	newest := make(map[string]Observation)
	for _, o := range obs {
		if o.Target != target || o.Check != checkName {
			continue
		}
		cutoff := maxAge
		if o.TTL > 0 {
			cutoff = time.Duration(o.TTL) * time.Second
		}
		if cutoff > 0 && now.Sub(o.Seen) > cutoff {
			continue
		}
		if prev, ok := newest[o.Observer]; !ok || o.Seen.After(prev.Seen) {
			newest[o.Observer] = o
		}
	}

	if auth, ok := newest[target]; ok {
		d.Authority = true
		d.Voters = 1
		d.Needed = 1
		d.State = auth.State
		d.Detail = auth.Detail
		if auth.State != StateUp {
			d.Votes = 1
		}
		return d
	}

	// Deterministic order, so the chosen detail does not flap between
	// status calls.
	ordered := make([]Observation, 0, len(newest))
	for _, o := range newest {
		ordered = append(ordered, o)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Observer < ordered[j].Observer })

	downVotes := 0
	for _, o := range ordered {
		d.Voters++
		if o.State != StateUp {
			d.Votes++
			if o.State == StateDown {
				downVotes++
			}
			if d.Detail == "" {
				d.Detail = o.Detail
			}
		}
	}
	if d.Detail == "" && len(ordered) > 0 {
		d.Detail = ordered[0].Detail
	}
	// DOWN needs a quorum of lanterns actually saying down. Warnings can
	// join the count and escalate the subject to WARN, but they must never
	// launder a single down opinion into a swarm-confirmed outage: two
	// expiring-cert warnings plus one broken-cert opinion is a warning,
	// not an outage the swarm agreed on.
	switch {
	case downVotes >= d.Needed:
		d.State = StateDown
	case d.Votes >= d.Needed:
		d.State = StateWarn
	}
	return d
}
