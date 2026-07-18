package lantern

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/christianmeichtry/photinus/internal/check"
	"github.com/christianmeichtry/photinus/internal/quorum"
)

func TestReceiveFlashMerge(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	obs := func(observer string, state string, age time.Duration) quorum.Observation {
		return quorum.Observation{
			Observer: observer, Target: "db:5432", Check: "tcp",
			State: state, Seen: now.Add(-age),
		}
	}
	encode := func(t *testing.T, os ...quorum.Observation) []byte {
		t.Helper()
		b, err := json.Marshal(os)
		if err != nil {
			t.Fatalf("encoding flash: %v", err)
		}
		return b
	}

	tests := []struct {
		name    string
		preload []quorum.Observation
		flashes [][]quorum.Observation
		wantKey string
		want    quorum.Observation
		absent  bool
	}{
		{
			name:    "new observation is stored",
			flashes: [][]quorum.Observation{{obs("l2", quorum.StateDown, time.Second)}},
			wantKey: "l2|tcp|db:5432",
			want:    obs("l2", quorum.StateDown, time.Second),
		},
		{
			name:    "newer observation replaces older",
			flashes: [][]quorum.Observation{{obs("l2", quorum.StateDown, 10*time.Second)}, {obs("l2", quorum.StateUp, time.Second)}},
			wantKey: "l2|tcp|db:5432",
			want:    obs("l2", quorum.StateUp, time.Second),
		},
		{
			name:    "older observation does not roll back newer",
			flashes: [][]quorum.Observation{{obs("l2", quorum.StateUp, time.Second)}, {obs("l2", quorum.StateDown, 10*time.Second)}},
			wantKey: "l2|tcp|db:5432",
			want:    obs("l2", quorum.StateUp, time.Second),
		},
		{
			name:    "a peer claiming to be me is dropped, I am the authority on my own checks",
			flashes: [][]quorum.Observation{{obs("l1", quorum.StateDown, time.Second)}},
			wantKey: "l1|tcp|db:5432",
			absent:  true,
		},
		{
			name:    "my own stored observation survives an impersonating flash",
			preload: []quorum.Observation{obs("l1", quorum.StateUp, 2*time.Second)},
			flashes: [][]quorum.Observation{{obs("l1", quorum.StateDown, time.Second)}},
			wantKey: "l1|tcp|db:5432",
			want:    obs("l1", quorum.StateUp, 2*time.Second),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := New(Config{ID: "l1"})
			for _, o := range tt.preload {
				l.store[storeKey(o)] = o
			}
			for _, f := range tt.flashes {
				l.ReceiveFlash(encode(t, f...))
			}
			got, ok := l.store[tt.wantKey]
			if tt.absent {
				if ok {
					t.Fatalf("store[%s] = %+v, want it absent", tt.wantKey, got)
				}
				return
			}
			if !ok {
				t.Fatalf("store[%s] missing, want %+v", tt.wantKey, tt.want)
			}
			if got.State != tt.want.State || !got.Seen.Equal(tt.want.Seen) {
				t.Errorf("store[%s] = state %s seen %v, want state %s seen %v",
					tt.wantKey, got.State, got.Seen, tt.want.State, tt.want.Seen)
			}
		})
	}
}

func TestReceiveFlashGarbage(t *testing.T) {
	l := New(Config{ID: "l1"})
	l.ReceiveFlash([]byte("not json at all"))
	if len(l.store) != 0 {
		t.Errorf("garbage flash grew the store to %d entries, want 0", len(l.store))
	}
}

func TestReceiveFlashVersions(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	o := quorum.Observation{Observer: "l2", Target: "db:5432", Check: "tcp", State: quorum.StateDown, Seen: now}

	t.Run("current envelope is accepted", func(t *testing.T) {
		l := New(Config{ID: "l1"})
		payload, _ := json.Marshal(envelope{V: flashV, Obs: []quorum.Observation{o}})
		l.ReceiveFlash(payload)
		if len(l.store) != 1 {
			t.Errorf("store has %d entries, want 1", len(l.store))
		}
	})

	t.Run("legacy bare array still accepted, one release of grace", func(t *testing.T) {
		l := New(Config{ID: "l1"})
		payload, _ := json.Marshal([]quorum.Observation{o})
		l.ReceiveFlash(payload)
		if len(l.store) != 1 {
			t.Errorf("store has %d entries, want 1", len(l.store))
		}
	})

	t.Run("an unknown wire version is dropped, never guessed at", func(t *testing.T) {
		l := New(Config{ID: "l1"})
		payload, _ := json.Marshal(envelope{V: flashV + 1, Obs: []quorum.Observation{o}})
		l.ReceiveFlash(payload)
		l.ReceiveFlash(payload)
		if len(l.store) != 0 {
			t.Errorf("a future version's flash was merged: %d entries", len(l.store))
		}
	})
}

type pacedFake struct {
	runs   int
	every  time.Duration
	target string
}

func (p *pacedFake) Name() string         { return "fake" }
func (p *pacedFake) Target() string       { return p.target }
func (p *pacedFake) Every() time.Duration { return p.every }
func (p *pacedFake) Run(ctx context.Context) check.Result {
	p.runs++
	return check.Result{Verdict: check.OK, Detail: "ran"}
}

func TestPacedChecksRunOnTheirOwnCadence(t *testing.T) {
	fast := &pacedFake{every: 0, target: "fast"}
	slow := &pacedFake{every: time.Hour, target: "slow"}
	l := New(Config{ID: "l1", Interval: time.Second, Checks: []check.Check{fast, slow}})
	for i := 0; i < 3; i++ {
		l.flash(context.Background())
	}
	if fast.runs != 3 {
		t.Errorf("unpaced check ran %d times over 3 flashes, want 3", fast.runs)
	}
	if slow.runs != 1 {
		t.Errorf("hourly check ran %d times over 3 flashes, want 1", slow.runs)
	}
	// The slow check's verdict still rides every flash via the store.
	key := "l1|fake|slow"
	if o, ok := l.store[key]; !ok || o.TTL == 0 {
		t.Errorf("paced observation missing or without TTL: %+v", o)
	}
}

func TestSyncStateRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	l1 := New(Config{ID: "l1"})
	mine := quorum.Observation{Observer: "l1", Target: "l1", Check: "disk:/", State: quorum.StateWarn, Seen: now}
	heard := quorum.Observation{Observer: "l3", Target: "db:5432", Check: "tcp", State: quorum.StateDown, Seen: now}
	l1.store[storeKey(mine)] = mine
	l1.store[storeKey(heard)] = heard

	l2 := New(Config{ID: "l2"})
	l2.ReceiveFlash(l1.SyncState())
	if len(l2.store) != 2 {
		t.Fatalf("merged %d observations from a state sync, want 2", len(l2.store))
	}
	if got := l2.store[storeKey(heard)]; got.State != quorum.StateDown {
		t.Errorf("relayed observation lost in sync: %+v", got)
	}

	// A sync must not let a peer overwrite what the receiver knows about
	// itself: l1's view of l2 travels, l2's own word stays its own.
	imp := quorum.Observation{Observer: "l2", Target: "l2", Check: "disk:/", State: quorum.StateDown, Seen: now}
	l1.store[storeKey(imp)] = imp
	l2.ReceiveFlash(l1.SyncState())
	if _, ok := l2.store[storeKey(imp)]; ok {
		t.Error("a state sync smuggled in an observation claiming to be the receiver's own")
	}
}

func TestOversizedObservationsStillGossip(t *testing.T) {
	// memberlist's queue never transmits and never prunes a broadcast that
	// cannot fit the packet budget, so nothing over the limit may ever be
	// queued. The detail gets trimmed; the observation still travels.
	long := quorum.Observation{Observer: "l1", Target: "web:443", Check: "http",
		State: quorum.StateDown, Detail: strings.Repeat("x", 5000), Seen: time.Now()}
	fit, ok := fitForGossip(long, flashObsLimit)
	if !ok {
		t.Fatal("a long detail must be trimmed, not dropped")
	}
	if !strings.HasSuffix(fit.Detail, "...") || len(fit.Detail) >= 5000 {
		t.Errorf("detail not trimmed: %d bytes", len(fit.Detail))
	}
	payloads := chunkFlash([]quorum.Observation{fit}, 1000)
	if len(payloads) != 1 || len(payloads[0]) > 1000 {
		t.Fatalf("clamped observation still busts the packet budget: %d payloads, first %d bytes",
			len(payloads), len(payloads[0]))
	}

	// Quote-heavy details inflate when JSON escapes them; the clamp must
	// converge anyway.
	quoted := long
	quoted.Detail = strings.Repeat(`"\`, 2500)
	if fit, ok := fitForGossip(quoted, flashObsLimit); ok {
		if b, _ := json.Marshal(fit); len(b) > flashObsLimit {
			t.Errorf("escaped detail still oversized after clamp: %d bytes", len(b))
		}
	}

	// A giant target cannot be saved by dropping the detail and must be
	// reported unfit, never queued.
	bad := quorum.Observation{Observer: "l1", Target: strings.Repeat("t", 2000), Check: "http", Seen: time.Now()}
	if _, ok := fitForGossip(bad, flashObsLimit); ok {
		t.Error("an observation that cannot fit a packet claimed to fit")
	}
}

func TestFarewellBlocksSyncResurrection(t *testing.T) {
	now := time.Now().UTC()
	l1 := New(Config{ID: "l1"})
	ghost := quorum.Observation{Observer: "l3", Target: "db:5432", Check: "cert",
		State: quorum.StateDown, Seen: now.Add(-time.Minute), TTL: 18000}
	l1.store[storeKey(ghost)] = ghost

	l1.forget("l3")
	if len(l1.store) != 0 {
		t.Fatalf("forget left %d observations", len(l1.store))
	}

	// A peer that missed the farewell syncs the ghost back: refused.
	payload, _ := json.Marshal(envelope{V: flashV, Obs: []quorum.Observation{ghost}})
	l1.ReceiveFlash(payload)
	if len(l1.store) != 0 {
		t.Fatalf("a pre-departure observation resurrected through sync: %+v", l1.store)
	}

	// The lantern actually comes back: a post-departure observation is
	// accepted and clears the tombstone.
	back := ghost
	back.Seen = now.Add(time.Minute)
	payload, _ = json.Marshal(envelope{V: flashV, Obs: []quorum.Observation{back}})
	l1.ReceiveFlash(payload)
	if len(l1.store) != 1 {
		t.Fatalf("a returned lantern's fresh observation was refused")
	}
	if _, ok := l1.departed["l3"]; ok {
		t.Error("tombstone survived the lantern's return")
	}
}
