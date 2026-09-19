package classifier

import (
	"context"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// attachmentRequest carries an image block, which is the fact structural
// detection exists to use.
func attachmentRequest() *types.NormalizedRequest {
	return &types.NormalizedRequest{
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{
				types.TextBlock("what does this show?"),
				{Type: "attachment", MediaType: "image/png", Image: true, Data: "aGk="},
			}},
		},
	}
}

func toolsRequest() *types.NormalizedRequest {
	return &types.NormalizedRequest{
		Tools:    []types.Tool{{Name: "read_file"}},
		Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock("read the config")}}},
	}
}

func detectClassifier(t *testing.T, detect []string, tokens int) *HeuristicClassifier {
	t.Helper()
	return NewHeuristicClassifierFull("caps", AxisCapabilities, nil, nil, detect, tokens)
}

// An attachment is a FACT about the bytes, so detection is certain — unlike the
// keyword detection it sits beside, which guesses "image" from the word.
func TestDetectAttachmentFromRequestShape(t *testing.T) {
	c := detectClassifier(t, []string{types.CapAttachment}, 0)

	sig, err := c.Classify(context.Background(), attachmentRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(sig.RequiredCapabilities) != 1 || sig.RequiredCapabilities[0] != types.CapAttachment {
		t.Fatalf("RequiredCapabilities = %v, want [attachment]", sig.RequiredCapabilities)
	}
	if sig.Confidence != 1.0 {
		t.Fatalf("Confidence = %v, want 1.0 — an attachment is present or it is not", sig.Confidence)
	}
}

// The words "image" and "screenshot" in a text-only request must NOT satisfy a
// structural attachment check: that is exactly the false positive the keyword
// detector is prone to and this one is not.
func TestDetectAttachmentIgnoresTextMentions(t *testing.T) {
	c := detectClassifier(t, []string{types.CapAttachment}, 0)

	req := &types.NormalizedRequest{
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{types.TextBlock("write me a script that screenshots an image")}},
		},
	}
	sig, err := c.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(sig.RequiredCapabilities) != 0 {
		t.Fatalf("RequiredCapabilities = %v, want none — a text mention is not an attachment", sig.RequiredCapabilities)
	}
}

func TestDetectToolUseFromRequestShape(t *testing.T) {
	c := detectClassifier(t, []string{types.CapToolUse}, 0)

	sig, err := c.Classify(context.Background(), toolsRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(sig.RequiredCapabilities) != 1 || sig.RequiredCapabilities[0] != types.CapToolUse {
		t.Fatalf("RequiredCapabilities = %v, want [tool_use]", sig.RequiredCapabilities)
	}
}

// Several capabilities can hold at once, which is why this axis unions rather
// than picking a winner.
func TestDetectUnionsSeveralCapabilities(t *testing.T) {
	c := detectClassifier(t, []string{types.CapToolUse, types.CapAttachment}, 0)

	req := attachmentRequest()
	req.Tools = []types.Tool{{Name: "read_file"}}
	sig, err := c.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(sig.RequiredCapabilities) != 2 {
		t.Fatalf("RequiredCapabilities = %v, want both", sig.RequiredCapabilities)
	}
	// Config order, so the result is stable rather than map-ordered.
	if sig.RequiredCapabilities[0] != types.CapToolUse || sig.RequiredCapabilities[1] != types.CapAttachment {
		t.Fatalf("RequiredCapabilities = %v, want [tool_use attachment] in config order", sig.RequiredCapabilities)
	}
}

// long_context needs the configured threshold: without one it could never fire,
// which config validation rejects rather than leaving silent.
func TestDetectLongContextUsesThreshold(t *testing.T) {
	short := detectClassifier(t, []string{types.CapLongContext}, 100000)
	sig, err := short.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(sig.RequiredCapabilities) != 0 {
		t.Fatalf("RequiredCapabilities = %v, want none under the threshold", sig.RequiredCapabilities)
	}

	generous := detectClassifier(t, []string{types.CapLongContext}, 1)
	sig, err = generous.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(sig.RequiredCapabilities) != 1 || sig.RequiredCapabilities[0] != types.CapLongContext {
		t.Fatalf("RequiredCapabilities = %v, want [long_context] over the threshold", sig.RequiredCapabilities)
	}
}

// Structural detection and keyword detection coexist on one classifier: the
// keywords are the fallback for anything structural cannot prove.
func TestDetectUnionsWithKeywords(t *testing.T) {
	c := NewHeuristicClassifierFull("caps", AxisCapabilities,
		map[string][]string{"vision": {"screenshot"}}, nil,
		[]string{types.CapToolUse}, 0)

	req := toolsRequest()
	req.Messages[0].Content = append(req.Messages[0].Content, types.TextBlock("use the screenshot"))
	sig, err := c.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	// tool_use structurally, vision by keyword, both present, no duplicate.
	if len(sig.RequiredCapabilities) != 2 {
		t.Fatalf("RequiredCapabilities = %v, want [vision tool_use]", sig.RequiredCapabilities)
	}
	// The structural hit raises confidence even though the keyword hit was a
	// single-word guess.
	if sig.Confidence != 1.0 {
		t.Fatalf("Confidence = %v, want 1.0 when something was proven structurally", sig.Confidence)
	}
}

// A classifier with no detect block behaves exactly as before.
func TestNoDetectLeavesKeywordBehaviour(t *testing.T) {
	c := NewHeuristicClassifierFull("caps", AxisCapabilities,
		map[string][]string{"vision": {"screenshot"}}, nil, nil, 0)

	sig, err := c.Classify(context.Background(), toolsRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(sig.RequiredCapabilities) != 0 {
		t.Fatalf("RequiredCapabilities = %v, want none without a detect block", sig.RequiredCapabilities)
	}
}
