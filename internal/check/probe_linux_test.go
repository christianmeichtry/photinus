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
