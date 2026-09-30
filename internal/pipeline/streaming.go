package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/internal/upstream"
	"github.com/cnf/arbiter/pkg/types"
)

// toolCallAccum reassembles one streamed tool call from its fragments: an
// identity fragment (id, name) on content_block_start/the first delta, then
// zero or more argument fragments concatenated in arrival order — the same
// by-index concatenation an OpenAI client is contractually required to do,
// done here one layer down so the stored row has the same call an executed
// client would have made.
type toolCallAccum struct {
	id, name, args string
}

// accumulateToolCall folds one stream event into the per-index tool-call
// accumulator, growing the slice as needed (mirroring orderedText's growth
// pattern so a gap — an index skipped because that block produced no delta —
// stays a nil entry rather than shifting a later call into the wrong slot).
func accumulateToolCall(calls []*toolCallAccum, evt *types.NormalizedStreamEvent) []*toolCallAccum {
	if evt.ToolCallIndex >= len(calls) {
		calls = append(calls, make([]*toolCallAccum, evt.ToolCallIndex-len(calls)+1)...)
	}
	if calls[evt.ToolCallIndex] == nil {
		calls[evt.ToolCallIndex] = &toolCallAccum{}
	}
	call := calls[evt.ToolCallIndex]
	if evt.ToolCallID != "" {
		call.id = evt.ToolCallID
	}
	if evt.ToolCallName != "" {
		call.name = evt.ToolCallName
	}
	call.args += evt.ToolCallArgs
	return calls
}

// streamedToolCallNames lists the tool names accumulated from a stream, same
// shape as toolCallNames on the non-streaming path, so the reader/UI need no
// changes to read a streamed row's tool_calls_json.
func streamedToolCallNames(calls []*toolCallAccum) []string {
	var names []string
	for _, c := range calls {
		if c != nil && c.name != "" {
			names = append(names, c.name)
		}
	}
	return names
}

// capturedStreamBlocks turns the per-index text and tool-call fragments
// accumulated from a stream into capture blocks, matching CaptureResponse's
// indexing (position == block index) so streamed and non-streamed responses
// store the same shape. Empty text entries are skipped rather than stored as
// an empty block that would dedup against every other empty block. A tool
// call's arguments are best-effort JSON: if the accumulated fragments don't
// parse (a stream that ended mid-argument), the call is stored with a nil
// input rather than dropped — the id/name are still worth having.
func capturedStreamBlocks(orderedText []string, toolCalls []*toolCallAccum) []store.Block {
	var out []store.Block
	for index, text := range orderedText {
		if text == "" {
			continue
		}
		out = append(out, store.Block{
			Kind:     "text",
			Body:     []byte(text),
			Role:     "assistant",
			MsgIndex: 0,
			Position: index,
		})
	}
	for index, call := range toolCalls {
		if call == nil || (call.id == "" && call.name == "") {
			continue
		}
		var input map[string]interface{}
		if call.args != "" {
			_ = json.Unmarshal([]byte(call.args), &input) // best-effort; nil on failure
		}
		canonical, err := json.Marshal(struct {
			ID    string                 `json:"id"`
			Name  string                 `json:"name"`
			Input map[string]interface{} `json:"input"`
		}{call.id, call.name, input})
		if err != nil {
			continue
		}
		out = append(out, store.Block{
			Kind: "tool_use",
			Body: canonical,
			Role: "assistant",
			// Offset past orderedText's range: ToolCallIndex is a distinct
			// index space from BlockIndex on the OpenAI-origin path (parallel
			// tool calls number 0,1,2... independently of the single
			// candidate's BlockIndex, which is always 0), so using it as-is
			// would collide with a text block's position. Anthropic's own
			// stream sets ToolCallIndex equal to BlockIndex, so this offset
			// costs it nothing beyond not being byte-identical to
			// CaptureResponse's ordering — the same tolerance orderedText's
			// own gap-filling already accepts.
			MsgIndex: 0,
			Position: len(orderedText) + index,
		})
	}
	return out
}

// contentOrNil returns a pointer to the capture, or nil when there is nothing
// to store, so the Event carries no content field at all in the common case
// (capture off, or nothing extracted) rather than an empty non-nil value.
func contentOrNil(c store.CapturedContent) *store.CapturedContent {
	if c.Empty() {
		return nil
	}
	return &c
}

// aliasName reports the alias the client named, if req.Model resolves to one.
// Empty when the client named a literal model or nothing at all.
func (p *Pipeline) aliasName(model string) string {
	if p.aliasResolver != nil && p.aliasResolver.Has(model) {
		return model
	}
	return ""
}

// toolCallNames lists the tool names a response invoked, preserving order.
func toolCallNames(blocks []types.ContentBlock) []string {
	var names []string
	for _, b := range blocks {
		if b.Type == "tool_use" && b.ToolName != "" {
			names = append(names, b.ToolName)
		}
	}
	return names
}

// computeCost fills in cost from the static catalog when the upstream reported
// none (plain Anthropic/OpenAI report nothing; OpenRouter reports a real
// figure). A provider-reported cost is authoritative and left untouched.
// Cache-read and cache-write tokens are priced at the catalog's cache rates
// when it states them; a model with no cache pricing data (CacheReadCostPerMTok
// and CacheWriteCostPerMTok both 0) prices its cache tokens at 0, same as
// before these fields existed — this is a deliberate "unstated, not free"
// choice, not an estimate.
func (p *Pipeline) computeCost(provider, model string, usage types.Usage) float64 {
	if usage.CostUSD > 0 || p.costCatalog == nil {
		return usage.CostUSD
	}
	mc, ok := p.costCatalog.Lookup(provider, model)
	if !ok {
		return 0
	}
	const perMTok = 1_000_000.0
	return (float64(usage.InputTokens)*mc.InputCostPerMTok +
		float64(usage.OutputTokens)*mc.OutputCostPerMTok +
		float64(usage.CacheRead)*mc.CacheReadCostPerMTok +
		float64(usage.CacheWrite)*mc.CacheWriteCostPerMTok) / perMTok
}

// executeStream handles streaming requests. It returns a channel of normalized
// stream events that the HTTP handler will translate and send to the client.
func (p *Pipeline) executeStream(ctx context.Context, traceID string, route types.Route, req *types.NormalizedRequest, sessionKey, promptHash string, hasKey bool, sig types.Signals, start time.Time, content store.CapturedContent) (interface{}, error) {
	headers := headersFromContext(ctx)
	// Send the request upstream (with fallback/retry handling) and get the
	// event channel. A 429/5xx fails SendStream synchronously — the HTTP
	// status is known before any SSE bytes flow — so fallback works exactly
	// as on the non-streaming path.
	_, eventChan, errChan, served, err := p.tryUpstream(ctx, route, req)
	if err != nil {
		p.logger.LogError(ctx, "error", err, map[string]interface{}{"provider": route.Provider})
		status, provider := upstreamFailureStatus(err), upstreamFailureProvider(err)
		if provider == "" {
			provider = route.Provider
		}
		p.record(store.Event{
			TraceID:          traceID,
			SessionKey:       sessionKey,
			Format:           req.OriginalFormat,
			Provider:         provider,
			Model:            req.Model,
			AliasUsed:        p.aliasName(req.Model),
			RoutingRationale: route.Rationale,
			Domain:           sig.Domain,
			RequestKind:      sig.RequestKind,
			Effort:           sig.Effort,
			CostClass:        sig.CostClass,
			Confidence:       sig.Confidence,
			ArrivalTs:        start,
			LatencyMs:        time.Since(start).Milliseconds(),
			StatusCode:       status,
			Error:            err.Error(),
			Stream:           true,
			Content:          contentOrNil(content),
			Headers:          headers,
		})
		return nil, err
	}
	if hasKey && p.pins(sig.RequestKind) {
		p.affinity.pin(ctx, sessionKey, promptHash, req.Model, served.Provider, served.Model, p.cacheTTLFor(served.Provider))
	}

	// Forward upstream events, stamping each with the trace ID — the
	// equivalent of resp.TraceID on the non-streaming path — and the model
	// actually serving the request (upstream-reported when the upstream says
	// so on message_start, the routed model otherwise). Also accumulates
	// usage from stream events and logs the upstream call once (with latency)
	// when the stream ends, mirroring the non-streaming path's LogUpstream.
	//
	// The stream's terminal status is not assumed to be 200: errChan carries
	// the read goroutine's outcome, so a stream that dies mid-flight records
	// the failure rather than an optimistic success. Recording happens after
	// the event channel drains, where both usage and the terminal error are
	// known.
	out := make(chan *types.NormalizedStreamEvent)
	go func() {
		defer close(out)
		streamStart := time.Now()
		var usage types.Usage
		// actualModel captures the upstream's own reported model before the
		// fallback below overwrites an absent one with served.Model — same
		// reasoning as the non-streaming path's actualModel.
		var actualModel string
		// orderedText accumulates text deltas per block index so the streamed
		// response body can be captured after the fact. A slice indexed by
		// BlockIndex preserves the block order the client saw.
		var orderedText []string
		// toolCalls accumulates tool-call fragments per ToolCallIndex — see
		// toolCallAccum — so a streamed tool call gets the same recorded row
		// (tool_calls_json, captured tool_use block) as the non-streaming
		// path already produces. Before this, the row existed but the call
		// itself left no trace: not in tool_calls_json (only the non-stream
		// path set it) and not in captured content (capturedStreamBlocks
		// only ever emitted text).
		var toolCalls []*toolCallAccum
		for evt := range eventChan {
			evt.TraceID = traceID
			if evt.MessageModel != "" && evt.MessageModel != served.Model && actualModel == "" {
				actualModel = evt.MessageModel
			}
			if evt.MessageModel == "" {
				evt.MessageModel = served.Model
			}
			if evt.InputTokens > 0 {
				usage.InputTokens = evt.InputTokens
			}
			if evt.OutputTokens > 0 {
				usage.OutputTokens = evt.OutputTokens
			}
			// Cache counters and provider-reported cost arrive on the usage
			// event; copying them here is what makes a streamed row show real
			// token counts and let a cache-affinity check be read off the store.
			if evt.CacheReadTokens > 0 {
				usage.CacheRead = evt.CacheReadTokens
			}
			if evt.CacheWriteTokens > 0 {
				usage.CacheWrite = evt.CacheWriteTokens
			}
			if evt.CostUSD > 0 {
				usage.CostUSD = evt.CostUSD
			}
			// Anthropic's message_delta — the event this stream's OpenAI-wire
			// translation actually attaches Usage to — carries only
			// OutputTokens; InputTokens and both cache counters exist ONLY
			// on message_start, an earlier, separate event. An OpenAI-format
			// client (Hermes) reads its whole usage picture off one chunk,
			// so without this backfill it saw prompt_tokens=0 on every
			// relayed Claude stream — a bigger gap than the missing cache
			// breakdown alone, and one a naive cache-percentage fix would
			// have made worse (a real cache count over a fabricated zero
			// denominator). The OpenAI-compatible upstream's own terminal
			// "usage" event already carries every field on itself, so this
			// backfill is a same-value no-op there — restricted to these two
			// types precisely because they are the only ones an OpenAI
			// client reads Usage from; touching any other type would risk
			// stamping stale totals onto an event that has no Usage field to
			// carry them in the first place.
			if evt.Type == "message_delta" || evt.Type == "usage" {
				evt.InputTokens = usage.InputTokens
				evt.CacheReadTokens = usage.CacheRead
				evt.CacheWriteTokens = usage.CacheWrite
			}
			if evt.TextDelta != "" {
				if evt.BlockIndex >= len(orderedText) {
					// Grow to the index; a gap (a block that produced no text,
					// such as tool_use) leaves an empty entry rather than
					// shifting later blocks into the wrong position.
					orderedText = append(orderedText, make([]string, evt.BlockIndex-len(orderedText)+1)...)
				}
				orderedText[evt.BlockIndex] += evt.TextDelta
			}
			// A tool call's identity (id, name) arrives on content_block_start
			// (Anthropic) or the first tool_use_delta chunk (OpenAI); every
			// later fragment carries only the index and a slice of arguments.
			// evt.ToolCallID/Name/Args are unset on non-tool-call events, so
			// this only fires for the events that actually carry a call.
			if evt.ToolCallID != "" || evt.ToolCallName != "" || evt.DeltaType == "tool_use_delta" {
				toolCalls = accumulateToolCall(toolCalls, evt)
			}
			out <- evt
		}

		// Drain-then-read: the contract SendStream documents. A nil error is
		// a clean finish; non-nil means the SSE read failed after the event
		// channel closed.
		streamErr := <-errChan
		status := http.StatusOK
		errMsg := ""
		if streamErr != nil {
			status = http.StatusBadGateway
			errMsg = streamErr.Error()
		}
		p.logger.LogUpstream(ctx, served.Provider, status, time.Since(streamStart), usage)

		usage.CostUSD = p.computeCost(served.Provider, served.Model, usage)
		// Content capture on the streaming path. The response half is rebuilt
		// from the accumulated deltas, since the text arrives in fragments; the
		// REQUEST half is the capture taken before pre-guardrails at the top of
		// Execute and handed down here. Both halves must ride on the event: an
		// earlier version recorded only the response, so every streamed row had
		// a reply with no prompt, and a session transcript read as a list of
		// answers to questions nobody asked. A tool call's arguments are
		// reassembled from their fragments the same way orderedText
		// reassembles text — see toolCallAccum.
		respContent := content
		if p.captureContent {
			respContent.Response = capturedStreamBlocks(orderedText, toolCalls)
		}
		p.record(store.Event{
			TraceID:          traceID,
			SessionKey:       sessionKey,
			Format:           req.OriginalFormat,
			Provider:         served.Provider,
			Model:            served.Model,
			ActualModel:      actualModel,
			AliasUsed:        p.aliasName(req.Model),
			RoutingRationale: served.Rationale,
			Domain:           sig.Domain,
			RequestKind:      sig.RequestKind,
			Effort:           sig.Effort,
			CostClass:        sig.CostClass,
			Confidence:       sig.Confidence,
			Usage:            usage,
			ArrivalTs:        start,
			LatencyMs:        time.Since(start).Milliseconds(),
			StatusCode:       status,
			Error:            errMsg,
			Stream:           true,
			Content:          contentOrNil(respContent),
			ToolCalls:        streamedToolCallNames(toolCalls),
			Headers:          headers,
		})
	}()

	// The HTTP handler consumes this and flushes events as SSE.
	return &upstream.StreamResponse{EventChan: out}, nil
}
