package check

import (
	"context"
	"errors"
	"fmt"
)

// CPU trips when the processor stays busier than a threshold. What "busy"
// means is up to the platform probe: real utilization where the OS exposes
// it, a load average scaled by core count where it does not. The probe may
// need a first sample before it can answer.
type CPU struct {
	// Host is this lantern's name. A local check is about its own host.
	Host string
	// Max is the busy percentage that trips the check. Zero means 95.
	Max float64

	probe func() (percent float64, ready bool, err error)
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
	if pct > max {
		return Result{Verdict: Warn, Detail: fmt.Sprintf("cpu is %.0f%% busy, threshold is %.0f%%", pct, max)}
	}
	return Result{Verdict: OK, Detail: fmt.Sprintf("cpu is %.0f%% busy", pct)}
}
