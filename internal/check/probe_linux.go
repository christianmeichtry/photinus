//go:build linux

package check

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func hostUptime() (time.Duration, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, fmt.Errorf("reading /proc/uptime: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("parsing /proc/uptime: empty")
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parsing /proc/uptime: %w", err)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

func readDiskUsage(path string) (diskUsage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return diskUsage{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	return statfsToUsage(uint64(st.Blocks), uint64(st.Bfree), uint64(st.Bavail), uint64(st.Bsize)), nil
}

func readMemUsage() (memUsage, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return memUsage{}, fmt.Errorf("reading /proc/meminfo: %w", err)
	}
	vals, err := parseMeminfo(data, "MemTotal", "MemAvailable")
	if err != nil {
		// Kernels before 3.14 have no MemAvailable. The old approximation
		// of free plus reclaimable caches is close enough for a threshold.
		vals, err = parseMeminfo(data, "MemTotal", "MemFree", "Buffers", "Cached")
		if err != nil {
			return memUsage{}, err
		}
		vals["MemAvailable"] = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
	}
	total, avail := vals["MemTotal"], vals["MemAvailable"]
	if avail > total {
		avail = total
	}
	return memUsage{usedBytes: total - avail, totalBytes: total}, nil
}

func readSwapUsage() (swapUsage, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return swapUsage{}, fmt.Errorf("reading /proc/meminfo: %w", err)
	}
	vals, err := parseMeminfo(data, "SwapTotal", "SwapFree")
	if err != nil {
		return swapUsage{}, err
	}
	total, free := vals["SwapTotal"], vals["SwapFree"]
	if free > total {
		free = total
	}
	return swapUsage{usedBytes: total - free, totalBytes: total}, nil
}

// parseMeminfo pulls the named kB fields out of /proc/meminfo and returns
// them in bytes.
func parseMeminfo(data []byte, keys ...string) (map[string]uint64, error) {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	out := make(map[string]uint64, len(keys))
	for _, line := range bytes.Split(data, []byte("\n")) {
		name, rest, ok := strings.Cut(string(line), ":")
		if !ok || !want[name] {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parsing /proc/meminfo field %s: %w", name, err)
		}
		out[name] = kb * 1024
	}
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			return nil, fmt.Errorf("parsing /proc/meminfo: field %s missing", k)
		}
	}
	return out, nil
}

// newCPUProbe reports pressure as the one minute load average spread over
// the cores, in percent. The kernel keeps this damped mean for free, which
// fits a tool that stores no history, and it measures demand rather than
// use: cores at full tilt with an empty queue are a machine doing its job,
// while a deep run queue is a machine in trouble. Linux load also counts
// tasks stuck in uninterruptible disk wait, so an IO-sick box shows up
// here too; the detail sentence says load, not busy, to stay honest. The
// old two-second tick delta paged operators about healthy boxes whenever
// a cron burst pinned the cores for one sampling window.
func newCPUProbe() func() (float64, bool, error) {
	return func() (float64, bool, error) {
		data, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return 0, false, fmt.Errorf("reading /proc/loadavg: %w", err)
		}
		fields := strings.Fields(string(data))
		if len(fields) == 0 {
			return 0, false, fmt.Errorf("parsing /proc/loadavg: empty")
		}
		load, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return 0, false, fmt.Errorf("parsing /proc/loadavg: %w", err)
		}
		return 100 * load / float64(runtime.NumCPU()), true, nil
	}
}

func newNetProbe() func() (float64, float64, string, bool, error) {
	var prevRx, prevTx uint64
	var prevAt time.Time
	var prevIface string
	return func() (float64, float64, string, bool, error) {
		iface, err := defaultRouteIface()
		if err != nil {
			return 0, 0, "", false, err
		}
		rx, tx, err := readNetDev(iface)
		if err != nil {
			return 0, 0, "", false, err
		}
		now := time.Now()
		usable := prevIface == iface && !prevAt.IsZero() && rx >= prevRx && tx >= prevTx
		dt := now.Sub(prevAt).Seconds()
		defer func() { prevRx, prevTx, prevAt, prevIface = rx, tx, now, iface }()
		if !usable || dt <= 0 {
			return 0, 0, iface, false, nil
		}
		return float64(rx-prevRx) / dt, float64(tx-prevTx) / dt, iface, true, nil
	}
}

// defaultRouteIface names the interface carrying the default route.
func defaultRouteIface() (string, error) {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "", fmt.Errorf("reading /proc/net/route: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "00000000" {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("no default route in /proc/net/route")
}

func readNetDev(iface string) (rx, tx uint64, err error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, fmt.Errorf("reading /proc/net/dev: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != iface {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			return 0, 0, fmt.Errorf("parsing /proc/net/dev: short line for %s", iface)
		}
		if rx, err = strconv.ParseUint(f[0], 10, 64); err != nil {
			return 0, 0, fmt.Errorf("parsing /proc/net/dev: %w", err)
		}
		if tx, err = strconv.ParseUint(f[8], 10, 64); err != nil {
			return 0, 0, fmt.Errorf("parsing /proc/net/dev: %w", err)
		}
		return rx, tx, nil
	}
	return 0, 0, fmt.Errorf("interface %s not in /proc/net/dev", iface)
}
