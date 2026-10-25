//go:build !linux

package server

import "os/exec"

func hlsProcAttr(*exec.Cmd) {}
