package check

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestUptimeThreshold(t *testing.T) {
	tests := []struct {
		name   string
		uptime time.Duration
		min    time.Duration
		want   Verdict
	}{
		{"fresh reboot trips", 30 * time.Second, 3 * time.Minute, Warn},
		{"long uptime passes", 40 * 24 * time.Hour, 3 * time.Minute, OK},
		{"exactly at threshold passes", 3 * time.Minute, 3 * time.Minute, OK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := &Uptime{Host: "l1", Min: tt.min, probe: func() (time.Duration, error) { return tt.uptime, nil }}
			if got := u.Run(context.Background()); got.Verdict != tt.want {
				t.Errorf("verdict = %s, want %s (detail: %s)", got.Verdict, tt.want, got.Detail)
			}
		})
	}

	t.Run("probe error is not an outage", func(t *testing.T) {
		u := &Uptime{Host: "l1", probe: func() (time.Duration, error) { return 0, errUnsupported }}
		if got := u.Run(context.Background()); got.Verdict != NotApplicable {
			t.Errorf("verdict = %s, want %s", got.Verdict, NotApplicable)
		}
	})
}

func TestDiskThreshold(t *testing.T) {
	tests := []struct {
		name string
		used float64
		max  float64
		want Verdict
	}{
		{"nearly full trips", 95, 90, Warn},
		{"half full passes", 50, 90, OK},
		{"default threshold is 90", 92, 0, Warn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Disk{Host: "l1", Path: "/", Max: tt.max, probe: func(string) (diskUsage, error) {
				return diskUsage{usedPercent: tt.used, freeBytes: 5 << 30}, nil
			}}
			if got := d.Run(context.Background()); got.Verdict != tt.want {
				t.Errorf("verdict = %s, want %s (detail: %s)", got.Verdict, tt.want, got.Detail)
			}
		})
	}

	if got, want := (&Disk{Host: "l1", Path: "/data"}).Name(), "disk:/data"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

func TestCPUThreshold(t *testing.T) {
	run := func(pct float64, ready bool, max float64) Result {
		c := &CPU{Host: "l1", Max: max, probe: func() (float64, bool, error) { return pct, ready, nil }}
		return c.Run(context.Background())
	}
	if got := run(99, true, 95); got.Verdict != Warn {
		t.Errorf("busy cpu: verdict = %s, want %s", got.Verdict, Warn)
	}
	if got := run(20, true, 95); got.Verdict != OK {
		t.Errorf("idle cpu: verdict = %s, want %s", got.Verdict, OK)
	}
	got := run(0, false, 95)
	if got.Verdict != OK || !strings.Contains(got.Detail, "warming up") {
		t.Errorf("first sample: got %s %q, want OK about warming up", got.Verdict, got.Detail)
	}
}

func TestMemoryThreshold(t *testing.T) {
	run := func(used, total uint64, max float64) Result {
		m := &Memory{Host: "l1", Max: max, probe: func() (memUsage, error) {
			return memUsage{usedBytes: used, totalBytes: total}, nil
		}}
		return m.Run(context.Background())
	}
	if got := run(31<<30, 32<<30, 95); got.Verdict != Warn {
		t.Errorf("nearly full memory: verdict = %s, want %s", got.Verdict, Warn)
	}
	if got := run(16<<30, 32<<30, 95); got.Verdict != OK {
		t.Errorf("half used memory: verdict = %s, want %s", got.Verdict, OK)
	}
}

func TestSwapThreshold(t *testing.T) {
	run := func(used, total uint64, max float64) Result {
		s := &Swap{Host: "l1", Max: max, probe: func() (swapUsage, error) {
			return swapUsage{usedBytes: used, totalBytes: total}, nil
		}}
		return s.Run(context.Background())
	}
	if got := run(7<<30, 8<<30, 80); got.Verdict != Warn {
		t.Errorf("nearly full swap: verdict = %s, want %s", got.Verdict, Warn)
	}
	if got := run(1<<30, 8<<30, 80); got.Verdict != OK {
		t.Errorf("light swap: verdict = %s, want %s", got.Verdict, OK)
	}
	got := run(0, 0, 80)
	if got.Verdict != OK || !strings.Contains(got.Detail, "no swap") {
		t.Errorf("swapless host: got %s %q, want OK about no swap", got.Verdict, got.Detail)
	}
}

// TestProbesOnThisPlatform runs the real probes once. On linux and darwin
// they must answer; anywhere else they must degrade to NotApplicable.
func TestProbesOnThisPlatform(t *testing.T) {
	ctx := context.Background()
	checks := []Check{
		&Uptime{Host: "l1"},
		&Disk{Host: "l1", Path: "/"},
		&CPU{Host: "l1"},
		&Memory{Host: "l1"},
		&Swap{Host: "l1"},
		&Net{Host: "l1"},
	}
	for _, c := range checks {
		res := c.Run(ctx)
		if res.Detail == "" {
			t.Errorf("%s: empty detail, operators need a sentence", c.Name())
		}
		t.Logf("%-8s %-14s %s", c.Name(), res.Verdict, res.Detail)
	}

	// The cpu and net probes need a second sample on platforms that
	// measure deltas.
	cpu := &CPU{Host: "l1"}
	cpu.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	res := cpu.Run(ctx)
	t.Logf("%-8s %-14s %s (second sample)", cpu.Name(), res.Verdict, res.Detail)
	netc := &Net{Host: "l1"}
	netc.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	res = netc.Run(ctx)
	t.Logf("%-8s %-14s %s (second sample)", netc.Name(), res.Verdict, res.Detail)
}

func TestNetRate(t *testing.T) {
	run := func(rx, tx float64, ready bool, max float64) Result {
		n := &Net{Host: "l1", Max: max, probe: func() (float64, float64, string, bool, error) {
			return rx, tx, "eth0", ready, nil
		}}
		return n.Run(context.Background())
	}
	got := run(2<<20, 100<<10, true, 0)
	if got.Verdict != OK || !strings.Contains(got.Detail, "eth0") || !strings.Contains(got.Detail, "in") {
		t.Errorf("quiet net: got %s %q, want OK naming the interface and both directions", got.Verdict, got.Detail)
	}
	// 20 MB/s each way is about 335 Mbit/s combined, past a 100 Mbit/s limit.
	if got := run(20<<20, 20<<20, true, 100); got.Verdict != Warn {
		t.Errorf("busy net with a threshold: verdict = %s, want %s (%s)", got.Verdict, Warn, got.Detail)
	}
	if got := run(20<<20, 20<<20, true, 0); got.Verdict != OK {
		t.Errorf("busy net without a threshold: verdict = %s, want %s, the rate is information", got.Verdict, OK)
	}
	got = run(0, 0, false, 0)
	if got.Verdict != OK || !strings.Contains(got.Detail, "warming up") {
		t.Errorf("first sample: got %s %q, want OK about warming up", got.Verdict, got.Detail)
	}
	n := &Net{Host: "l1", probe: func() (float64, float64, string, bool, error) {
		return 0, 0, "", false, errUnsupported
	}}
	if got := n.Run(context.Background()); got.Verdict != NotApplicable {
		t.Errorf("unsupported platform: verdict = %s, want %s", got.Verdict, NotApplicable)
	}
}

func TestHysteresis(t *testing.T) {
	// The band: trip above max, hold through the band, clear only below it.
	steps := []struct {
		pct  float64
		want Verdict
	}{
		{90, OK},   // below everything
		{96, Warn}, // crosses max 95
		{91, Warn}, // in the band: still warned, no cleared page
		{88, Warn}, // still in the band
		{84, OK},   // below clear 85: genuinely receded
		{91, OK},   // band entered from below: stays ok
	}
	c := &CPU{Host: "l1", Max: 95}
	for i, st := range steps {
		c.probe = func() (float64, bool, error) { return st.pct, true, nil }
		if got := c.Run(context.Background()); got.Verdict != st.want {
			t.Fatalf("step %d (pct %.0f): verdict = %s, want %s (%s)", i, st.pct, got.Verdict, st.want, got.Detail)
		}
	}

	// Net uses a ratio band on its Mbit limit.
	n := &Net{Host: "l1", Max: 100}
	for i, st := range []struct {
		mbps float64
		want Verdict
	}{{101, Warn}, {90, Warn}, {84, OK}} {
		v := st.mbps * 1e6 / 8 / 2
		n.probe = func() (float64, float64, string, bool, error) { return v, v, "eth0", true, nil }
		if got := n.Run(context.Background()); got.Verdict != st.want {
			t.Fatalf("net step %d: verdict = %s, want %s (%s)", i, got.Verdict, st.want, got.Detail)
		}
	}
}
