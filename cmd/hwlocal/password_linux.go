package main

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func hideInput() func() {
	fd := int(os.Stdin.Fd())
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil // not a terminal (piped input)
	}
	old := *t
	t.Lflag &^= unix.ECHO
	if unix.IoctlSetTermios(fd, unix.TCSETS, t) != nil {
		return nil
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, &old) }
}

// matchOwner gives the file the owner of the data directory: run as root
// (sudo hwlocal password), it would otherwise be unreadable for the service
// user.
func matchOwner(path, dir string) {
	if st, err := os.Stat(dir); err == nil {
		if s, ok := st.Sys().(*syscall.Stat_t); ok {
			_ = os.Chown(path, int(s.Uid), int(s.Gid))
		}
	}
}
