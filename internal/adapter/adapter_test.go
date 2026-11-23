// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestMain(m *testing.M) {
	if isFake() {
		os.Exit(fakeClaude())
	}
	os.Exit(m.Run())
}

// schema is the JSON Schema of the structured output in the tests.
const schema = `{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"]}`

// setup puts the fake claude first on the PATH, in the given mode, and
// returns the directory of its record and a workspace.
func setup(t *testing.T, mode string) (recordDir, workspace string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(bin, "claude")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	recordDir = t.TempDir()
	t.Setenv(fakeMode, mode)
	t.Setenv(fakeRecord, recordDir)
	t.Setenv(fakeOutput, `{"summary": "done"}`)
	t.Setenv(fakeSignIn, "the sign-in of the machine")
	t.Setenv("ANTHROPIC_BASE_URL", "the machine's own")
	return recordDir, t.TempDir()
}

func invocation(workspace string) *harnessv1.Invocation {
	return &harnessv1.Invocation{
		Invocation:     "inv-1",
		IdempotencyKey: "key-1",
		Component:      &harnessv1.Component{Kind: harnessv1.Component_KIND_AGENT_RUN, Name: "claude-code", Version: "0.1.0"},
		Input:          &harnessv1.Input{Repo: "repo", Tree: "tree", Task: "Implement the plan."},
		Role:           harnessv1.Role_ROLE_AGENT,
		Capabilities: &harnessv1.Capabilities{
			WritePaths: []string{"src/**"},
			Tools:      []harnessv1.Capabilities_ToolKind{harnessv1.Capabilities_TOOL_KIND_READ, harnessv1.Capabilities_TOOL_KIND_EDIT},
		},
		Budget:       &harnessv1.Usage{CostMicrodollars: 1_500_000},
		Workspace:    workspace,
		OutputSchema: &harnessv1.OutputSchema{Message: "pumi.workflow.v1.Summary", JsonSchema: schema},
	}
}

func line(t *testing.T, inv *harnessv1.Invocation) string {
	t.Helper()
	data, err := protojson.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

// run runs the adapter with the invocation line on its standard input, which
// stays open until the adapter returns, as the runner keeps it.
func run(t *testing.T, input string) (*harnessv1.Result, string, error) {
	t.Helper()
	stdin, w := io.Pipe()
	go func() { _, _ = io.WriteString(w, input) }()
	defer func() { _ = w.Close() }()
	return runWith(t, Config{}, stdin)
}

func runWith(t *testing.T, cfg Config, stdin io.Reader) (*harnessv1.Result, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), cfg, stdin, &stdout, &stderr)
	if err != nil {
		if stdout.Len() != 0 {
			t.Errorf("Run returned %v and wrote %q", err, stdout.String())
		}
		return nil, stderr.String(), err
	}
	var result harnessv1.Result
	if err := protojson.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("the standard output %q is not a Result: %v", stdout.String(), err)
	}
	return &result, stderr.String(), nil
}

func readRecord(t *testing.T, dir string) record {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile("record.json")
	if err != nil {
		t.Fatal(err)
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// flag returns the value that follows name in args, and whether name is
// there.
func flag(args []string, name string) (string, bool) {
	i := slices.Index(args, name)
	if i < 0 || i+1 >= len(args) {
		return "", i >= 0
	}
	return args[i+1], true
}

func TestRunCompleted(t *testing.T) {
	recordDir, workspace := setup(t, "success")
	result, stderr, err := run(t, line(t, invocation(workspace)))
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr)
	}
	if result.GetStatus() != harnessv1.Result_STATUS_COMPLETED {
		t.Errorf("status = %v, want completed\n%s", result.GetStatus(), stderr)
	}
	if got, want := result.GetOutput(), `{"summary":"done"}`; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	usage := result.GetUsage()
	if usage.GetTokens() != fakeTokens || usage.GetCostMicrodollars() != 250_000 || usage.GetSeconds() < 1 {
		t.Errorf("usage = %v, want %d tokens, 250000 microdollars and at least 1 second", usage, fakeTokens)
	}
	if !strings.Contains(stderr, "fake claude started") {
		t.Errorf("the adapter's log does not hold claude's log:\n%s", stderr)
	}

	checkRecord(t, readRecord(t, recordDir), workspace)
}

// checkRecord checks how the adapter ran claude.
func checkRecord(t *testing.T, r record, workspace string) {
	t.Helper()
	if r.Stdin != "Implement the plan." {
		t.Errorf("claude read %q as the prompt, want the task", r.Stdin)
	}
	if got, err := filepath.EvalSymlinks(r.Dir); err != nil || got != mustEval(t, workspace) {
		t.Errorf("claude ran in %q, want the workspace %q", r.Dir, workspace)
	}
	if r.SignIn != "the sign-in of the machine" || r.BaseURL != "the machine's own" {
		t.Errorf("claude's environment has sign-in %q and base URL %q, want the machine's own, unchanged", r.SignIn, r.BaseURL)
	}
	want := map[string]string{
		"--output-format":      "json",
		"--json-schema":        schema,
		"--max-budget-usd":     "1.500000",
		"--permission-mode":    "dontAsk",
		"--permission-prompts": "none",
		"--setting-sources":    "",
		"--tools":              "Edit,Glob,Grep,NotebookEdit,Read,Write",
	}
	for name, value := range want {
		if got, ok := flag(r.Args, name); !ok || got != value {
			t.Errorf("claude's flag %s = %q (present %t), want %q; args %q", name, got, ok, value, r.Args)
		}
	}
	for _, name := range []string{"-p", "--no-session-persistence"} {
		if !slices.Contains(r.Args, name) {
			t.Errorf("claude's args %q do not hold %s", r.Args, name)
		}
	}
	settings, _ := flag(r.Args, "--settings")
	var s Settings
	if err := json.Unmarshal([]byte(settings), &s); err != nil {
		t.Fatalf("--settings %q: %v", settings, err)
	}
	if !slices.Contains(s.Permissions.Allow, "Edit(./src/**)") || !slices.Contains(s.Permissions.Deny, "Bash") {
		t.Errorf("--settings %s does not translate the capabilities", settings)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	got, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRunWithoutBudgetOrSchema(t *testing.T) {
	recordDir, workspace := setup(t, "success")
	t.Setenv(fakeOutput, "")
	inv := invocation(workspace)
	inv.Budget = nil
	inv.OutputSchema = nil
	result, stderr, err := run(t, line(t, inv))
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr)
	}
	if result.GetStatus() != harnessv1.Result_STATUS_COMPLETED || result.GetOutput() != "" {
		t.Errorf("result = %v, want completed with no output", result)
	}
	args := readRecord(t, recordDir).Args
	for _, name := range []string{"--max-budget-usd", "--json-schema"} {
		if slices.Contains(args, name) {
			t.Errorf("claude's args %q hold %s", args, name)
		}
	}
}

func TestRunFailed(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		output   string
		wantCost int64
		wantLog  string
	}{
		{"budget ran out", "budget", "", 250_000, "error_max_budget_usd"},
		{"report is not JSON", "garbage", "", 0, "not a report"},
		{"no structured output", "success", "", 250_000, "no structured output"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, workspace := setup(t, tt.mode)
			t.Setenv(fakeOutput, tt.output)
			result, stderr, err := run(t, line(t, invocation(workspace)))
			if err != nil {
				t.Fatalf("Run: %v\n%s", err, stderr)
			}
			if result.GetStatus() != harnessv1.Result_STATUS_FAILED || result.GetOutput() != "" {
				t.Errorf("result = %v, want failed with no output", result)
			}
			if got := result.GetUsage().GetCostMicrodollars(); got != tt.wantCost {
				t.Errorf("cost = %d, want %d", got, tt.wantCost)
			}
			if !strings.Contains(stderr, tt.wantLog) {
				t.Errorf("the log does not say %q:\n%s", tt.wantLog, stderr)
			}
		})
	}
}

func TestRunClaudeMissing(t *testing.T) {
	_, workspace := setup(t, "success")
	t.Setenv("PATH", t.TempDir())
	result, stderr, err := run(t, line(t, invocation(workspace)))
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr)
	}
	if result.GetStatus() != harnessv1.Result_STATUS_FAILED {
		t.Errorf("status = %v, want failed", result.GetStatus())
	}
	if !strings.Contains(stderr, "claude") {
		t.Errorf("the log does not name claude:\n%s", stderr)
	}
}

// waitForStart waits until the fake has written its record.
func waitForStart(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "record.json")); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the fake claude did not start")
}

// stopAfterStart runs the adapter and closes its standard input once the
// fake has started.
func stopAfterStart(t *testing.T, cfg Config, recordDir, input string) (*harnessv1.Result, string, time.Duration) {
	t.Helper()
	stdin, w := io.Pipe()
	go func() {
		_, _ = io.WriteString(w, input)
		waitForStart(t, recordDir)
		_ = w.Close()
	}()
	start := time.Now()
	result, stderr, err := runWith(t, cfg, stdin)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr)
	}
	return result, stderr, time.Since(start)
}

func TestRunStopsWhenStdinCloses(t *testing.T) {
	recordDir, workspace := setup(t, "wait")
	result, stderr, _ := stopAfterStart(t, Config{}, recordDir, line(t, invocation(workspace)))
	if result.GetStatus() != harnessv1.Result_STATUS_STOPPED || result.GetOutput() != "" {
		t.Errorf("result = %v, want stopped with no output\n%s", result, stderr)
	}
	if got := result.GetUsage().GetCostMicrodollars(); got != 250_000 {
		t.Errorf("cost = %d, want the 250000 that claude reported when it stopped", got)
	}
}

func TestRunKillsClaudeAfterGrace(t *testing.T) {
	recordDir, workspace := setup(t, "ignore")
	cfg := Config{StopGrace: 500 * time.Millisecond}
	result, stderr, took := stopAfterStart(t, cfg, recordDir, line(t, invocation(workspace)))
	if result.GetStatus() != harnessv1.Result_STATUS_STOPPED {
		t.Errorf("status = %v, want stopped\n%s", result.GetStatus(), stderr)
	}
	if took > 20*time.Second {
		t.Errorf("the adapter took %v to stop a claude that ignores interrupts", took)
	}
}

func TestRunStopsAtTimeBudget(t *testing.T) {
	_, workspace := setup(t, "wait")
	inv := invocation(workspace)
	inv.Budget = &harnessv1.Usage{Seconds: 1}
	result, stderr, err := run(t, line(t, inv))
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr)
	}
	if result.GetStatus() != harnessv1.Result_STATUS_FAILED {
		t.Errorf("status = %v, want failed\n%s", result.GetStatus(), stderr)
	}
	if !strings.Contains(stderr, "time budget") {
		t.Errorf("the log does not name the time budget:\n%s", stderr)
	}
}

func TestRunRefusesInvocation(t *testing.T) {
	_, workspace := setup(t, "success")
	noTask := invocation(workspace)
	noTask.Input.Task = ""
	gateway := invocation(workspace)
	gateway.Gateway = &harnessv1.Gateway{BaseUrl: "http://127.0.0.1:1", Token: "t"}
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no line", "", "invocation"},
		{"not protojson", "{not json}\n", "invocation"},
		{"unknown field", `{"invocation":"inv-1","newField":true}` + "\n", "newField"},
		{"no task", line(t, noTask), "task"},
		{"gateway", line(t, gateway), "gateway"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.input == "" {
				_, _, err = runWith(t, Config{}, strings.NewReader(""))
			} else {
				_, _, err = run(t, tt.input)
			}
			if !errors.Is(err, ErrInvocation) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Run returned %v, want ErrInvocation about %q", err, tt.want)
			}
		})
	}
}
