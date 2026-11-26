// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package adapter

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// InvocationHeader is the header in which claude sends the invocation token when
// the harness keeps its own sign-in (decision 0225).
const InvocationHeader = "X-Pumi-Token"

// elsewhere are the variables of the person's own provider settings that
// would send claude's calls somewhere other than the gateway (decision
// 0226). The adapter clears them when the invocation has a gateway. The
// names are from the Claude Code documentation of its environment
// variables.
var elsewhere = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_CUSTOM_HEADERS",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_USE_MANTLE",
	"CLAUDE_CODE_USE_ANTHROPIC_AWS",
	"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD",
	"ANTHROPIC_BEDROCK_BASE_URL",
	"ANTHROPIC_BEDROCK_MANTLE_BASE_URL",
	"ANTHROPIC_VERTEX_BASE_URL",
	"ANTHROPIC_FOUNDRY_BASE_URL",
	"ANTHROPIC_AWS_BASE_URL",
	"ANTHROPIC_GOOGLE_CLOUD_BASE_URL",
}

// otherModels are the variables of the person's own settings that would
// send claude's calls to a model other than the one the invocation names:
// the model role through the gateway (decision 0233), or the role's model
// on the sign-in path (decision 0254). The adapter clears them on both
// paths. The names are from the Claude Code documentation of its
// environment variables.
var otherModels = []string{
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL",
}

// waitsOutLimits are the variables of claude's settings that make it wait
// out a provider's usage or rate limit instead of stopping, which the
// adapter clears on both paths (decision 0249). The names are from
// Claude Code 2.1.332, in whose binary they are; a cloud container of
// Claude Code sets the first three, and claude then waited more than two
// minutes on a retry-after of one hour (decision 0249).
var waitsOutLimits = []string{
	"CLAUDE_CODE_RETRY_WATCHDOG",
	"CLAUDE_CODE_RETRY_WATCHDOG_MAX_WAIT_MS",
	"CLAUDE_CODE_RETRY_WATCHDOG_MAX_USAGE_LIMIT_WAIT_MS",
	"CLAUDE_CODE_RETRY_WATCHDOG_USAGE_LIMIT_RECHECK_MS",
	"CLAUDE_CODE_RETRY_WATCHDOG_SCOPE",
	"CLAUDE_CODE_DESKTOP_WORKFLOW_LIMIT_WAIT",
}

// signIns are the variables of the person's own credentials. The adapter
// clears them when the gateway holds the provider's API key, so that
// claude sends only the invocation token, and leaves them when the harness
// keeps its sign-in.
var signIns = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_PROFILE",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_REFRESH_TOKEN",
}

// checkGateway refuses an invocation whose gateway claude cannot use. Every
// agent run has a gateway (decision 0226). The error never holds the
// token.
func checkGateway(gw *harnessv1.Gateway) error {
	if gw == nil {
		return fmt.Errorf("%w: it has no model gateway, which every agent run needs (decision 0226)", ErrInvocation)
	}
	u, err := url.Parse(gw.GetBaseUrl())
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: the model gateway's base URL %q is not an http or https URL", ErrInvocation, gw.GetBaseUrl())
	}
	t := gw.GetToken()
	if t == "" {
		return fmt.Errorf("%w: the model gateway has no token", ErrInvocation)
	}
	for _, c := range []byte(t) {
		// The token goes into an HTTP header, as ANTHROPIC_AUTH_TOKEN or in
		// ANTHROPIC_CUSTOM_HEADERS, whose lines are headers: only visible
		// ASCII, with no space, keeps it one header value.
		if c < '!' || c > '~' {
			return fmt.Errorf("%w: the model gateway's token holds a character that is not visible ASCII", ErrInvocation)
		}
	}
	return nil
}

// Env returns claude's environment: the adapter's environment without the
// person's settings that would send calls to another model or wait out a
// provider's limit.
//
// With a gateway, it also leaves out the person's provider settings that
// would send calls elsewhere, and sends claude's calls to the gateway
// (decision 0225). When the gateway holds the provider's API key, claude
// has no sign-in of its own and sends the token as its bearer token. When
// the harness keeps its sign-in, claude sends the sign-in, and the token in
// the header X-Pumi-Token.
//
// Without a gateway, on the sign-in path, claude keeps the person's own
// sign-in and provider settings and talks to the provider directly
// (decision 0240).
func Env(environ []string, gw *harnessv1.Gateway) []string {
	cleared := slices.Concat(otherModels, waitsOutLimits)
	if gw != nil {
		cleared = append(cleared, elsewhere...)
	}
	if gw != nil && !gw.GetKeepsSignIn() {
		cleared = append(cleared, signIns...)
	}
	env := make([]string, 0, len(environ)+2)
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(cleared, name) {
			env = append(env, kv)
		}
	}
	if gw == nil {
		return env
	}
	env = append(env, "ANTHROPIC_BASE_URL="+gw.GetBaseUrl())
	if gw.GetKeepsSignIn() {
		return append(env, "ANTHROPIC_CUSTOM_HEADERS="+InvocationHeader+": "+gw.GetToken())
	}
	return append(env, "ANTHROPIC_AUTH_TOKEN="+gw.GetToken())
}
