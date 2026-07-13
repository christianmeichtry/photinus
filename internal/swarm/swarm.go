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
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"

	"github.com/christianmeichtry/photinus/internal/version"
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
	// lanterns without the same key cannot join or contribute observations. The
	// actual cipher key is derived from it, so any passphrase works.
	Key string
	// Expect names lanterns that must exist whether or not they are alive.
	// A declared lantern that is down at startup, or that dies while the
	// swarm is restarted, is still counted and reported down, instead of
	// being invisible because membership is only ever discovered. Same list
	// on every box, like the seeds.
	Expect []string
	// HTTP, when set, is served on the gossip port itself: the mux
	// classifies each TCP connection by its first byte and routes
	// memberlist traffic to memberlist and HTTP to this handler. One open
	// port serves both. Nil leaves the gossip port speaking gossip only.
	HTTP http.Handler
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
	state    func() []byte
	everSeen map[string]struct{}
	expected map[string]struct{}

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
		expected: make(map[string]struct{}),
		stop:     make(chan struct{}),
	}
	// Declared members exist from the start: seed them into the roster so a
	// box that never joins is counted and reported down, not invisible.
	for _, name := range cfg.Expect {
		if name != "" {
			s.everSeen[name] = struct{}{}
			s.expected[name] = struct{}{}
		}
	}

	mc := memberlist.DefaultLANConfig()
	mc.Name = cfg.ID
	mc.BindAddr = host
	mc.BindPort = port
	mc.AdvertisePort = port
	mc.Delegate = &delegate{s: s}
	mc.Events = &events{s: s}
	// memberlist's chatter is developer noise, but its warnings are exactly
	// the operator information that explains a sick swarm: refused joins,
	// UDP that fails where TCP works. Pass those through, drop the rest.
	mc.LogOutput = warnFilter{logger}

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
			var picked net.IP
			for _, ip := range ips {
				// Debian-family boxes map their own hostname to 127.0.1.1
				// in /etc/hosts, and mDNS names on Macs can resolve to a
				// dead interface's self-assigned 169.254 address. Peers
				// can route to neither; advertising one quietly kills the
				// lantern for the whole swarm.
				if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
					continue
				}
				if picked == nil || (picked.To4() == nil && ip.To4() != nil) {
					picked = ip
				}
			}
			if picked == nil {
				picked, err = outboundIP()
				if err != nil {
					return nil, fmt.Errorf("advertise host %q resolves only to unroutable addresses and no route out was found: %w", ahost, err)
				}
				logger.Printf("advertise host %q resolves only to unroutable addresses, using this box's outbound address instead", ahost)
			}
			ahost = picked.String()
		}
		mc.AdvertiseAddr = ahost
		mc.AdvertisePort = aport
		logger.Printf("peers will reach this lantern at %s:%d", ahost, aport)
	}

	if cfg.Key != "" {
		// Derive a fixed-size cipher key so operators can pick any
		// passphrase instead of minting exactly 32 bytes.
		sum := sha256.Sum256([]byte(cfg.Key))
		mc.SecretKey = sum[:]
	}

	if cfg.HTTP != nil {
		mux, err := newMuxTransport(host, port, cfg.HTTP, log.New(warnFilter{logger}, "", 0))
		if err != nil {
			return nil, fmt.Errorf("starting the shared gossip and status port on %s: %w", cfg.Bind, err)
		}
		mc.Transport = mux
		logger.Printf("status clients answered on the gossip port itself")
	}

	ml, err := memberlist.Create(mc)
	if err != nil {
		return nil, fmt.Errorf("starting gossip on %s: %w", cfg.Bind, err)
	}
	s.ml = ml
	s.queue = &memberlist.TransmitLimitedQueue{
		NumNodes: ml.NumMembers,
		// Each queued flash chunk is transmitted RetransmitMult *
		// log10(N+1) times, each time to one random peer. At 3 that is
		// three sends into a swarm of five: one peer is structurally left
		// out of every chunk, and five unlucky rounds in a row age the
		// observation out on that peer, which showed up as random "?"
		// flickers on the panel. Four sends cover every peer of a small
		// swarm; push/pull state sync below repairs anything still lost.
		RetransmitMult: 4,
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

// SetState installs the anti-entropy source: a snapshot of everything this
// lantern currently believes, in flash envelope form. memberlist ships it
// to one random peer during every periodic push/pull sync, and the peer
// merges it like any flash. This is the repair path for gossip packets
// lost on the wire, and it hands a late joiner the full picture at once.
func (s *Swarm) SetState(fn func() []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = fn
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

// MemberAddrs maps each currently alive lantern to its advertised
// host:port, the address the swarm itself uses to reach it. A status
// client that can reach one lantern can derive every other door from
// this, NAT remaps and custom ports included.
func (s *Swarm) MemberAddrs() map[string]string {
	out := make(map[string]string)
	for _, n := range s.ml.Members() {
		out[n.Name] = net.JoinHostPort(n.Addr.String(), strconv.Itoa(int(n.Port)))
	}
	return out
}

// MemberVersions maps each currently alive lantern to the release it
// announced when it joined.
func (s *Swarm) MemberVersions() map[string]string {
	out := make(map[string]string)
	for _, n := range s.ml.Members() {
		out[n.Name] = string(n.Meta)
	}
	return out
}

// Forget removes a lantern from the swarm's memory, shrinking the quorum
// denominator. Only a graceful farewell earns this; the merely dead stay
// counted, which keeps a minority partition quiet.
func (s *Swarm) Forget(name string) {
	s.mu.Lock()
	if _, declared := s.expected[name]; !declared {
		delete(s.everSeen, name)
	}
	s.mu.Unlock()
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

// outboundIP asks the routing table which local address reaches the world.
// Dialing UDP sends no packet; it only makes the kernel pick the route.
func outboundIP() (net.IP, error) {
	conn, err := net.Dial("udp4", "1.1.1.1:53")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP, nil
}

// warnFilter lets memberlist's warnings and errors through to the operator
// log and swallows its debug chatter.
type warnFilter struct{ log *log.Logger }

func (w warnFilter) Write(p []byte) (int, error) {
	line := string(p)
	if strings.Contains(line, "[WARN]") || strings.Contains(line, "[ERR]") {
		w.log.Print("gossip: " + strings.TrimSpace(line))
	}
	return len(p), nil
}

// broadcast adapts a flash payload to memberlist's broadcast queue.
type broadcast struct{ payload []byte }

func (b broadcast) Invalidates(memberlist.Broadcast) bool { return false }
func (b broadcast) Message() []byte                       { return b.payload }
func (b broadcast) Finished()                             {}

// delegate carries flashes over memberlist's gossip messages.
type delegate struct{ s *Swarm }

// NodeMeta announces this lantern's release and platform, so the swarm can
// see version skew during rolling upgrades and operators can tell their
// architectures apart. Additive changes only: consumers split on spaces
// and ignore fields they do not know.
func (d *delegate) NodeMeta(limit int) []byte {
	v := version.Release + " " + runtime.GOOS + "/" + runtime.GOARCH + " " + version.Distro()
	if len(v) > limit {
		v = v[:limit]
	}
	return []byte(v)
}

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

// LocalState and MergeRemoteState ride memberlist's periodic push/pull
// sync (TCP, full state, one random peer per period). Gossip packets are
// best-effort UDP, so an unlucky chunk can miss a peer several rounds in a
// row; this exchange repairs those gaps and seeds late joiners instantly.
// The payload is an ordinary flash envelope, so merging is the same
// newest-wins path every flash takes, impersonation guard included.
func (d *delegate) LocalState(join bool) []byte {
	d.s.mu.Lock()
	fn := d.s.state
	d.s.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

func (d *delegate) MergeRemoteState(buf []byte, join bool) {
	if len(buf) == 0 {
		return
	}
	d.s.mu.Lock()
	fn := d.s.onFlash
	d.s.mu.Unlock()
	if fn != nil {
		fn(buf)
	}
}

// events keeps the ever-seen ledger that LastKnownSize reads.
type events struct{ s *Swarm }

func (e *events) NotifyJoin(n *memberlist.Node) {
	e.s.mu.Lock()
	e.s.everSeen[n.Name] = struct{}{}
	e.s.mu.Unlock()
	e.s.log.Printf("lantern %s joined the swarm", n.Name)
}

func (e *events) NotifyLeave(n *memberlist.Node) {
	// A graceful goodbye shrinks the swarm's memory: a decommissioned
	// lantern must stop counting toward quorum. A lantern that merely
	// stopped answering stays counted, which is what keeps a minority
	// partition quiet instead of screaming.
	if n.State == memberlist.StateLeft {
		e.s.mu.Lock()
		_, declared := e.s.expected[n.Name]
		if !declared {
			delete(e.s.everSeen, n.Name)
		}
		e.s.mu.Unlock()
		if declared {
			e.s.log.Printf("lantern %s left gracefully but is a declared member, kept and reported down", n.Name)
		} else {
			e.s.log.Printf("lantern %s left the swarm gracefully and is forgotten", n.Name)
		}
		return
	}
	e.s.log.Printf("lantern %s stopped answering", n.Name)
}

func (e *events) NotifyUpdate(n *memberlist.Node) {}
