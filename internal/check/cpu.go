package check

import (
	"context"
	"errors"
	"fmt"
	"runtime"
)

// CPU trips when sustained demand for the processor crosses a threshold.
// The measure is the one minute load average spread over the cores, in
// percent: the kernel's own damped mean, which a tool that stores no
// history gets for free, and which ignores the two-second bursts that a
// raw busy percentage pages about. On Linux the load also counts tasks in
// uninterruptible disk wait, so an IO-starved box trips this check too,
// which the detail sentence owns up to by saying load, not busy.
type CPU struct {
	// Host is this lantern's name. A local check is about its own host.
	Host string
	// Max is the busy percentage that trips the check. Zero means 95.
	Max float64

	warned bool
	probe  func() (percent float64, ready bool, err error)
}

func (c *CPU) Name() string   { return "cpu" }
func (c *CPU) Target() string { return c.Host }

func (c *CPU) Run(ctx context.Context) Result {
	max := c.Max
	if max <= 0 {
		max = 95
	}
	if c.probe == nil {
		c.probe = newCPUProbe()
	}
	pct, ready, err := c.probe()
	if errors.Is(err, errUnsupported) {
		return Result{Verdict: NotApplicable, Detail: "cpu usage cannot be read on this platform yet"}
	}
	if err != nil {
		return Result{Verdict: NotApplicable, Detail: fmt.Sprintf("cannot read cpu usage: %v", err)}
	}
	if !ready {
		return Result{Verdict: OK, Detail: "cpu measurement warming up, first verdict next flash"}
	}
	cores := runtime.NumCPU()
	clear := max - 10
	if clear < 0 {
		clear = 0
	}
	c.warned = hysteresis(c.warned, pct, max, clear)
	if c.warned {
		return Result{Verdict: Warn, Detail: fmt.Sprintf("cpu load is %.0f%% of %d cores, threshold is %.0f%%, clears below %.0f%%", pct, cores, max, clear)}
	}
	return Result{Verdict: OK, Detail: fmt.Sprintf("cpu load is %.0f%% of %d cores", pct, cores)}
}
