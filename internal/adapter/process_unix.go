// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

//go:build unix

package adapter

import (
	"os/exec"
	"syscall"
)

// newProcessGroup starts claude in a process group of its own, so that
// interrupt and kill reach the processes it starts, such as the commands
// that it runs.
func newProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// interrupt sends SIGINT to claude's process group. claude then ends the
// run and writes its report.
func interrupt(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}

// kill kills claude's process group.
func kill(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
