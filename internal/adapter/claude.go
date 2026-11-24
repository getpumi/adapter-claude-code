// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// report is the part of claude's report, from claude -p --output-format
// json, that the adapter reads. claude writes it when the run ends, also
// when it ends because it was interrupted.
type report struct {
	Type             string                `json:"type"`
	Subtype          string                `json:"subtype"`
	IsError          bool                  `json:"is_error"`
	Errors           []string              `json:"errors"`
	Result           string                `json:"result"`
	TotalCostUSD     float64               `json:"total_cost_usd"`
	ModelUsage       map[string]modelUsage `json:"modelUsage"`
	StructuredOutput json.RawMessage       `json:"structured_output"`
}

// modelUsage is claude's usage of one model.
type modelUsage struct {
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
}

// why is why the adapter stopped claude before it ended by itself.
type why int

const (
	notStopped why = iota
	// The runner closed standard input, or the adapter's context ended.
	stopRequested
	// The run reached the time budget.
	timeBudget
)

// runClaude runs claude and returns the result. It stops claude when closed
// is closed, when ctx ends or when the time budget runs out.
func runClaude(ctx context.Context, cfg Config, inv *harnessv1.Invocation, args []string, closed <-chan struct{}, stderr io.Writer) *harnessv1.Result {
	program := cfg.Claude
	if program == "" {
		program = "claude"
	}
	grace := cfg.StopGrace
	if grace == 0 {
		grace = DefaultStopGrace
	}
	path, err := exec.LookPath(program)
	if err != nil {
		logf(stderr, "cannot find claude: %v", err)
		return failed(nil, 0)
	}

	// The arguments come from the invocation, which is what the adapter
	// runs; no shell reads them, and the task goes to standard input.
	// Stopping cancels runCtx, and its Cancel interrupts claude's process
	// group instead of killing claude.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, path, args...)
	cmd.Cancel = func() error {
		interrupt(cmd)
		return nil
	}
	cmd.Dir = inv.GetWorkspace()
	cmd.Env = Env(os.Environ(), inv.GetGateway())
	cmd.Stdin = strings.NewReader(inv.GetInput().GetTask())
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	// A process that claude started can hold its standard error open after
	// claude exits; Wait then stops waiting for it.
	cmd.WaitDelay = grace
	newProcessGroup(cmd)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		logf(stderr, "cannot start claude: %v", err)
		return failed(nil, 0)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var budget <-chan time.Time
	if s := inv.GetBudget().GetSeconds(); s > 0 {
		timer := time.NewTimer(time.Duration(s) * time.Second)
		defer timer.Stop()
		budget = timer.C
	}
	stopped := notStopped
	select {
	case err = <-done:
	case <-closed:
		stopped = stopRequested
	case <-ctx.Done():
		stopped = stopRequested
	case <-budget:
		stopped = timeBudget
	}
	if stopped != notStopped {
		cancel()
		err = wait(cmd, done, grace)
	}
	elapsed := time.Since(start)
	return result(stdout.Bytes(), err, stopped, inv, elapsed, stderr)
}

// wait waits for claude to exit after it was interrupted, and kills its
// process group when it has not exited after the grace period. It returns
// the error of Wait.
func wait(cmd *exec.Cmd, done <-chan error, grace time.Duration) error {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		kill(cmd)
		return <-done
	}
}

// result builds the result from claude's report.
func result(out []byte, waitErr error, stopped why, inv *harnessv1.Invocation, elapsed time.Duration, stderr io.Writer) *harnessv1.Result {
	switch {
	case stopped == stopRequested:
		logf(stderr, "stopped claude on request")
	case stopped == timeBudget:
		logf(stderr, "stopped claude at the time budget of %d seconds", inv.GetBudget().GetSeconds())
	case waitErr != nil:
		logf(stderr, "claude exited: %v", waitErr)
	}
	seconds := int64(math.Ceil(elapsed.Seconds()))
	var r report
	if err := json.Unmarshal(bytes.TrimSpace(out), &r); err != nil || r.Type != "result" {
		if stopped == stopRequested {
			return &harnessv1.Result{Status: harnessv1.Result_STATUS_STOPPED, Usage: &harnessv1.Usage{Seconds: seconds}}
		}
		logf(stderr, "claude's standard output is not a report: %q", clip(out))
		return failed(nil, seconds)
	}
	switch {
	case stopped == stopRequested:
		return &harnessv1.Result{Status: harnessv1.Result_STATUS_STOPPED, Usage: r.usage(seconds)}
	case stopped == timeBudget:
		return failed(&r, seconds)
	case r.Subtype != "success":
		logf(stderr, "claude reported %s: %s", r.Subtype, strings.Join(r.Errors, "; "))
		return failed(&r, seconds)
	case r.IsError:
		// claude reports an error of the API, such as a call that the
		// model gateway refused at the invocation's cap, with the subtype
		// success and the error in result.
		logf(stderr, "claude reported an error: %s", clip([]byte(r.Result)))
		return failed(&r, seconds)
	}
	if inv.GetOutputSchema().GetJsonSchema() == "" {
		return &harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Usage: r.usage(seconds)}
	}
	var output bytes.Buffer
	if len(r.StructuredOutput) == 0 || string(r.StructuredOutput) == "null" || json.Compact(&output, r.StructuredOutput) != nil {
		logf(stderr, "claude reported no structured output")
		return failed(&r, seconds)
	}
	return &harnessv1.Result{
		Status: harnessv1.Result_STATUS_COMPLETED,
		Output: output.String(),
		Usage:  r.usage(seconds),
	}
}

// failed is a failed result, with the usage of the report when there is
// one.
func failed(r *report, seconds int64) *harnessv1.Result {
	usage := &harnessv1.Usage{Seconds: seconds}
	if r != nil {
		usage = r.usage(seconds)
	}
	return &harnessv1.Result{Status: harnessv1.Result_STATUS_FAILED, Usage: usage}
}

// usage is the report's usage. The tokens are every token of every model
// that the run used: input, output, and input written to and read from the
// prompt cache. The cost is claude's total cost, rounded to a microdollar.
// The seconds are the wall time of claude's process, rounded up.
func (r *report) usage(seconds int64) *harnessv1.Usage {
	var tokens int64
	for _, m := range r.ModelUsage {
		tokens += m.InputTokens + m.OutputTokens + m.CacheReadInputTokens + m.CacheCreationInputTokens
	}
	return &harnessv1.Usage{
		Tokens:           tokens,
		CostMicrodollars: int64(math.Round(r.TotalCostUSD * 1e6)),
		Seconds:          seconds,
	}
}

// clip shortens claude's output for the log.
func clip(out []byte) string {
	const limit = 500
	if len(out) > limit {
		return string(out[:limit]) + "..."
	}
	return string(out)
}

func logf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, "adapter-claude-code: "+format+"\n", args...)
}
