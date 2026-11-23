// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package adapter is the Pumi harness adapter for Claude Code (decisions
// 0158 to 0160 and 0185). The runner starts it as one process per
// invocation, in the invocation's workspace. It reads the invocation as one
// line of protojson from standard input, runs claude -p in the workspace,
// and writes the result as protojson to standard output. Its standard error
// is the log. When standard input closes, it stops claude and writes what
// the run had.
package adapter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// ErrInvocation is returned when the adapter cannot read or cannot run the
// invocation. It then writes no result, and the process exits with a
// non-zero status, which fails the invocation (decision 0158).
var ErrInvocation = errors.New("the invocation cannot be run")

// DefaultStopGrace is how long the adapter waits for claude to stop after
// it interrupts it, before it kills it. It is shorter than the runner's
// grace period of 30 seconds, so that the adapter can still write a result.
const DefaultStopGrace = 20 * time.Second

// Config configures Run.
type Config struct {
	// Claude is the claude program, looked up on the PATH when it has no
	// slash. When it is empty, it is "claude".
	Claude string
	// StopGrace replaces DefaultStopGrace when it is not zero.
	StopGrace time.Duration
}

// Run runs one invocation. It returns an error, and writes nothing to
// stdout, only when the invocation cannot be read or cannot be run; the
// error then wraps ErrInvocation. Otherwise it writes one Result, also when
// claude fails, and returns nil. stderr is the log.
func Run(ctx context.Context, cfg Config, stdin io.Reader, stdout, stderr io.Writer) error {
	inv, closed, err := readInvocation(stdin)
	if err != nil {
		return err
	}
	args, err := Args(inv)
	if err != nil {
		return err
	}
	result := runClaude(ctx, cfg, inv, args, closed, stderr)
	data, err := protojson.Marshal(result)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(data, '\n'))
	return err
}

// readInvocation reads the invocation line and returns a channel that is
// closed when standard input closes after it, which is the runner's request
// to stop (decision 0158). The line is parsed strictly, so an unknown
// field, such as a capability that this adapter does not know, refuses the
// invocation instead of being ignored.
func readInvocation(stdin io.Reader) (*harnessv1.Invocation, <-chan struct{}, error) {
	r := bufio.NewReader(stdin)
	line, err := r.ReadBytes('\n')
	closed := make(chan struct{})
	switch {
	case errors.Is(err, io.EOF) && len(line) > 0:
		close(closed)
	case err != nil:
		return nil, nil, fmt.Errorf("%w: reading the invocation line: %w", ErrInvocation, err)
	default:
		go func() {
			_, _ = io.Copy(io.Discard, r)
			close(closed)
		}()
	}
	inv := &harnessv1.Invocation{}
	if err := protojson.Unmarshal(line, inv); err != nil {
		return nil, nil, fmt.Errorf("%w: the invocation line is not a pumi.harness.v1.Invocation: %w", ErrInvocation, err)
	}
	return inv, closed, nil
}

// Args returns the arguments of claude for an invocation. The prompt is
// the task, which claude reads from its standard input, so that a task that
// starts with "-" is not read as a flag.
//
//   - --setting-sources "" ignores the user's settings and the settings
//     files in the workspace, so that only --settings sets the permissions.
//   - --permission-mode dontAsk and --permission-prompts none deny what no
//     rule allows, with no prompt.
//   - --no-session-persistence keeps no session to resume.
//
// The gateway is optional until E7 (decision 0185). With no gateway, claude
// uses its own sign-in on the machine, which the adapter leaves as it
// finds it in its environment. An invocation with a gateway is refused,
// because the adapter cannot send claude's model calls through it before
// E7 defines how.
func Args(inv *harnessv1.Invocation) ([]string, error) {
	if inv.GetInput().GetTask() == "" {
		return nil, fmt.Errorf("%w: it has no task", ErrInvocation)
	}
	if inv.GetGateway() != nil {
		return nil, fmt.Errorf("%w: it names a model gateway, which this adapter does not support until E7 (decision 0185)", ErrInvocation)
	}
	tools, settings := Translate(inv.GetCapabilities())
	data, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	args := []string{
		"-p",
		"--output-format", "json",
		"--no-session-persistence",
		"--setting-sources", "",
		"--permission-mode", "dontAsk",
		"--permission-prompts", "none",
		"--tools", strings.Join(tools, ","),
		"--settings", string(data),
	}
	if schema := inv.GetOutputSchema().GetJsonSchema(); schema != "" {
		args = append(args, "--json-schema", schema)
	}
	if cost := inv.GetBudget().GetCostMicrodollars(); cost > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%d.%06d", cost/1_000_000, cost%1_000_000))
	}
	return args, nil
}
