// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package adapter

import (
	"slices"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// Settings are the Claude Code settings that the adapter passes with
// --settings: the permission rules that translate an invocation's
// capability list (decision 0159).
type Settings struct {
	Permissions Permissions `json:"permissions"`
}

// Permissions are Claude Code's permission settings.
type Permissions struct {
	// DefaultMode is "dontAsk", which denies every tool call that no allow
	// rule permits instead of asking.
	DefaultMode string `json:"defaultMode"`
	// DisableBypassPermissionsMode is "disable", which refuses the mode
	// that skips the permission rules.
	DisableBypassPermissionsMode string `json:"disableBypassPermissionsMode"`
	// Allow and Deny are the permission rules.
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

// The built-in tools of Claude Code that each kind of tool in the capability
// list stands for. The adapter makes no other built-in tool available.
var (
	readTools = []string{"Glob", "Grep", "Read"}
	editTools = []string{"Edit", "NotebookEdit", "Write"}
	runTools  = []string{"Bash"}
	webTools  = []string{"WebFetch", "WebSearch"}
)

// Translate translates a capability list into the built-in tools that
// claude makes available (--tools) and the permission rules of its settings
// (decision 0159). What the list leaves out is not available, or not
// allowed:
//
//   - Reading allows Read, Glob and Grep.
//   - Editing makes Edit, Write and NotebookEdit available and allows them
//     only in the write paths: an Edit rule covers every tool that edits
//     files. A write path is a glob relative to the workspace, and the rule
//     anchors it to the working directory, which is the workspace.
//   - Running commands makes Bash available and allows only the commands
//     that start with one of the listed prefixes.
//   - The web makes WebFetch available only when the network access lists
//     hosts, and allows it only for those hosts. WebSearch cannot be limited
//     to hosts, so it is never available.
//
// Every tool of those kinds that is not available is also denied by a rule,
// and the default mode denies whatever no rule allows.
func Translate(caps *harnessv1.Capabilities) (tools []string, settings Settings) {
	tools = []string{}
	allow := []string{}
	for _, kind := range caps.GetTools() {
		t, a := translateKind(kind, caps)
		tools = append(tools, t...)
		allow = append(allow, a...)
	}
	tools = sorted(tools)
	deny := []string{}
	for _, t := range sorted(slices.Concat(readTools, editTools, runTools, webTools)) {
		if !slices.Contains(tools, t) {
			deny = append(deny, t)
		}
	}
	return tools, Settings{Permissions: Permissions{
		DefaultMode:                  "dontAsk",
		DisableBypassPermissionsMode: "disable",
		Allow:                        sorted(allow),
		Deny:                         deny,
	}}
}

// translateKind returns the tools that one kind of tool makes available and
// the rules that allow them.
func translateKind(kind harnessv1.Capabilities_ToolKind, caps *harnessv1.Capabilities) (tools, allow []string) {
	switch kind {
	case harnessv1.Capabilities_TOOL_KIND_READ:
		return readTools, readTools
	case harnessv1.Capabilities_TOOL_KIND_EDIT:
		for _, p := range caps.GetWritePaths() {
			allow = append(allow, "Edit(./"+p+")")
		}
		return editTools, allow
	case harnessv1.Capabilities_TOOL_KIND_RUN_COMMANDS:
		for _, c := range caps.GetCommands() {
			allow = append(allow, "Bash("+c+")", "Bash("+c+" *)")
		}
		return runTools, allow
	case harnessv1.Capabilities_TOOL_KIND_WEB:
		network := caps.GetNetwork()
		if network.GetAccess() != harnessv1.Capabilities_Network_ACCESS_HOSTS || len(network.GetHosts()) == 0 {
			return nil, nil
		}
		for _, h := range network.GetHosts() {
			allow = append(allow, "WebFetch(domain:"+h+")")
		}
		return []string{"WebFetch"}, allow
	case harnessv1.Capabilities_TOOL_KIND_UNSPECIFIED:
	}
	return nil, nil
}

// sorted returns the strings sorted, without repeats.
func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return slices.Compact(s)
}
