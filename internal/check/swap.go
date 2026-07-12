package check

import (
	"context"
	"errors"
	"fmt"
)

// swapUsage is what a platform probe reports about swap.
type swapUsage struct {
	usedBytes  uint64
	totalBytes uint64
}

// Swap trips when swap fills up. Rising swap is the canary before the OOM
// killer sings, which is why the default threshold sits lower than memory's.
type Swap struct {
	// Host is this lantern's name. A local check is about its own host.
	Host string
	// Max is the used percentage that trips the check. Zero means 80.
	Max float64

	probe func() (swapUsage, error)
}

func (s *Swap) Name() string   { return "swap" }
func (s *Swap) Target() string { return s.Host }

func (s *Swap) Run(ctx context.Context) Result {
	max := s.Max
	if max <= 0 {
		max = 80
	}
	probe := s.probe
	if probe == nil {
		probe = readSwapUsage
	}
	u, err := probe()
	if errors.Is(err, errUnsupported) {
		return Result{Verdict: NotApplicable, Detail: "swap usage cannot be read on this platform yet"}
	}
	if err != nil {
		return Result{Verdict: NotApplicable, Detail: fmt.Sprintf("cannot read swap usage: %v", err)}
	}
	if u.totalBytes == 0 {
		return Result{Verdict: OK, Detail: "no swap configured"}
	}
	pct := 100 * float64(u.usedBytes) / float64(u.totalBytes)
	detail := fmt.Sprintf("swap is %.0f%% used, %s of %s", pct, humanBytes(u.usedBytes), humanBytes(u.totalBytes))
	if pct > max {
		return Result{Verdict: Failed, Detail: detail + fmt.Sprintf(", threshold is %.0f%%", max)}
	}
	return Result{Verdict: OK, Detail: detail}
}
