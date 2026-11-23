<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: The Pumi Authors
-->

# Pumi adapter for Claude Code

The harness adapter that lets [Pumi](https://github.com/getpumi/pumi) run Claude Code. Pumi's runner starts it once per invocation, in the invocation's workspace. It reads a `pumi.harness.v1.Invocation` as one line of protojson from standard input, runs `claude -p` with the task, the output's JSON Schema, the budget and Claude Code settings that translate the capability list, and writes a `pumi.harness.v1.Result` to standard output. Closing standard input stops it.

Until Pumi's model gateway exists (epic E7), `claude` uses its own sign-in on the machine that runs it.

Pumi is in early development and has no release yet.

- Design: [getpumi/design](https://github.com/getpumi/design) holds the settled design and the decision records.
- Work: the epics and ready work are in the [Pumi GitHub project](https://github.com/orgs/getpumi/projects/5).
- Contributing: see the organization's [CONTRIBUTING](https://github.com/getpumi/.github/blob/main/CONTRIBUTING.md). Every commit carries a `Signed-off-by` line under the Developer Certificate of Origin.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
