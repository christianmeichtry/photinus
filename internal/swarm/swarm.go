// Package swarm wraps hashicorp/memberlist. It answers who is in the swarm,
// remembers how big the swarm was, and carries flashes between lanterns. It
// does not interpret what a flash contains.
//
// Membership and failure detection stay memberlist's job. Do not grow a
// membership protocol in here.
package swarm

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"
)

// Config describes one lantern's place in the swarm.
type Config struct {
	// ID is the lantern's name, unique across the swarm.
	ID string
	// Bind is the host:port the gossip layer listens on.
	Bind string
	// Seeds are addresses of lanterns to join through. Order does not matter
	// and no seed matters after startup.
	Seeds []string
	// Advertise is the host:port peers should reach this lantern on, for
	// machines where the bind address is not what the world sees (NAT,
	// several interfaces). A bare host keeps the bind port. Empty lets
	// memberlist guess, which is right on simple LAN boxes.
	Advertise string
	// Key is the shared swarm secret. When set, gossip is encrypted and
	// lanterns without the same key cannot join or inject anything. The
	// actual cipher key is derived from it, so any passphrase works.
	Key string
	// OnFlash is called for every flash payload received from a peer. It must
	// not block; copy is already taken care of.
	OnFlash func(payload []byte)
	// Logger receives operator-facing log lines. Nil silences the swarm.
	Logger *log.Logger
}

// Swarm is the live membership view of one lantern.
type Swarm struct {
	ml    *memberlist.Memberlist
	queue *memberlist.TransmitLimitedQueue
	log   *log.Logger

	mu       sync.Mutex
	onFlash  func([]byte)
	everSeen map[string]struct{}

	stop chan struct{}
}

// Join starts the gossip layer and tries the seed list. A seed that is not
// up yet is fine: joining keeps being retried in the background, and a peer
// that starts later with us as its seed will find us anyway.
func Join(cfg Config) (*Swarm, error) {
	host, portStr, err := net.SplitHostPort(cfg.Bind)
	if err != nil {
		return nil, fmt.Errorf("parsing bind address %q: %w", cfg.Bind, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("parsing bind port %q: %w", portStr, err)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}

	s := &Swarm{
		log:      logger,
		onFlash:  cfg.OnFlash,
		everSeen: map[string]struct{}{cfg.ID: {}},
		stop:     make(chan struct{}),
	}

	mc := memberlist.DefaultLANConfig()
	mc.Name = cfg.ID
	mc.BindAddr = host
	mc.BindPort = port
	mc.AdvertisePort = port
	mc.Delegate = &delegate{s: s}
	mc.Events = &events{s: s}
	// memberlist's own logging is developer noise, not operator information.
	mc.LogOutput = io.Discard

	if cfg.Advertise != "" {
		ahost, aport := cfg.Advertise, port
		if h, p, err := net.SplitHostPort(cfg.Advertise); err == nil {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("parsing advertise port %q: %w", p, err)
			}
			ahost, aport = h, n
		}
		// memberlist wants an IP here, but operators have names. A DNS
		// name resolves once at startup; boxes whose address changes at
		// runtime need the IP given explicitly anyway.
		if net.ParseIP(ahost) == nil {
			ips, err := net.LookupIP(ahost)
			if err != nil || len(ips) == 0 {
				return nil, fmt.Errorf("resolving advertise host %q: %w", ahost, err)
			}
			picked := ips[0]
			for _, ip := range ips {
				if ip.To4() != nil {
					picked = ip
					break
				}
			}
			ahost = picked.String()
		}
		mc.AdvertiseAddr = ahost
		mc.AdvertisePort = aport
	}

	if cfg.Key != "" {
		// Derive a fixed-size cipher key so operators can pick any
		// passphrase instead of minting exactly 32 bytes.
		sum := sha256.Sum256([]byte(cfg.Key))
		mc.SecretKey = sum[:]
	}

	ml, err := memberlist.Create(mc)
	if err != nil {
		return nil, fmt.Errorf("starting gossip on %s: %w", cfg.Bind, err)
	}
	s.ml = ml
	s.queue = &memberlist.TransmitLimitedQueue{
		NumNodes:       ml.NumMembers,
		RetransmitMult: 3,
	}
	if cfg.Key != "" {
		logger.Printf("gossip is encrypted, only lanterns holding the same key can join")
	}

	if len(cfg.Seeds) > 0 {
		go s.keepJoining(cfg.Seeds)
	}
	return s, nil
}

// keepJoining retries the seed list until at least one peer is reachable.
// After the first success the swarm heals itself and seeds stop mattering.
func (s *Swarm) keepJoining(seeds []string) {
	for {
		if s.ml.NumMembers() > 1 {
			return
		}
		if n, err := s.ml.Join(seeds); err != nil {
			s.log.Printf("no seed reachable yet, still flashing alone: %v", err)
		} else if n > 0 {
			s.log.Printf("joined the swarm through %d seed(s)", n)
			return
		}
		select {
		case <-s.stop:
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// SetOnFlash replaces the flash handler. Flashes arriving before a handler is
// set are dropped; they repeat on the next interval anyway.
func (s *Swarm) SetOnFlash(fn func(payload []byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onFlash = fn
}

// Members lists the lanterns currently believed alive, including this one.
func (s *Swarm) Members() []string {
	nodes := s.ml.Members()
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	return names
}

// LastKnownSize is how many distinct lanterns this one has ever seen alive,
// including itself. A lantern that fails still counts: quorum is computed
// against the swarm as last known, not as currently reachable, so a minority
// partition goes quiet instead of alerting. Graceful shrinking of the swarm
// is a later problem and is recorded in docs/design.md.
func (s *Swarm) LastKnownSize() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.everSeen)
}

// Roster lists every lantern ever seen alive, including this one and any
// that have since died. It is the swarm as remembered, the same denominator
// the quorum counts against.
func (s *Swarm) Roster() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.everSeen))
	for name := range s.everSeen {
		names = append(names, name)
	}
	return names
}

// Flash queues a payload for gossip. memberlist fans it out to a constant
// sample of peers per round, never to everyone at once.
func (s *Swarm) Flash(payload []byte) {
	s.queue.QueueBroadcast(broadcast{payload: payload})
}

// Leave announces a graceful departure and shuts the gossip layer down.
func (s *Swarm) Leave() error {
	close(s.stop)
	if err := s.ml.Leave(2 * time.Second); err != nil {
		return fmt.Errorf("leaving the swarm: %w", err)
	}
	return s.ml.Shutdown()
}

// broadcast adapts a flash payload to memberlist's broadcast queue.
type broadcast struct{ payload []byte }

func (b broadcast) Invalidates(memberlist.Broadcast) bool { return false }
func (b broadcast) Message() []byte                       { return b.payload }
func (b broadcast) Finished()                             {}

// delegate carries flashes over memberlist's gossip messages.
type delegate struct{ s *Swarm }

func (d *delegate) NodeMeta(limit int) []byte { return nil }

func (d *delegate) NotifyMsg(msg []byte) {
	if len(msg) == 0 {
		return
	}
	cp := make([]byte, len(msg))
	copy(cp, msg)
	d.s.mu.Lock()
	fn := d.s.onFlash
	d.s.mu.Unlock()
	if fn != nil {
		fn(cp)
	}
}

func (d *delegate) GetBroadcasts(overhead, limit int) [][]byte {
	return d.s.queue.GetBroadcasts(overhead, limit)
}

func (d *delegate) LocalState(join bool) []byte            { return nil }
func (d *delegate) MergeRemoteState(buf []byte, join bool) {}

// events keeps the ever-seen ledger that LastKnownSize reads.
type events struct{ s *Swarm }

func (e *events) NotifyJoin(n *memberlist.Node) {
	e.s.mu.Lock()
	e.s.everSeen[n.Name] = struct{}{}
	e.s.mu.Unlock()
	e.s.log.Printf("lantern %s joined the swarm", n.Name)
}

func (e *events) NotifyLeave(n *memberlist.Node) {
	e.s.log.Printf("lantern %s left the swarm or stopped answering", n.Name)
}

func (e *events) NotifyUpdate(n *memberlist.Node) {}
