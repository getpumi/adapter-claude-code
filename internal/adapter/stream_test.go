// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package adapter

import (
	"bytes"
	"strings"
	"testing"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// TestStreamCacheWriteWithoutSplit checks that a cache write that the
// usage does not split by duration is a 5-minute write, and that the
// stream reads a last line that no newline ends.
func TestStreamCacheWriteWithoutSplit(t *testing.T) {
	var out, log bytes.Buffer
	s := newStream(&out, &log)
	lines := `{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"x","usage":{"input_tokens":1,"cache_creation_input_tokens":40,"output_tokens":1}}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":3}}}` + "\n" +
		`{"type":"stream_event","event":{"type":"message_stop"}}`
	if _, err := s.Write([]byte(lines)); err != nil {
		t.Fatal(err)
	}
	if r := s.finish(); r != nil {
		t.Errorf("finish() = %v, want no report", r)
	}
	l := &harnessv1.Line{}
	if err := protojson.Unmarshal(bytes.TrimSpace(out.Bytes()), l); err != nil {
		t.Fatal(err)
	}
	want := &harnessv1.UsageFigure{Model: "x", InputTokens: 1, FiveMinuteCacheWriteTokens: 40, OutputTokens: 3}
	if !proto.Equal(l.GetUsage(), want) {
		t.Errorf("figure = %v, want %v", l.GetUsage(), want)
	}
}

// TestStreamReportCountsLess checks that when claude's report counts fewer
// tokens than its stream did, the adapter passes nothing more for that
// model and says so in the log.
func TestStreamReportCountsLess(t *testing.T) {
	var out, log bytes.Buffer
	s := newStream(&out, &log)
	s.pass(&harnessv1.UsageFigure{Model: "x", InputTokens: 10, OutputTokens: 5})
	if got := s.rest("x", modelUsage{InputTokens: 4, OutputTokens: 5}); got != nil {
		t.Errorf("rest = %v, want nil", got)
	}
	if !strings.Contains(log.String(), "fewer tokens of x") {
		t.Errorf("the log does not say that the report counts fewer tokens:\n%s", log.String())
	}
}

// TestStreamSkipsLinesThatAreNotJSON checks that a line that is not JSON
// goes to the log.
func TestStreamSkipsLinesThatAreNotJSON(t *testing.T) {
	var out, log bytes.Buffer
	s := newStream(&out, &log)
	if _, err := s.Write([]byte("\n  \nnot JSON\n")); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || !strings.Contains(log.String(), "not JSON") {
		t.Errorf("out = %q, log = %q; want nothing out and the line in the log", out.String(), log.String())
	}
}
