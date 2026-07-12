package quorum

import (
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	maxAge := 30 * time.Second

	// down and up build observations about the same subject.
	down := func(observer string, age time.Duration) Observation {
		return Observation{Observer: observer, Target: "db:5432", Check: "tcp", State: StateDown, Seen: now.Add(-age)}
	}
	up := func(observer string, age time.Duration) Observation {
		return Observation{Observer: observer, Target: "db:5432", Check: "tcp", State: StateUp, Seen: now.Add(-age)}
	}

	tests := []struct {
		name          string
		obs           []Observation
		lastKnownSize int
		wantState     string
		wantVotes     int
		wantVoters    int
		wantNeeded    int
	}{
		{
			name:          "two of two agree, alert fires",
			obs:           []Observation{down("l1", time.Second), down("l2", time.Second)},
			lastKnownSize: 2,
			wantState:     StateDown,
			wantVotes:     2,
			wantVoters:    2,
			wantNeeded:    2,
		},
		{
			name:          "one opinion in a swarm of two is not an outage",
			obs:           []Observation{down("l1", time.Second), up("l2", time.Second)},
			lastKnownSize: 2,
			wantState:     StateUp,
			wantVotes:     1,
			wantVoters:    2,
			wantNeeded:    2,
		},
		{
			name: "minority partition goes quiet: 6 of a remembered 13 all vote down",
			obs: []Observation{
				down("l1", time.Second), down("l2", time.Second), down("l3", time.Second),
				down("l4", time.Second), down("l5", time.Second), down("l6", time.Second),
			},
			lastKnownSize: 13,
			wantState:     StateUp,
			wantVotes:     6,
			wantVoters:    6,
			wantNeeded:    7,
		},
		{
			name: "majority partition still alerts: 7 of a remembered 13",
			obs: []Observation{
				down("l1", time.Second), down("l2", time.Second), down("l3", time.Second),
				down("l4", time.Second), down("l5", time.Second), down("l6", time.Second),
				down("l7", time.Second),
			},
			lastKnownSize: 13,
			wantState:     StateDown,
			wantVotes:     7,
			wantVoters:    7,
			wantNeeded:    7,
		},
		{
			name:          "stale observations stop counting",
			obs:           []Observation{down("l1", time.Second), down("l2", 5*time.Minute)},
			lastKnownSize: 2,
			wantState:     StateUp,
			wantVotes:     1,
			wantVoters:    1,
			wantNeeded:    2,
		},
		{
			name:          "newest observation per observer wins, recovery clears the vote",
			obs:           []Observation{down("l1", 10*time.Second), up("l1", time.Second), down("l2", time.Second)},
			lastKnownSize: 2,
			wantState:     StateUp,
			wantVotes:     1,
			wantVoters:    2,
			wantNeeded:    2,
		},
		{
			name: "observations about other subjects are ignored",
			obs: []Observation{
				down("l1", time.Second),
				{Observer: "l2", Target: "web:80", Check: "tcp", State: StateDown, Seen: now.Add(-time.Second)},
				{Observer: "l2", Target: "db:5432", Check: "http", State: StateDown, Seen: now.Add(-time.Second)},
			},
			lastKnownSize: 2,
			wantState:     StateUp,
			wantVotes:     1,
			wantVoters:    1,
			wantNeeded:    2,
		},
		{
			name:          "no observations, no outage",
			obs:           nil,
			lastKnownSize: 5,
			wantState:     StateUp,
			wantVotes:     0,
			wantVoters:    0,
			wantNeeded:    3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide("db:5432", "tcp", tt.obs, tt.lastKnownSize, maxAge, now)
			if got.Authority {
				t.Errorf("Authority = true for a remote subject, want false")
			}
			if got.State != tt.wantState {
				t.Errorf("State = %s, want %s", got.State, tt.wantState)
			}
			if got.Votes != tt.wantVotes {
				t.Errorf("Votes = %d, want %d", got.Votes, tt.wantVotes)
			}
			if got.Voters != tt.wantVoters {
				t.Errorf("Voters = %d, want %d", got.Voters, tt.wantVoters)
			}
			if got.Needed != tt.wantNeeded {
				t.Errorf("Needed = %d, want %d", got.Needed, tt.wantNeeded)
			}
		})
	}
}

func TestDecideWarn(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	maxAge := 30 * time.Second
	warn := func(observer string) Observation {
		return Observation{Observer: observer, Target: "l3", Check: "skew", State: StateWarn,
			Detail: "clock of l3 runs about 40s behind mine", Seen: now.Add(-time.Second)}
	}
	up := func(observer string) Observation {
		return Observation{Observer: observer, Target: "l3", Check: "skew", State: StateUp, Seen: now.Add(-time.Second)}
	}

	t.Run("quorum of warnings is a warning, not an outage", func(t *testing.T) {
		got := Decide("l3", "skew", []Observation{warn("l1"), warn("l2")}, 3, maxAge, now)
		if got.State != StateWarn {
			t.Errorf("State = %s, want %s", got.State, StateWarn)
		}
		if got.Detail == "" {
			t.Error("Detail is empty, the complaint should travel with the decision")
		}
	})

	t.Run("one warning without quorum stays up", func(t *testing.T) {
		got := Decide("l3", "skew", []Observation{warn("l1"), up("l2")}, 3, maxAge, now)
		if got.State != StateUp {
			t.Errorf("State = %s, want %s", got.State, StateUp)
		}
	})
}

func TestDecideAuthority(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	maxAge := 30 * time.Second

	// Local subject: lantern l1 reporting on its own disk. Target is l1.
	own := func(state string, age time.Duration) Observation {
		return Observation{Observer: "l1", Target: "l1", Check: "disk:/", State: state,
			Detail: "disk / is 91% full", Seen: now.Add(-age)}
	}
	hearsay := func(observer string, state string, age time.Duration) Observation {
		return Observation{Observer: observer, Target: "l1", Check: "disk:/", State: state, Seen: now.Add(-age)}
	}

	tests := []struct {
		name          string
		obs           []Observation
		lastKnownSize int
		wantState     string
		wantAuthority bool
		wantNeeded    int
	}{
		{
			name:          "the host's own warning is enough, even in a big swarm",
			obs:           []Observation{own(StateWarn, time.Second)},
			lastKnownSize: 13,
			wantState:     StateWarn,
			wantAuthority: true,
			wantNeeded:    1,
		},
		{
			name:          "hearsay never overrules the authority",
			obs:           []Observation{own(StateUp, time.Second), hearsay("l2", StateDown, time.Second), hearsay("l3", StateDown, time.Second)},
			lastKnownSize: 3,
			wantState:     StateUp,
			wantAuthority: true,
			wantNeeded:    1,
		},
		{
			name:          "a stale authority falls back to ordinary quorum",
			obs:           []Observation{own(StateWarn, 5*time.Minute), hearsay("l2", StateDown, time.Second)},
			lastKnownSize: 3,
			wantState:     StateUp,
			wantAuthority: false,
			wantNeeded:    2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide("l1", "disk:/", tt.obs, tt.lastKnownSize, maxAge, now)
			if got.State != tt.wantState {
				t.Errorf("State = %s, want %s", got.State, tt.wantState)
			}
			if got.Authority != tt.wantAuthority {
				t.Errorf("Authority = %v, want %v", got.Authority, tt.wantAuthority)
			}
			if got.Needed != tt.wantNeeded {
				t.Errorf("Needed = %d, want %d", got.Needed, tt.wantNeeded)
			}
		})
	}
}
