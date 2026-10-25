//go:build unix

package server

import (
	"os/exec"
	"syscall"
)

// A paused encoder keeps its place without spending CPU or disk while the
// listener is far behind; SIGKILL still ends a stopped process.
func hlsPause(cmd *exec.Cmd, pause bool) {
	if cmd.Process == nil {
		return
	}
	if pause {
		_ = cmd.Process.Signal(syscall.SIGSTOP)
	} else {
		_ = cmd.Process.Signal(syscall.SIGCONT)
	}
}
