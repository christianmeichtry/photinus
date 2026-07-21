package check

import (
	"context"
	"errors"
	"fmt"
)

// memUsage is what a platform probe reports about RAM.
type memUsage struct {
	usedBytes  uint64
	totalBytes uint64
}

func (m memUsage) usedPercent() float64 {
	if m.totalBytes == 0 {
		return 0
	}
	return 100 * float64(m.usedBytes) / float64(m.totalBytes)
}

// Memory trips when RAM utilization crosses a threshold.
type Memory struct {
	// Host is this lantern's name. A local check is about its own host.
	Host string
	// Max is the used percentage that trips the check. Zero means 95.
	Max float64

	warned bool
	probe  func() (memUsage, error)
}

func (m *Memory) Name() string   { return "memory" }
func (m *Memory) Target() string { return m.Host }

func (m *Memory) Run(ctx context.Context) Result {
	max := m.Max
	if max <= 0 {
		max = 95
	}
	probe := m.probe
	if probe == nil {
		probe = readMemUsage
	}
	u, err := probe()
	if errors.Is(err, errUnsupported) {
		return Result{Verdict: NotApplicable, Detail: "memory usage cannot be read on this platform yet"}
	}
	if err != nil {
		return Result{Verdict: NotApplicable, Detail: fmt.Sprintf("cannot read memory usage: %v", err)}
	}
	pct := u.usedPercent()
	detail := fmt.Sprintf("memory is %.0f%% used, %s of %s", pct, humanBytes(u.usedBytes), humanBytes(u.totalBytes))
	clear := max - 10
	if clear < 0 {
		clear = 0
	}
	m.warned = hysteresis(m.warned, pct, max, clear)
	if m.warned {
		return Result{Verdict: Warn, Detail: detail + fmt.Sprintf(", threshold is %.0f%%, clears below %.0f%%", max, clear)}
	}
	return Result{Verdict: OK, Detail: detail}
}
