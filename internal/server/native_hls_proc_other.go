//go:build !unix

package server

import "os/exec"

func hlsPause(*exec.Cmd, bool) {}
