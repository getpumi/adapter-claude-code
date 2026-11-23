// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

//go:build !unix

package adapter

import "os/exec"

// newProcessGroup does nothing on systems other than Unix.
func newProcessGroup(*exec.Cmd) {}

// interrupt kills claude on systems other than Unix, where the os package
// cannot send an interrupt (decision 0158), so claude writes no report.
func interrupt(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
}

// kill kills claude.
func kill(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
}
