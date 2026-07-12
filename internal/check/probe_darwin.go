//go:build darwin

package check

import (
	"encoding/binary"
	"fmt"
	"runtime"
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
