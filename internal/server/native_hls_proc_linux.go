//go:build linux

package server

import (
	"os/exec"
	"syscall"
)

// Encoders never outlive the server, even when it is killed.
func hlsProcAttr(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL} }
