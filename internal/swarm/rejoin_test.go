package swarm

import (
	"errors"
	"io"
	"log"
	"sync"
	"testing"
	"time"
)

// fakeRejoiner stands in for memberlist so the rejoin loop can be driven
// deterministically: it counts Join calls and reports a fixed membership.
type fakeRejoiner struct {
	mu      sync.Mutex
	members int
	joinN   int
	joinErr error
	calls   int
}

func (f *fakeRejoiner) NumMembers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.members
}

func (f *fakeRejoiner) Join([]string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.joinN, f.joinErr
}

func (f *fakeRejoiner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// The reaped-but-online partition: a lantern briefly drops off the network,
// its peers reap it, but from its own side it still sees the whole swarm. The
// old loop exited for good the moment it was joined, so such a box sat online
// and reported down until a human restarted it. The loop must instead keep
// re-joining the seeds on its heartbeat, whatever its own membership count
// says, so the swarm gets it back on its own.
func TestRejoinLoopKeepsReintroducing(t *testing.T) {
	cases := []struct {
		name    string
		members int // what this node sees from its own side
		joinN   int
		joinErr error
	}{
		{"reaped but still sees the swarm", 5, 5, nil}, // the bug: old loop stopped here
		{"healthy swarm still heartbeats", 3, 3, nil},
		{"alone keeps retrying the seeds", 1, 0, errors.New("no seed reachable")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRejoiner{members: tc.members, joinN: tc.joinN, joinErr: tc.joinErr}
			s := &Swarm{log: log.New(io.Discard, "", 0), stop: make(chan struct{})}

			done := make(chan struct{})
			go func() {
				s.rejoinLoop(f, []string{"seed:7946"}, time.Millisecond, time.Millisecond)
				close(done)
			}()

			time.Sleep(40 * time.Millisecond)
			close(s.stop)

			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("rejoinLoop did not return after stop was closed")
			}

			if c := f.callCount(); c < 3 {
				t.Fatalf("Join called %d times; the loop must keep re-joining (>=3), not stop once joined", c)
			}
		})
	}
}
