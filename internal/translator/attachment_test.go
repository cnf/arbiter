package translator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// These tests are driven with raw wire JSON rather than by building the Go
// structs, and that is deliberate. A round-trip through the local types only
// proves Arbiter agrees with itself: the bug this file pins — the array-form
// `content` being rejected at parse time — was invisible to exactly that kind
// of test, because the test would have constructed the very shape the parser
// could already handle. The Anthropic side of this bug was hidden the same way.
//
// So every payload below is the literal bytes a client puts on the wire.

// imageDataURL is a 1x1 PNG as a client sends it: a data: URL inside image_url.
const imageDataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="

// TestOpenAIMessageContentArrayFormParses is the core regression: a message
// whose content is an array of parts must parse, and its parts must survive as
// real blocks. Before the fix this failed at parse time with
// "cannot unmarshal array into ... content of type string", producing a 400
// before routing — the request never reached a handler, so nothing recorded it.
func TestOpenAIMessageContentArrayFormParses(t *testing.T) {
	payload := `{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "what is in this image?"},
			{"type": "image_url", "image_url": {"url": "` + imageDataURL + `"}}
		]}]
	}`

	tr := NewDefaultTranslator()
	norm, err := tr.ToNormalized([]byte(payload), "openai")
	if err != nil {
		t.Fatalf("array-form content must parse, got: %v", err)
	}
	if len(norm.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(norm.Messages))
	}
	blocks := norm.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (text + attachment): %+v", len(blocks), blocks)
	}
	if blocks[0].Type != "text" || blocks[0].Text != "what is in this image?" {
		t.Errorf("block 0 = %+v, want the text part", blocks[0])
	}
	if blocks[1].Type != "attachment" {
		t.Fatalf("block 1 type = %q, want attachment", blocks[1].Type)
	}
	if blocks[1].MediaType != "image/png" {
		t.Errorf("media type = %q, want image/png split out of the data URL", blocks[1].MediaType)
	}
	if blocks[1].IsURL {
		t.Error("IsURL = true for a data: URL, want false (the bytes are inline)")
	}
	if !strings.HasPrefix(blocks[1].Data, "iVBORw0KGgo") {
		t.Errorf("data = %q, want the base64 payload with the data: prefix stripped", blocks[1].Data)
	}
	if blocks[1].Name != "" {
		t.Errorf("name = %q, want empty — only documents carry a filename", blocks[1].Name)
	}
}

// TestOpenAIFilePartCarriesFilename proves a document survives with its
// filename, which is not decoration: `filename` is required in OpenAI's file
// shape, so losing it makes the document unemittable upstream.
func TestOpenAIFilePartCarriesFilename(t *testing.T) {
	payload := `{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "summarise this"},
			{"type": "file", "file": {
				"filename": "report.pdf",
				"file_data": "data:application/pdf;base64,JVBERi0xLjQK"
			}}
		]}]
	}`

	tr := NewDefaultTranslator()
	norm, err := tr.ToNormalized([]byte(payload), "openai")
	if err != nil {
		t.Fatalf("file part must parse, got: %v", err)
	}
	blocks := norm.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2: %+v", len(blocks), blocks)
	}
	att := blocks[1]
	if att.Type != "attachment" || att.MediaType != "application/pdf" {
		t.Errorf("attachment = %+v, want an application/pdf attachment", att)
	}
	if att.Name != "report.pdf" {
		t.Errorf("name = %q, want report.pdf — without it the document cannot be re-emitted", att.Name)
	}
	if att.IsImage() {
		t.Error("IsImage = true for a PDF, want false (it must go out as a document, not an image)")
	}
}

// TestOpenAIMessageContentBareStringStillParses is the backward-compatibility
// half: every text-only client sends content as a bare string, and that shape
// must keep working exactly as before.
func TestOpenAIMessageContentBareStringStillParses(t *testing.T) {
	payload := `{"model":"gpt-4o","messages":[{"role":"user","content":"plain text"}]}`

	tr := NewDefaultTranslator()
	norm, err := tr.ToNormalized([]byte(payload), "openai")
	if err != nil {
		t.Fatalf("bare-string content must still parse, got: %v", err)
	}
	blocks := norm.Messages[0].Content
	if len(blocks) != 1 || blocks[0].Text != "plain text" {
		t.Fatalf("blocks = %+v, want one text block", blocks)
	}
}

// TestOpenAIOutboundKeepsTextOnlyBodiesIdentical is the property that protects
// prompt caching: a request that never carried an attachment must serialize to
// the same bare-string content it did before attachments existed. Emitting the
// array form unconditionally would change the bytes of every request and
// invalidate cached prefixes for traffic that has nothing to do with this
// feature.
func TestOpenAIOutboundKeepsTextOnlyBodiesIdentical(t *testing.T) {
	norm := &types.NormalizedRequest{
		Model:        "gpt-4o",
		SystemPrompt: "be brief",
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{types.TextBlock("hello")}},
		},
	}

	out := normalizedToOpenAIRequest(norm)
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The content field must be a JSON string, not an array.
	if !strings.Contains(string(body), `"content":"hello"`) {
		t.Errorf("text-only content did not serialize as a bare string: %s", body)
	}
	if strings.Contains(string(body), `"content":[`) {
		t.Errorf("text-only content serialized as an array, which changes every request's bytes: %s", body)
	}
}

// TestOpenAIOutboundEmitsAttachmentParts proves the outbound half, which is the
// half that actually reaches the provider: an attachment block must come back
// out as an image_url or file part rather than being flattened into text.
// Without this, inbound parsing could work perfectly and the attachment would
// still never reach the model.
func TestOpenAIOutboundEmitsAttachmentParts(t *testing.T) {
	norm := &types.NormalizedRequest{
		Model: "gpt-4o",
		Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{
			types.TextBlock("look"),
			types.AttachmentBlock("image/png", "iVBORw0KGgo", "", false),
			types.AttachmentBlock("application/pdf", "JVBERi0", "report.pdf", false),
		}}},
	}

	out := normalizedToOpenAIRequest(norm)
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(body)

	if !strings.Contains(got, `"type":"image_url"`) {
		t.Errorf("no image_url part emitted: %s", got)
	}
	if !strings.Contains(got, `"type":"file"`) {
		t.Errorf("no file part emitted: %s", got)
	}
	if !strings.Contains(got, `"filename":"report.pdf"`) {
		t.Errorf("document filename not emitted; the upstream requires it: %s", got)
	}
	// The data: URL must be rebuilt, since the internal form stores the payload
	// and media type separately.
	if !strings.Contains(got, `"url":"data:image/png;base64,iVBORw0KGgo"`) {
		t.Errorf("image data URL not rebuilt from media type + payload: %s", got)
	}
	if !strings.Contains(got, `"file_data":"data:application/pdf;base64,JVBERi0"`) {
		t.Errorf("document data URL not rebuilt: %s", got)
	}
}

// TestAttachmentURLPassesThrough proves a URL-sourced attachment is forwarded
// as the URL rather than being treated as base64. The upstream fetches it
// itself, so Arbiter carries a reference, not bytes.
func TestAttachmentURLPassesThrough(t *testing.T) {
	payload := `{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": [
			{"type": "image_url", "image_url": {"url": "https://example.com/cat.png"}}
		]}]
	}`

	tr := NewDefaultTranslator()
	norm, err := tr.ToNormalized([]byte(payload), "openai")
	if err != nil {
		t.Fatalf("http image url must parse, got: %v", err)
	}
	att := norm.Messages[0].Content[0]
	if att.Type != "attachment" || !att.IsURL {
		t.Fatalf("attachment = %+v, want an IsURL attachment", att)
	}
	if att.Data != "https://example.com/cat.png" {
		t.Errorf("data = %q, want the URL verbatim", att.Data)
	}

	// And back out unchanged — no data: prefix invented for a URL.
	out := normalizedToOpenAIRequest(norm)
	body, _ := json.Marshal(out)
	if !strings.Contains(string(body), `"url":"https://example.com/cat.png"`) {
		t.Errorf("URL not passed through on the way out: %s", body)
	}
}

// TestAttachmentReachesAnthropicUpstream proves an attachment from an OpenAI
// client can still be routed to an Anthropic-speaking provider. The block type
// is chosen by media type: image/* goes out as `image`, everything else as
// `document`.
func TestAttachmentReachesAnthropicUpstream(t *testing.T) {
	norm := &types.NormalizedRequest{
		Model: "claude-3-haiku",
		Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{
			types.TextBlock("read these"),
			types.AttachmentBlock("image/png", "iVBORw0KGgo", "", false),
			types.AttachmentBlock("application/pdf", "JVBERi0", "report.pdf", false),
		}}},
	}

	out := normalizedToAnthropicRequest(norm)
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(body)

	if !strings.Contains(got, `"type":"image"`) {
		t.Errorf("image attachment did not emit as an Anthropic image block: %s", got)
	}
	if !strings.Contains(got, `"type":"document"`) {
		t.Errorf("PDF attachment did not emit as an Anthropic document block: %s", got)
	}
	if !strings.Contains(got, `"media_type":"application/pdf"`) {
		t.Errorf("media type not carried into the Anthropic source: %s", got)
	}
	if !strings.Contains(got, `"data":"JVBERi0"`) {
		t.Errorf("base64 payload not carried into the Anthropic source: %s", got)
	}
	if !strings.Contains(got, `"title":"report.pdf"`) {
		t.Errorf("document name not carried as Anthropic's title: %s", got)
	}
}

// TestUnknownPartTypesAreSkippedNotFatal proves an unrecognised part is dropped
// rather than failing the whole request. A client sending a part type Arbiter
// has never heard of should still get its text through — degrading is better
// than a 400 for traffic that is mostly fine.
func TestUnknownPartTypesAreSkippedNotFatal(t *testing.T) {
	payload := `{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "keep me"},
			{"type": "something_new", "payload": "whatever"}
		]}]
	}`

	tr := NewDefaultTranslator()
	norm, err := tr.ToNormalized([]byte(payload), "openai")
	if err != nil {
		t.Fatalf("an unknown part type must not fail the request: %v", err)
	}
	blocks := norm.Messages[0].Content
	if len(blocks) != 1 || blocks[0].Text != "keep me" {
		t.Fatalf("blocks = %+v, want just the text block", blocks)
	}
}

// TestEmptyStringContentYieldsNoBlocks pins a store interaction rather than a
// wire one: an empty text block hashes to the same address in every request and
// would dominate the "seen everywhere" boilerplate query with noise, so an
// empty string must produce no block at all.
func TestEmptyStringContentYieldsNoBlocks(t *testing.T) {
	payload := `{"model":"gpt-4o","messages":[{"role":"user","content":""}]}`

	tr := NewDefaultTranslator()
	norm, err := tr.ToNormalized([]byte(payload), "openai")
	if err != nil {
		t.Fatalf("empty content must parse, got: %v", err)
	}
	if len(norm.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(norm.Messages))
	}
	if n := len(norm.Messages[0].Content); n != 0 {
		t.Errorf("blocks = %d, want 0 for empty content", n)
	}
}

// TestAttachmentNeverFoldsIntoTheSystemPrompt proves an attachment cannot leak
// into the system prompt as text. Base64 in a prompt string would be both
// useless to the model and a huge token cost, and the system prompt is built by
// concatenating content — exactly the place it could go wrong.
func TestAttachmentNeverFoldsIntoTheSystemPrompt(t *testing.T) {
	payload := `{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": [
				{"type": "text", "text": "be terse"},
				{"type": "image_url", "image_url": {"url": "` + imageDataURL + `"}}
			]},
			{"role": "user", "content": "hi"}
		]
	}`

	tr := NewDefaultTranslator()
	norm, err := tr.ToNormalized([]byte(payload), "openai")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if norm.SystemPrompt != "be terse" {
		t.Errorf("system prompt = %q, want only the text part", norm.SystemPrompt)
	}
	if strings.Contains(norm.SystemPrompt, "iVBORw0KGgo") {
		t.Error("base64 image data leaked into the system prompt")
	}
}

// TestAnthropicInboundImageBlockParses proves the Anthropic client-facing side
// of #23: a client sending Anthropic's own `image` block with an inline
// base64 source must produce an attachment block, not a silently-dropped
// empty text block.
func TestAnthropicInboundImageBlockParses(t *testing.T) {
	payload := `{
		"model": "claude-3-haiku",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "what is this?"},
			{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo"}}
		]}]
	}`

	tr := NewDefaultTranslator()
	norm, err := tr.ToNormalized([]byte(payload), "anthropic")
	if err != nil {
		t.Fatalf("anthropic image block must parse, got: %v", err)
	}
	blocks := norm.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (text + attachment): %+v", len(blocks), blocks)
	}
	att := blocks[1]
	if att.Type != "attachment" || !att.IsImage() {
		t.Fatalf("block 1 = %+v, want an image attachment", att)
	}
	if att.MediaType != "image/png" {
		t.Errorf("media type = %q, want image/png", att.MediaType)
	}
	if att.Data != "iVBORw0KGgo" {
		t.Errorf("data = %q, want the base64 payload", att.Data)
	}
	if att.IsURL {
		t.Error("IsURL = true for a base64 source, want false")
	}
}

// TestAnthropicInboundDocumentBlockCarriesTitle proves a document block's
// `title` becomes the attachment's Name, mirroring OpenAI's `filename` — the
// shape #23 called out explicitly.
func TestAnthropicInboundDocumentBlockCarriesTitle(t *testing.T) {
	payload := `{
		"model": "claude-3-haiku",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": [
			{"type": "document", "source": {"type": "base64", "media_type": "application/pdf", "data": "JVBERi0"}, "title": "report.pdf"}
		]}]
	}`

	tr := NewDefaultTranslator()
	norm, err := tr.ToNormalized([]byte(payload), "anthropic")
	if err != nil {
		t.Fatalf("anthropic document block must parse, got: %v", err)
	}
	att := norm.Messages[0].Content[0]
	if att.Type != "attachment" || att.IsImage() {
		t.Fatalf("block = %+v, want a non-image attachment", att)
	}
	if att.MediaType != "application/pdf" {
		t.Errorf("media type = %q, want application/pdf", att.MediaType)
	}
	if att.Name != "report.pdf" {
		t.Errorf("name = %q, want report.pdf carried from title", att.Name)
	}
}

// TestAnthropicInboundURLSourcePassesThrough proves the second source shape
// — a URL rather than inline base64 — is recognized and forwarded as a URL
// attachment rather than being treated as (empty) base64 data.
func TestAnthropicInboundURLSourcePassesThrough(t *testing.T) {
	payload := `{
		"model": "claude-3-haiku",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": [
			{"type": "image", "source": {"type": "url", "url": "https://example.com/cat.png"}}
		]}]
	}`

	tr := NewDefaultTranslator()
	norm, err := tr.ToNormalized([]byte(payload), "anthropic")
	if err != nil {
		t.Fatalf("anthropic url source must parse, got: %v", err)
	}
	att := norm.Messages[0].Content[0]
	if !att.IsURL {
		t.Fatalf("att = %+v, want IsURL = true", att)
	}
	if att.Data != "https://example.com/cat.png" {
		t.Errorf("data = %q, want the URL verbatim", att.Data)
	}
}
