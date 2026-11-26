// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package adapter

import (
	"errors"
	"slices"
	"strings"
	"testing"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"google.golang.org/protobuf/proto"
)

// Kinds of tool in the tests.
const (
	toolRead = harnessv1.Capabilities_TOOL_KIND_READ
	toolEdit = harnessv1.Capabilities_TOOL_KIND_EDIT
	toolRun  = harnessv1.Capabilities_TOOL_KIND_RUN_COMMANDS
	toolWeb  = harnessv1.Capabilities_TOOL_KIND_WEB
)

// equal reports whether two lists of messages are equal, message by
// message.
func equal[M proto.Message](a, b []M) bool {
	return slices.EqualFunc(a, b, func(x, y M) bool { return proto.Equal(x, y) })
}

// signInInvocation is an invocation on the sign-in path: no gateway, and
// the models of the role (decisions 0240 and 0254).
func signInInvocation(workspace string) *harnessv1.Invocation {
	inv := invocation(workspace)
	inv.Gateway = nil
	inv.Model = "claude-fable-5-2"
	inv.FallbackModel = "claude-opus-5-5"
	return inv
}

// TestRunOnSignIn runs an invocation on the sign-in path: claude keeps the
// person's own sign-in and provider settings, asks for the role's model and
// its fallback, and gets no budget of its own, because the runner counts
// and caps the usage (decisions 0240, 0241 and 0254).
func TestRunOnSignIn(t *testing.T) {
	recordDir, workspace := setup(t, "success")
	result, stderr, err := run(t, line(t, signInInvocation(workspace)))
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr)
	}
	if result.GetStatus() != harnessv1.Result_STATUS_COMPLETED {
		t.Errorf("status = %v, want completed\n%s", result.GetStatus(), stderr)
	}
	r := readRecord(t, recordDir)
	for name, want := range map[string]string{"--model": "claude-fable-5-2", "--fallback-model": "claude-opus-5-5", "--output-format": "stream-json"} {
		if got, ok := flag(r.Args, name); !ok || got != want {
			t.Errorf("claude's flag %s = %q (present %t), want %q; args %q", name, got, ok, want, r.Args)
		}
	}
	if slices.Contains(r.Args, "--max-budget-usd") {
		t.Errorf("claude's args %q hold --max-budget-usd on the sign-in path, where the runner caps the run", r.Args)
	}
	for _, name := range append(slices.Clone(personSignIns), "ANTHROPIC_BASE_URL", "ANTHROPIC_CUSTOM_HEADERS", "CLAUDE_CODE_USE_BEDROCK") {
		if got, want := r.Env[name], machine[name]; got != want {
			t.Errorf("%s = %q, want the person's own %q on the sign-in path", name, got, want)
		}
	}
	for _, name := range slices.Concat(personModels, personWaits) {
		if value, ok := r.Env[name]; ok {
			t.Errorf("claude's environment has %s = %q, want it cleared", name, value)
		}
	}
}

// TestRunWithoutFallbackModel runs an invocation on the sign-in path whose
// role has no fallback on the same adapter.
func TestRunWithoutFallbackModel(t *testing.T) {
	recordDir, workspace := setup(t, "success")
	inv := signInInvocation(workspace)
	inv.FallbackModel = ""
	if _, stderr, err := run(t, line(t, inv)); err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr)
	}
	if args := readRecord(t, recordDir).Args; slices.Contains(args, "--fallback-model") {
		t.Errorf("claude's args %q hold --fallback-model, but the invocation has no fallback model", args)
	}
}

// TestRunPassesUsageFigures checks that the adapter passes each model
// call's usage as a line before the result, with the final output tokens
// of message_delta, and then what claude's report counts beyond the
// stream, such as a call of claude's own (decisions 0241 and 0252).
func TestRunPassesUsageFigures(t *testing.T) {
	_, workspace := setup(t, "success")
	result, figures, stderr, err := runFigures(t, line(t, signInInvocation(workspace)))
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr)
	}
	want := []*harnessv1.UsageFigure{
		{
			Model: fakeModel, InputTokens: 2, FiveMinuteCacheWriteTokens: 671, OneHourCacheWriteTokens: 10000,
			CacheReadTokens: 812, OutputTokens: 167, WebSearches: 1, Speed: "fast", InferenceGeo: "us",
		},
		{Model: sideModel, InputTokens: 5, OutputTokens: 9},
	}
	if !equal(want, figures) {
		t.Errorf("usage figures = %v, want %v", figures, want)
	}
	if got := result.GetUsage().GetTokens(); got != fakeTokens {
		t.Errorf("the result's tokens = %d, want %d", got, fakeTokens)
	}
}

// TestRunLogsClaudeVersion checks that the log names claude's version.
func TestRunLogsClaudeVersion(t *testing.T) {
	_, workspace := setup(t, "success")
	_, stderr, err := run(t, line(t, signInInvocation(workspace)))
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "claude --version: "+fakeVersion) {
		t.Errorf("the log does not name claude's version %q:\n%s", fakeVersion, stderr)
	}
}

// TestRunLimited checks that a call that a provider's usage or rate limit
// refused ends the run with STATUS_LIMITED (decision 0249).
func TestRunLimited(t *testing.T) {
	for name, inv := range map[string]func(string) *harnessv1.Invocation{"sign-in": signInInvocation, "gateway": invocation} {
		t.Run(name, func(t *testing.T) {
			_, workspace := setup(t, "limit")
			result, stderr, err := run(t, line(t, inv(workspace)))
			if err != nil {
				t.Fatalf("Run: %v\n%s", err, stderr)
			}
			if result.GetStatus() != harnessv1.Result_STATUS_LIMITED {
				t.Errorf("status = %v, want limited\n%s", result.GetStatus(), stderr)
			}
			if !strings.Contains(stderr, "rate limit") {
				t.Errorf("the log does not say what the provider said:\n%s", stderr)
			}
		})
	}
}

// TestRunReportsRefusals checks that the result names the tool calls that
// claude's settings refused, each once, by the kind of tool and what was
// asked (decision 0249).
func TestRunReportsRefusals(t *testing.T) {
	_, workspace := setup(t, "denials")
	result, stderr, err := run(t, line(t, signInInvocation(workspace)))
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr)
	}
	want := []*harnessv1.Refusal{
		{Tool: toolEdit, Asked: &harnessv1.Refusal_Path{Path: "docs/a.txt"}},
		{Tool: toolRun, Asked: &harnessv1.Refusal_Command{Command: "rm -rf out"}},
		{Tool: toolWeb, Asked: &harnessv1.Refusal_Host{Host: "example.com"}},
		{Tool: toolRead, Asked: &harnessv1.Refusal_Path{Path: "/etc/passwd"}},
		{Tool: toolRead, Asked: &harnessv1.Refusal_Path{Path: "."}},
		{Tool: toolEdit, Asked: &harnessv1.Refusal_Path{Path: "n.ipynb"}},
	}
	if !equal(want, result.GetRefusals()) {
		t.Errorf("refusals = %v, want %v", result.GetRefusals(), want)
	}
	if !strings.Contains(stderr, "refused a call of Agent") {
		t.Errorf("the log does not name the refused call of a tool of no kind:\n%s", stderr)
	}
}

// TestRunRefusesSignInWithoutModel checks that an invocation with no
// gateway must name a model (decision 0254).
func TestRunRefusesSignInWithoutModel(t *testing.T) {
	_, workspace := setup(t, "success")
	inv := signInInvocation(workspace)
	inv.Model = ""
	_, _, err := run(t, line(t, inv))
	if !errors.Is(err, ErrInvocation) || !strings.Contains(err.Error(), "neither a model gateway nor a model") {
		t.Errorf("Run returned %v, want ErrInvocation about the model", err)
	}
}
