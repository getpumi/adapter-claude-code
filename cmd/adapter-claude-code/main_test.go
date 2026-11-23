// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRefusesInvocation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run(t.Context(), strings.NewReader("{}\n"), &stdout, &stderr); got != 1 {
		t.Errorf("exit status = %d, want 1", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no task") {
		t.Errorf("stderr = %q, want the reason", stderr.String())
	}
}
