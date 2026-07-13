//go:build darwin

package check

import (
	"encoding/binary"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func hostUptime() (time.Duration, error) {
	raw, err := unix.SysctlRaw("kern.boottime")
	if err != nil {
		return 0, fmt.Errorf("reading kern.boottime: %w", err)
	}
	if len(raw) < 12 {
		return 0, fmt.Errorf("parsing kern.boottime: got %d bytes", len(raw))
	}
	sec := int64(binary.LittleEndian.Uint64(raw[0:8]))
	boot := time.Unix(sec, 0)
	return time.Since(boot), nil
}

func readDiskUsage(path string) (diskUsage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return diskUsage{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	return statfsToUsage(st.Blocks, st.Bfree, uint64(st.Bavail), uint64(st.Bsize)), nil
}

// readMemUsage leans on kern.memorystatus_level, the kernel's own estimate
// of how much memory is still available in percent. Counting free pages
// instead would read a healthy Mac as nearly full, because file cache is
// deliberately kept hot.
func readMemUsage() (memUsage, error) {
	level, err := unix.SysctlUint32("kern.memorystatus_level")
	if err != nil {
		return memUsage{}, fmt.Errorf("reading kern.memorystatus_level: %w", err)
	}
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return memUsage{}, fmt.Errorf("reading hw.memsize: %w", err)
	}
	if level > 100 {
		level = 100
	}
	usedPct := float64(100 - level)
	return memUsage{
		usedBytes:  uint64(usedPct / 100 * float64(total)),
		totalBytes: total,
	}, nil
}

// readSwapUsage parses vm.swapusage, a struct of three uint64 byte counts:
// total, available, used.
func readSwapUsage() (swapUsage, error) {
	raw, err := unix.SysctlRaw("vm.swapusage")
	if err != nil {
		return swapUsage{}, fmt.Errorf("reading vm.swapusage: %w", err)
	}
	if len(raw) < 24 {
		return swapUsage{}, fmt.Errorf("parsing vm.swapusage: got %d bytes", len(raw))
	}
	return swapUsage{
		totalBytes: binary.LittleEndian.Uint64(raw[0:8]),
		usedBytes:  binary.LittleEndian.Uint64(raw[16:24]),
	}, nil
}

// newCPUProbe approximates utilization as the one minute load average
// spread over the cores. macOS does not expose cpu tick counters through
// sysctl, and real utilization needs mach calls that are not worth the
// dependency yet. The proxy overcounts when processes wait on disk and
// undercounts short bursts; both are acceptable for a threshold check.
func newCPUProbe() func() (float64, bool, error) {
	return func() (float64, bool, error) {
		raw, err := unix.SysctlRaw("vm.loadavg")
		if err != nil {
			return 0, false, fmt.Errorf("reading vm.loadavg: %w", err)
		}
		if len(raw) < 24 {
			return 0, false, fmt.Errorf("parsing vm.loadavg: got %d bytes", len(raw))
		}
		load1 := binary.LittleEndian.Uint32(raw[0:4])
		fscale := binary.LittleEndian.Uint64(raw[16:24])
		if fscale == 0 {
			return 0, false, fmt.Errorf("parsing vm.loadavg: fscale is zero")
		}
		load := float64(load1) / float64(fscale)
		pct := 100 * load / float64(runtime.NumCPU())
		if pct > 100 {
			pct = 100
		}
		return pct, true, nil
	}
}

// newNetProbe measures the traffic rate on the default-route interface
// from netstat counter deltas between flashes. macOS keeps these counters
// behind mach APIs, so the bundled route and netstat tools are the
// dependency-free way at them.
func newNetProbe() func() (float64, float64, string, bool, error) {
	var prevRx, prevTx uint64
	var prevAt time.Time
	var prevIface string
	return func() (float64, float64, string, bool, error) {
		iface, err := defaultRouteIface()
		if err != nil {
			return 0, 0, "", false, err
		}
		rx, tx, err := readNetstat(iface)
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
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return "", fmt.Errorf("asking route for the default interface: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && k == "interface" {
			return strings.TrimSpace(v), nil
		}
	}
	return "", fmt.Errorf("no interface in route's default answer")
}

// readNetstat takes the link-level byte counters for one interface. Fields
// are matched to the header by position and rows that do not line up with
// the header (an interface without a hardware address) are skipped.
func readNetstat(iface string) (rx, tx uint64, err error) {
	out, err := exec.Command("netstat", "-ibn", "-I", iface).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("asking netstat about %s: %w", iface, err)
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) < 2 {
		return 0, 0, fmt.Errorf("netstat said nothing about %s", iface)
	}
	head := strings.Fields(lines[0])
	ib, ob := -1, -1
	for i, h := range head {
		switch h {
		case "Ibytes":
			ib = i
		case "Obytes":
			ob = i
		}
	}
	if ib < 0 || ob < 0 {
		return 0, 0, fmt.Errorf("netstat output has no byte columns")
	}
	for _, line := range lines[1:] {
		if !strings.Contains(line, "<Link") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != len(head) {
			continue
		}
		rxv, err1 := strconv.ParseUint(f[ib], 10, 64)
		txv, err2 := strconv.ParseUint(f[ob], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		return rxv, txv, nil
	}
	return 0, 0, fmt.Errorf("no link-level counters for %s", iface)
}
