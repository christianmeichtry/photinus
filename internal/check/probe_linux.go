//go:build linux

package check

import (
	"bytes"
	"fmt"
	"os"
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
		return memUsage{}, err
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

// newCPUProbe measures real utilization from /proc/stat deltas between
// flashes. The first call only takes a baseline.
func newCPUProbe() func() (float64, bool, error) {
	var prevBusy, prevTotal uint64
	var have bool
	return func() (float64, bool, error) {
		busy, total, err := readProcStat()
		if err != nil {
			return 0, false, err
		}
		defer func() { prevBusy, prevTotal, have = busy, total, true }()
		if !have || total <= prevTotal {
			return 0, false, nil
		}
		dBusy := float64(busy - prevBusy)
		dTotal := float64(total - prevTotal)
		return 100 * dBusy / dTotal, true, nil
	}
}

func readProcStat() (busy, total uint64, err error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, fmt.Errorf("reading /proc/stat: %w", err)
	}
	line, _, _ := bytes.Cut(data, []byte("\n"))
	return parseProcStatCPU(string(line))
}

// parseProcStatCPU reads the aggregate cpu line: user nice system idle
// iowait irq softirq steal. Idle and iowait count as not busy.
func parseProcStatCPU(line string) (busy, total uint64, err error) {
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, fmt.Errorf("parsing /proc/stat: unexpected line %q", line)
	}
	var ticks []uint64
	for _, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parsing /proc/stat: %w", err)
		}
		ticks = append(ticks, v)
	}
	for i, v := range ticks {
		total += v
		// fields after "cpu": user nice system idle iowait ...
		if i != 3 && i != 4 {
			busy += v
		}
	}
	return busy, total, nil
}
