//go:build linux

package check

import "testing"

func TestParseMeminfo(t *testing.T) {
	data := []byte(`MemTotal:       16384000 kB
MemFree:          512000 kB
MemAvailable:    8192000 kB
SwapTotal:       4096000 kB
SwapFree:        4095000 kB
`)
	vals, err := parseMeminfo(data, "MemTotal", "MemAvailable", "SwapTotal", "SwapFree")
	if err != nil {
		t.Fatalf("parseMeminfo: %v", err)
	}
	if got, want := vals["MemTotal"], uint64(16384000*1024); got != want {
		t.Errorf("MemTotal = %d, want %d", got, want)
	}
	if got, want := vals["SwapFree"], uint64(4095000*1024); got != want {
		t.Errorf("SwapFree = %d, want %d", got, want)
	}

	if _, err := parseMeminfo([]byte("MemTotal: 1 kB\n"), "MemAvailable"); err == nil {
		t.Error("missing field did not error")
	}
}

func TestParseProcStatCPU(t *testing.T) {
	// user nice system idle iowait irq softirq steal
	busy, total, err := parseProcStatCPU("cpu  100 0 50 800 40 5 5 0")
	if err != nil {
		t.Fatalf("parseProcStatCPU: %v", err)
	}
	if want := uint64(160); busy != want {
		t.Errorf("busy = %d, want %d", busy, want)
	}
	if want := uint64(1000); total != want {
		t.Errorf("total = %d, want %d", total, want)
	}

	if _, _, err := parseProcStatCPU("intr 12345"); err == nil {
		t.Error("non-cpu line did not error")
	}
}
