<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->

# Instructions for agents

Read this file before you change anything in this repository. It is harness-neutral: CLAUDE.md only imports it, so every agent reads the same rules ([0091](https://github.com/getpumi/design/blob/main/decisions/0091-agents-md.md)). People who work here follow the same rules.

This repository is the Pumi harness adapter for Claude Code ([0185](https://github.com/getpumi/design/blob/main/decisions/0185-claude-code-adapter-repository-and-no-gateway.md)). The design lives in [getpumi/design](https://github.com/getpumi/design). Read its [decisions index](https://github.com/getpumi/design/blob/main/decisions/README.md) and the [runner protocol](https://github.com/getpumi/design/blob/main/components/runner-protocol.md) before you change the adapter; the adapter contract is decided in [0158](https://github.com/getpumi/design/blob/main/decisions/0158-harness-adapter-process.md) to [0160](https://github.com/getpumi/design/blob/main/decisions/0160-adapter-output-schema-and-result.md). A change to the code that changes the design needs a decision there first. Do not rely on anything you seem to remember.

The rules of the core repository's [AGENTS.md](https://github.com/getpumi/pumi/blob/main/AGENTS.md) hold here too: starting only Ready work, test first, follow-up work, dependency updates alone, the merge rule, the SPDX lines, no TODO or FIXME comments, no `//nolint` comments and the commit trailers. This file lists only what differs.

## Layout

- One Go module, `getpumi.dev/adapter-claude-code`. It requires the core's `api` module, `getpumi.dev/pumi/api`, and its conformance module, `getpumi.dev/pumi/conformance`, at a pseudo-version of the same commit on getpumi/pumi's main until those modules have a release.
- `cmd/adapter-claude-code` is the `main` package of the adapter.
- `internal/adapter` reads the invocation, translates the capability list into Claude Code's settings, runs `claude -p`, passes each call's usage from its stream and writes the result.
- `cmd/adapter-claude-code/conformance_test.go` runs the conformance suite of `getpumi.dev/pumi/conformance` against the adapter, which is the test binary started again, and the `claude` on the PATH, and checks that it is the version pinned in `.devcontainer/install-tools.sh`.
- `internal/checks` holds copies of the core's DCO, SPDX and nolint checks and the tests of the lint configuration, from getpumi/pumi at `11eb184`, with the import paths changed. The SPDX check lists no exempt path of the core.

## Commands

- `make build` compiles the module and writes the adapter to `bin/`.
- `make test` runs `go test -race -shuffle=on -count=1 ./...` and writes a coverage profile to `coverage/`. The tests of `internal/adapter` run it against a fake `claude`: the test binary, through a link named `claude` that the tests put first on the PATH. The conformance tests run the real `claude` that the devcontainer pins, in an environment that holds only `PATH`, `TMPDIR`, `LANG` and `GOCOVERDIR` of the test's and a `HOME` of their own.
- `make lint`, `make dco RANGE=<base>..<head>`, `make spdx` and `make image` work as in the core repository. `make image` pushes to `ghcr.io/getpumi/adapter-claude-code-devcontainer`.

The supported development environment is the repository's devcontainer, on Linux ([0087](https://github.com/getpumi/design/blob/main/decisions/0087-devcontainer-is-the-environment.md)).

## Facts

- Claude Code 2.1.330 (checked on 2026-11-23): `claude -p --output-format json` writes one JSON report when the run ends, also when it gets SIGINT, with `subtype` (such as `success`, `error_max_budget_usd` or `error_during_execution`), `is_error`, `total_cost_usd`, `modelUsage` (tokens per model) and, with `--json-schema`, `structured_output`. The top-level `usage` can be all zeros when `modelUsage` is not. It exits with status 1 when the budget runs out.
- Claude Code 2.1.330: a prompt given as an argument after `--disallowedTools` or `--allowedTools` is read as a tool name, so the adapter gives the prompt on standard input. `--setting-sources ""` leaves out the user's settings and the settings in the working directory; a `.claude/settings.json` that allowed Bash and Write was ignored. With `--permission-mode dontAsk` and an `Edit(./src/**)` rule, Write created `src/a.txt` and was denied `b.txt`. Bash ran `cat README` with only `Bash(ls)` allowed, because Claude Code allows some read-only commands without a rule.
- Claude Code 2.1.330 (checked on 2026-11-23): `--json-schema` with a `$schema` of draft 2020-12, as in the bundles that the core's `make generate` writes, exits with "no schema with key or ref". Without `$schema`, a root with only `$ref` gives success with no `structured_output`; with `"type":"object"` beside the `$ref`, nested `$defs` and `$ref` work. The adapter changes the schema in this way before it passes it.
- Cloud threads only: getpumi.dev is blocked, and proxy.golang.org answers for `getpumi.dev/pumi/api` with a pseudo-version, such as `https://proxy.golang.org/getpumi.dev/pumi/api/@v/11eb184.info`.
- Cloud threads only: the golangci-lint on the PATH is an older release; download the version pinned in `.devcontainer/install-tools.sh` and check its SHA-256 before running `make lint` or `make test`.
- Claude Code 2.1.331 (checked on 2026-11-24): `--model frontier`, a name it does not know, writes `[claude-code:unrecognized_model] {"model":"frontier","query_source":"sdk"}` to standard error and runs as usual, with `subtype` `success` and `modelUsage` keyed by `frontier`. A "say woof" run through a local proxy at `ANTHROPIC_BASE_URL` made five `/v1/messages` calls, four that named `frontier` and one that named `claude-sonnet-5-5`, not the role; `ANTHROPIC_CUSTOM_HEADERS` added its header to all five.
- Cloud threads only: `CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST` is set, and `claude` sends the host's own bearer token and ignores `ANTHROPIC_AUTH_TOKEN` (checked on 2026-11-24 with 2.1.331), so the path where the gateway holds the API key cannot be shown with a real `claude` there. `ANTHROPIC_BASE_URL` is followed, and `claude` also sends its `/v1/code/agent-proxy/` requests to it.
- Claude Code 2.1.332 (checked on 2026-11-26): with `--output-format stream-json --verbose`, an `assistant` line holds the usage of the call's `message_start`, whose `output_tokens` is 1, not the final count. With `--include-partial-messages`, `stream_event` lines hold the provider's events with `api_message_id`: `message_start` with the model and the input and cache tokens, split into `ephemeral_5m_input_tokens` and `ephemeral_1h_input_tokens`, and `message_delta` with the final `output_tokens`. The report's `modelUsage` gives per model `cacheCreationEphemeral5mInputTokens`, `cacheCreationEphemeral1hInputTokens` and `webSearchRequests`, and the report also holds `permission_denials` (`tool_name`, `tool_use_id`, `tool_input`), `terminal_reason` and, after a refused call, `api_error_status`.
- Claude Code 2.1.332 (checked on 2026-11-26): with `CLAUDE_CODE_RETRY_WATCHDOG=1`, `CLAUDE_CODE_RETRY_WATCHDOG_MAX_WAIT_MS=1200000` and `CLAUDE_CODE_RETRY_WATCHDOG_MAX_USAGE_LIMIT_WAIT_MS=90000`, as a cloud container sets them, claude went on waiting for more than two and a half minutes on the conformance suite's 429 with `retry-after: 3600`; with them cleared it ended in under a second with `api_error_status` 429. The other `CLAUDE_CODE_RETRY_WATCHDOG_` variables and `CLAUDE_CODE_DESKTOP_WORKFLOW_LIMIT_WAIT` are names in its binary whose effect was not checked.
- Claude Code 2.1.332 (checked on 2026-11-26): the rules `Bash(PUMI_CONFORMANCE=1 touch)` and `Bash(PUMI_CONFORMANCE=1 touch *)` allowed `PUMI_CONFORMANCE=1 touch out/assigned` and refused `touch out/bare`, `PUMI_OTHER=1 touch out/other` and `PUMI_CONFORMANCE=1 PUMI_OTHER=1 touch out/extra`. A `WebFetch(domain:<host>)` rule refused a fetch of another host, which claude listed in `permission_denials` with its `url`. `Write` with a `file_path` relative to the working directory wrote the file there.
- The npm package `@anthropic-ai/claude-code-linux-<arch>` holds the native `claude` as `package/claude`; the npm registry publishes its SHA-512 integrity, in base64, at `https://registry.npmjs.org/@anthropic-ai/claude-code-linux-<arch>/<version>`.
