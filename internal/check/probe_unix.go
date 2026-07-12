//go:build linux || darwin

package check

// statfsToUsage computes df-style usage: the percentage is of the space a
// user can actually consume, so root-reserved blocks do not hide a full disk.
func statfsToUsage(blocks, bfree, bavail, bsize uint64) diskUsage {
	used := blocks - bfree
	usable := used + bavail
	var pct float64
	if usable > 0 {
		pct = 100 * float64(used) / float64(usable)
	}
	return diskUsage{
		usedPercent: pct,
		freeBytes:   bavail * bsize,
	}
}
