// Package check defines what a check is and holds the built-in check types,
// one file per type. A check tests one thing right now and reports a verdict.
// It never stores a metric over time.
package check

import (
	"context"
	"errors"
	"fmt"
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
	// Failed means the check ran and the thing it tests is broken.
	Failed
	// NotApplicable means the check cannot run on this platform. It is not
	// a failure and must never take the lantern down.
	NotApplicable
)

func (v Verdict) String() string {
	switch v {
	case OK:
		return "ok"
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

// A Check tests one thing about one target.
type Check interface {
	// Name identifies the check type, for example "tcp".
	Name() string
	// Target is the host or endpoint the check is about.
	Target() string
	// Run performs the check once and reports what it saw.
	Run(ctx context.Context) Result
}
