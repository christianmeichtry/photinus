package swarm

import "testing"

// A declared member is in the roster from the start, so a box that never
// joins is still counted and can be reported down, and a graceful leave
// does not forget it.
func TestExpectedMembersStay(t *testing.T) {
	s := &Swarm{
		everSeen: map[string]struct{}{"self": {}},
		expected: map[string]struct{}{},
	}
	for _, n := range []string{"ghost", "peer"} {
		s.everSeen[n] = struct{}{}
		s.expected[n] = struct{}{}
	}

	if got := len(s.everSeen); got != 3 {
		t.Fatalf("roster size = %d, want 3 (self + two declared)", got)
	}

	// A declared member's graceful farewell must not shrink the roster.
	s.Forget("ghost")
	if _, ok := s.everSeen["ghost"]; !ok {
		t.Error("declared member ghost was forgotten on Forget")
	}

	// An undeclared member is still forgettable.
	s.everSeen["visitor"] = struct{}{}
	s.Forget("visitor")
	if _, ok := s.everSeen["visitor"]; ok {
		t.Error("undeclared visitor should be forgotten")
	}
}
