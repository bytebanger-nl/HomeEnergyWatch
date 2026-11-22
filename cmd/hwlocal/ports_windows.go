package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

func portInUse(err error) bool { return errors.Is(err, windows.WSAEADDRINUSE) }

// portReserved: Hyper-V, WSL and Docker reserve port ranges; binding in
// one fails with "access denied".
func portReserved(err error) bool { return errors.Is(err, windows.WSAEACCES) }
