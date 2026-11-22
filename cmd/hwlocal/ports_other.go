//go:build !windows

package main

import (
	"errors"
	"syscall"
)

func portInUse(err error) bool { return errors.Is(err, syscall.EADDRINUSE) }

func portReserved(error) bool { return false }
