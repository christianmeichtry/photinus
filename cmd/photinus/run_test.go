package main

import (
	"github.com/christianmeichtry/photinus/internal/notify"
	"strings"
	"testing"
	"time"
)

func TestParseWatchesPulse(t *testing.T) {
	tests := []struct {
		name       string
		watch      string
		wantName   string
		wantWindow time.Duration
		wantErr    bool
	}{
		{
			name:       "name alone gets the default window",
			watch:      "pulse:backup-db",
			wantName:   "backup-db",
			wantWindow: 25 * time.Hour,
		},
		{
			name:       "an explicit window is a Go duration",
			watch:      "pulse:backup-db:30m",
			wantName:   "backup-db",
			wantWindow: 30 * time.Minute,
		},
		{
			name:    "pulse needs a name",
			watch:   "pulse",
			wantErr: true,
		},
		{
			name:    "pulse with a trailing colon still needs a name",
			watch:   "pulse:",
			wantErr: true,
		},
		{
			name:    "a slash in the name is refused",
			watch:   "pulse:back/up",
			wantErr: true,
		},
		{
			name:    "a colon in the name misreads as a window and is refused",
			watch:   "pulse:back:up",
			wantErr: true,
		},
		{
			name:    "a window that is not a duration is refused",
			watch:   "pulse:backup-db:soon",
			wantErr: true,
		},
		{
			name:    "a negative window is refused",
			watch:   "pulse:backup-db:-5m",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks, pulses, err := parseWatches("l1", []string{tt.watch})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseWatches(%q) accepted, want an error", tt.watch)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWatches(%q): %v", tt.watch, err)
			}
			if len(checks) != 0 {
				t.Errorf("a pulse declaration produced %d checks, want 0: a pulse waits, it does not run", len(checks))
			}
			got, ok := pulses[tt.wantName]
			if !ok {
				t.Fatalf("pulse %q not declared, got %v", tt.wantName, pulses)
			}
			if got != tt.wantWindow {
				t.Errorf("window = %v, want %v", got, tt.wantWindow)
			}
		})
	}
}

func TestPulseNameBounds(t *testing.T) {
	long := strings.Repeat("a", 101)
	bad := []string{
		"pulse:" + long,
		"pulse:has space",
		"pulse:pa%2Fth",
		"pulse:l1", // the lantern's own name, rule 4 collision
	}
	for _, w := range bad {
		if _, _, err := parseWatches("l1", []string{w}); err == nil {
			t.Errorf("parseWatches accepted %q", w)
		}
	}
	if _, _, err := parseWatches("l1", []string{"pulse:backup-db.daily_v2:30m"}); err != nil {
		t.Errorf("a reasonable name was refused: %v", err)
	}
}

// FuzzParseWatches asserts flag parsing never panics on hostile input.
func FuzzParseWatches(f *testing.F) {
	for _, s := range []string{"tcp:h:1", "http:https://x", "cert:h:443:10",
		"disk:/:90", "pulse:a:26h", "net:100", "uptime:3m", "::::", "pulse:", "cert:"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, w string) {
		parseWatches("l1", []string{w})
	})
}

func TestFanOut(t *testing.T) {
	ev := notify.Event{Kind: "down", Check: "tcp", Target: "db:5432", Detail: "down"}

	t.Run("both channels see the event", func(t *testing.T) {
		var first, second []notify.Event
		send := fanOut([]notify.Sender{
			func(e notify.Event) { first = append(first, e) },
			func(e notify.Event) { second = append(second, e) },
		})
		send(ev)
		if len(first) != 1 || len(second) != 1 {
			t.Fatalf("channels saw %d and %d events, want 1 each", len(first), len(second))
		}
		if first[0] != ev || second[0] != ev {
			t.Errorf("channels saw %+v and %+v, want %+v", first[0], second[0], ev)
		}
	})

	t.Run("a single channel is passed through untouched", func(t *testing.T) {
		var got []notify.Event
		one := notify.Sender(func(e notify.Event) { got = append(got, e) })
		send := fanOut([]notify.Sender{one})
		send(ev)
		if len(got) != 1 {
			t.Fatalf("channel saw %d events, want 1", len(got))
		}
	})
}
