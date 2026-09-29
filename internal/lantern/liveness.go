package lantern

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/christianmeichtry/photinus/internal/notify"
	"github.com/christianmeichtry/photinus/internal/quorum"
)

// The lantern check is the mesh watching itself. Membership already knows
// who stopped answering; these observations put that knowledge in front of
// quorum, so a dead host pages like anything else, with no -watch flag.
//
// Every lantern reports every known peer as up or down according to its own
// membership view. Nobody reports on themselves, so the subject is always
// aggregated: a partition's minority can see the majority as dead all it
// wants, it will never reach quorum about it.

// livenessObservations turns this lantern's membership view into
// observations, one per known peer.
func (l *Lantern) livenessObservations(alive, roster []string, now time.Time) []quorum.Observation {
	aliveSet := make(map[string]bool, len(alive))
	for _, a := range alive {
		aliveSet[a] = true
	}
	var obs []quorum.Observation
	for _, peer := range roster {
		if peer == l.id {
			continue
		}
		state, detail := quorum.StateUp, "answering gossip"
		if !aliveSet[peer] {
			state, detail = quorum.StateDown, "stopped answering gossip"
		}
		obs = append(obs, quorum.Observation{
			Observer: l.id,
			Target:   peer,
			Check:    "lantern",
			State:    state,
			Detail:   detail,
			Seen:     now,
		})
	}
	return obs
}

// flashV is the wire version of the flash payload. The envelope is the
// contract that makes rolling upgrades safe: within a version, changes are
// additive only; anything else bumps the number, and a lantern drops
// versions it does not understand instead of misreading them.
const flashV = 1

// envelope is what actually rides the gossip packet. Leave, when set, is a
// farewell: the named lantern is decommissioning itself and asks the swarm
// to forget it. Push carries phone registrations for the APNs pager, so
// whichever lantern wins a notification election holds every token.
// Additive since wire v1.
type envelope struct {
	V   int                  `json:"v"`
	Obs []quorum.Observation `json:"obs,omitempty"`
	// Leave names a lantern decommissioning itself. Forget names a single
	// subject ("check target") the operator retired: a removed watch or a
	// dead node whose stale observations would otherwise linger for their
	// whole TTL. Both ask the swarm to drop what they name and refuse its
	// resurrection by anti-entropy. Additive since wire v1.
	Leave  string                    `json:"leave,omitempty"`
	Forget string                    `json:"forget,omitempty"`
	Push   []notify.PushRegistration `json:"push,omitempty"`
	// From and Sent name the lantern that put this envelope on the wire and
	// the moment it did so by its own clock. They are what the skew check
	// measures: arrival minus Sent is the clock offset plus transit delay.
	// Nothing else may serve as a clock sample, because an observation's
	// timestamp is the moment the thing was observed, not the moment it was
	// sent, and the two differ by a check's whole cadence or, for a pulse
	// receipt, by however long ago the job pinged. Additive since wire v1:
	// an older lantern omits them and is left unmeasured rather than
	// measured wrongly.
	From string     `json:"from,omitempty"`
	Sent *time.Time `json:"sent,omitempty"`
}

// chunkFlash splits observations into payloads that each fit inside one UDP
// gossip packet. A flash that outgrows the packet would never leave the
// queue, and the failure would be silence, so the size limit is enforced
// here where it can be tested. Every chunk carries the sender and the send
// time: they are small, and a receiver that dropped all but one chunk still
// gets its clock sample.
func chunkFlash(from string, sent time.Time, obs []quorum.Observation, limit int) [][]byte {
	var payloads [][]byte
	var batch []quorum.Observation
	// Measure the envelope around the batch instead of guessing it: From and
	// Sent made it big enough that a wrong guess could push a chunk past the
	// packet budget, and an oversized chunk is never transmitted at all.
	base := 16
	if b, err := json.Marshal(envelope{V: flashV, From: from, Sent: &sent}); err == nil {
		base = len(b)
	}
	size := base
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if payload, err := json.Marshal(envelope{V: flashV, From: from, Sent: &sent, Obs: batch}); err == nil {
			payloads = append(payloads, payload)
		}
		batch, size = nil, base
	}
	for _, o := range obs {
		b, err := json.Marshal(o)
		if err != nil {
			continue
		}
		if len(batch) > 0 && size+len(b)+1 > limit {
			flush()
		}
		batch = append(batch, o)
		size += len(b) + 1
	}
	flush()
	return payloads
}

// fitForGossip clamps an observation so it fits one flash chunk.
// memberlist's broadcast queue only ever sends what fits the UDP packet
// budget; anything larger is skipped every round, never transmitted and
// never pruned, which is the worst failure a monitor can have: an
// observation that silently exists nowhere but here. The detail sentence
// is the one unbounded field (error strings, urls), so it is trimmed
// first; an observation that does not fit even without its detail cannot
// ride a packet at all and is reported unfit so the caller can log it.
func fitForGossip(o quorum.Observation, limit int) (quorum.Observation, bool) {
	b, err := json.Marshal(o)
	if err != nil {
		return o, false
	}
	if len(b) <= limit {
		return o, true
	}
	cut := len(o.Detail) - (len(b) - limit) - 8
	if cut > 0 {
		o.Detail = strings.ToValidUTF8(o.Detail[:cut], "") + "..."
		if b, err = json.Marshal(o); err == nil && len(b) <= limit {
			return o, true
		}
	}
	o.Detail = ""
	if b, err = json.Marshal(o); err == nil && len(b) <= limit {
		return o, true
	}
	return o, false
}
