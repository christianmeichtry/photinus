package lantern

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/christianmeichtry/photinus/internal/quorum"
)

func TestPulseStoresReceipt(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		pulses       map[string]time.Duration
		ping         string
		wantDeclared bool
		wantTTL      int
	}{
		{
			name:         "declared pulse carries its window as TTL",
			pulses:       map[string]time.Duration{"backup-db": 30 * time.Minute},
			ping:         "backup-db",
			wantDeclared: true,
			wantTTL:      1800,
		},
		{
			name:         "undeclared pulse is still recorded with the default TTL",
			pulses:       nil,
			ping:         "mystery-job",
			wantDeclared: false,
			wantTTL:      defaultPulseTTL,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := New(Config{ID: "l1", Pulses: tt.pulses})
			if got := l.Pulse(tt.ping, now); got != tt.wantDeclared {
				t.Errorf("Pulse() declared = %v, want %v", got, tt.wantDeclared)
			}
			o, ok := l.store["l1|pulse|"+tt.ping]
			if !ok {
				t.Fatalf("no receipt stored for pulse %s", tt.ping)
			}
			if o.State != quorum.StateUp {
				t.Errorf("receipt state = %s, want %s", o.State, quorum.StateUp)
			}
			if o.TTL != tt.wantTTL {
				t.Errorf("receipt TTL = %d, want %d", o.TTL, tt.wantTTL)
			}
			if !strings.Contains(o.Detail, "pulsed at") {
				t.Errorf("receipt detail = %q, want it to name the ping time", o.Detail)
			}
			if got := l.lastPulse[tt.ping]; !got.Equal(now) {
				t.Errorf("lastPulse = %v, want %v", got, now)
			}
		})
	}
}

func TestReceiveFlashLearnsPulse(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	receipt := func(observer, name, state string, seen time.Time) quorum.Observation {
		return quorum.Observation{
			Observer: observer, Target: name, Check: "pulse",
			State: state, Seen: seen,
		}
	}

	tests := []struct {
		name    string
		preset  time.Time
		flashes []quorum.Observation
		want    time.Time
	}{
		{
			name:    "a peer receipt sets the baseline",
			flashes: []quorum.Observation{receipt("l2", "backup-db", quorum.StateUp, now)},
			want:    now,
		},
		{
			name:    "an older receipt does not roll the baseline back",
			preset:  now,
			flashes: []quorum.Observation{receipt("l2", "backup-db", quorum.StateUp, now.Add(-time.Hour))},
			want:    now,
		},
		{
			name:    "a peer's down vote is not a ping",
			flashes: []quorum.Observation{receipt("l2", "backup-db", quorum.StateDown, now)},
			want:    time.Time{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := New(Config{ID: "l1"})
			if !tt.preset.IsZero() {
				l.lastPulse["backup-db"] = tt.preset
			}
			payload, err := json.Marshal(envelope{V: flashV, Obs: tt.flashes})
			if err != nil {
				t.Fatalf("encoding flash: %v", err)
			}
			l.ReceiveFlash(payload)
			if got := l.lastPulse["backup-db"]; !got.Equal(tt.want) {
				t.Errorf("lastPulse = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFlashVotesDownOnSilence(t *testing.T) {
	window := 10 * time.Second

	tests := []struct {
		name string
		// ages are relative to the flash; zero means untouched.
		startAge time.Duration
		pulseAge time.Duration
		wantDown bool
	}{
		{
			name:     "freshly started, never pulsed, still within the window",
			startAge: window / 2,
			wantDown: false,
		},
		{
			name:     "never pulsed and the window has passed since start",
			startAge: 3 * window,
			wantDown: true,
		},
		{
			name:     "pulsed recently, quiet",
			startAge: 3 * window,
			pulseAge: window / 2,
			wantDown: false,
		},
		{
			name:     "the last pulse is past the window",
			startAge: 5 * window,
			pulseAge: 2 * window,
			wantDown: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := New(Config{ID: "l1", Pulses: map[string]time.Duration{"backup-db": window}})
			now := time.Now().UTC()
			l.start = now.Add(-tt.startAge)
			if tt.pulseAge > 0 {
				l.lastPulse["backup-db"] = now.Add(-tt.pulseAge)
			}
			l.flash(context.Background())

			o, ok := l.store["l1|pulse|backup-db"]
			if !tt.wantDown {
				if ok && o.State == quorum.StateDown {
					t.Fatalf("silence within the window voted down: %+v", o)
				}
				return
			}
			if !ok {
				t.Fatal("silence past the window produced no down vote")
			}
			if o.State != quorum.StateDown {
				t.Errorf("vote state = %s, want %s", o.State, quorum.StateDown)
			}
			if !strings.Contains(o.Detail, "no pulse from backup-db") || !strings.Contains(o.Detail, "window is") {
				t.Errorf("vote detail = %q, want the age and the window in it", o.Detail)
			}
			if o.TTL != 0 {
				t.Errorf("vote TTL = %d, want 0 so it ages like any opinion", o.TTL)
			}
		})
	}
}

// TestPulseAfterSilenceClears pins the recovery path: a late job that
// finally pings replaces this lantern's down vote with a fresh receipt,
// because both live under the same store key.
func TestPulseAfterSilenceClears(t *testing.T) {
	window := 10 * time.Second
	l := New(Config{ID: "l1", Pulses: map[string]time.Duration{"backup-db": window}})
	now := time.Now().UTC()
	l.start = now.Add(-3 * window)
	l.flash(context.Background())
	if o := l.store["l1|pulse|backup-db"]; o.State != quorum.StateDown {
		t.Fatalf("no down vote before the late ping, got %+v", o)
	}
	l.Pulse("backup-db", time.Now().UTC())
	l.flash(context.Background())
	if o := l.store["l1|pulse|backup-db"]; o.State != quorum.StateUp {
		t.Errorf("state after a late ping = %s, want %s", o.State, quorum.StateUp)
	}
}
