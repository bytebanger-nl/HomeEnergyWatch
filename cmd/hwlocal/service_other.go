//go:build !windows

package main

import "errors"

func isService() bool { return false }

func runService() {}

func serviceCommand([]string) error {
	return errors.New("'hwlocal service' is alleen voor Windows; gebruik op Linux deploy/hwlocal.service (systemd)")
}

func serviceDefaultDataDir() string { return "" }
