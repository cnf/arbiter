package types

import (
	"bytes"
	"encoding/json"
	"errors"
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

	// Thinking and OutputEffort are the client's extended-reasoning request,
	// carried through to an Anthropic-speaking upstream. They are pointers so
	// "the client asked for adaptive thinking" is distinguishable from "the
	// client said nothing", which matters on the outbound body: an absent
	// `thinking` field and `thinking: null` are not the same request.
	Thinking     *AnthropicThinking
	OutputEffort string

	// ClientBeta carries the client's `anthropic-beta` header to the upstream.
	// It is a per-request opt-in for features that are gated behind it —
	// interleaved thinking is one — so it cannot live in the provider's static
	// Headers map: the upstream must be asked for the same betas the client
	// asked Arbiter for, or a feature the client negotiated is silently
	// stripped in the middle of the chain.
	ClientBeta string
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

	// Signature is Anthropic's opaque per-thinking-block token, and is empty
	// for every other block type and every other provider.
	//
	// It is not content: it is a handle Anthropic issues with a thinking block
	// and requires back verbatim on the next turn, or the conversation is
	// rejected or silently degraded. The streaming path has carried it since
	// the beginning (NormalizedStreamEvent.Signature); this field exists so the
	// non-streaming path can do the same. Same text, same reason, different
	// carrier — a non-streaming Anthropic thinking conversation is otherwise
	// corrupted on every turn after the first.
	//
	// OpenAI has no equivalent, so the OpenAI translators drop it. That is
	// deliberate and matches what the streaming path already does.
	Signature string

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

// FirstUserText returns the plain text of the first user message that
// actually has text content, skipping user turns whose only content is a
// tool_result block (agentic clients send those; ExtractText yields "" for
// them, and a turn with no text carries no useful entropy for the caller).
//
// This is the one text selector for "what is this request about": session-key
// derivation and every classifier read it. A LastUserText counterpart existed
// and returned the last user turn's text even when that turn was
// tool_result-only, so an agentic request handed classifiers an empty string —
// and a model asked to classify nothing answers anyway, at high confidence.
// The helper is gone rather than deprecated: two selectors side by side is how
// the split re-emerges, and the safe one has to be the only one.
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

	// Thinking and OutputConfig are the client's request for extended
	// reasoning. Claude Desktop in 3p mode and Claude Code send `thinking`
	// ({"type":"adaptive"} — see the captured LiteLLM body) together with
	// `output_config.effort`, and expect thinking blocks back. Omitting both
	// from this struct meant an Anthropic client's thinking request was
	// dropped before routing: the upstream was never asked to think, and a
	// reply that would have carried a thinking trace came back with none.
	Thinking     *AnthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *AnthropicOutputConfig `json:"output_config,omitempty"`
}

// UnmarshalJSON parses the request normally, then — only on a content-shape
// failure — re-walks the raw `messages` array one element at a time to find
// which index produced it. Mirrors OpenAIRequest.UnmarshalJSON for the same
// reason: AnthropicMessage's own UnmarshalJSON has no way to see its
// position in the array, so a per-message ContentShapeError comes back with
// MessageIndex -1. A shape error on the top-level `system` field needs no
// such re-walk — AnthropicSystem.UnmarshalJSON already knows it isn't part
// of an array — so it passes through unchanged.
func (r *AnthropicRequest) UnmarshalJSON(data []byte) error {
	type requestAlias AnthropicRequest
	var alias requestAlias
	err := json.Unmarshal(data, &alias)
	if err == nil {
		*r = AnthropicRequest(alias)
		return nil
	}

	var shapeErr *ContentShapeError
	if !errors.As(err, &shapeErr) || !shapeErr.PerMessage {
		return err
	}

	var probe struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if jsonErr := json.Unmarshal(data, &probe); jsonErr != nil {
		// The messages array itself doesn't even parse — fall back to the
		// original error rather than guessing.
		return err
	}
	for i, raw := range probe.Messages {
		var m AnthropicMessage
		if msgErr := json.Unmarshal(raw, &m); msgErr != nil {
			var innerShapeErr *ContentShapeError
			if errors.As(msgErr, &innerShapeErr) {
				innerShapeErr.MessageIndex = i
				return innerShapeErr
			}
			return msgErr
		}
	}
	// Every message parsed cleanly in isolation but the whole request didn't
	// — retain the original error rather than claiming a location that
	// isn't real.
	return err
}

// AnthropicThinking is the request's extended-thinking block. Type is
// "enabled" with an explicit BudgetTokens, or "adaptive" with neither — the
// newer form, where the model decides. Both are carried verbatim: Arbiter
// relays this field, it does not interpret it.
type AnthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// AnthropicOutputConfig carries output-shaping knobs. Only `effort` is read by
// real clients today ("high" / "medium" / "low").
type AnthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"`
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

	if trimmed[0] != '[' {
		// Neither the string form handled above nor the block-array form:
		// an object, number, boolean, or other shape a client should never
		// send for `system`. Report the observed JSON type rather than
		// trying (and failing, unhelpfully) to unmarshal it as a block
		// array.
		return &ContentShapeError{PerMessage: false, Field: "system", GotType: jsonValueKind(trimmed)}
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

// MarshalJSON emits the block form, carrying the prompt-cache marker.
//
// This deliberately changed shape. It used to emit the bare string, which kept
// the outbound body identical whichever shape the client sent — but a string
// has nowhere to hang `cache_control`, and a system prompt is the one part of
// a conversation that is stable for its whole length. Sending it unmarked meant
// every turn of every Anthropic conversation re-read it at full input price,
// which is the cost the whole caching mechanism exists to avoid. The block form
// is what Anthropic's own docs use for a cached system prompt, and Anthropic
// accepts both shapes, so this is not a wire compromise: it is the shape that
// can carry the marker.
//
// Non-text blocks never arrive here — UnmarshalJSON joins only text blocks — so
// the single emitted block needs no per-block policy.
func (s AnthropicSystem) MarshalJSON() ([]byte, error) {
	if s == "" {
		return json.Marshal("")
	}
	return json.Marshal([]AnthropicContent{{
		Type:         "text",
		Text:         string(s),
		CacheControl: NewAnthropicCacheControl(),
	}})
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

	if trimmed[0] != '[' {
		// Neither the string form handled above nor the block-array form.
		// MessageIndex is unknown here — AnthropicMessage's own
		// UnmarshalJSON has no visibility into its position in the
		// request's messages array; AnthropicRequest.UnmarshalJSON fills
		// it in on the failing path, mirroring OpenAIRequest's approach.
		return &ContentShapeError{PerMessage: true, MessageIndex: -1, Field: "content", GotType: jsonValueKind(trimmed)}
	}

	var alias messageAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	m.Content = alias.Content
	return nil
}

// AnthropicCacheControl is Anthropic's prompt-cache marker. Caching is opt-in
// per content block: a block without one of these is not part of any cacheable
// prefix, so the upstream re-reads it at full input price every turn. A
// breakpoint marks the END of a cacheable prefix, and Anthropic allows at most
// four per request.
type AnthropicCacheControl struct {
	Type string `json:"type"`
}

// AnthropicCacheControlEphemeral is the only cache mode Anthropic documents:
// "ephemeral", a 5-minute TTL refreshed on every hit.
const AnthropicCacheControlEphemeral = "ephemeral"

// NewAnthropicCacheControl returns a fresh marker. A function rather than a
// shared value because the marker is marshalled into outbound bodies — a shared
// pointer invites a later mutation to rewrite every request's marker at once.
func NewAnthropicCacheControl() *AnthropicCacheControl {
	return &AnthropicCacheControl{Type: AnthropicCacheControlEphemeral}
}

// AnthropicContent is a content block in Anthropic wire format.
type AnthropicContent struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"` // text

	// Thinking is a thinking block's text. Anthropic spells this key
	// differently from a text block's, so Text cannot serve both: a thinking
	// block carries {"thinking": "..."} and a text block carries
	// {"text": "..."}, and reading the wrong one yields an empty string with
	// no error. Both directions of the non-streaming path were doing exactly
	// that, so thinking text was silently lost.
	Thinking string `json:"thinking,omitempty"` // thinking

	// Signature is Anthropic's opaque thinking-block token: it is issued with
	// a thinking block and must be replayed verbatim alongside it on the next
	// turn. omitempty keeps it off text and tool blocks, where it never
	// applies. See types.ContentBlock.Signature for why it is carried.
	Signature string `json:"signature,omitempty"`

	// CacheControl marks this block as the end of a cacheable prefix.
	// Pointer so an unmarked block omits the key entirely, and a marked one
	// always emits it — see normalizedToAnthropicRequest for which blocks
	// Arbiter marks.
	CacheControl *AnthropicCacheControl `json:"cache_control,omitempty"`

	ID   string `json:"id,omitempty"`   // tool_use
	Name string `json:"name,omitempty"` // tool_use

	// Input is kept behind the struct tag's omitempty for every block type
	// EXCEPT tool_use, where MarshalJSON below forces the key to appear.
	// Anthropic's API requires `input` to be present on every tool_use
	// block, even as `{}` for a tool call with no arguments, and 400s a
	// request that replays one with the key missing. A struct tag can't
	// express "always for this type, never for the others" — omitempty
	// would drop a nil/empty map on every type including tool_use, and
	// dropping omitempty would add a spurious `"input":null`/`{}` to text,
	// thinking, and tool_result blocks that never carried one on the real
	// wire. See MarshalJSON.
	Input map[string]interface{} `json:"input,omitempty"` // tool_use

	ToolUseID string      `json:"tool_use_id,omitempty"` // tool_result
	Content   interface{} `json:"content,omitempty"`     // tool_result (string or blocks)
	IsError   bool        `json:"is_error,omitempty"`    // tool_result

	// Source and Title belong to image/document blocks: Source carries either
	// inline base64 ({"type":"base64","media_type":...,"data":...}) or a URL
	// ({"type":"url","url":...}) — the two forms Anthropic accepts — and
	// Title is the document's name. Held as a map rather than a typed struct
	// because the two shapes share no required fields beyond `type`; see
	// anthropicSourceToParts (internal/translator/convert.go) for the inbound
	// reader, which treats an unrecognized shape as an attachment with no
	// data rather than failing the request (issue #23).
	Source map[string]interface{} `json:"source,omitempty"`
	Title  string                 `json:"title,omitempty"`
}

// MarshalJSON forces `input` onto the wire for a tool_use block even when
// Input is nil/empty, defaulting to `{}` — Anthropic requires the key on
// every tool_use block, including a no-argument call, and 400s a request
// that omits it (issue #78). The plain `json:"input,omitempty"` tag on the
// field stays in place for every other block type (text, thinking,
// tool_result, image/document), where an `input` key never belongs and
// omitempty is exactly right; a type alias plus a local override field is
// the standard Go way to reuse the default encoding for everything except
// one field that needs type-dependent handling.
func (c AnthropicContent) MarshalJSON() ([]byte, error) {
	type alias AnthropicContent
	if c.Type != "tool_use" {
		return json.Marshal(alias(c))
	}
	input := c.Input
	if input == nil {
		input = map[string]interface{}{}
	}
	return json.Marshal(struct {
		alias
		Input map[string]interface{} `json:"input"`
	}{alias: alias(c), Input: input})
}

// AnthropicTool is a tool definition in Anthropic wire format.
type AnthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`

	// CacheControl is set on the LAST tool only, by the outbound translator —
	// never on this side. A breakpoint on the final tool caches the whole tool
	// prefix plus the system prompt, which is the stable part of a request that
	// repeats verbatim on every turn.
	CacheControl *AnthropicCacheControl `json:"cache_control,omitempty"`
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

// UnmarshalJSON parses the request normally, then — only on a content-shape
// failure — re-walks the raw `messages` array one element at a time to find
// which index produced it. OpenAIMessageContent's UnmarshalJSON has no way to
// see its own position in the array, so ContentShapeError comes back with
// MessageIndex -1; this is the one place that index is knowable, and it costs
// a second pass only on the already-failing path.
func (r *OpenAIRequest) UnmarshalJSON(data []byte) error {
	type requestAlias OpenAIRequest
	var alias requestAlias
	err := json.Unmarshal(data, &alias)
	if err == nil {
		*r = OpenAIRequest(alias)
		return nil
	}

	var shapeErr *ContentShapeError
	if !errors.As(err, &shapeErr) {
		return err
	}

	var probe struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if jsonErr := json.Unmarshal(data, &probe); jsonErr != nil {
		// The messages array itself doesn't even parse — fall back to the
		// original error rather than guessing.
		return err
	}
	for i, raw := range probe.Messages {
		var m OpenAIMessage
		if msgErr := json.Unmarshal(raw, &m); msgErr != nil {
			var innerShapeErr *ContentShapeError
			if errors.As(msgErr, &innerShapeErr) {
				innerShapeErr.MessageIndex = i
				return innerShapeErr
			}
			return msgErr
		}
	}
	// Every message parsed cleanly in isolation but the whole request didn't
	// — retain the original error rather than claiming a location that
	// isn't real.
	return err
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

	if trimmed[0] != '[' {
		// Neither the string form handled above nor the array form: an
		// object, number, boolean, or other shape a client should never
		// send here. Report the observed JSON type rather than trying (and
		// failing, unhelpfully) to unmarshal it as a part array.
		return &ContentShapeError{PerMessage: true, MessageIndex: -1, Field: "content", GotType: jsonValueKind(trimmed)}
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
