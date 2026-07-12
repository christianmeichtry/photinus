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
	notify   *notify.Tracker
	log      *log.Logger

	mu       sync.Mutex
	store    map[string]quorum.Observation
	clocks   map[string]*peerClock
	lastSeen map[string]time.Time
	sw       *swarm.Swarm
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
		id:       cfg.ID,
		interval: interval,
		maxAge:   maxAge,
		skewMax:  cfg.SkewMax,
		checks:   cfg.Checks,
		notify:   cfg.Notify,
		log:      logger,
		store:    make(map[string]quorum.Observation),
		clocks:   make(map[string]*peerClock),
		lastSeen: make(map[string]time.Time),
	}
}

// AttachSwarm wires the lantern to its swarm and starts receiving flashes.
func (l *Lantern) AttachSwarm(s *swarm.Swarm) {
	l.mu.Lock()
	l.sw = s
	l.mu.Unlock()
	s.SetOnFlash(l.ReceiveFlash)
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

// flash runs the local checks, stores the results as this lantern's own
// observations, and gossips them. Only own observations are gossiped:
// memberlist's broadcast queue handles spreading them swarm-wide, so fan-out
// stays constant instead of growing with the swarm.
func (l *Lantern) flash(ctx context.Context) {
	now := time.Now().UTC()
	own := make([]quorum.Observation, 0, len(l.checks))
	for _, c := range l.checks {
		res := c.Run(ctx)
		if res.Verdict == check.NotApplicable {
			continue
		}
		own = append(own, quorum.Observation{
			Observer: l.id,
			Target:   c.Target(),
			Check:    c.Name(),
			Healthy:  res.Verdict == check.OK,
			Detail:   res.Detail,
			Seen:     now,
		})
	}

	l.mu.Lock()
	own = append(own, l.skewObservations(now)...)
	for _, o := range own {
		l.store[storeKey(o)] = o
	}
	sw := l.sw
	l.mu.Unlock()

	if sw != nil && len(own) > 0 {
		if payload, err := json.Marshal(own); err != nil {
			l.log.Printf("could not encode a flash, skipping this one: %v", err)
		} else {
			sw.Flash(payload)
		}
	}

	// With the flash out, look at what the swarm now agrees on and let the
	// elected lantern notify. Every lantern runs this; only the winner acts.
	if l.notify != nil {
		st := l.Status()
		decisions := make([]quorum.Decision, len(st.Subjects))
		for i, s := range st.Subjects {
			decisions[i] = s.Decision
		}
		l.notify.Observe(decisions, st.Swarm, now)
	}
}

// ReceiveFlash merges a peer's flash into local memory. The newest
// observation per observer and subject wins. Observations claiming to come
// from this lantern are dropped: a lantern is the sole authority on its own
// checks and no peer may overwrite that.
func (l *Lantern) ReceiveFlash(payload []byte) {
	var obs []quorum.Observation
	if err := json.Unmarshal(payload, &obs); err != nil {
		l.log.Printf("dropped a flash that did not parse: %v", err)
		return
	}
	now := time.Now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, o := range obs {
		if o.Observer == l.id {
			continue
		}
		key := storeKey(o)
		if prev, ok := l.store[key]; !ok || o.Seen.After(prev.Seen) {
			l.store[key] = o
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

// SubjectStatus is the swarm's view of one check on one target.
type SubjectStatus struct {
	quorum.Decision
	Observations []quorum.Observation `json:"observations"`
}

// Status is everything one lantern knows, from local memory only.
type Status struct {
	ID            string          `json:"id"`
	Swarm         []string        `json:"swarm"`
	LastKnownSize int             `json:"last_known_size"`
	Subjects      []SubjectStatus `json:"subjects"`
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
