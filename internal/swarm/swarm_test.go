package swarm

import (
	"github.com/hashicorp/memberlist"
	"io"
	"log"
	"testing"
	"time"
)

func TestReapForgetsDeadUndeclaredNodes(t *testing.T) {
	base := time.Now()
	newSwarm := func() *Swarm {
		return &Swarm{
			log:      log.New(io.Discard, "", 0),
			everSeen: map[string]struct{}{"self": {}},
			expected: map[string]struct{}{"jawa": {}},
			died:     map[string]time.Time{},
		}
	}

	t.Run("an undeclared node dead past the grace is forgotten", func(t *testing.T) {
		s := newSwarm()
		s.everSeen["pushdump"] = struct{}{}
		s.died["pushdump"] = base
		s.reap(base.Add(reapDeadAfter+time.Minute), map[string]bool{"self": true})
		if _, ok := s.everSeen["pushdump"]; ok {
			t.Error("a long-dead undeclared node was not reaped from the roster")
		}
		if _, ok := s.died["pushdump"]; ok {
			t.Error("the died stamp lingered after reaping")
		}
	})

	t.Run("within the grace it stays counted", func(t *testing.T) {
		s := newSwarm()
		s.everSeen["pushdump"] = struct{}{}
		s.died["pushdump"] = base
		s.reap(base.Add(reapDeadAfter-time.Minute), map[string]bool{"self": true})
		if _, ok := s.everSeen["pushdump"]; !ok {
			t.Error("a briefly-dead node was reaped too early (partition safety broken)")
		}
	})

	t.Run("a declared member is never reaped, however long it is dead", func(t *testing.T) {
		s := newSwarm()
		s.everSeen["jawa"] = struct{}{}
		s.died["jawa"] = base // should not happen (NotifyLeave never stamps declared), belt and braces
		s.reap(base.Add(100*reapDeadAfter), map[string]bool{"self": true})
		if _, ok := s.everSeen["jawa"]; !ok {
			t.Error("a declared member was reaped; -expect boxes must show down forever")
		}
	})

	t.Run("a node that came back is dropped from the reap list, kept in the roster", func(t *testing.T) {
		s := newSwarm()
		s.everSeen["straggler"] = struct{}{}
		s.died["straggler"] = base
		s.reap(base.Add(reapDeadAfter+time.Minute), map[string]bool{"self": true, "straggler": true})
		if _, ok := s.everSeen["straggler"]; !ok {
			t.Error("a revived node was reaped")
		}
		if _, ok := s.died["straggler"]; ok {
			t.Error("a revived node kept its death stamp")
		}
	})
}

// newQueueForTest mirrors the production queue settings against a swarm
// that never drains, which is exactly the condition on a host whose gossip
// falls behind: enqueue keeps running, transmissions do not.
func newQueueForTest() *Swarm {
	return &Swarm{
		queue: &memberlist.TransmitLimitedQueue{
			NumNodes:       func() int { return 6 },
			RetransmitMult: 4,
		},
		log: log.New(io.Discard, "", 0),
	}
}

func TestFlashNamedSupersedesQueuedChunk(t *testing.T) {
	s := newQueueForTest()
	// Two full flashes of eight chunks each, nothing draining in between.
	// The second flash must replace the first in the queue, not stack on
	// top of it: unbounded stacking is the leak that ate gigabytes on two
	// real hosts.
	for flash := 0; flash < 2; flash++ {
		for i := 0; i < 8; i++ {
			s.FlashNamed("flash/"+string(rune('0'+i)), []byte{byte(flash), byte(i)})
		}
		s.FlashNamed("push", []byte{byte(flash)})
	}
	if got := s.queue.NumQueued(); got != 9 {
		t.Fatalf("queue holds %d payloads after two undrained flashes, want 9 (8 chunks + push)", got)
	}
}

func TestFlashNamedKeepsTheFresherPayload(t *testing.T) {
	s := newQueueForTest()
	s.FlashNamed("flash/0", []byte("stale"))
	s.FlashNamed("flash/0", []byte("fresh"))
	got := s.queue.GetBroadcasts(0, 1400)
	if len(got) != 1 || string(got[0]) != "fresh" {
		t.Fatalf("queue transmitted %q, want the fresher payload only", got)
	}
}

func TestOneOffFlashIsNotSuperseded(t *testing.T) {
	s := newQueueForTest()
	// Farewells and forgets are one-offs: a later flash chunk must never
	// knock them out of the queue before they have been heard.
	s.Flash([]byte(`{"forget":"cert example.com:443"}`))
	for i := 0; i < 3; i++ {
		s.FlashNamed("flash/0", []byte{byte(i)})
	}
	if got := s.queue.NumQueued(); got != 2 {
		t.Fatalf("queue holds %d payloads, want 2 (the forget and one chunk)", got)
	}
}
