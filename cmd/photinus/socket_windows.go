//go:build windows

package main

import "os"

// ownedByMe is the Windows stand-in. The per-user temp directory is not
// shared the way /tmp is, and Go exposes no owner through os.FileInfo here,
// so ownership is not checked on this platform.
func ownedByMe(os.FileInfo) bool { return true }
