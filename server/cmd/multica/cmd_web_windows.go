//go:build windows

package main

import (
	"os"
	"syscall"
)

func detachedProcessAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func signalProcessGroup(pid int, sig os.Signal) error {
	return signalProcess(pid, sig)
}
