//go:build !linux && !darwin

package check

import "time"

// The resource checks have no probes on this platform yet. They report
// NotApplicable rather than failing: a check that cannot run must never
// look like an outage. Windows probes are on the roadmap.

func hostUptime() (time.Duration, error)      { return 0, errUnsupported }
func readDiskUsage(string) (diskUsage, error) { return diskUsage{}, errUnsupported }
func readMemUsage() (memUsage, error)         { return memUsage{}, errUnsupported }
func readSwapUsage() (swapUsage, error)       { return swapUsage{}, errUnsupported }
func newCPUProbe() func() (float64, bool, error) {
	return func() (float64, bool, error) { return 0, false, errUnsupported }
}
