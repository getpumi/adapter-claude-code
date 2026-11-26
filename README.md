<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->

# Pumi adapter for Claude Code

The harness adapter that lets [Pumi](https://github.com/getpumi/pumi) run Claude Code. Pumi's runner starts it once per invocation, in the invocation's workspace. It reads a `pumi.harness.v1.Invocation` as one line of protojson from standard input, runs `claude -p` with the task, the output's JSON Schema, the budget and Claude Code settings that translate the capability list, and writes lines of `pumi.harness.v1.Line` to standard output: the usage of each model call as `claude` reports it, then the `pumi.harness.v1.Result`, with the refusals that `claude` reported and the status `STATUS_LIMITED` when a provider's usage or rate limit stopped the run. Its log starts with `claude --version`. Closing standard input stops it.

The adapter offers both of Pumi's credential modes. In the sign-in mode, the invocation names no gateway: `claude` runs on the person's own sign-in and talks to the provider directly, with the role's model in `--model` and its fallback in `--fallback-model`, and Pumi's runner counts and caps the usage figures that the adapter passes. In both modes the adapter clears the person's settings that would send calls to another model or make `claude` wait out a provider's limit.

In the API key mode, every model call of `claude` goes through Pumi's model gateway, which the invocation names. The adapter sets `ANTHROPIC_BASE_URL` to the gateway and asks for the invocation's model role with `--model`. When the gateway holds the provider's API key, `claude` sends the invocation's token as `ANTHROPIC_AUTH_TOKEN`; when the harness keeps its own sign-in, `claude` sends the sign-in, and the token in the `X-Pumi-Token` header through `ANTHROPIC_CUSTOM_HEADERS`. The adapter clears the person's own provider settings that would send calls elsewhere.

`make test` runs Pumi's adapter conformance suite, `getpumi.dev/pumi/conformance`, against the adapter and the real `claude` that the devcontainer pins, which calls the suite's fake provider.

Pumi is in early development and has no release yet.

- Design: [getpumi/design](https://github.com/getpumi/design) holds the settled design and the decision records.
- Work: the epics and ready work are in the [Pumi GitHub project](https://github.com/orgs/getpumi/projects/5).
- Contributing: see the organization's [CONTRIBUTING](https://github.com/getpumi/.github/blob/main/CONTRIBUTING.md). Every commit carries a `Signed-off-by` line under the Developer Certificate of Origin.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
