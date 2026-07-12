// Package check defines what a check is and holds the built-in check types,
// one file per type. A check tests one thing right now and reports a verdict.
// It never stores a metric over time.
package check

import "context"

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
