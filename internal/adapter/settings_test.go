// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package adapter

import (
	"encoding/json"
	"reflect"
	"testing"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

func TestTranslate(t *testing.T) {
	read := harnessv1.Capabilities_TOOL_KIND_READ
	edit := harnessv1.Capabilities_TOOL_KIND_EDIT
	run := harnessv1.Capabilities_TOOL_KIND_RUN_COMMANDS
	web := harnessv1.Capabilities_TOOL_KIND_WEB
	network := func(a harnessv1.Capabilities_Network_Access, hosts ...string) *harnessv1.Capabilities_Network {
		return &harnessv1.Capabilities_Network{Access: a, Hosts: hosts}
	}
	tests := []struct {
		name      string
		caps      *harnessv1.Capabilities
		wantTools []string
		wantAllow []string
		wantDeny  []string
	}{
		{
			name:      "no capabilities",
			caps:      nil,
			wantTools: []string{},
			wantAllow: []string{},
			wantDeny:  []string{"Bash", "Edit", "Glob", "Grep", "NotebookEdit", "Read", "WebFetch", "WebSearch", "Write"},
		},
		{
			name:      "read only",
			caps:      &harnessv1.Capabilities{Tools: []harnessv1.Capabilities_ToolKind{read}},
			wantTools: []string{"Glob", "Grep", "Read"},
			wantAllow: []string{"Glob", "Grep", "Read"},
			wantDeny:  []string{"Bash", "Edit", "NotebookEdit", "WebFetch", "WebSearch", "Write"},
		},
		{
			name: "edit in the write paths",
			caps: &harnessv1.Capabilities{
				Tools:      []harnessv1.Capabilities_ToolKind{read, edit},
				WritePaths: []string{"src/**", "*.go"},
			},
			wantTools: []string{"Edit", "Glob", "Grep", "NotebookEdit", "Read", "Write"},
			wantAllow: []string{"Edit(./*.go)", "Edit(./src/**)", "Glob", "Grep", "Read"},
			wantDeny:  []string{"Bash", "WebFetch", "WebSearch"},
		},
		{
			name: "commands by prefix",
			caps: &harnessv1.Capabilities{
				Tools:    []harnessv1.Capabilities_ToolKind{run},
				Commands: []string{"go test", "make"},
			},
			wantTools: []string{"Bash"},
			wantAllow: []string{"Bash(go test *)", "Bash(go test)", "Bash(make *)", "Bash(make)"},
			wantDeny:  []string{"Edit", "Glob", "Grep", "NotebookEdit", "Read", "WebFetch", "WebSearch", "Write"},
		},
		{
			// An entry that begins with assignments is translated as
			// written, so it allows exactly those assignments (decision
			// 0248); the conformance suite checks what claude then
			// allows and refuses.
			name: "commands with assignments",
			caps: &harnessv1.Capabilities{
				Tools:    []harnessv1.Capabilities_ToolKind{run},
				Commands: []string{"GOWORK=off go test", "A=1 B=2 make"},
			},
			wantTools: []string{"Bash"},
			wantAllow: []string{"Bash(A=1 B=2 make *)", "Bash(A=1 B=2 make)", "Bash(GOWORK=off go test *)", "Bash(GOWORK=off go test)"},
			wantDeny:  []string{"Edit", "Glob", "Grep", "NotebookEdit", "Read", "WebFetch", "WebSearch", "Write"},
		},
		{
			name:      "web with no network",
			caps:      &harnessv1.Capabilities{Tools: []harnessv1.Capabilities_ToolKind{web}, Network: network(harnessv1.Capabilities_Network_ACCESS_NONE)},
			wantTools: []string{},
			wantAllow: []string{},
			wantDeny:  []string{"Bash", "Edit", "Glob", "Grep", "NotebookEdit", "Read", "WebFetch", "WebSearch", "Write"},
		},
		{
			name:      "web with the gateway only",
			caps:      &harnessv1.Capabilities{Tools: []harnessv1.Capabilities_ToolKind{web}, Network: network(harnessv1.Capabilities_Network_ACCESS_GATEWAY)},
			wantTools: []string{},
			wantAllow: []string{},
			wantDeny:  []string{"Bash", "Edit", "Glob", "Grep", "NotebookEdit", "Read", "WebFetch", "WebSearch", "Write"},
		},
		{
			name:      "web with hosts",
			caps:      &harnessv1.Capabilities{Tools: []harnessv1.Capabilities_ToolKind{web}, Network: network(harnessv1.Capabilities_Network_ACCESS_HOSTS, "go.dev", "pkg.go.dev")},
			wantTools: []string{"WebFetch"},
			wantAllow: []string{"WebFetch(domain:go.dev)", "WebFetch(domain:pkg.go.dev)"},
			wantDeny:  []string{"Bash", "Edit", "Glob", "Grep", "NotebookEdit", "Read", "WebSearch", "Write"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools, settings := Translate(tt.caps)
			checkTranslation(t, tools, settings, tt.wantTools, tt.wantAllow, tt.wantDeny)
		})
	}
}

func checkTranslation(t *testing.T, tools []string, settings Settings, wantTools, wantAllow, wantDeny []string) {
	t.Helper()
	if !reflect.DeepEqual(tools, wantTools) {
		t.Errorf("tools = %q, want %q", tools, wantTools)
	}
	if !reflect.DeepEqual(settings.Permissions.Allow, wantAllow) {
		t.Errorf("allow = %q, want %q", settings.Permissions.Allow, wantAllow)
	}
	if !reflect.DeepEqual(settings.Permissions.Deny, wantDeny) {
		t.Errorf("deny = %q, want %q", settings.Permissions.Deny, wantDeny)
	}
	if settings.Permissions.DefaultMode != "dontAsk" {
		t.Errorf("default mode = %q, want dontAsk", settings.Permissions.DefaultMode)
	}
	if settings.Permissions.DisableBypassPermissionsMode != "disable" {
		t.Errorf("disableBypassPermissionsMode = %q, want disable", settings.Permissions.DisableBypassPermissionsMode)
	}
}

// TestSettingsJSON checks the names of the keys that Claude Code reads.
func TestSettingsJSON(t *testing.T) {
	_, settings := Translate(&harnessv1.Capabilities{Tools: []harnessv1.Capabilities_ToolKind{harnessv1.Capabilities_TOOL_KIND_READ}})
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"permissions":{"defaultMode":"dontAsk","disableBypassPermissionsMode":"disable","allow":["Glob","Grep","Read"],"deny":["Bash","Edit","NotebookEdit","WebFetch","WebSearch","Write"]}}`
	if string(data) != want {
		t.Errorf("settings = %s, want %s", data, want)
	}
}
