package ui

import (
	"strings"
	"testing"
)

// The three tool-name families DESIGN.md names explicitly: a shell/terminal
// command, a grep/search pattern plus path, and a file-edit tool's path. Each
// must summarize to the meaningful part, not the {id,name,input} envelope.
func TestSummarizeToolInputKnownFamilies(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]interface{}
		want  string
	}{
		{"terminal", map[string]interface{}{"command": "go test ./..."}, "go test ./..."},
		{"Bash", map[string]interface{}{"command": "ls -la"}, "ls -la"},
		{"Grep", map[string]interface{}{"pattern": "func main", "path": "cmd/"}, "func main  cmd/"},
		{"search_files", map[string]interface{}{"pattern": "TODO"}, "TODO"},
		{"Edit", map[string]interface{}{"path": "internal/ui/requests.go"}, "internal/ui/requests.go"},
		{"str_replace_editor", map[string]interface{}{"file_path": "main.go"}, "main.go"},
		{"Read", map[string]interface{}{"path": "README.md"}, "README.md"},
	}
	for _, c := range cases {
		summary, known := summarizeToolInput(c.name, c.input)
		if !known {
			t.Errorf("summarizeToolInput(%q) known = false, want true", c.name)
		}
		if summary != c.want {
			t.Errorf("summarizeToolInput(%q) = %q, want %q", c.name, summary, c.want)
		}
	}
}

// A tool name this UI doesn't recognize still gets a compact key=value
// summary rather than nothing — more scannable than raw JSON even without a
// dedicated renderer — but is flagged as not Known so the template can style
// it distinctly per DESIGN.md's "unrecognized names fall back to a generic
// dump" line.
func TestSummarizeToolInputUnknownToolFallsBackGenerically(t *testing.T) {
	summary, known := summarizeToolInput("some_custom_tool", map[string]interface{}{"foo": "bar", "n": float64(3)})
	if known {
		t.Error("an unrecognized tool name reported known = true")
	}
	if !strings.Contains(summary, "foo=bar") || !strings.Contains(summary, "n=3") {
		t.Errorf("generic summary = %q, want it to mention both keys", summary)
	}
}

// parseToolCall must decode the canonical {id,name,input} JSON body (see
// store.blockBody's tool_use encoding) into a usable summary, and must
// degrade to a raw/empty view rather than erroring the whole transcript when
// the body isn't that shape.
func TestParseToolCallDecodesCanonicalBody(t *testing.T) {
	body := `{"id":"tu_1","name":"terminal","input":{"command":"echo hi"}}`
	v := parseToolCall(body)
	if v.Name != "terminal" || !v.Known || v.Summary != "echo hi" {
		t.Errorf("parseToolCall(%s) = %+v, want name=terminal known=true summary=echo hi", body, v)
	}
	if !strings.Contains(v.RawJSON, "echo hi") {
		t.Error("parseToolCall's RawJSON does not carry the original input")
	}

	degraded := parseToolCall("not json")
	if degraded.Name != "" || degraded.Known {
		t.Errorf("parseToolCall(malformed) = %+v, want a zeroed/unknown view, not a panic or error", degraded)
	}
}

func TestParseToolResultDecodesCanonicalBody(t *testing.T) {
	body := `{"for_id":"tu_1","content":"hi there","is_error":true}`
	v := parseToolResult(body)
	if v.Content != "hi there" || !v.IsError {
		t.Errorf("parseToolResult(%s) = %+v, want content=%q is_error=true", body, v, "hi there")
	}
}
