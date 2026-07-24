package swarm

import (
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
