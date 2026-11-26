// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package adapter

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"sync"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// stream reads the lines that claude -p --output-format stream-json
// --verbose --include-partial-messages writes while it runs. It passes
// each model call's usage to the runner as a line of the adapter's
// standard output as soon as the call ends (decisions 0241 and 0252), and
// keeps claude's report, which is the last line.
//
// claude writes the events of the provider's stream as stream_event lines:
// message_start holds the call's model and its input and cache tokens, and
// message_delta the final output tokens. The assistant lines hold only
// the usage of message_start, whose output tokens are not yet counted, so
// the stream reads the events. A call whose events claude does not write,
// such as a call of its own outside the conversation, is in the report's
// usage per model, which finish compares with what the stream passed.
type stream struct {
	mu  sync.Mutex
	out io.Writer
	log io.Writer
	// partial is the part of a line not yet ended by a newline.
	partial []byte
	// calls are the model calls in progress, by claude's id of the call's
	// message, and current is the id of the call that started last, for
	// an event that names none.
	calls   map[string]*callUsage
	current string
	// passed are the tokens passed so far, by model.
	passed map[string]*harnessv1.UsageFigure
	report *report
	// rejected says that claude reported a usage or rate limit that
	// refused a call.
	rejected bool
	// writeErr is the first error of a write to out.
	writeErr error
}

func newStream(out, log io.Writer) *stream {
	return &stream{out: out, log: log, calls: map[string]*callUsage{}, passed: map[string]*harnessv1.UsageFigure{}}
}

// Write takes claude's standard output and handles each line it ends.
func (s *stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			return len(p), nil
		}
		s.handle(s.partial[:i])
		s.partial = s.partial[i+1:]
	}
}

// event is the part of one line of claude's stream that the stream reads.
type event struct {
	Type          string    `json:"type"`
	Event         *apiEvent `json:"event"`
	APIMessageID  string    `json:"api_message_id"`
	RateLimitInfo *struct {
		Status string `json:"status"`
	} `json:"rate_limit_info"`
}

// apiEvent is an event of the provider's stream, as claude passes it.
type apiEvent struct {
	Type    string `json:"type"`
	Message *struct {
		ID    string    `json:"id"`
		Model string    `json:"model"`
		Usage *apiUsage `json:"usage"`
	} `json:"message"`
	Usage *apiUsage `json:"usage"`
}

// apiUsage is the usage of a call in the Messages API. A field that an
// event leaves out is nil.
type apiUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheCreation            *struct {
		FiveMinute *int64 `json:"ephemeral_5m_input_tokens"`
		OneHour    *int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	ServerToolUse *struct {
		WebSearchRequests *int64 `json:"web_search_requests"`
	} `json:"server_tool_use"`
	Speed        *string `json:"speed"`
	InferenceGeo *string `json:"inference_geo"`
}

// callUsage is the usage of one model call so far.
type callUsage struct {
	model string
	usage apiUsage
}

// merge sets the fields of u that v holds.
func (u *apiUsage) merge(v *apiUsage) {
	if v == nil {
		return
	}
	for _, f := range []struct{ to, from **int64 }{
		{&u.InputTokens, &v.InputTokens},
		{&u.CacheCreationInputTokens, &v.CacheCreationInputTokens},
		{&u.CacheReadInputTokens, &v.CacheReadInputTokens},
		{&u.OutputTokens, &v.OutputTokens},
	} {
		if *f.from != nil {
			*f.to = *f.from
		}
	}
	if v.CacheCreation != nil {
		u.CacheCreation = v.CacheCreation
	}
	if v.ServerToolUse != nil {
		u.ServerToolUse = v.ServerToolUse
	}
	if v.Speed != nil {
		u.Speed = v.Speed
	}
	if v.InferenceGeo != nil {
		u.InferenceGeo = v.InferenceGeo
	}
}

func value(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// figure is the usage figure of a call. A cache write that the usage does
// not split by duration is a 5-minute write, as UsageFigure says.
func (c *callUsage) figure() *harnessv1.UsageFigure {
	u := c.usage
	f := &harnessv1.UsageFigure{
		Model:           c.model,
		InputTokens:     value(u.InputTokens),
		CacheReadTokens: value(u.CacheReadInputTokens),
		OutputTokens:    value(u.OutputTokens),
	}
	if u.CacheCreation != nil {
		f.FiveMinuteCacheWriteTokens = value(u.CacheCreation.FiveMinute)
		f.OneHourCacheWriteTokens = value(u.CacheCreation.OneHour)
	}
	if rest := value(u.CacheCreationInputTokens) - f.FiveMinuteCacheWriteTokens - f.OneHourCacheWriteTokens; rest > 0 {
		f.FiveMinuteCacheWriteTokens += rest
	}
	if u.ServerToolUse != nil {
		f.WebSearches = value(u.ServerToolUse.WebSearchRequests)
	}
	if u.Speed != nil {
		f.Speed = *u.Speed
	}
	if u.InferenceGeo != nil {
		f.InferenceGeo = *u.InferenceGeo
	}
	return f
}

// handle handles one line of claude's stream.
func (s *stream) handle(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var e event
	if err := json.Unmarshal(line, &e); err != nil {
		logf(s.log, "claude wrote a line that is not JSON: %q", clip(line))
		return
	}
	switch e.Type {
	case "stream_event":
		s.handleEvent(e)
	case "rate_limit_event":
		if e.RateLimitInfo != nil && e.RateLimitInfo.Status == "rejected" {
			s.rejected = true
		}
	case "result":
		r := &report{}
		if err := json.Unmarshal(line, r); err != nil {
			logf(s.log, "claude's report does not parse: %v", err)
			return
		}
		s.report = r
	}
}

// handleEvent follows a call through the events of the provider's stream,
// and passes its usage when it ends.
func (s *stream) handleEvent(e event) {
	if e.Event == nil {
		return
	}
	id := e.APIMessageID
	if id == "" && e.Event.Type != "message_start" {
		id = s.current
	}
	switch e.Event.Type {
	case "message_start":
		c := &callUsage{}
		if m := e.Event.Message; m != nil {
			c.model = m.Model
			c.usage.merge(m.Usage)
			if id == "" {
				id = m.ID
			}
		}
		s.calls[id] = c
		s.current = id
	case "message_delta":
		if c, ok := s.calls[id]; ok {
			c.usage.merge(e.Event.Usage)
		}
	case "message_stop":
		if c, ok := s.calls[id]; ok {
			delete(s.calls, id)
			s.pass(c.figure())
		}
	}
}

// pass writes a usage figure as a line and adds it to what was passed.
func (s *stream) pass(f *harnessv1.UsageFigure) {
	t, ok := s.passed[f.GetModel()]
	if !ok {
		t = &harnessv1.UsageFigure{Model: f.GetModel()}
		s.passed[f.GetModel()] = t
	}
	t.InputTokens += f.GetInputTokens()
	t.FiveMinuteCacheWriteTokens += f.GetFiveMinuteCacheWriteTokens()
	t.OneHourCacheWriteTokens += f.GetOneHourCacheWriteTokens()
	t.CacheReadTokens += f.GetCacheReadTokens()
	t.OutputTokens += f.GetOutputTokens()
	t.WebSearches += f.GetWebSearches()
	s.writeLine(&harnessv1.Line{Kind: &harnessv1.Line_Usage{Usage: f}})
}

// writeLine writes one line of the adapter's standard output.
func (s *stream) writeLine(l *harnessv1.Line) {
	if s.writeErr != nil {
		return
	}
	data, err := protojson.Marshal(l)
	if err == nil {
		_, err = s.out.Write(append(data, '\n'))
	}
	s.writeErr = err
}

// finish handles a last line that no newline ended and passes what
// claude's report counts beyond the figures passed, model by model: the
// usage of calls whose events claude did not write. It returns claude's
// report, or nil when claude wrote none.
func (s *stream) finish() *report {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.partial) > 0 {
		s.handle(s.partial)
		s.partial = nil
	}
	if s.report == nil {
		return nil
	}
	models := make([]string, 0, len(s.report.ModelUsage))
	for m := range s.report.ModelUsage {
		models = append(models, m)
	}
	sort.Strings(models)
	for _, m := range models {
		if f := s.rest(m, s.report.ModelUsage[m]); f != nil {
			s.pass(f)
		}
	}
	return s.report
}

// rest returns the figure of what the report counts for the model m beyond
// what the stream passed, or nil when it counts nothing more.
func (s *stream) rest(m string, u modelUsage) *harnessv1.UsageFigure {
	passed := s.passed[m]
	if passed == nil {
		passed = &harnessv1.UsageFigure{}
	}
	fiveMinute, oneHour := u.CacheCreationFiveMinute, u.CacheCreationOneHour
	if extra := u.CacheCreationInputTokens - fiveMinute - oneHour; extra > 0 {
		fiveMinute += extra
	}
	f := &harnessv1.UsageFigure{
		Model:                      m,
		InputTokens:                u.InputTokens - passed.GetInputTokens(),
		FiveMinuteCacheWriteTokens: fiveMinute - passed.GetFiveMinuteCacheWriteTokens(),
		OneHourCacheWriteTokens:    oneHour - passed.GetOneHourCacheWriteTokens(),
		CacheReadTokens:            u.CacheReadInputTokens - passed.GetCacheReadTokens(),
		OutputTokens:               u.OutputTokens - passed.GetOutputTokens(),
		WebSearches:                u.WebSearchRequests - passed.GetWebSearches(),
	}
	more := false
	for _, v := range []*int64{&f.InputTokens, &f.FiveMinuteCacheWriteTokens, &f.OneHourCacheWriteTokens, &f.CacheReadTokens, &f.OutputTokens, &f.WebSearches} {
		if *v < 0 {
			logf(s.log, "claude's report counts fewer tokens of %s than its stream did; the adapter passed the stream's", m)
			*v = 0
		}
		more = more || *v > 0
	}
	if !more {
		return nil
	}
	return f
}

// tokens is every token that the stream passed.
func (s *stream) tokens() int64 {
	var n int64
	for _, f := range s.passed {
		n += f.GetInputTokens() + f.GetFiveMinuteCacheWriteTokens() + f.GetOneHourCacheWriteTokens() + f.GetCacheReadTokens() + f.GetOutputTokens()
	}
	return n
}

// denial is one tool call that claude's settings refused, as its report
// lists it in permission_denials.
type denial struct {
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// refusal translates a denial into a refusal of the result (decision
// 0249), or returns nil for a tool that is not of a kind in the capability
// list.
func (d denial) refusal() *harnessv1.Refusal {
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
		Pattern      string `json:"pattern"`
		Command      string `json:"command"`
		URL          string `json:"url"`
	}
	_ = json.Unmarshal(d.ToolInput, &in)
	switch d.ToolName {
	case "Write", "Edit", "MultiEdit":
		return pathRefusal(harnessv1.Capabilities_TOOL_KIND_EDIT, in.FilePath)
	case "NotebookEdit":
		return pathRefusal(harnessv1.Capabilities_TOOL_KIND_EDIT, in.NotebookPath)
	case "Read":
		return pathRefusal(harnessv1.Capabilities_TOOL_KIND_READ, in.FilePath)
	case "Glob", "Grep":
		p := in.Path
		if p == "" {
			p = "."
		}
		return pathRefusal(harnessv1.Capabilities_TOOL_KIND_READ, p)
	case "Bash":
		return &harnessv1.Refusal{Tool: harnessv1.Capabilities_TOOL_KIND_RUN_COMMANDS, Asked: &harnessv1.Refusal_Command{Command: in.Command}}
	case "WebFetch":
		u, err := url.Parse(in.URL)
		if err != nil || u.Hostname() == "" {
			return nil
		}
		return &harnessv1.Refusal{Tool: harnessv1.Capabilities_TOOL_KIND_WEB, Asked: &harnessv1.Refusal_Host{Host: u.Hostname()}}
	}
	return nil
}

func pathRefusal(kind harnessv1.Capabilities_ToolKind, p string) *harnessv1.Refusal {
	if p == "" {
		return nil
	}
	return &harnessv1.Refusal{Tool: kind, Asked: &harnessv1.Refusal_Path{Path: p}}
}

// refusals returns the refusals of the report's denials, each once.
func (r *report) refusals(log io.Writer) []*harnessv1.Refusal {
	if r == nil {
		return nil
	}
	var out []*harnessv1.Refusal
	for _, d := range r.PermissionDenials {
		f := d.refusal()
		if f == nil {
			logf(log, "claude's settings refused a call of %s, which is of no kind in the capability list", d.ToolName)
			continue
		}
		seen := false
		for _, o := range out {
			seen = seen || proto.Equal(o, f)
		}
		if !seen {
			out = append(out, f)
		}
	}
	return out
}
