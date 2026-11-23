// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package adapter

import (
	"encoding/json"
	"fmt"
)

// claudeSchema returns the invocation's JSON Schema in the form that claude
// takes for --json-schema. make generate in the core writes a bundle of
// draft 2020-12 (decision 0160), and Claude Code 2.1.330 refuses it and
// leaves out the structured output, checked on 2026-11-23:
//
//   - With a "$schema" of draft 2020-12 anywhere, claude exits with "no
//     schema with key or ref". claudeSchema removes every "$schema", and
//     claude's validator reads the rest, "$defs" and "$ref" included.
//   - With a root that has only "$ref", claude reports success with no
//     structured output. The output is always a protobuf message, which is
//     a JSON object, so claudeSchema sets the root's "type" to "object".
//
// A schema that does not parse, that is not an object or whose root has
// another type refuses the invocation.
func claudeSchema(s string) (string, error) {
	var root any
	if err := json.Unmarshal([]byte(s), &root); err != nil {
		return "", fmt.Errorf("%w: its output schema is not JSON: %w", ErrInvocation, err)
	}
	m, ok := root.(map[string]any)
	if !ok {
		return "", fmt.Errorf("%w: its output schema is not a JSON object", ErrInvocation)
	}
	if t, ok := m["type"]; ok && t != "object" {
		return "", fmt.Errorf("%w: its output schema has the type %v, not object", ErrInvocation, t)
	}
	m["type"] = "object"
	data, err := json.Marshal(withoutSchemaKeyword(m))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// withoutSchemaKeyword removes the "$schema" members of every object in v.
// A protobuf field name cannot start with "$", so no property is removed.
func withoutSchemaKeyword(v any) any {
	switch v := v.(type) {
	case map[string]any:
		delete(v, "$schema")
		for k, e := range v {
			v[k] = withoutSchemaKeyword(e)
		}
	case []any:
		for i, e := range v {
			v[i] = withoutSchemaKeyword(e)
		}
	}
	return v
}
