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
	"sync"
	"time"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// report is the part of claude's report, the result line of claude -p
// --output-format stream-json, that the adapter reads. claude writes it
// when the run ends, also when it ends because it was interrupted.
type report struct {
	Type             string                `json:"type"`
	Subtype          string                `json:"subtype"`
	IsError          bool                  `json:"is_error"`
	Errors           []string              `json:"errors"`
	Result           string                `json:"result"`
	TotalCostUSD     float64               `json:"total_cost_usd"`
	ModelUsage       map[string]modelUsage `json:"modelUsage"`
	StructuredOutput json.RawMessage       `json:"structured_output"`
	// PermissionDenials are the tool calls that claude's settings refused.
	PermissionDenials []denial `json:"permission_denials"`
	// TerminalReason is why the run ended, such as "completed" or
	// "api_error".
	TerminalReason string `json:"terminal_reason"`
	// APIErrorStatus is the HTTP status of the API's error that ended the
	// run, such as 429.
	APIErrorStatus int `json:"api_error_status"`
}

// modelUsage is claude's usage of one model.
type modelUsage struct {
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
	CacheCreationFiveMinute  int64 `json:"cacheCreationEphemeral5mInputTokens"`
	CacheCreationOneHour     int64 `json:"cacheCreationEphemeral1hInputTokens"`
	WebSearchRequests        int64 `json:"webSearchRequests"`
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

// versionTimeout is how long claude --version may take.
const versionTimeout = 30 * time.Second

// logVersion writes claude's version to the log, from claude --version
// with the run's environment.
func logVersion(ctx context.Context, path string, env []string, stderr io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		logf(stderr, "claude --version failed: %v", err)
		return
	}
	logf(stderr, "claude --version: %s", strings.TrimSpace(string(out)))
}

// runClaude runs claude and returns the result. It passes the usage of
// each model call to stdout as claude reports it. It stops claude when
// closed is closed, when ctx ends or when the time budget runs out.
func runClaude(ctx context.Context, cfg Config, inv *harnessv1.Invocation, args []string, closed <-chan struct{}, stdout, stderr io.Writer) *harnessv1.Result {
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
	// claude's standard error and the stream's notes both go to the log,
	// from two goroutines of the command.
	stderr = &syncWriter{w: stderr}
	env := Env(os.Environ(), inv.GetGateway())
	logVersion(ctx, path, env, stderr)

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
	cmd.Env = env
	cmd.Stdin = strings.NewReader(inv.GetInput().GetTask())
	s := newStream(stdout, stderr)
	cmd.Stdout = s
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
	if sec := inv.GetBudget().GetSeconds(); sec > 0 {
		timer := time.NewTimer(time.Duration(sec) * time.Second)
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
	r := s.finish()
	res := result(r, s, err, stopped, inv, elapsed, stderr)
	if s.writeErr != nil {
		logf(stderr, "cannot write a usage figure: %v", s.writeErr)
	}
	return res
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

// result builds the result from claude's report r, which is nil when
// claude wrote none, and from its stream s.
func result(r *report, s *stream, waitErr error, stopped why, inv *harnessv1.Invocation, elapsed time.Duration, stderr io.Writer) *harnessv1.Result {
	switch {
	case stopped == stopRequested:
		logf(stderr, "stopped claude on request")
	case stopped == timeBudget:
		logf(stderr, "stopped claude at the time budget of %d seconds", inv.GetBudget().GetSeconds())
	case waitErr != nil:
		logf(stderr, "claude exited: %v", waitErr)
	}
	seconds := int64(math.Ceil(elapsed.Seconds()))
	res := &harnessv1.Result{Status: harnessv1.Result_STATUS_FAILED, Usage: r.usage(s, seconds), Refusals: r.refusals(stderr)}
	switch {
	case stopped == stopRequested:
		res.Status = harnessv1.Result_STATUS_STOPPED
	case limited(r, s):
		logf(stderr, "the provider's usage or rate limit refused a call of claude: %s", clip([]byte(r.message())))
		res.Status = harnessv1.Result_STATUS_LIMITED
	case r == nil:
		logf(stderr, "claude wrote no report")
	case stopped == timeBudget:
	case r.Subtype != "success":
		logf(stderr, "claude reported %s: %s", r.Subtype, strings.Join(r.Errors, "; "))
	case r.IsError:
		// claude reports an error of the API, such as a call that the
		// model gateway refused at the invocation's cap, with the subtype
		// success and the error in result.
		logf(stderr, "claude reported an error: %s", clip([]byte(r.Result)))
	case inv.GetOutputSchema().GetJsonSchema() == "":
		res.Status = harnessv1.Result_STATUS_COMPLETED
	default:
		var output bytes.Buffer
		if len(r.StructuredOutput) == 0 || string(r.StructuredOutput) == "null" || json.Compact(&output, r.StructuredOutput) != nil {
			logf(stderr, "claude reported no structured output")
			break
		}
		res.Status, res.Output = harnessv1.Result_STATUS_COMPLETED, output.String()
	}
	return res
}

// limited reports whether a provider's usage or rate limit ended the run:
// the run did not succeed, and claude reported a limit that refused a
// call, as a rate_limit_event whose status is rejected or an API error with
// the status 429 (decision 0249). A limit that claude waited out, after
// which the run succeeded, does not count.
func limited(r *report, s *stream) bool {
	if r != nil && r.Subtype == "success" && !r.IsError {
		return false
	}
	return s.rejected || (r != nil && r.APIErrorStatus == 429)
}

// message is what the report says about how the run ended.
func (r *report) message() string {
	if r == nil {
		return "claude wrote no report"
	}
	if r.Result != "" {
		return r.Result
	}
	return strings.Join(r.Errors, "; ")
}

// failed is a failed result with the wall time only.
func failed(r *report, seconds int64) *harnessv1.Result {
	return &harnessv1.Result{Status: harnessv1.Result_STATUS_FAILED, Usage: r.usage(nil, seconds)}
}

// usage is the run's usage. The tokens are every token of every model
// that the run used: input, output, and input written to and read from the
// prompt cache, from the report, or from what the stream passed when
// claude wrote no report. The cost is claude's total cost, rounded to a
// microdollar. The seconds are the wall time of claude's process, rounded
// up.
func (r *report) usage(s *stream, seconds int64) *harnessv1.Usage {
	u := &harnessv1.Usage{Seconds: seconds}
	if r == nil {
		if s != nil {
			u.Tokens = s.tokens()
		}
		return u
	}
	for _, m := range r.ModelUsage {
		u.Tokens += m.InputTokens + m.OutputTokens + m.CacheReadInputTokens + m.CacheCreationInputTokens
	}
	u.CostMicrodollars = int64(math.Round(r.TotalCostUSD * 1e6))
	return u
}

// syncWriter makes the writes to w one at a time.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
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
