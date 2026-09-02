package lantern

import (
	"strings"
	"testing"

	"github.com/christianmeichtry/photinus/internal/quorum"
)

// sub is one watched subject in the state the swarm agreed on.
func sub(checkName, target, state string) SubjectStatus {
	return SubjectStatus{Decision: quorum.Decision{Check: checkName, Target: target, State: state}}
}

func TestBlackout(t *testing.T) {
	const ewok, park = "178.105.34.71", "128.65.195.180"

	// The real morning that motivated this: apache wedged on one host and
	// took every vhost with it, each subject routine, no page.
	ewokDark := []SubjectStatus{
		sub("http", "https://agence360.ch", quorum.StateDown),
		sub("cert", "agence360.ch:443", quorum.StateDown),
		sub("http", "https://cercletheatral.ch", quorum.StateDown),
		sub("cert", "cercletheatral.ch:443", quorum.StateDown),
		sub("http", "https://bajalocation.ch", quorum.StateDown),
		sub("cert", "bajalocation.ch:443", quorum.StateDown),
	}
	ewokAddrs := map[string]string{
		"http https://agence360.ch": ewok, "cert agence360.ch:443": ewok,
		"http https://cercletheatral.ch": ewok, "cert cercletheatral.ch:443": ewok,
		"http https://bajalocation.ch": ewok, "cert bajalocation.ch:443": ewok,
	}

	tests := []struct {
		name     string
		subjects []SubjectStatus
		addrs    map[string]string
		min      int
		want     []string // the addresses expected to be declared dark
	}{
		{
			name:     "a whole host going dark is one blackout, not six failures",
			subjects: ewokDark,
			addrs:    ewokAddrs,
			min:      3,
			want:     []string{ewok},
		},
		{
			name: "one healthy service at the address means the host is not dark",
			subjects: append(append([]SubjectStatus{}, ewokDark...),
				sub("http", "https://affaires-classees.ch", quorum.StateUp)),
			addrs: withAddr(ewokAddrs, "http https://affaires-classees.ch", ewok),
			min:   3,
			want:  nil,
		},
		{
			name: "one site's http and cert are one site, never a blackout",
			subjects: []SubjectStatus{
				sub("http", "https://agence360.ch", quorum.StateDown),
				sub("cert", "agence360.ch:443", quorum.StateDown),
			},
			addrs: map[string]string{
				"http https://agence360.ch": ewok, "cert agence360.ch:443": ewok,
			},
			min:  2, // even with the count lowered, two subjects of one site do not qualify
			want: nil,
		},
		{
			name:     "fewer dark services than the minimum stays per-subject business",
			subjects: ewokDark[:2],
			addrs:    ewokAddrs,
			min:      3,
			want:     nil,
		},
		{
			// No live observations reads as up with nobody voting. It is
			// not evidence of darkness, so it must not help declare one.
			name: "a subject nobody has a live word on blocks the call",
			subjects: append(append([]SubjectStatus{}, ewokDark...),
				SubjectStatus{Decision: quorum.Decision{Check: "http", Target: "https://affaires-classees.ch", State: quorum.StateUp, Voters: 0}}),
			addrs: withAddr(ewokAddrs, "http https://affaires-classees.ch", ewok),
			min:   3,
			want:  nil,
		},
		{
			name: "a warning is not darkness",
			subjects: append(append([]SubjectStatus{}, ewokDark...),
				sub("cert", "affaires-classees.ch:443", quorum.StateWarn)),
			addrs: withAddr(ewokAddrs, "cert affaires-classees.ch:443", ewok),
			min:   3,
			want:  nil,
		},
		{
			name:     "subjects the lantern never reached carry no address and no vote",
			subjects: ewokDark,
			addrs:    map[string]string{},
			min:      3,
			want:     nil,
		},
		{
			name: "two hosts dark at once are two blackouts",
			subjects: append(append([]SubjectStatus{}, ewokDark...),
				sub("http", "https://photinus.dev", quorum.StateDown),
				sub("cert", "photinus.dev:443", quorum.StateDown),
				sub("http", "https://example.dev", quorum.StateDown)),
			addrs: withAddr(withAddr(withAddr(ewokAddrs,
				"http https://photinus.dev", park),
				"cert photinus.dev:443", park),
				"http https://example.dev", park),
			min:  3,
			want: []string{park, ewok},
		},
		{
			name:     "the rule switched off says nothing",
			subjects: ewokDark,
			addrs:    ewokAddrs,
			min:      0,
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []quorum.Decision
			for _, d := range blackout(tt.subjects, tt.addrs, tt.min) {
				if d.State == quorum.StateDown {
					got = append(got, d)
				}
			}
			if len(got) != len(tt.want) {
				t.Fatalf("declared %d blackouts, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, d := range got {
				if d.Target != tt.want[i] {
					t.Errorf("blackout %d at %q, want %q", i, d.Target, tt.want[i])
				}
				if d.Check != "blackout" || d.State != quorum.StateDown {
					t.Errorf("blackout %d is %s/%s, want blackout/down", i, d.Check, d.State)
				}
			}
		})
	}
}

func TestBlackoutDetailNamesTheSites(t *testing.T) {
	const addr = "178.105.34.71"
	got := blackout([]SubjectStatus{
		sub("http", "https://agence360.ch", quorum.StateDown),
		sub("cert", "agence360.ch:443", quorum.StateDown),
		sub("http", "https://cercletheatral.ch/", quorum.StateDown),
	}, map[string]string{
		"http https://agence360.ch": addr, "cert agence360.ch:443": addr,
		"http https://cercletheatral.ch/": addr,
	}, 3)
	if len(got) != 1 || got[0].State != quorum.StateDown {
		t.Fatalf("declared %d blackouts, want 1 dark one: %+v", len(got), got)
	}
	// The operator reads the sentence on a phone: it must say how big this
	// is and name the sites once each, not once per check.
	d := got[0].Detail
	for _, want := range []string{"3 across 2 sites", "agence360.ch", "cercletheatral.ch"} {
		if !strings.Contains(d, want) {
			t.Errorf("detail %q does not mention %q", d, want)
		}
	}
	if strings.Count(d, "agence360.ch") != 1 {
		t.Errorf("detail %q names a site more than once", d)
	}
}

func TestSiteOf(t *testing.T) {
	tests := map[string]string{
		"https://agence360.ch":        "agence360.ch",
		"https://agence360.ch/":       "agence360.ch",
		"http://agence360.ch/deep/er": "agence360.ch",
		"agence360.ch:443":            "agence360.ch",
		"db.example.com:5432":         "db.example.com:5432", // a meaningful port stays
	}
	for in, want := range tests {
		if got := siteOf(in); got != want {
			t.Errorf("siteOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func withAddr(m map[string]string, k, v string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for kk, vv := range m {
		out[kk] = vv
	}
	out[k] = v
	return out
}

// TestBlackoutSpeaksWhileHealthySoItCanEnd is the bug the first live run
// caught: when the machine came back, the blackout subject vanished from
// the decisions instead of turning up. Nothing then closed the alarm, and
// a second blackout at the same address would have looked like no change
// at all and paged nobody.
func TestBlackoutSpeaksWhileHealthySoItCanEnd(t *testing.T) {
	const addr = "178.105.34.71"
	addrs := map[string]string{
		"http https://agence360.ch": addr, "cert agence360.ch:443": addr,
		"http https://cercletheatral.ch": addr,
	}
	healthy := []SubjectStatus{
		sub("http", "https://agence360.ch", quorum.StateUp),
		sub("cert", "agence360.ch:443", quorum.StateUp),
		sub("http", "https://cercletheatral.ch", quorum.StateUp),
	}
	got := blackout(healthy, addrs, 3)
	if len(got) != 1 {
		t.Fatalf("a blackout-capable address must be spoken about even when healthy, got %+v", got)
	}
	if got[0].State != quorum.StateUp {
		t.Errorf("state = %q, want up", got[0].State)
	}
	if got[0].Voters == 0 {
		t.Error("a healthy blackout decision with no voters reads as unknown and the tracker would ignore it")
	}

	// An address too small to ever go dark stays out of the conversation
	// entirely, rather than parading as a permanently healthy subject.
	small := []SubjectStatus{sub("http", "https://only.example", quorum.StateUp)}
	if got := blackout(small, map[string]string{"http https://only.example": addr}, 3); len(got) != 0 {
		t.Errorf("an address with one watched service produced %+v, want nothing", got)
	}
}
