package http

import (
	"errors"
	"net/http/httptest"
	"testing"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// #75: when a request fails to normalize because of a content-shape mismatch,
// the structured log fields must carry the safe diagnostic (message index,
// field, observed type) alongside the fields every failure gets — never the
// offending value or message text.
func TestErrorLogFieldsSurfacesContentShapeDiagnostic(t *testing.T) {
	shapeErr := &types.ContentShapeError{MessageIndex: 4, Field: "content", GotType: "object"}
	wrapped := arbitererrors.NewTranslationError("pre_routing", "normalize request", shapeErr)

	req := httptest.NewRequest("POST", "/chat/completions", nil)
	fields := errorLogFields(req, "openai", wrapped)

	if fields["message_index"] != 4 {
		t.Errorf("message_index = %v, want 4", fields["message_index"])
	}
	if fields["field"] != "content" {
		t.Errorf("field = %v, want %q", fields["field"], "content")
	}
	if fields["got_type"] != "object" {
		t.Errorf("got_type = %v, want %q", fields["got_type"], "object")
	}
	if fields["format"] != "openai" {
		t.Errorf("format = %v, want %q", fields["format"], "openai")
	}
}

// A failure unrelated to content shape (e.g. a transport error) must not
// invent shape-diagnostic fields out of nothing.
func TestErrorLogFieldsOmitsShapeFieldsForOtherErrors(t *testing.T) {
	req := httptest.NewRequest("POST", "/chat/completions", nil)
	fields := errorLogFields(req, "openai", errors.New("boom"))

	for _, key := range []string{"message_index", "field", "got_type"} {
		if _, ok := fields[key]; ok {
			t.Errorf("unexpected key %q in fields for a non-shape error: %+v", key, fields)
		}
	}
}
