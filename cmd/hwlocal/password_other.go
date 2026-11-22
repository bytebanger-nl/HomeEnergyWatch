//go:build !windows && !linux

package main

import (
	"os"
	"syscall"
)

// hideInput is not implemented here: the password is visible while typed.
func hideInput() func() { return nil }

// matchOwner gives the file the owner of the data directory (see the Linux
// version).
func matchOwner(path, dir string) {
	if st, err := os.Stat(dir); err == nil {
		if s, ok := st.Sys().(*syscall.Stat_t); ok {
			_ = os.Chown(path, int(s.Uid), int(s.Gid))
		}
	}
}
