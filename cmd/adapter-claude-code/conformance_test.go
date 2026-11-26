// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"getpumi.dev/pumi/conformance"
)

// adapterEnv is set when the test binary runs as the adapter under test:
// TestMain then runs the adapter's main function instead of the tests.
const adapterEnv = "PUMI_ADAPTER_CLAUDE_CODE_UNDER_TEST"

// keptEnv are the variables of the test's environment that the conformance
// runs keep. TestMain clears the rest, so that a person's own settings, or
// the settings of a cloud container, do not reach claude, and gives claude
// a HOME of its own with no sign-in.
var keptEnv = []string{"PATH", "TMPDIR", "LANG", "GOCOVERDIR"}

func TestMain(m *testing.M) {
	if os.Getenv(adapterEnv) != "" {
		os.Exit(run(context.Background(), os.Stdin, os.Stdout, os.Stderr))
	}
	clearEnv()
	os.Exit(m.Run())
}

// installTools is the script of the devcontainer image that pins claude's
// version.
const installTools = "../../.devcontainer/install-tools.sh"

// pinnedClaude returns the version of claude that install-tools.sh pins.
func pinnedClaude(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(installTools)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^CLAUDE_CODE_VERSION=(\S+)$`).FindSubmatch(data)
	if m == nil {
		t.Fatalf("%s pins no CLAUDE_CODE_VERSION", installTools)
	}
	return string(m[1])
}

// TestClaudeVersion checks that the claude on the PATH, which the
// conformance suite runs, is the version that the devcontainer pins.
func TestClaudeVersion(t *testing.T) {
	want := pinnedClaude(t)
	out, err := exec.CommandContext(t.Context(), "claude", "--version").Output()
	if err != nil {
		t.Fatalf("claude --version: %v; the conformance suite runs the pinned claude %s, which the devcontainer installs", err, want)
	}
	if got, _, _ := strings.Cut(strings.TrimSpace(string(out)), " "); got != want {
		t.Errorf("claude --version = %q, want %s, the version that %s pins", out, want, installTools)
	}
}

// TestConformance runs the adapter conformance suite (decision 0244)
// against the adapter and the pinned real claude, which calls the suite's
// fake Messages API provider.
func TestConformance(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("claude is not on the PATH: %v", err)
	}
	home := t.TempDir()
	conformance.Run(t, conformance.Adapter{
		Path:  exe,
		API:   conformance.APIMessages,
		Modes: []conformance.Mode{conformance.ModeSignIn, conformance.ModeAPIKey},
		Turn:  turn,
		SignIn: func(baseURL, credential string) []string {
			return []string{"ANTHROPIC_BASE_URL=" + baseURL, "ANTHROPIC_AUTH_TOKEN=" + credential}
		},
		Env: []string{
			adapterEnv + "=1",
			"HOME=" + home,
			"PATH=" + filepath.Dir(claude) + string(os.PathListSeparator) + os.Getenv("PATH"),
			// The settings with which claude waits out a provider's limit,
			// as a cloud container of Claude Code sets them; the adapter
			// turns them off (decision 0249).
			"CLAUDE_CODE_RETRY_WATCHDOG=1",
			"CLAUDE_CODE_RETRY_WATCHDOG_MAX_WAIT_MS=1200000",
			"CLAUDE_CODE_RETRY_WATCHDOG_MAX_USAGE_LIMIT_WAIT_MS=90000",
		},
		NestedSession:   []string{"CLAUDECODE=1"},
		ReportsRefusals: true,
		ReportsLimits:   true,
		Timeout:         3 * time.Minute,
	})
}

// clearEnv clears the environment except keptEnv.
func clearEnv() {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(keptEnv, name) {
			_ = os.Unsetenv(name)
		}
	}
}

// turn turns a step of a conformance scenario into Claude Code's tool call.
func turn(s conformance.Step) conformance.Turn {
	var name string
	var input map[string]string
	switch s.Kind {
	case conformance.StepWrite:
		name, input = "Write", map[string]string{"file_path": s.Path, "content": s.Content}
	case conformance.StepRead:
		name, input = "Read", map[string]string{"file_path": s.Path}
	case conformance.StepRun:
		name, input = "Bash", map[string]string{"command": s.Command, "description": "Run the command of a conformance step"}
	case conformance.StepFetch:
		name, input = "WebFetch", map[string]string{"url": s.URL, "prompt": "Say what the page holds."}
	case conformance.StepOutput:
		return conformance.Turn{Calls: []conformance.ToolCall{{Name: "StructuredOutput", Input: s.Output}}}
	}
	data, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return conformance.Turn{Calls: []conformance.ToolCall{{Name: name, Input: string(data)}}}
}
