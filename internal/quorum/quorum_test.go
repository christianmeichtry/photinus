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
		return Observation{Observer: observer, Target: "db:5432", Check: "tcp", Healthy: false, Seen: now.Add(-age)}
	}
	up := func(observer string, age time.Duration) Observation {
		return Observation{Observer: observer, Target: "db:5432", Check: "tcp", Healthy: true, Seen: now.Add(-age)}
	}

	tests := []struct {
		name          string
		obs           []Observation
		lastKnownSize int
		wantDown      bool
		wantVotes     int
		wantVoters    int
		wantNeeded    int
	}{
		{
			name:          "two of two agree, alert fires",
			obs:           []Observation{down("l1", time.Second), down("l2", time.Second)},
			lastKnownSize: 2,
			wantDown:      true,
			wantVotes:     2,
			wantVoters:    2,
			wantNeeded:    2,
		},
		{
			name:          "one opinion in a swarm of two is not an outage",
			obs:           []Observation{down("l1", time.Second), up("l2", time.Second)},
			lastKnownSize: 2,
			wantDown:      false,
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
			wantDown:      false,
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
			wantDown:      true,
			wantVotes:     7,
			wantVoters:    7,
			wantNeeded:    7,
		},
		{
			name:          "stale observations stop counting",
			obs:           []Observation{down("l1", time.Second), down("l2", 5*time.Minute)},
			lastKnownSize: 2,
			wantDown:      false,
			wantVotes:     1,
			wantVoters:    1,
			wantNeeded:    2,
		},
		{
			name:          "newest observation per observer wins, recovery clears the vote",
			obs:           []Observation{down("l1", 10*time.Second), up("l1", time.Second), down("l2", time.Second)},
			lastKnownSize: 2,
			wantDown:      false,
			wantVotes:     1,
			wantVoters:    2,
			wantNeeded:    2,
		},
		{
			name: "observations about other subjects are ignored",
			obs: []Observation{
				down("l1", time.Second),
				{Observer: "l2", Target: "web:80", Check: "tcp", Healthy: false, Seen: now.Add(-time.Second)},
				{Observer: "l2", Target: "db:5432", Check: "http", Healthy: false, Seen: now.Add(-time.Second)},
			},
			lastKnownSize: 2,
			wantDown:      false,
			wantVotes:     1,
			wantVoters:    1,
			wantNeeded:    2,
		},
		{
			name:          "no observations, no outage",
			obs:           nil,
			lastKnownSize: 5,
			wantDown:      false,
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
			if got.Down != tt.wantDown {
				t.Errorf("Down = %v, want %v", got.Down, tt.wantDown)
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

func TestDecideAuthority(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	maxAge := 30 * time.Second

	// Local subject: lantern l1 reporting on its own disk. Target is l1.
	own := func(healthy bool, age time.Duration) Observation {
		return Observation{Observer: "l1", Target: "l1", Check: "disk:/", Healthy: healthy, Seen: now.Add(-age)}
	}
	hearsay := func(observer string, healthy bool, age time.Duration) Observation {
		return Observation{Observer: observer, Target: "l1", Check: "disk:/", Healthy: healthy, Seen: now.Add(-age)}
	}

	tests := []struct {
		name          string
		obs           []Observation
		lastKnownSize int
		wantDown      bool
		wantAuthority bool
		wantNeeded    int
	}{
		{
			name:          "the host's own word is enough, even in a big swarm",
			obs:           []Observation{own(false, time.Second)},
			lastKnownSize: 13,
			wantDown:      true,
			wantAuthority: true,
			wantNeeded:    1,
		},
		{
			name:          "hearsay never overrules the authority",
			obs:           []Observation{own(true, time.Second), hearsay("l2", false, time.Second), hearsay("l3", false, time.Second)},
			lastKnownSize: 3,
			wantDown:      false,
			wantAuthority: true,
			wantNeeded:    1,
		},
		{
			name:          "a stale authority falls back to ordinary quorum",
			obs:           []Observation{own(false, 5*time.Minute), hearsay("l2", false, time.Second)},
			lastKnownSize: 3,
			wantDown:      false,
			wantAuthority: false,
			wantNeeded:    2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide("l1", "disk:/", tt.obs, tt.lastKnownSize, maxAge, now)
			if got.Down != tt.wantDown {
				t.Errorf("Down = %v, want %v", got.Down, tt.wantDown)
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
