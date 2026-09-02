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
	"strconv"
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
	// Critical holds the subjects ("check target") the operator marked
	// worth interrupting a day for. It only decorates status answers;
	// enforcement lives in the notify senders.
	Critical map[string]bool
	// Blackout is how many watched subjects at one address must all be
	// down before the swarm calls it a blackout and pages about the
	// machine rather than about each service. Zero means the default of
	// three; a negative value switches the rule off.
	Blackout int
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
	critical map[string]bool
	// blackoutMin is how many dark subjects at one address make a blackout.
	// Zero switches the rule off.
	blackoutMin int
	start       time.Time
	notify      *notify.Tracker
	log         *log.Logger

	mu          sync.Mutex
	store       map[string]quorum.Observation
	clocks      map[string]*peerClock
	lastSeen    map[string]time.Time
	lastRun     map[string]time.Time
	lastVerdict map[string]check.Verdict
	lastPulse   map[string]time.Time
	pulseStuck  map[string]int
	pulseWarned map[string]bool
	departed    map[string]time.Time
	forgotten   map[string]time.Time // subject -> when the operator retired it
	addrs       map[string]string    // subject -> the address its check last reached
	badVersions map[int]bool
	pushRegs    map[string]notify.PushRegistration
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
	// Zero means the default; a negative value is the operator switching
	// the rule off, which must not read as "use the default".
	blackoutMin := cfg.Blackout
	switch {
	case blackoutMin == 0:
		blackoutMin = defaultBlackout
	case blackoutMin < 0:
		blackoutMin = 0
	}
	return &Lantern{
		id:          cfg.ID,
		interval:    interval,
		maxAge:      maxAge,
		skewMax:     cfg.SkewMax,
		checks:      cfg.Checks,
		pulses:      cfg.Pulses,
		critical:    cfg.Critical,
		blackoutMin: blackoutMin,
		start:       time.Now().UTC(),
		notify:      cfg.Notify,
		log:         logger,
		store:       make(map[string]quorum.Observation),
		clocks:      make(map[string]*peerClock),
		lastSeen:    make(map[string]time.Time),
		lastRun:     make(map[string]time.Time),
		lastVerdict: make(map[string]check.Verdict),
		lastPulse:   make(map[string]time.Time),
		pulseStuck:  make(map[string]int),
		pulseWarned: make(map[string]bool),
		departed:    make(map[string]time.Time),
		forgotten:   make(map[string]time.Time),
		addrs:       make(map[string]string),
		badVersions: make(map[int]bool),
		pushRegs:    make(map[string]notify.PushRegistration),
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
	regs := make([]notify.PushRegistration, 0, len(l.pushRegs))
	for _, r := range l.pushRegs {
		regs = append(regs, r)
	}
	l.mu.Unlock()
	payload, err := json.Marshal(envelope{V: flashV, Obs: obs, Push: regs})
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
		// every flash, immune to ticker jitter. nominal is the check's own
		// cadence; every may shrink to the re-probe interval while down.
		every := l.interval
		nominal := every
		paced := false
		var key string
		if p, ok := c.(check.Paced); ok && p.Every() > every {
			every = p.Every()
			nominal = every
			paced = true
			key = c.Name() + "|" + c.Target()
			// A check that last said down does not get to sulk for its whole
			// cadence: it re-probes fast until it says up again. The alert
			// delay can only filter a blip if fresh evidence arrives inside
			// its window, and a shared host's two-minute brownout must not
			// read as down for five. Warnings keep their pace; a cert that
			// expires in days will not change its mind in thirty seconds.
			if l.lastVerdict[key] == check.Failed && every > recheckWhileDown {
				every = recheckWhileDown
			}
			if last, ok := l.lastRun[key]; ok && now.Sub(last) < every {
				continue
			}
			l.lastRun[key] = now
		}

		res := c.Run(ctx)
		if paced {
			l.lastVerdict[key] = res.Verdict
		}
		if res.Addr != "" {
			// Remember where this subject lives. Kept from the last probe
			// that got far enough to know, so a check that is failing now
			// still groups with its neighbours: at the moment a machine
			// goes dark, nothing can be resolved from it any more.
			l.mu.Lock()
			l.addrs[c.Name()+" "+c.Target()] = res.Addr
			l.mu.Unlock()
		}
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
		//
		// The TTL follows the NOMINAL cadence, never the shrunk re-probe
		// interval: the run that carries a down check back up is the last
		// run for a whole cadence, and a short TTL on it left the subject
		// voterless ("no fresh word") until the next scheduled probe.
		ttl := int(3 * l.maxAge / time.Second)
		if nominal > l.interval {
			ttl = int(5 * nominal / time.Second)
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
		// has outgrown the packet goes out as several small flashes. Each
		// chunk is named by its index so the next flash supersedes it in
		// the gossip queue: every flash carries the whole view, so a
		// queued older chunk holds nothing the newer flash does not
		// restate, and retransmitting it would only crowd out fresh news.
		for i, payload := range chunkFlash(own, 1000) {
			sw.FlashNamed("flash/"+strconv.Itoa(i), payload)
		}
		// Phone registrations ride their own small envelope, so a token a
		// phone handed this lantern reaches the swarm within a flash. Same
		// superseding rule: only the latest registration set matters.
		if payload := l.pushPayload(); payload != nil {
			sw.FlashNamed("push", payload)
		}
	}

	// With the flash out, look at what the swarm now agrees on and let the
	// elected lantern notify. Every lantern runs this; only the winner acts.
	if l.notify != nil || len(l.pulses) > 0 {
		st, blackouts := l.statusAndBlackouts()
		if l.notify != nil {
			decisions := make([]quorum.Decision, 0, len(st.Subjects)+len(blackouts))
			for _, s := range st.Subjects {
				decisions = append(decisions, s.Decision)
			}
			decisions = append(decisions, blackouts...)
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
		if env.Forget != "" {
			l.forgetSubject(env.Forget)
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
		if len(env.Push) > 0 {
			l.mu.Lock()
			l.mergePush(env.Push)
			l.mu.Unlock()
		}
		obs = env.Obs
	}
	now := time.Now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, o := range obs {
		// Pulse receipts pass every guard below: a receipt records a job's
		// ping, not any lantern's health, and the fact must survive its
		// receiver leaving, dying, or restarting (see forget). That includes
		// this lantern taking its own receipt back after a restart, the one
		// case where hearing your own word from a peer is not spoofing but
		// the swarm handing back what only it still remembers.
		pulseReceipt := o.Check == "pulse" && o.State == quorum.StateUp
		if o.Observer == l.id && !pulseReceipt {
			continue
		}
		if dep, ok := l.departed[o.Observer]; ok && !pulseReceipt {
			if !o.Seen.After(dep) {
				continue
			}
			// Post-departure word from the lantern itself: it is back.
			delete(l.departed, o.Observer)
		}
		if dep, ok := l.departed[o.Target]; ok && !o.Seen.After(dep) && !pulseReceipt {
			continue
		}
		// A retired subject stays gone: refuse the stale observations that a
		// peer which missed the forget keeps re-sharing. A fresh one, stamped
		// after the forget, means the operator re-added the watch, so it
		// passes and clears the tombstone.
		if fg, ok := l.forgotten[o.Check+" "+o.Target]; ok {
			if !o.Seen.After(fg) {
				continue
			}
			delete(l.forgotten, o.Check+" "+o.Target)
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

// ForgetSubject retires one subject ("check target") across the swarm: it
// drops the local observations, tombstones the subject so anti-entropy
// cannot re-add them, and broadcasts the same so every lantern does the
// same. It is how an operator removes a watch or a decommissioned box
// without waiting out the observation's whole TTL. If a box still watches
// the subject it will simply reappear on the next flash, freshly stamped;
// forget is for what nobody watches any more.
func (l *Lantern) ForgetSubject(check, target string) {
	l.forgetSubject(check + " " + target)
	l.mu.Lock()
	sw := l.sw
	l.mu.Unlock()
	if sw == nil {
		return
	}
	if payload, err := json.Marshal(envelope{V: flashV, Forget: check + " " + target}); err == nil {
		sw.Flash(payload)
	}
}

// forgetSubject drops every observation about one subject and tombstones it.
func (l *Lantern) forgetSubject(subject string) {
	l.mu.Lock()
	for k, o := range l.store {
		if o.Check+" "+o.Target == subject {
			delete(l.store, k)
		}
	}
	l.forgotten[subject] = time.Now().UTC()
	l.mu.Unlock()
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
		// A pulse receipt survives its receiver's farewell: it records an
		// external event (the job pinged at time T), not the departing
		// lantern's opinion about anything. Forgetting it would erase the
		// swarm's memory of the ping and silently restart the silence
		// countdown; the fact must stay true when any single node leaves.
		if o.Check == "pulse" && o.State == quorum.StateUp {
			continue
		}
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
	for subject, at := range l.forgotten {
		if now.Sub(at) > 72*time.Hour {
			delete(l.forgotten, subject)
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
	l.prunePush(now)
}

// SubjectStatus is the swarm's view of one check on one target.
type SubjectStatus struct {
	quorum.Decision
	Observations []quorum.Observation `json:"observations"`
	// Critical mirrors the operator's marking (and lantern liveness, which
	// is always critical) so the panel and the app can weight it. Additive;
	// absent means routine.
	Critical bool `json:"critical,omitempty"`
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
	// IntervalMS is this lantern's flash interval in milliseconds. Clients
	// (panel, app) derive their heartbeat and staleness thresholds from it,
	// so they stay correct at any -interval: a larger interval means flashes
	// arrive less often, not that the swarm is unwell. Additive; a client
	// that does not see it falls back to the historical 2s assumption.
	IntervalMS int `json:"interval_ms,omitempty"`
}

// Status answers from local memory. It makes no network calls and must never
// need to: if answering requires talking to another machine, it is broken.
func (l *Lantern) Status() Status {
	st, blackouts := l.statusAndBlackouts()
	// The panel and the app hear about a blackout only while it is dark:
	// a lantern does not announce good news.
	for _, d := range blackouts {
		if d.State == quorum.StateDown {
			st.Subjects = append(st.Subjects, SubjectStatus{Decision: d, Critical: true})
		}
	}
	return st
}

// statusAndBlackouts is Status without the blackout subjects folded in,
// plus every blackout-capable address as its own decision, dark or not.
// The notification tracker needs the healthy ones too, or an alarm it
// opened could never be closed.
func (l *Lantern) statusAndBlackouts() (Status, []quorum.Decision) {
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
	st.IntervalMS = int(l.interval / time.Millisecond)

	subjects := make(map[string][2]string)
	for _, o := range all {
		subjects[o.Subject()] = [2]string{o.Target, o.Check}
	}
	for _, tc := range subjects {
		target, checkName := tc[0], tc[1]
		dec := quorum.Decide(target, checkName, all, lastKnown, l.maxAge, now)
		ss := SubjectStatus{Decision: dec,
			Critical: l.critical[checkName+" "+target] || checkName == "lantern"}
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

	// A whole address gone dark is its own subject, derived from the ones
	// above and always critical: the operator marked the members routine
	// one by one, never the machine losing all of them at once.
	l.mu.Lock()
	addrs := make(map[string]string, len(l.addrs))
	for k, v := range l.addrs {
		addrs[k] = v
	}
	l.mu.Unlock()

	return st, blackout(st.Subjects, addrs, l.blackoutMin)
}

// flashObsLimit is what one observation may occupy inside a flash chunk,
// leaving room for the envelope within chunkFlash's packet budget.
const flashObsLimit = 900

// recheckWhileDown is how fast a paced check re-probes after saying down.
// Fast enough that a brownout clears inside the alert delay and never
// pages; slow enough not to hammer a host that is already struggling.
const recheckWhileDown = 30 * time.Second
