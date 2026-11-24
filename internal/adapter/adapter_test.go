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
	for name, value := range machine {
		t.Setenv(name, value)
	}
	return recordDir, t.TempDir()
}

// machine is the person's own provider settings and credentials, which the
// tests put in the adapter's environment.
var machine = map[string]string{
	"ANTHROPIC_BASE_URL":                     "https://elsewhere.example",
	"ANTHROPIC_CUSTOM_HEADERS":               "X-Elsewhere: 1",
	"ANTHROPIC_MODEL":                        "claude-opus-5-5",
	"ANTHROPIC_SMALL_FAST_MODEL":             "claude-haiku-5-5",
	"ANTHROPIC_DEFAULT_SONNET_MODEL":         "claude-sonnet-5-5",
	"ANTHROPIC_VERTEX_BASE_URL":              "https://vertex.example",
	"CLAUDE_CODE_SUBAGENT_MODEL":             "haiku",
	"CLAUDE_CODE_USE_BEDROCK":                "1",
	"CLAUDE_CODE_USE_VERTEX":                 "1",
	"CLAUDE_CODE_USE_FOUNDRY":                "1",
	"CLAUDE_CODE_USE_MANTLE":                 "1",
	"CLAUDE_CODE_USE_ANTHROPIC_AWS":          "1",
	"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD": "1",
	"ANTHROPIC_API_KEY":                      "the person's API key",
	"ANTHROPIC_AUTH_TOKEN":                   "the person's bearer token",
	"CLAUDE_CODE_OAUTH_TOKEN":                "the person's sign-in",
}

// personSignIns are the variables of the person's own credentials, which stay
// when the harness keeps its sign-in.
var personSignIns = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}

// personElsewhere are the variables of the person's provider settings that would
// send claude's calls elsewhere, or to other models than the role, which
// the adapter clears.
var personElsewhere = []string{
	"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_VERTEX_BASE_URL", "CLAUDE_CODE_SUBAGENT_MODEL",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_USE_MANTLE", "CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD",
}

// token is the gateway's invocation token in the tests.
const token = "pumi-invocation-token-0123456789"

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
		Gateway:      &harnessv1.Gateway{BaseUrl: "http://127.0.0.1:4100", Token: token, KeepsSignIn: true},
		OutputSchema: &harnessv1.OutputSchema{Message: "pumi.workflow.v1.Summary", JsonSchema: schema},
		ModelRole:    "frontier",
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
	if !strings.Contains(stderr, "fake claude started") || !strings.Contains(stderr, "unrecognized_model") {
		t.Errorf("the adapter's log does not hold claude's log:\n%s", stderr)
	}
	checkNoToken(t, stderr)

	r := readRecord(t, recordDir)
	checkRecord(t, r, workspace)
	checkGatewayEnv(t, r.Env)
	if got, want := r.Env["ANTHROPIC_CUSTOM_HEADERS"], "X-Pumi-Token: "+token; got != want {
		t.Errorf("ANTHROPIC_CUSTOM_HEADERS = %q, want %q", got, want)
	}
	for _, name := range personSignIns {
		if got, want := r.Env[name], machine[name]; got != want {
			t.Errorf("%s = %q, want the person's own %q, because the harness keeps its sign-in", name, got, want)
		}
	}
}

// TestRunWithAPIKeyInGateway runs an invocation whose gateway holds the
// provider's API key, so that claude has no sign-in of its own and sends
// the token as its credential (decision 0225).
func TestRunWithAPIKeyInGateway(t *testing.T) {
	recordDir, workspace := setup(t, "success")
	inv := invocation(workspace)
	inv.Gateway.KeepsSignIn = false
	result, stderr, err := run(t, line(t, inv))
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr)
	}
	if result.GetStatus() != harnessv1.Result_STATUS_COMPLETED {
		t.Errorf("status = %v, want completed\n%s", result.GetStatus(), stderr)
	}
	checkNoToken(t, stderr)
	r := readRecord(t, recordDir)
	checkGatewayEnv(t, r.Env)
	if got := r.Env["ANTHROPIC_AUTH_TOKEN"]; got != token {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %q, want the invocation token", got)
	}
	for _, name := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_CUSTOM_HEADERS"} {
		if value, ok := r.Env[name]; ok {
			t.Errorf("claude's environment has %s = %q, want it cleared", name, value)
		}
	}
}

// checkGatewayEnv checks that claude's environment sends its calls to the
// gateway and holds none of the person's settings that would send them
// elsewhere.
func checkGatewayEnv(t *testing.T, env map[string]string) {
	t.Helper()
	if got, want := env["ANTHROPIC_BASE_URL"], "http://127.0.0.1:4100"; got != want {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want the gateway %q", got, want)
	}
	for _, name := range personElsewhere {
		if value, ok := env[name]; ok {
			t.Errorf("claude's environment has %s = %q, want it cleared", name, value)
		}
	}
}

// checkNoToken checks that the invocation token is not in the log.
func checkNoToken(t *testing.T, log string) {
	t.Helper()
	if strings.Contains(log, token) {
		t.Errorf("the log holds the invocation token:\n%s", log)
	}
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
	want := map[string]string{
		"--model":              "frontier",
		"--output-format":      "json",
		"--json-schema":        `{"properties":{"summary":{"type":"string"}},"required":["summary"],"type":"object"}`,
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
	noGateway := invocation(workspace)
	noGateway.Gateway = nil
	noBaseURL := invocation(workspace)
	noBaseURL.Gateway.BaseUrl = ""
	notHTTP := invocation(workspace)
	notHTTP.Gateway.BaseUrl = "unix:///run/pumi.sock"
	noToken := invocation(workspace)
	noToken.Gateway.Token = ""
	tokenWithNewline := invocation(workspace)
	tokenWithNewline.Gateway.Token = token + "\nX-Other: 1"
	noRole := invocation(workspace)
	noRole.ModelRole = ""
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no line", "", "invocation"},
		{"not protojson", "{not json}\n", "invocation"},
		{"unknown field", `{"invocation":"inv-1","newField":true}` + "\n", "newField"},
		{"no task", line(t, noTask), "task"},
		{"no gateway", line(t, noGateway), "no model gateway"},
		{"no base URL", line(t, noBaseURL), "base URL"},
		{"base URL not HTTP", line(t, notHTTP), "base URL"},
		{"no token", line(t, noToken), "token"},
		{"token with a newline", line(t, tokenWithNewline), "token"},
		{"no model role", line(t, noRole), "model role"},
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
			if err != nil {
				checkNoToken(t, err.Error())
			}
		})
	}
}
