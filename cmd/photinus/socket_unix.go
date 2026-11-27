//go:build !windows

package main

import (
	"os"
	"syscall"
)

// ownedByMe reports whether this file belongs to the user running the command.
// The temp directory is shared on unix (/tmp is mode 1777 on Linux), so a
// socket someone else owns is not a lantern of ours and must not be dialed.
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Uid) == os.Getuid()
}
