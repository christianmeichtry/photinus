package lantern

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/christianmeichtry/photinus/internal/quorum"
)

func TestLivenessObservations(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	l := New(Config{ID: "jawa"})

	roster := []string{"jawa", "ewok", "drongar", "bespin"}
	alive := []string{"jawa", "ewok", "bespin"}

	obs := l.livenessObservations(alive, roster, now)

	got := make(map[string]string)
	for _, o := range obs {
		if o.Check != "lantern" {
			t.Errorf("check = %q, want lantern", o.Check)
		}
		if o.Observer != "jawa" {
			t.Errorf("observer = %q, want jawa", o.Observer)
		}
		if o.Observer == o.Target {
			t.Error("a lantern must never report on its own liveness, that would be authority")
		}
		got[o.Target] = o.State
	}

	want := map[string]string{
		"ewok":    quorum.StateUp,
		"bespin":  quorum.StateUp,
		"drongar": quorum.StateDown,
	}
	if len(got) != len(want) {
		t.Fatalf("observations about %v, want %v", got, want)
	}
	for peer, state := range want {
		if got[peer] != state {
			t.Errorf("%s = %s, want %s", peer, got[peer], state)
		}
	}
}

func TestChunkFlash(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	obs := make([]quorum.Observation, 14)
	for i := range obs {
		obs[i] = quorum.Observation{
			Observer: "jawa", Target: "ewok.atelier-agile.ch", Check: "lantern",
			State: quorum.StateUp, Detail: "answering gossip", Seen: now,
		}
	}

	const limit = 1000
	payloads := chunkFlash(obs, limit)
	if len(payloads) < 2 {
		t.Fatalf("14 observations fit one packet (%d payloads), the split is not happening", len(payloads))
	}

	total := 0
	for i, p := range payloads {
		if len(p) > limit {
			t.Errorf("payload %d is %d bytes, over the %d limit", i, len(p), limit)
		}
		var back []quorum.Observation
		if err := json.Unmarshal(p, &back); err != nil {
			t.Fatalf("payload %d does not parse: %v", i, err)
		}
		total += len(back)
	}
	if total != len(obs) {
		t.Errorf("%d observations across all payloads, want %d: the split loses data", total, len(obs))
	}

	if got := chunkFlash(nil, limit); len(got) != 0 {
		t.Errorf("no observations produced %d payloads, want none", len(got))
	}
}
