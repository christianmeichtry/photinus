package lantern

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/christianmeichtry/photinus/internal/quorum"
)

func TestReceiveFlashMerge(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	obs := func(observer string, healthy bool, age time.Duration) quorum.Observation {
		return quorum.Observation{
			Observer: observer, Target: "db:5432", Check: "tcp",
			Healthy: healthy, Seen: now.Add(-age),
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
			flashes: [][]quorum.Observation{{obs("l2", false, time.Second)}},
			wantKey: "l2|tcp|db:5432",
			want:    obs("l2", false, time.Second),
		},
		{
			name:    "newer observation replaces older",
			flashes: [][]quorum.Observation{{obs("l2", false, 10*time.Second)}, {obs("l2", true, time.Second)}},
			wantKey: "l2|tcp|db:5432",
			want:    obs("l2", true, time.Second),
		},
		{
			name:    "older observation does not roll back newer",
			flashes: [][]quorum.Observation{{obs("l2", true, time.Second)}, {obs("l2", false, 10*time.Second)}},
			wantKey: "l2|tcp|db:5432",
			want:    obs("l2", true, time.Second),
		},
		{
			name:    "a peer claiming to be me is dropped, I am the authority on my own checks",
			flashes: [][]quorum.Observation{{obs("l1", false, time.Second)}},
			wantKey: "l1|tcp|db:5432",
			absent:  true,
		},
		{
			name:    "my own stored observation survives an impersonating flash",
			preload: []quorum.Observation{obs("l1", true, 2*time.Second)},
			flashes: [][]quorum.Observation{{obs("l1", false, time.Second)}},
			wantKey: "l1|tcp|db:5432",
			want:    obs("l1", true, 2*time.Second),
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
			if got.Healthy != tt.want.Healthy || !got.Seen.Equal(tt.want.Seen) {
				t.Errorf("store[%s] = healthy %v seen %v, want healthy %v seen %v",
					tt.wantKey, got.Healthy, got.Seen, tt.want.Healthy, tt.want.Seen)
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
