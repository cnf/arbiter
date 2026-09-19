package types

import (
	"bytes"
	"encoding/json"
	"strings"
)

// NormalizedRequest is the canonical internal representation of a request,
// independent of Anthropic or OpenAI wire format.
type NormalizedRequest struct {
	Messages     []Message
	Model        string
	MaxTokens    int
	Temperature  float64
	SystemPrompt string
	Tools        []Tool
	Stream       bool // if true, caller expects SSE response

	// ClientSystemPrompt is SystemPrompt as the CLIENT sent it, before any
	// pre-guardrail mutated it. Empty when nothing has run yet, so it is never
	// a second source of truth — a caller reading it must fall back to
	// SystemPrompt.
	//
	// It exists because a `system_prompt` guardrail with override:false
	// PREPENDS its own text, and pre-guardrails run before classification. So a
	// `prefix` match against SystemPrompt — "the request starts with this
	// signature" — sees Arbiter's injected preamble first and cannot see the
	// client's signature behind it. The failure is silent and looks exactly
	// like a wrong pattern, while every view the operator has (captured
	// content, the transcript) shows the prompt as sent, because capture also
	// happens before guardrails. RequestMatcher reads this field for that
	// reason: matching should see what the operator sees.
	ClientSystemPrompt string

	// Tracking
	OriginalFormat  string // "anthropic" or "openai"
	OriginalPayload []byte
	TraceID         string
	SessionKey      string // set by the pipeline after guardrails; "" if no usable session key
}

// Message represents a single conversation turn.
type Message struct {
	Role    string // "user", "assistant", "tool"
	Content []ContentBlock
}

// ContentBlock is a single piece of content in a message. Flattened (rather
// than an interface{} union) so translators don't need type assertions.
type ContentBlock struct {
	Type string // "text", "tool_use", "tool_result", "thinking", "attachment"

	// type == "text" or "thinking"
	Text string

	// type == "tool_use"
	ToolUseID string
	ToolName  string
	ToolInput map[string]interface{}

	// type == "tool_result"
	ToolResultForID string // references a prior tool_use ID
	ToolResult      string
	ToolIsError     bool

	// type == "attachment": an image, PDF or other document the client
	// attached.
	//
	// The two wire formats spell these very differently — OpenAI as
	// image_url/file parts, Anthropic as image/document blocks with a
	// source — but the substance is identical: a media type plus either
	// inline bytes or a URL. One block type carries all of them and the
	// translators own the spelling, so a new format is a translator change
	// rather than a new internal concept.
	//
	// MediaType is the MIME type ("image/png", "application/pdf"). Data is the
	// payload — base64 with no data: prefix when IsURL is false, otherwise the
	// URL itself, which the upstream fetches on its own.
	//
	// Name is the filename, and only documents carry one: it is required on
	// the wire for OpenAI documents, so losing it makes the attachment
	// unemittable rather than merely unlabelled. Every image has an empty
	// Name.
	//
	// Image is whether this was sent as an image part. It is recorded rather
	// than derived from MediaType because a URL-sourced attachment has no MIME
	// hint to derive from: "https://example.com/cat.png" is an image the
	// client put in an image_url part, and guessing from a file extension
	// would be wrong for every URL without one. The wire part type is the only
	// reliable statement of intent, so it is carried.
	MediaType string
	Data      string
	Name      string
	IsURL     bool
	Image     bool
}

// AttachmentBlock is a convenience constructor for an attachment content block.
func AttachmentBlock(mediaType, data, name string, isURL bool) ContentBlock {
	return ContentBlock{Type: "attachment", MediaType: mediaType, Data: data, Name: name, IsURL: isURL}
}

// AttachmentImageBlock is AttachmentBlock for a part the client sent as an
// image, which is what decides whether it re-emits as an image or a document
// when the media type cannot say (a bare URL).
func AttachmentImageBlock(mediaType, data, name string, isURL bool) ContentBlock {
	return ContentBlock{Type: "attachment", MediaType: mediaType, Data: data, Name: name, IsURL: isURL, Image: true}
}

// IsImage reports whether an attachment should be treated as an image, which
// decides whether it goes out as an image part (OpenAI) or an `image` rather
// than `document` block (Anthropic). The recorded part type wins; the media
// type is the fallback for a block built without one.
func (cb ContentBlock) IsImage() bool {
	return cb.Image || strings.HasPrefix(cb.MediaType, "image/")
}

// Tool is a function/tool definition the model can call.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
}

// TextBlock is a convenience constructor for a plain text content block.
func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: "text", Text: text}
}

// ExtractText concatenates all text (and thinking) blocks in a message.
// Tool use/result blocks are ignored — this is meant for classifiers that
// need "what did the user actually say" in plain text.
func ExtractText(msg Message) string {
	var out string
	for _, block := range msg.Content {
		if block.Type == "text" {
			if out != "" {
				out += " "
			}
			out += block.Text
		}
	}
	return out
}

// LastUserText returns the plain text of the last user message, or "" if
// there isn't one. Used by classifiers as the primary signal source.
func LastUserText(req *NormalizedRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return ExtractText(req.Messages[i])
		}
	}
	return ""
}

// FirstUserText returns the plain text of the first user message that
// actually has text content, skipping user turns whose only content is a
// tool_result block (agentic clients send those; ExtractText yields "" for
// them, and a turn with no text carries no useful entropy for the caller).
func FirstUserText(req *NormalizedRequest) string {
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		if text := ExtractText(m); text != "" {
			return text
		}
	}
	return ""
}

// AnthropicRequest represents a raw Anthropic /v1/messages request.
type AnthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
	System      AnthropicSystem    `json:"system,omitempty"`
	Messages    []AnthropicMessage `json:"messages"`
	Tools       []AnthropicTool    `json:"tools,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

// AnthropicSystem is the request's top-level system prompt. Anthropic's wire
// format allows either a bare string or a list of content blocks
// ("system": [{"type":"text","text":"..."}]), and real clients send both —
// Claude Desktop/Code send the block form, plain curl and simple clients send
// the string. Declaring it as `string` rejected the block form outright, with
// a 400 before any routing happened, which made Arbiter unreachable for the
// clients that speak its own native format. The block form's text blocks are
// joined; non-text blocks carry no system prompt (Anthropic documents text
// only here) so they contribute nothing rather than failing.
type AnthropicSystem string

// UnmarshalJSON accepts a string or an array of content blocks.
func (s *AnthropicSystem) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*s = ""
		return nil
	}

	if trimmed[0] == '"' {
		var plain string
		if err := json.Unmarshal(trimmed, &plain); err != nil {
			return err
		}
		*s = AnthropicSystem(plain)
		return nil
	}

	var blocks []AnthropicContent
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return err
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	*s = AnthropicSystem(strings.Join(parts, "\n\n"))
	return nil
}

// MarshalJSON always emits the string form. Anthropic accepts both, and the
// string form is what a normalizing proxy should send: it is the shape a
// single-prompt request has anyway, and it keeps the outbound body identical
// regardless of which shape the client used.
func (s AnthropicSystem) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(s))
}

// AnthropicMessage is a message in Anthropic wire format. Its Content accepts
// either a bare string or a list of content blocks — Anthropic allows both, and
// simple clients (and plain curl) send the string form, which a plain
// `[]AnthropicContent` field rejected at parse time.
type AnthropicMessage struct {
	Role    string             `json:"role"`
	Content []AnthropicContent `json:"content"`
}

// UnmarshalJSON normalizes a bare-string `content` into a single text block so
// the rest of the pipeline only ever sees the block form.
func (m *AnthropicMessage) UnmarshalJSON(data []byte) error {
	// An alias avoids recursing into this method for the block-list case.
	type messageAlias struct {
		Role    string             `json:"role"`
		Content []AnthropicContent `json:"content"`
	}

	var probe struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}

	m.Role = probe.Role

	trimmed := bytes.TrimSpace(probe.Content)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		m.Content = nil
		return nil
	}
	if trimmed[0] == '"' {
		var plain string
		if err := json.Unmarshal(trimmed, &plain); err != nil {
			return err
		}
		if plain == "" {
			m.Content = nil
			return nil
		}
		m.Content = []AnthropicContent{{Type: "text", Text: plain}}
		return nil
	}

	var alias messageAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	m.Content = alias.Content
	return nil
}

// AnthropicContent is a content block in Anthropic wire format.
type AnthropicContent struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"` // text, thinking

	ID    string                 `json:"id,omitempty"`    // tool_use
	Name  string                 `json:"name,omitempty"`  // tool_use
	Input map[string]interface{} `json:"input,omitempty"` // tool_use

	ToolUseID string      `json:"tool_use_id,omitempty"` // tool_result
	Content   interface{} `json:"content,omitempty"`     // tool_result (string or blocks)
	IsError   bool        `json:"is_error,omitempty"`    // tool_result

	// Source and Title belong to image/document blocks: Source carries either
	// inline base64 or a URL (the two forms Anthropic accepts), and Title is
	// the document's name. Held as a map/string rather than a typed struct
	// because nothing on the inbound side reads them yet — Anthropic
	// client-facing is deliberately out of scope for now — so a shape that
	// only has to survive marshalling is better than one that pretends to
	// validate a payload nothing consumes.
	Source map[string]interface{} `json:"source,omitempty"`
	Title  string                 `json:"title,omitempty"`
}

// AnthropicTool is a tool definition in Anthropic wire format.
type AnthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// OpenAIRequest represents a raw OpenAI /chat/completions request.
type OpenAIRequest struct {
	Model       string          `json:"model"`
	Messages    []OpenAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	Tools       []OpenAITool    `json:"tools,omitempty"`
	Stream      bool            `json:"stream,omitempty"`

	// StreamOptions is sent on streaming requests to ask the upstream for a
	// terminal usage chunk. Without it the provider reports no token counts on
	// a stream at all, so every streamed request would be recorded with zero
	// tokens, zero cost, and no cache-read figure — which is also the only
	// number that shows whether prompt-cache affinity is working.
	StreamOptions *OpenAIStreamOptions `json:"stream_options,omitempty"`
}

// OpenAIStreamOptions carries the streaming request options OpenRouter and
// OpenAI both accept.
type OpenAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// OpenAIMessage is a message in OpenAI wire format. Tool calls live in
// ToolCalls on assistant messages, tool results are role="tool" messages.
type OpenAIMessage struct {
	Role       string               `json:"role"`
	Content    OpenAIMessageContent `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall     `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"` // role == "tool"
}

// OpenAIMessageContent is a message's content, which OpenAI lets a client send
// either as a bare string (the simple case, and what hand-written curl and
// every text-only client send) or as an array of typed parts (what any client
// sending an image or a document must use).
//
// Declaring it as a plain `string` rejected the array form at *parse* time —
// a 400 naming the field, thrown before routing, logging or any store row, so
// the request was invisible and nothing explained it. That made Arbiter unable
// to carry attachment traffic at all. This mirrors the fix already applied to
// AnthropicSystem and AnthropicMessage for exactly the same reason on the
// Anthropic side.
//
// Both shapes normalize to []ContentBlock, so nothing downstream sees two
// forms and no handler has to branch on the shape.
type OpenAIMessageContent []ContentBlock

// UnmarshalJSON accepts a bare string or an array of parts.
func (c *OpenAIMessageContent) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*c = nil
		return nil
	}

	// A bare string: the whole content is one text block. An empty string
	// yields no blocks rather than an empty text block, which would otherwise
	// dedup against every other empty block in the content store.
	if trimmed[0] == '"' {
		var plain string
		if err := json.Unmarshal(trimmed, &plain); err != nil {
			return err
		}
		if plain == "" {
			*c = nil
			return nil
		}
		*c = []ContentBlock{TextBlock(plain)}
		return nil
	}

	var parts []openAIContentPart
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return err
	}
	blocks := make([]ContentBlock, 0, len(parts))
	for _, p := range parts {
		if b, ok := p.toBlock(); ok {
			blocks = append(blocks, b)
		}
	}
	*c = blocks
	return nil
}

// MarshalJSON emits a bare string when every block is text — so text-only
// traffic produces a byte-identical body to before attachments existed, which
// is what keeps prompt caching intact for the clients that never send one —
// and the parts array only when it has to.
func (c OpenAIMessageContent) MarshalJSON() ([]byte, error) {
	if c.isAllText() {
		return json.Marshal(c.text())
	}
	parts := make([]openAIContentPart, 0, len(c))
	for _, b := range c {
		parts = append(parts, blockToOpenAIPart(b))
	}
	return json.Marshal(parts)
}

// String returns the concatenated text of every text block, which is what a
// caller that only understands text (the system-prompt join, the classifier's
// signal extraction) wants. Attachment blocks contribute nothing — their
// payload is not text and must never be folded into a prompt string.
func (c OpenAIMessageContent) String() string { return c.text() }

func (c OpenAIMessageContent) text() string {
	var parts []string
	for _, b := range c {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (c OpenAIMessageContent) isAllText() bool {
	for _, b := range c {
		if b.Type != "text" {
			return false
		}
	}
	return true
}

// openAIContentPart is one element of an array-form `content`. The three shapes
// a client actually sends: text, an image (a URL, usually a data: URL), and a
// file (a document, with a filename and either a data: URL or an http one).
type openAIContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
	File *struct {
		Filename string `json:"filename"`
		FileData string `json:"file_data"`
	} `json:"file,omitempty"`
}

// toBlock converts a part to the internal block form. ok=false means the part
// carried nothing usable (an unknown type, or an image/file with no payload),
// which is skipped rather than turned into an empty text block.
func (p openAIContentPart) toBlock() (ContentBlock, bool) {
	switch p.Type {
	case "text", "input_text":
		if p.Text == "" {
			return ContentBlock{}, false
		}
		return TextBlock(p.Text), true

	case "image_url", "input_image":
		if p.ImageURL == nil || p.ImageURL.URL == "" {
			return ContentBlock{}, false
		}
		mediaType, data, isURL := splitDataURL(p.ImageURL.URL)
		return AttachmentImageBlock(mediaType, data, "", isURL), true

	case "file", "input_file":
		if p.File == nil || p.File.FileData == "" {
			return ContentBlock{}, false
		}
		mediaType, data, isURL := splitDataURL(p.File.FileData)
		return AttachmentBlock(mediaType, data, p.File.Filename, isURL), true

	default:
		return ContentBlock{}, false
	}
}

// blockToOpenAIPart renders an internal block back to a wire part.
func blockToOpenAIPart(b ContentBlock) openAIContentPart {
	switch b.Type {
	case "attachment":
		payload := joinDataURL(b)
		if b.IsImage() {
			return openAIContentPart{Type: "image_url", ImageURL: &struct {
				URL string `json:"url"`
			}{URL: payload}}
		}
		return openAIContentPart{Type: "file", File: &struct {
			Filename string `json:"filename"`
			FileData string `json:"file_data"`
		}{Filename: b.Name, FileData: payload}}

	default:
		return openAIContentPart{Type: "text", Text: b.Text}
	}
}

// splitDataURL separates a data: URL into its media type and base64 payload.
// A plain http(s) URL is returned as-is with isURL=true — the upstream fetches
// it itself, so there are no bytes for Arbiter to carry. An unparseable data
// URL yields an empty media type rather than an error: the payload still
// round-trips, and failing the whole request over a cosmetic header would be
// worse than passing it through.
func splitDataURL(raw string) (mediaType, data string, isURL bool) {
	if !strings.HasPrefix(raw, "data:") {
		return "", raw, true
	}
	rest := raw[len("data:"):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", raw, false
	}
	header, payload := rest[:comma], rest[comma+1:]
	mediaType = header
	if semi := strings.IndexByte(mediaType, ';'); semi >= 0 {
		mediaType = mediaType[:semi]
	}
	return mediaType, payload, false
}

// joinDataURL rebuilds the wire payload for an attachment: the data: URL form
// for inline bytes, or the URL unchanged when the upstream is to fetch it.
func joinDataURL(b ContentBlock) string {
	if b.IsURL {
		return b.Data
	}
	mediaType := b.MediaType
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return "data:" + mediaType + ";base64," + b.Data
}

// OpenAIToolCall is a tool call requested by the assistant.
type OpenAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // "function"
	Function OpenAIFunctionCall `json:"function"`
}

// OpenAIFunctionCall is the function name/arguments of a tool call.
type OpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded
}

// OpenAITool is a tool in OpenAI wire format (function definition).
type OpenAITool struct {
	Type     string         `json:"type"` // "function"
	Function OpenAIFunction `json:"function"`
}

// OpenAIFunction is the function definition inside an OpenAITool.
type OpenAIFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters"`
}
