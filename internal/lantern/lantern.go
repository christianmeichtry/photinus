// Package lantern is the agent loop: check, gossip, merge. One lantern runs
// on one host, is the sole authority on its own local checks, and holds in
// local memory everything needed to answer a status query with the network
// on fire.
package lantern

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/christianmeichtry/photinus/internal/check"
	"github.com/christianmeichtry/photinus/internal/notify"
	"github.com/christianmeichtry/photinus/internal/quorum"
	"github.com/christianmeichtry/photinus/internal/swarm"
)

// Config describes one lantern.
type Config struct {
	// ID is the lantern's name, unique across the swarm.
	ID string
	// Interval is the time between flashes. Zero means 2 seconds.
	Interval time.Duration
	// MaxAge is how old an observation may be and still count toward quorum.
	// Zero means five intervals.
	MaxAge time.Duration
	// Checks are the checks this lantern runs locally.
	Checks []check.Check
	// SkewMax is the clock drift against a peer that trips the skew check.
	// Zero or negative disables skew measurement.
	SkewMax time.Duration
	// Pulses maps each declared pulse name to its silence window. Like
	// -expect and seeds, the same declarations belong on every box: only a
	// lantern that declares a pulse evaluates its silence.
	Pulses map[string]time.Duration
	// Notify, when set, is fed the swarm's decisions after every flash so
	// the elected lantern can send the one notification. Nil means no
	// notifications from this lantern.
	Notify *notify.Tracker
	// Logger receives operator-facing log lines. Nil silences the lantern.
	Logger *log.Logger
}

// Lantern is one agent process.
type Lantern struct {
	id       string
	interval time.Duration
	maxAge   time.Duration
	skewMax  time.Duration
	checks   []check.Check
	pulses   map[string]time.Duration
	start    time.Time
	notify   *notify.Tracker
	log      *log.Logger

	mu          sync.Mutex
	store       map[string]quorum.Observation
	clocks      map[string]*peerClock
	lastSeen    map[string]time.Time
	lastRun     map[string]time.Time
	lastPulse   map[string]time.Time
	pulseStuck  map[string]int
	pulseWarned map[string]bool
	departed    map[string]time.Time
	badVersions map[int]bool
	sw          *swarm.Swarm
}

// New builds a lantern. Attach a swarm and call Run to light it.
func New(cfg Config) *Lantern {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = 5 * interval
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Lantern{
		id:          cfg.ID,
		interval:    interval,
		maxAge:      maxAge,
		skewMax:     cfg.SkewMax,
		checks:      cfg.Checks,
		pulses:      cfg.Pulses,
		start:       time.Now().UTC(),
		notify:      cfg.Notify,
		log:         logger,
		store:       make(map[string]quorum.Observation),
		clocks:      make(map[string]*peerClock),
		lastSeen:    make(map[string]time.Time),
		lastRun:     make(map[string]time.Time),
		lastPulse:   make(map[string]time.Time),
		pulseStuck:  make(map[string]int),
		pulseWarned: make(map[string]bool),
		departed:    make(map[string]time.Time),
		badVersions: make(map[int]bool),
	}
}

// AttachSwarm wires the lantern to its swarm and starts receiving flashes.
func (l *Lantern) AttachSwarm(s *swarm.Swarm) {
	l.mu.Lock()
	l.sw = s
	l.mu.Unlock()
	s.SetOnFlash(l.ReceiveFlash)
	s.SetState(l.SyncState)
}

// SyncState snapshots everything this lantern currently believes, its own
// observations and everything heard, as one flash envelope. It feeds the
// swarm's push/pull anti-entropy: observations a peer missed on the wire
// arrive here at the latest, and a late joiner gets the full picture
// without waiting out every check's cadence.
func (l *Lantern) SyncState() []byte {
	l.mu.Lock()
	obs := make([]quorum.Observation, 0, len(l.store))
	for _, o := range l.store {
		obs = append(obs, o)
	}
	l.mu.Unlock()
	payload, err := json.Marshal(envelope{V: flashV, Obs: obs})
	if err != nil {
		return nil
	}
	return payload
}

// Run flashes on every interval until the context ends. It blocks.
func (l *Lantern) Run(ctx context.Context) {
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	l.flash(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.flash(ctx)
		}
	}
}

// flash runs the local checks that are due, stores the results as this
// lantern's own observations, and gossips its whole current view of itself.
// Paced checks run on their own cadence; their last verdict keeps riding
// every flash with a TTL that outlives the gap, so slow checks never look
// stale. Only own observations are gossiped: memberlist's broadcast queue
// spreads them swarm-wide, so fan-out stays constant.
func (l *Lantern) flash(ctx context.Context) {
	now := time.Now().UTC()
	fresh := make([]quorum.Observation, 0, len(l.checks))
	for _, c := range l.checks {
		// Only genuinely paced checks are gated; everything else runs on
		// every flash, immune to ticker jitter.
		every := l.interval
		if p, ok := c.(check.Paced); ok && p.Every() > every {
			every = p.Every()
			key := c.Name() + "|" + c.Target()
			if last, ok := l.lastRun[key]; ok && now.Sub(last) < every {
				continue
			}
			l.lastRun[key] = now
		}

		res := c.Run(ctx)
		var state string
		switch res.Verdict {
		case check.OK:
			state = quorum.StateUp
		case check.Warn:
			state = quorum.StateWarn
		case check.Failed:
			state = quorum.StateDown
		default:
			continue
		}
		// Every own observation carries a TTL floor of three aging windows.
		// A lantern that stalls past maxAge (garbage collection, swap, the
		// hypervisor's whims) used to have its authority rows blank out
		// fleet-wide as unknown; a short stall is not news, and the
		// membership check still catches a box that actually died.
		ttl := int(3 * l.maxAge / time.Second)
		if every > l.interval {
			ttl = int(5 * every / time.Second)
		}
		fresh = append(fresh, quorum.Observation{
			Observer: l.id,
			Target:   c.Target(),
			Check:    c.Name(),
			State:    state,
			Detail:   res.Detail,
			Seen:     now,
			TTL:      ttl,
		})
	}

	l.mu.Lock()
	fresh = append(fresh, l.skewObservations(now)...)
	fresh = append(fresh, l.pulseObservations(now)...)
	sw := l.sw
	if sw != nil {
		fresh = append(fresh, l.livenessObservations(sw.Members(), sw.Roster(), now)...)
	}
	for _, o := range fresh {
		l.store[storeKey(o)] = o
	}
	l.prune(now)
	// The flash carries everything this lantern currently believes about
	// its own checks, not just what ran this cycle, so late joiners hear
	// about slow-paced subjects without waiting a cadence.
	var own []quorum.Observation
	var unfit []string
	for _, o := range l.store {
		if o.Observer != l.id {
			continue
		}
		if fit, ok := fitForGossip(o, flashObsLimit); ok {
			own = append(own, fit)
		} else {
			unfit = append(unfit, o.Check+" "+o.Target)
		}
	}
	l.mu.Unlock()
	for _, s := range unfit {
		l.log.Printf("observation %s is too large for a gossip packet even without its detail and was not gossiped: shorten the name or target", s)
	}

	if sw != nil {
		// A flash must ride inside one UDP gossip packet, so a view that
		// has outgrown the packet goes out as several small flashes.
		for _, payload := range chunkFlash(own, 1000) {
			sw.Flash(payload)
		}
	}

	// With the flash out, look at what the swarm now agrees on and let the
	// elected lantern notify. Every lantern runs this; only the winner acts.
	if l.notify != nil || len(l.pulses) > 0 {
		st := l.Status()
		if l.notify != nil {
			decisions := make([]quorum.Decision, len(st.Subjects))
			for i, s := range st.Subjects {
				decisions[i] = s.Decision
			}
			l.notify.Observe(decisions, st.Swarm, now)
		}
		l.warnUnderDeclaredPulses(st)
	}
}

// warnUnderDeclaredPulses catches the one way a pulse fails silently: every
// lantern that declares it calls it silent, yet quorum cannot be reached
// because too few boxes declare it. A safety net that cannot fire must say
// so in the log. The condition has to hold for a stretch of flashes first,
// so the moments when declarers merely have not all voted yet do not warn.
func (l *Lantern) warnUnderDeclaredPulses(st Status) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range st.Subjects {
		if s.Check != "pulse" {
			continue
		}
		name := s.Target
		if _, declaredHere := l.pulses[name]; !declaredHere || l.pulseWarned[name] {
			continue
		}
		stuck := s.Votes > 0 && s.Votes == s.Voters && s.Votes < s.Needed
		if !stuck {
			l.pulseStuck[name] = 0
			continue
		}
		l.pulseStuck[name]++
		if l.pulseStuck[name] >= 15 {
			l.pulseWarned[name] = true
			l.log.Printf("pulse %s is silent by every lantern that declares it (%d of quorum %d), but too few declare it to ever page: add -watch pulse:%s to more boxes",
				name, s.Votes, s.Needed, name)
		}
	}
}

// ReceiveFlash merges a peer's flash into local memory. The newest
// observation per observer and subject wins. Observations claiming to come
// from this lantern are dropped: a lantern is the sole authority on its own
// checks and no peer may overwrite that.
//
// Flashes arrive as a versioned envelope. An unknown version is dropped
// with a log line, never guessed at: wrong monitoring conclusions are
// worse than missing ones. Bare arrays, the format before the envelope
// existed, are still accepted for one release.
func (l *Lantern) ReceiveFlash(payload []byte) {
	var obs []quorum.Observation
	if len(payload) > 0 && payload[0] == '[' {
		// Legacy pre-envelope flash from a 0.0.1 lantern.
		if err := json.Unmarshal(payload, &obs); err != nil {
			l.log.Printf("dropped a flash that did not parse: %v", err)
			return
		}
	} else {
		var env envelope
		if err := json.Unmarshal(payload, &env); err != nil {
			l.log.Printf("dropped a flash that did not parse: %v", err)
			return
		}
		if env.Leave != "" && env.Leave != l.id {
			l.forget(env.Leave)
			return
		}
		if env.V != flashV {
			l.mu.Lock()
			seen := l.badVersions[env.V]
			l.badVersions[env.V] = true
			l.mu.Unlock()
			if !seen {
				l.log.Printf("dropping flashes with wire version %d, this lantern speaks %d: upgrade the older side", env.V, flashV)
			}
			return
		}
		obs = env.Obs
	}
	now := time.Now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, o := range obs {
		if o.Observer == l.id {
			continue
		}
		if dep, ok := l.departed[o.Observer]; ok {
			if !o.Seen.After(dep) {
				continue
			}
			// Post-departure word from the lantern itself: it is back.
			delete(l.departed, o.Observer)
		}
		if dep, ok := l.departed[o.Target]; ok && !o.Seen.After(dep) {
			continue
		}
		key := storeKey(o)
		if prev, ok := l.store[key]; !ok || o.Seen.After(prev.Seen) {
			l.store[key] = o
		}
		// A pulse receipt carries the ping time in Seen. Remembering the
		// newest one separately lets every lantern keep the true silence
		// baseline even after the receipt observation itself ages out.
		if o.Check == "pulse" && o.State == quorum.StateUp && o.Seen.After(l.lastPulse[o.Target]) {
			l.lastPulse[o.Target] = o.Seen
		}
		// A flash stamped later than anything heard from this observer is a
		// fresh clock sample; re-gossiped old flashes are not.
		if l.skewMax > 0 && o.Seen.After(l.lastSeen[o.Observer]) {
			l.lastSeen[o.Observer] = o.Seen
			l.observeClock(o.Observer, o.Seen, now)
		}
	}
}

func storeKey(o quorum.Observation) string {
	return o.Observer + "|" + o.Check + "|" + o.Target
}

// Farewell tells the swarm this lantern is leaving on purpose, then gives
// the gossip a moment to carry the message before the transport goes away.
func (l *Lantern) Farewell() {
	l.mu.Lock()
	sw := l.sw
	l.mu.Unlock()
	if sw == nil {
		return
	}
	if payload, err := json.Marshal(envelope{V: flashV, Leave: l.id}); err == nil {
		sw.Flash(payload)
		time.Sleep(1200 * time.Millisecond)
	}
}

// forget erases a gracefully departed lantern: its word, everything said
// about it, and its seat in the quorum denominator.
func (l *Lantern) forget(name string) {
	l.mu.Lock()
	for k, o := range l.store {
		if o.Observer == name || o.Target == name {
			delete(l.store, k)
		}
	}
	// The tombstone guards against anti-entropy resurrection: a peer that
	// missed the farewell still holds this lantern's observations, some
	// with hours of TTL, and its next push/pull sync would hand the ghosts
	// back to everyone who correctly forgot. Anything stamped before the
	// departure is refused; anything newer means the lantern came back.
	l.departed[name] = time.Now().UTC()
	delete(l.clocks, name)
	delete(l.lastSeen, name)
	sw := l.sw
	l.mu.Unlock()
	if sw != nil {
		sw.Forget(name)
	}
	l.log.Printf("lantern %s said farewell and is forgotten", name)
}

// prune drops observations long past any chance of counting again, so
// removed checks and decommissioned boxes fade from status instead of
// haunting it forever. Callers hold l.mu.
func (l *Lantern) prune(now time.Time) {
	for k, o := range l.store {
		ttl := l.maxAge
		if o.TTL > 0 {
			ttl = time.Duration(o.TTL) * time.Second
		}
		horizon := 3 * ttl
		if horizon < time.Hour {
			horizon = time.Hour
		}
		if now.Sub(o.Seen) > horizon {
			delete(l.store, k)
		}
	}
	// Tombstones outlive the longest TTL any ghost could carry, then go.
	for name, dep := range l.departed {
		if now.Sub(dep) > 72*time.Hour {
			delete(l.departed, name)
		}
	}
	// Pings for names nobody declares (typos, jobs pinging before their
	// declaration ships) stop being remembered after the same horizon, so
	// the pulse maps cannot grow without bound.
	for name, t0 := range l.lastPulse {
		if _, declared := l.pulses[name]; !declared && now.Sub(t0) > 72*time.Hour {
			delete(l.lastPulse, name)
		}
	}
}

// SubjectStatus is the swarm's view of one check on one target.
type SubjectStatus struct {
	quorum.Decision
	Observations []quorum.Observation `json:"observations"`
}

// Status is everything one lantern knows, from local memory only.
type Status struct {
	ID            string            `json:"id"`
	Swarm         []string          `json:"swarm"`
	LastKnownSize int               `json:"last_known_size"`
	Versions      map[string]string `json:"versions,omitempty"`
	// Endpoints maps each lantern to its advertised host:port. A client
	// that reached one lantern uses this to reach every other directly,
	// so a single configured address is never a single point of failure.
	Endpoints map[string]string `json:"endpoints,omitempty"`
	Subjects  []SubjectStatus   `json:"subjects"`
}

// Status answers from local memory. It makes no network calls and must never
// need to: if answering requires talking to another machine, it is broken.
func (l *Lantern) Status() Status {
	now := time.Now().UTC()

	l.mu.Lock()
	all := make([]quorum.Observation, 0, len(l.store))
	for _, o := range l.store {
		all = append(all, o)
	}
	sw := l.sw
	l.mu.Unlock()

	st := Status{ID: l.id}
	lastKnown := 1
	if sw != nil {
		st.Swarm = sw.Members()
		sort.Strings(st.Swarm)
		lastKnown = sw.LastKnownSize()
		st.Versions = sw.MemberVersions()
		st.Endpoints = sw.MemberAddrs()
	}
	st.LastKnownSize = lastKnown

	subjects := make(map[string][2]string)
	for _, o := range all {
		subjects[o.Subject()] = [2]string{o.Target, o.Check}
	}
	for _, tc := range subjects {
		target, checkName := tc[0], tc[1]
		dec := quorum.Decide(target, checkName, all, lastKnown, l.maxAge, now)
		ss := SubjectStatus{Decision: dec}
		for _, o := range all {
			if o.Target == target && o.Check == checkName {
				ss.Observations = append(ss.Observations, o)
			}
		}
		sort.Slice(ss.Observations, func(i, j int) bool {
			return ss.Observations[i].Observer < ss.Observations[j].Observer
		})
		st.Subjects = append(st.Subjects, ss)
	}
	sort.Slice(st.Subjects, func(i, j int) bool {
		a, b := st.Subjects[i], st.Subjects[j]
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		return a.Target < b.Target
	})

	return st
}

// flashObsLimit is what one observation may occupy inside a flash chunk,
// leaving room for the envelope within chunkFlash's packet budget.
const flashObsLimit = 900
