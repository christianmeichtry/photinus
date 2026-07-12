// Package quorum decides when the swarm agrees that something is broken.
// One lantern seeing a host as down is a fact about that lantern. Quorum is
// the line between opinion and outage.
package quorum

import "time"

// An Observation is one lantern's opinion about one check on one host.
type Observation struct {
	Observer string    `json:"observer"`
	Target   string    `json:"target"`
	Check    string    `json:"check"`
	Healthy  bool      `json:"healthy"`
	Detail   string    `json:"detail,omitempty"`
	Seen     time.Time `json:"seen"`
}

// Subject names what an observation is about, regardless of who observed it.
func (o Observation) Subject() string { return o.Check + " " + o.Target }

// Decision is the swarm's verdict on one subject.
type Decision struct {
	Target string `json:"target"`
	Check  string `json:"check"`
	// Down is true when quorum agrees the subject is broken.
	Down bool `json:"down"`
	// Authority is true when the subject is a lantern's own local check, so
	// that lantern's word decided alone and no agreement was needed.
	Authority bool `json:"authority,omitempty"`
	// Votes counts lanterns currently reporting the subject broken.
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
		Needed:    lastKnownSize/2 + 1,
		SwarmSize: lastKnownSize,
	}

	newest := make(map[string]Observation)
	for _, o := range obs {
		if o.Target != target || o.Check != checkName {
			continue
		}
		if maxAge > 0 && now.Sub(o.Seen) > maxAge {
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
		if !auth.Healthy {
			d.Votes = 1
		}
		d.Down = !auth.Healthy
		return d
	}

	for _, o := range newest {
		d.Voters++
		if !o.Healthy {
			d.Votes++
		}
	}
	d.Down = d.Votes >= d.Needed
	return d
}
