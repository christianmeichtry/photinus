// Package check defines what a check is and holds the built-in check types,
// one file per type. A check tests one thing right now and reports a verdict.
// It never stores a metric over time.
package check

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// errUnsupported is returned by platform probes on operating systems that
// cannot answer. The check turns it into NotApplicable, never a failure.
var errUnsupported = errors.New("not supported on this platform")

// humanBytes formats a byte count the way an operator would say it.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for u := n / unit; u >= unit; u /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Verdict is the outcome of running a check once.
type Verdict int

const (
	// OK means the check passed.
	OK Verdict = iota
	// Warn means the thing works but is heading somewhere bad: a filling
	// disk, a busy cpu, a drifting clock. The host is not down and the
	// verdict must never say it is.
	Warn
	// Failed means the check ran and the thing it tests is broken or
	// unreachable.
	Failed
	// NotApplicable means the check cannot run on this platform. It is not
	// a failure and must never take the lantern down.
	NotApplicable
)

func (v Verdict) String() string {
	switch v {
	case OK:
		return "ok"
	case Warn:
		return "warning"
	case Failed:
		return "failed"
	case NotApplicable:
		return "not applicable"
	default:
		return "unknown"
	}
}

// Result carries the verdict and a short detail line written for operators.
type Result struct {
	Verdict Verdict
	Detail  string
}

// Paced is implemented by checks too heavy or too slow-moving to run every
// flash: TLS handshakes against production websites do not belong in a two
// second loop. The lantern runs a paced check on its own cadence and keeps
// gossiping the last verdict in between.
type Paced interface {
	// Every is the interval between runs of this check.
	Every() time.Duration
}

// A Check tests one thing about one target.
type Check interface {
	// Name identifies the check type, for example "tcp".
	Name() string
	// Target is the host or endpoint the check is about.
	Target() string
	// Run performs the check once and reports what it saw.
	Run(ctx context.Context) Result
}

// hysteresis keeps a threshold check from narrating every ripple when the
// signal rides its line: tripped past max, it stays tripped until the
// value recedes below the clear line. One warning when capacity is
// reached, one cleared when it genuinely recedes.
func hysteresis(warned bool, value, max, clear float64) bool {
	if value > max {
		return true
	}
	return warned && value > clear
}
