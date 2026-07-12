package check

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Uptime flags a host that rebooted recently. The host being back up does
// not make an unexplained reboot fine; somebody should ask why it happened.
type Uptime struct {
	// Host is this lantern's name. A local check is about its own host.
	Host string
	// Min is how long the host must have been up. Zero means 3 minutes.
	Min time.Duration

	probe func() (time.Duration, error)
}

func (u *Uptime) Name() string   { return "uptime" }
func (u *Uptime) Target() string { return u.Host }

func (u *Uptime) Run(ctx context.Context) Result {
	min := u.Min
	if min <= 0 {
		min = 3 * time.Minute
	}
	probe := u.probe
	if probe == nil {
		probe = hostUptime
	}
	up, err := probe()
	if errors.Is(err, errUnsupported) {
		return Result{Verdict: NotApplicable, Detail: "uptime cannot be read on this platform yet"}
	}
	if err != nil {
		return Result{Verdict: NotApplicable, Detail: fmt.Sprintf("cannot read uptime: %v", err)}
	}
	if up < min {
		return Result{Verdict: Failed, Detail: fmt.Sprintf("host rebooted %s ago", up.Round(time.Second))}
	}
	return Result{Verdict: OK, Detail: fmt.Sprintf("up for %s", up.Round(time.Minute))}
}
