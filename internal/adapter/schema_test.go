// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package adapter

import (
	"errors"
	"testing"
)

func TestClaudeSchema(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   string
	}{
		{
			name: "bundle of make generate",
			schema: `{"$defs":{"a.v1.M.jsonschema.json":{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
				`"properties":{"summary":{"type":"string"}},"type":"object"}},` +
				`"$id":"a.v1.M.jsonschema.bundle.json","$ref":"#/$defs/a.v1.M.jsonschema.json",` +
				`"$schema":"https://json-schema.org/draft/2020-12/schema"}`,
			want: `{"$defs":{"a.v1.M.jsonschema.json":{"properties":{"summary":{"type":"string"}},"type":"object"}},` +
				`"$id":"a.v1.M.jsonschema.bundle.json","$ref":"#/$defs/a.v1.M.jsonschema.json","type":"object"}`,
		},
		{
			name:   "already an object",
			schema: schema,
			want:   `{"properties":{"summary":{"type":"string"}},"required":["summary"],"type":"object"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := claudeSchema(tt.schema)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("claudeSchema = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestClaudeSchemaRefuses(t *testing.T) {
	for _, s := range []string{"{not json", `["array"]`, `{"type":"string"}`} {
		if _, err := claudeSchema(s); !errors.Is(err, ErrInvocation) {
			t.Errorf("claudeSchema(%s) returned %v, want ErrInvocation", s, err)
		}
	}
}
