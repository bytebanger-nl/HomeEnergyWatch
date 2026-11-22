package main

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func hideInput() func() {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return nil // not a console (piped input)
	}
	if windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT) != nil {
		return nil
	}
	return func() { _ = windows.SetConsoleMode(h, mode) }
}

// serviceDefaultDataDir is the service's data directory when it has a
// database.
func serviceDefaultDataDir() string {
	d := serviceDataDir()
	if _, err := os.Stat(filepath.Join(d, "hwlocal.db")); err == nil {
		return d
	}
	return ""
}

// matchOwner is not needed on Windows: the data directory's permissions are
// inherited.
func matchOwner(string, string) {}
