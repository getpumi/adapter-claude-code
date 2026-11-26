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

// The fake's stream, in the shape of claude -p --output-format stream-json
// --verbose --include-partial-messages: one model call whose events it
// writes, and a report that counts that call and a call of its own
// outside the conversation, as claude does, with usage figures that the
// tests check.
const (
	fakeCost    = 0.25
	fakeTokens  = 2 + 167 + 812 + 10671 + 5 + 9
	fakeVersion = "2.1.332 (Claude Code)"
	fakeModel   = "claude-fable-5-2"
	sideModel   = "claude-haiku-5-5"
)

// fakeCall is the events of the model call that the fake writes, with the
// usage of message_start, whose output tokens are not yet counted, and the
// final output tokens in message_delta.
var fakeCall = []string{
	`{"type":"stream_event","api_message_id":"msg_1","event":{"type":"message_start","message":{"id":"msg_1","model":"` + fakeModel + `",` +
		`"usage":{"input_tokens":2,"cache_creation_input_tokens":10671,"cache_read_input_tokens":812,"output_tokens":1,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":671,"ephemeral_1h_input_tokens":10000},"service_tier":"standard","speed":"fast","inference_geo":"us"}}}}`,
	`{"type":"assistant","message":{"id":"msg_1","model":"` + fakeModel + `","usage":{"input_tokens":2,"output_tokens":1}}}`,
	`{"type":"stream_event","api_message_id":"msg_1","event":{"type":"message_delta","usage":{"input_tokens":2,"cache_creation_input_tokens":10671,"cache_read_input_tokens":812,"output_tokens":167,"server_tool_use":{"web_search_requests":1}}}}`,
	`{"type":"stream_event","api_message_id":"msg_1","event":{"type":"message_stop"}}`,
}

// fakeDenials are the permission denials of the fake's report in the mode
// denials.
const fakeDenials = `[` +
	`{"tool_name":"Write","tool_use_id":"t1","tool_input":{"file_path":"docs/a.txt","content":"x"}},` +
	`{"tool_name":"Write","tool_use_id":"t2","tool_input":{"file_path":"docs/a.txt","content":"y"}},` +
	`{"tool_name":"Bash","tool_use_id":"t3","tool_input":{"command":"rm -rf out"}},` +
	`{"tool_name":"WebFetch","tool_use_id":"t4","tool_input":{"url":"https://example.com/page","prompt":"p"}},` +
	`{"tool_name":"Read","tool_use_id":"t5","tool_input":{"file_path":"/etc/passwd"}},` +
	`{"tool_name":"Glob","tool_use_id":"t6","tool_input":{"pattern":"**"}},` +
	`{"tool_name":"NotebookEdit","tool_use_id":"t7","tool_input":{"notebook_path":"n.ipynb"}},` +
	`{"tool_name":"Agent","tool_use_id":"t8","tool_input":{"prompt":"p"}}` +
	`]`

// fakeAPIError is the result of the fake's report in the mode apierror.
const fakeAPIError = "API Error: 403 the Pumi model gateway refused the call: the invocation reached its token or cost cap"

// fakeLimit is the result of the fake's report in the mode limit.
const fakeLimit = "API Error: Request rejected (429) · This request would exceed your rate limit."

// fakeResult is the fake's report. extra is more fields, each with a
// leading comma.
func fakeResult(subtype string, isError bool, output, extra string) string {
	structured := ""
	if output != "" {
		structured = `,"structured_output":` + output
	}
	return fmt.Sprintf(`{"type":"result","subtype":%q,"is_error":%t,"result":"text",`+
		`"total_cost_usd":%v,"modelUsage":{%q:{"inputTokens":2,"outputTokens":167,`+
		`"cacheReadInputTokens":812,"cacheCreationInputTokens":10671,"cacheCreationEphemeral5mInputTokens":671,`+
		`"cacheCreationEphemeral1hInputTokens":10000,"webSearchRequests":1,"costUSD":%v},`+
		`%q:{"inputTokens":5,"outputTokens":9,"costUSD":0}},"permission_denials":[]%s%s}`,
		subtype, isError, fakeCost, fakeModel, fakeCost, sideModel, structured, extra)
}

// fakeClaude is the fake claude's main function. With the argument
// --version it prints its version. Otherwise it writes its record and then
// the stream of the mode:
//
//   - success: the call, and a report of success with the structured
//     output in FAKE_CLAUDE_OUTPUT
//   - denials: the same, with the permission denials of fakeDenials
//   - budget: the call, and a report that the budget ran out, and exits
//     with status 1
//   - apierror: the call, and a report of an error of the API, as claude
//     writes when the model gateway refuses a call at the invocation's cap,
//     and exits with status 1
//   - limit: a rate_limit_event that rejects a call and a report of the
//     API's error 429, and exits with status 1
//   - garbage: writes something that is not JSON
//   - wait: writes the call, waits for an interrupt, then reports an
//     aborted run
//   - ignore: ignores interrupts and waits to be killed
func fakeClaude() int {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		_, _ = fmt.Println(fakeVersion)
		return 0
	}
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
	return fakeStream(os.Getenv(fakeMode), interrupts)
}

// fakeStream writes the fake's stream in a mode.
func fakeStream(mode string, interrupts <-chan os.Signal) int {
	say := func(lines ...string) {
		for _, l := range lines {
			_, _ = fmt.Println(l)
		}
	}
	say(`{"type":"system","subtype":"init","claude_code_version":"2.1.332"}`)
	switch mode {
	case "success":
		say(fakeCall...)
		say(fakeResult("success", false, os.Getenv(fakeOutput), ""))
	case "denials":
		say(fakeCall...)
		say(fakeResult("success", false, os.Getenv(fakeOutput), `,"permission_denials":`+fakeDenials))
	case "budget":
		say(fakeCall...)
		say(fakeResult("error_max_budget_usd", true, "", ""))
		return 1
	case "apierror":
		// Claude Code 2.1.331 reports a refused call with the subtype
		// success, is_error true, no errors and the API's error in result.
		say(fakeCall...)
		say(strings.Replace(fakeResult("success", true, "", ""), `"result":"text"`, `"result":"`+fakeAPIError+`"`, 1))
		return 1
	case "limit":
		// Claude Code 2.1.332 against a provider that answers 429.
		say(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour"}}`)
		say(strings.Replace(fakeResult("success", true, "", `,"terminal_reason":"api_error","api_error_status":429`), `"result":"text"`, `"result":"`+fakeLimit+`"`, 1))
		return 1
	case "garbage":
		say("not JSON")
	case "wait":
		say(fakeCall...)
		<-interrupts
		say(fakeResult("error_during_execution", true, "", ""))
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
