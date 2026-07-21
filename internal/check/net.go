package check

import (
	"context"
	"errors"
	"fmt"
)

// Net reports the traffic rate on the host's main network interface, the
// one carrying the default route. It is a lens for anomalies: a box that
// suddenly moves ten times its usual traffic is telling a story, whether
// that story is a backup, a crawler, or something worse. The rate is
// measured as the counter delta between flashes, like the cpu probe, so
// the first run only takes a baseline.
type Net struct {
	// Host is this lantern's name. A local check is about its own host.
	Host string
	// Max is the combined in-plus-out rate in Mbit/s past which the check
	// warns. Zero never warns: the rate is information, not an alarm,
	// until an operator names a limit.
	Max float64

	warned bool
	probe  func() (rxBps, txBps float64, iface string, ready bool, err error)
}

func (n *Net) Name() string   { return "net" }
func (n *Net) Target() string { return n.Host }

func (n *Net) Run(ctx context.Context) Result {
	if n.probe == nil {
		n.probe = newNetProbe()
	}
	rx, tx, iface, ready, err := n.probe()
	if errors.Is(err, errUnsupported) {
		return Result{Verdict: NotApplicable, Detail: "network rate cannot be read on this platform yet"}
	}
	if err != nil {
		return Result{Verdict: NotApplicable, Detail: fmt.Sprintf("cannot read network rate: %v", err)}
	}
	if !ready {
		return Result{Verdict: OK, Detail: "network measurement warming up, first rate next flash"}
	}
	detail := fmt.Sprintf("net is %s in, %s out on %s", humanRate(rx), humanRate(tx), iface)
	if n.Max > 0 {
		mbit := (rx + tx) * 8 / 1e6
		n.warned = hysteresis(n.warned, mbit, n.Max, 0.85*n.Max)
		if n.warned {
			return Result{Verdict: Warn, Detail: fmt.Sprintf("%s, %.0f Mbit/s combined, threshold is %.0f, clears below %.0f", detail, mbit, n.Max, 0.85*n.Max)}
		}
	}
	return Result{Verdict: OK, Detail: detail}
}

func humanRate(bps float64) string {
	if bps < 0 {
		bps = 0
	}
	return humanBytes(uint64(bps)) + "/s"
}
