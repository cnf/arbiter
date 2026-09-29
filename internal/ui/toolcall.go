package ui

import (
	"encoding/json"
	"sort"
	"strings"
)

// toolCallView is a content-first rendering of one tool_use block: the
// meaningful part per DESIGN.md's "Session transcript" spec (a command
// string, a pattern+path, a file path), not the {id,name,input} envelope.
// Unrecognized tool names still get a JSON dump, just not a name-specific
// summary — the raw JSON behind a toggle is always available either way.
type toolCallView struct {
	// ID is the tool_use block's own id, kept so a tool_result can be matched
	// to the call it answered: the store records that id in the result's
	// for_id, which makes the pairing exact rather than positional.
	ID      string
	Name    string
	Known   bool
	Summary string
	RawJSON string
}

// toolResultView is a content-first rendering of one tool_result block: the
// output text (or the error), with the {for_id,content,is_error} envelope
// behind the same raw-JSON toggle.
type toolResultView struct {
	// ForID is the id of the tool_use this result answered, when the client
	// recorded one — see toolCallView.ID.
	ForID   string
	Content string
	IsError bool
	RawJSON string
}

// toolUseCanonical mirrors store.blockBody's tool_use encoding — the shape a
// tool_use block's Body was canonicalized to before hashing (see
// internal/store/content.go). Decoded here, on the read side, rather than
// exporting the store's private struct, since the two packages have no other
// reason to share a type.
type toolUseCanonical struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

// toolResultCanonical mirrors store.blockBody's tool_result encoding.
type toolResultCanonical struct {
	ForID   string `json:"for_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

// toolDefView is a content-first rendering of one tool_def block: the name
// and description are what a reader scans (#63's "12 tools offered, name +
// description" shape), the schema is available behind the same raw-JSON
// toggle a tool call's input gets.
type toolDefView struct {
	Name        string
	Description string
	SchemaJSON  string
}

// toolDefCanonical mirrors store.captureTools' encoding
// (internal/store/content.go).
type toolDefCanonical struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema,omitempty"`
}

// parseToolDef decodes a captured tool_def block. A decode failure (captured
// before this UI existed, or a format this UI doesn't know) degrades to a
// named-less entry carrying the raw body, the same "still renders, just
// without a summary" rule parseToolCall follows.
func parseToolDef(body string) toolDefView {
	var c toolDefCanonical
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		return toolDefView{SchemaJSON: body}
	}
	return toolDefView{Name: c.Name, Description: c.Description, SchemaJSON: prettyJSON(c.InputSchema)}
}

// parseToolCall decodes a captured tool_use block into its content-first
// view. A decode failure (the body isn't the canonical shape — captured
// before this UI existed, or from a format this UI doesn't know) degrades to
// an unknown/empty view rather than erroring the whole transcript: the block
// still renders, just without a summary.
func parseToolCall(body string) toolCallView {
	var c toolUseCanonical
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		return toolCallView{RawJSON: body}
	}
	pretty := prettyJSON(c.Input)
	summary, known := summarizeToolInput(c.Name, c.Input)
	return toolCallView{ID: c.ID, Name: c.Name, Known: known, Summary: summary, RawJSON: pretty}
}

// parseToolResult decodes a captured tool_result block into its content-first
// view.
func parseToolResult(body string) toolResultView {
	var c toolResultCanonical
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		return toolResultView{RawJSON: body}
	}
	return toolResultView{ForID: c.ForID, Content: c.Content, IsError: c.IsError, RawJSON: prettyJSON(c)}
}

// toolKind is the capability a tool name refers to, independent of the name a
// particular client happened to use. Different clients name the same tool
// differently ("Bash" vs "terminal" vs "execute_command"), so both the input
// summariser and the badge column classify by substring and must agree —
// hence one table, consulted by both, rather than the same substring list
// written out twice (which is how they drifted before).
type toolKind int

const (
	toolKindOther toolKind = iota
	toolKindTerminal
	toolKindSearch
	toolKindEdit
	toolKindRead
)

// toolKindMatchers maps each kind to the substrings that indicate it, checked
// in order — so a name matching several kinds resolves the same way in the
// summary and the badge.
var toolKindMatchers = []struct {
	kind   toolKind
	substr []string
}{
	{toolKindTerminal, []string{"bash", "terminal", "shell", "exec", "run_command", "runcommand"}},
	{toolKindSearch, []string{"grep", "search", "ripgrep", "find"}},
	{toolKindEdit, []string{"edit", "write", "patch", "str_replace"}},
	{toolKindRead, []string{"read"}},
}

// classifyTool maps a tool name to its kind by substring. Unrecognised names
// return toolKindOther.
func classifyTool(name string) toolKind {
	lower := strings.ToLower(name)
	for _, m := range toolKindMatchers {
		if containsAny(lower, m.substr...) {
			return m.kind
		}
	}
	return toolKindOther
}

// summarizeToolInput renders the meaningful part of a tool call's input for
// the small set of tool names coding-agent clients (Claude Code, opencode,
// Hermes) actually send: a shell/terminal command, a grep/search pattern plus
// its path, or a file-edit tool's target path. Matching is by capability
// rather than an exact list, since different clients name the same tool
// differently (see classifyTool). An unrecognized name falls back to a
// generic one-line rendering of its input keys, which is still more readable
// than the raw JSON envelope even though it isn't the tool's dedicated
// summary.
func summarizeToolInput(name string, input map[string]interface{}) (summary string, known bool) {
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := input[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
		return ""
	}

	switch classifyTool(name) {
	case toolKindTerminal:
		if cmd := str("command", "cmd", "script"); cmd != "" {
			return cmd, true
		}
	case toolKindSearch:
		pattern := str("pattern", "query", "regex")
		path := str("path", "file", "glob", "file_glob")
		switch {
		case pattern != "" && path != "":
			return pattern + "  " + path, true
		case pattern != "":
			return pattern, true
		}
	case toolKindEdit, toolKindRead:
		if path := str("path", "file_path", "filepath", "file"); path != "" {
			return path, true
		}
	}
	return genericToolSummary(input), false
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// genericToolSummary is the fallback for a tool name this UI does not
// recognize: a compact "key=value, key=value" line over the input map, still
// more scannable at a glance than the full JSON envelope. Map iteration order
// is randomized by Go, so keys are sorted for a stable render across loads.
func genericToolSummary(input map[string]interface{}) string {
	if len(input) == 0 {
		return ""
	}
	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+compactValue(input[k]))
	}
	return strings.Join(parts, ", ")
}

// compactValue renders one input value compactly for the generic summary —
// short strings verbatim, anything else as its JSON form, capped so one huge
// value can't blow out the line.
func compactValue(v interface{}) string {
	s, ok := v.(string)
	if !ok {
		b, err := json.Marshal(v)
		if err != nil {
			return "?"
		}
		s = string(b)
	}
	const maxLen = 80
	if len(s) > maxLen {
		return s[:maxLen] + "…"
	}
	return s
}

// prettyJSON renders a value as indented JSON for the "raw JSON" toggle. A
// marshal failure (practically unreachable for the decoded canonical types
// above) falls back to an empty string rather than panicking a page render.
func prettyJSON(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}
