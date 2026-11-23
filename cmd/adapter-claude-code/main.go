// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Command adapter-claude-code is the Pumi harness adapter for Claude Code
// (decisions 0158 to 0160 and 0185). The runner starts it in the
// invocation's workspace and writes one pumi.harness.v1.Invocation as a line
// of protojson to its standard input. It runs claude -p there and writes a
// pumi.harness.v1.Result as protojson to its standard output. Its standard
// error is the log. Closing standard input asks it to stop claude; an
// interrupt or SIGTERM does the same.
//
// It exits with status 0 when it wrote a result, and with status 1 when it
// cannot read or run the invocation.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"getpumi.dev/adapter-claude-code/internal/adapter"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	status := run(ctx, os.Stdin, os.Stdout, os.Stderr)
	cancel()
	os.Exit(status)
}

func run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := adapter.Run(ctx, adapter.Config{}, stdin, stdout, stderr); err != nil {
		_, _ = fmt.Fprintf(stderr, "adapter-claude-code: %v\n", err)
		return 1
	}
	return 0
}
