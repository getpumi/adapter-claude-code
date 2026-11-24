// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package adapter

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The tests run the adapter against a fake claude: the test binary itself,
// through a link named claude in a directory that the tests put first on the
// PATH. TestMain runs the fake when the binary is started under that name.
// The fake takes its behavior from the environment, which the adapter passes
// on to claude.
const (
	// fakeMode is how the fake behaves; see fakeClaude.
	fakeMode = "FAKE_CLAUDE_MODE"
	// fakeOutput is the structured output that the fake reports.
	fakeOutput = "FAKE_CLAUDE_OUTPUT"
	// fakeRecord is the directory in which the fake writes record.json.
	fakeRecord = "FAKE_CLAUDE_RECORD"
)

// record is what the fake writes when it starts: how the adapter ran it.
type record struct {
	Args  []string
	Stdin string
	Dir   string
	// Env holds the variables of claude's environment whose names start
	// with ANTHROPIC_ or CLAUDE_CODE_.
	Env map[string]string
}

// The fake's report, in the shape of claude -p --output-format json, with
// usage figures that the tests check.
const (
	fakeCost   = 0.25
	fakeTokens = 2 + 167 + 812 + 10671
)

// fakeAPIError is the result of the fake's report in the mode apierror.
const fakeAPIError = "API Error: 403 the Pumi model gateway refused the call: the invocation reached its token or cost cap"

func fakeResult(subtype string, isError bool, output string) string {
	structured := ""
	if output != "" {
		structured = `,"structured_output":` + output
	}
	return fmt.Sprintf(`{"type":"result","subtype":%q,"is_error":%t,"result":"text",`+
		`"total_cost_usd":%v,"modelUsage":{"claude-haiku-5-5":{"inputTokens":2,"outputTokens":167,`+
		`"cacheReadInputTokens":812,"cacheCreationInputTokens":10671,"costUSD":%v}}%s}`,
		subtype, isError, fakeCost, fakeCost, structured)
}

// fakeClaude is the fake claude's main function. The modes are:
//
//   - success: reports success with the structured output in FAKE_CLAUDE_OUTPUT
//   - budget: reports that the budget ran out and exits with status 1
//   - apierror: reports an error of the API, as claude does when the model
//     gateway refuses a call at the invocation's cap, and exits with
//     status 1
//   - garbage: writes something that is not JSON
//   - wait: waits for an interrupt, then reports an aborted run
//   - ignore: ignores interrupts and waits to be killed
func fakeClaude() int {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 3
	}
	dir, _ := os.Getwd()
	env := map[string]string{}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_CODE_") {
			env[name] = value
		}
	}
	data, err := json.Marshal(record{
		Args:  os.Args[1:],
		Stdin: string(stdin),
		Dir:   dir,
		Env:   env,
	})
	if err != nil {
		return 3
	}
	if err := writeRecord(os.Getenv(fakeRecord), data); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "fake claude:", err)
		return 3
	}
	_, _ = fmt.Fprintln(os.Stderr, "fake claude started")
	// Claude Code writes this notice when --model names a model that it
	// does not know, such as a model role, and then runs as usual.
	_, _ = fmt.Fprintln(os.Stderr, `[claude-code:unrecognized_model] {"model":"frontier","query_source":"sdk"}`)
	switch os.Getenv(fakeMode) {
	case "success":
		_, _ = fmt.Println(fakeResult("success", false, os.Getenv(fakeOutput)))
	case "budget":
		_, _ = fmt.Println(fakeResult("error_max_budget_usd", true, ""))
		return 1
	case "apierror":
		// Claude Code 2.1.331 reports a refused call with the subtype
		// success, is_error true, no errors and the API's error in result.
		_, _ = fmt.Println(strings.Replace(fakeResult("success", true, ""), `"result":"text"`, `"result":"`+fakeAPIError+`"`, 1))
		return 1
	case "garbage":
		_, _ = fmt.Println("not JSON")
	case "wait":
		<-interrupts
		_, _ = fmt.Println(fakeResult("error_during_execution", true, ""))
	case "ignore":
		signal.Ignore(os.Interrupt, syscall.SIGTERM)
		time.Sleep(time.Minute)
	default:
		return 3
	}
	return 0
}

// writeRecord writes the record through an os.Root, because gosec reports
// a file written at a path from the environment (G703).
func writeRecord(dir string, data []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.WriteFile("record.json.tmp", data, 0o600); err != nil {
		return err
	}
	return root.Rename("record.json.tmp", "record.json")
}

func isFake() bool {
	return filepath.Base(os.Args[0]) == "claude"
}
