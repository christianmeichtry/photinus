package check

import (
	"context"
	"errors"
	"fmt"
)

// diskUsage is what a platform probe reports about one filesystem.
type diskUsage struct {
	usedPercent float64
	freeBytes   uint64
}

// Disk trips when a filesystem is nearly full.
type Disk struct {
	// Host is this lantern's name. A local check is about its own host.
	Host string
	// Path is any path on the filesystem to measure.
	Path string
	// Max is the used percentage that trips the check. Zero means 90.
	Max float64

	warned bool
	probe  func(path string) (diskUsage, error)
}

func (d *Disk) Name() string   { return "disk:" + d.Path }
func (d *Disk) Target() string { return d.Host }

func (d *Disk) Run(ctx context.Context) Result {
	max := d.Max
	if max <= 0 {
		max = 90
	}
	probe := d.probe
	if probe == nil {
		probe = readDiskUsage
	}
	u, err := probe(d.Path)
	if errors.Is(err, errUnsupported) {
		return Result{Verdict: NotApplicable, Detail: "disk usage cannot be read on this platform yet"}
	}
	if err != nil {
		return Result{Verdict: NotApplicable, Detail: fmt.Sprintf("cannot read disk usage of %s: %v", d.Path, err)}
	}
	clear := max - 10
	if clear < 0 {
		clear = 0
	}
	d.warned = hysteresis(d.warned, u.usedPercent, max, clear)
	if d.warned {
		return Result{Verdict: Warn, Detail: fmt.Sprintf("disk %s is %.0f%% full, %s left, threshold is %.0f%%, clears below %.0f%%",
			d.Path, u.usedPercent, humanBytes(u.freeBytes), max, clear)}
	}
	return Result{Verdict: OK, Detail: fmt.Sprintf("disk %s is %.0f%% full, %s free",
		d.Path, u.usedPercent, humanBytes(u.freeBytes))}
}
