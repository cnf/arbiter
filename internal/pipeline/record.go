package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/store"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// record enqueues a completed request for the event store. It never blocks
// the request path: a configured store owns the enqueue policy (drop with a
// warning when saturated), and the default NoopWriter discards outright.
func (p *Pipeline) record(ev store.Event) {
	// Stamped centrally rather than at each call site: the epoch is a property
	// of the pipeline, not of any one request, and every recorded event must
	// carry it for the per-epoch cost comparison to be complete.
	ev.ConfigEpoch = p.configEpoch
	// "client" is the default kind — real traffic — so every existing call
	// site (all of them client requests) needs no change. Non-client kinds
	// (e.g. "classifier") set Kind explicitly before calling record.
	if ev.Kind == "" {
		ev.Kind = "client"
	}
	p.store.Record(ev)
}

// recordClassifierCalls stores one event per upstream call a classifier made
// while producing sig (see types.ClassifierCallInfo — an LLM-backed
// classifier's own request, not the client's). Each shares the triggering
// request's trace and session key, so it shows up in context on that
// session's trajectory, and is tagged kind="classifier" so it's excluded
// from the default request list and every cost/latency aggregate (see
// Reader's kind='client' queries) — a classifier call's own spend must not
// be mistaken for what the client asked for.
//
// This runs before the real request's own event (recorded on success or
// failure, later in Execute), so a classifier call is visible even if
// routing or the upstream call that follows never completes.
//
// The row's axes come from the CALL when it reported its own (a decisions call
// fills several axes and knows which), falling back to the merged sig for a
// classifier that reports none — the LLM classifier's one-axis case, whose
// single verdict is the merged value anyway. Reading only the merged sig meant
// a multi-axis decisions call recorded its domain and nothing else, so
// cost_class was in the rationale text but empty as a field.
func (p *Pipeline) recordClassifierCalls(req *types.NormalizedRequest, sig types.Signals, arrivalTs time.Time) {
	for _, call := range sig.ClassifierCalls {
		rationale := classifierRationale(call)
		// The verdict alone doesn't say what was judged, and the rationale is
		// what the request list shows before anyone opens the captured content
		// — so the classified text rides here too. Without it, "replied
		// code_generation" is unreadable as evidence: you cannot tell a clear
		// message the model misjudged from a rubric that failed to describe
		// the category.
		if input := ellipsize(call.Input, rationalePreview); input != "" {
			rationale += fmt.Sprintf(" — input %q", input)
		}
		axes := call.Axes
		confidence := callConfidence(call)
		if axes == nil {
			axes = map[string]string{}
			if sig.Domain != "" {
				axes[classifier.AxisDomain] = sig.Domain
			}
			if sig.Effort != "" {
				axes[classifier.AxisEffort] = sig.Effort
			}
			if sig.CostClass != "" {
				axes[classifier.AxisCostClass] = sig.CostClass
			}
		}
		p.record(store.Event{
			TraceID:          req.TraceID,
			SessionKey:       req.SessionKey,
			Kind:             "classifier",
			ArrivalTs:        arrivalTs,
			Format:           req.OriginalFormat,
			Provider:         call.Provider,
			Model:            call.Model,
			RoutingRationale: rationale,
			Domain:           axes[classifier.AxisDomain],
			Effort:           axes[classifier.AxisEffort],
			CostClass:        axes[classifier.AxisCostClass],
			Confidence:       confidence,
			Usage:            call.Usage,
			LatencyMs:        call.LatencyMs,
			StatusCode:       call.StatusCode,
			Error:            call.Error,
			Content:          p.classifierContent(call),
		})
	}
}

// callConfidence is the certainty to record for one classifier call. A
// multi-axis call reports a confidence per axis and has no single honest value,
// so the highest wins — the same rule Signals.Confidence follows, and the only
// claim available without picking an arbitrary axis.
//
// A call reporting neither falls back to 0, which the page renders as "0.0%"
// rather than blank. That is a real gap for a classifier that made no claim
// about its certainty (the LLM classifier sets 1.0 on a match and reports
// nothing on a failure), not a rendering problem — see the classifier row's
// rationale for what actually happened.
func callConfidence(call *types.ClassifierCallInfo) float64 {
	var best float64
	for _, c := range call.AxisConfidence {
		if c > best {
			best = c
		}
	}
	return best
}

// rationalePreview bounds how much of a classifier's input is echoed into the
// routing rationale. The rationale is rendered in a list row (and again in the
// detail page's <pre>), so this is sized to be recognisable at a glance rather
// than to carry the whole message — the full text is in the captured content.
const rationalePreview = 120

// classifierRationale renders one classifier call's outcome. A classifier that
// supplied its own Verdict owns its wording — a decisions call's outcome is a
// set of axis values with probabilities, which the LLM classifier's phrasing
// cannot express. Every other caller gets that phrasing, unchanged from before
// Verdict existed, so nothing already in the store reads differently.
func classifierRationale(call *types.ClassifierCallInfo) string {
	if call.Verdict != "" {
		return call.Verdict
	}
	if call.Error != "" {
		return fmt.Sprintf("LLM classifier failed (%s), fell back to heuristic", call.Error)
	}
	return fmt.Sprintf("LLM classifier replied %q", call.RawReply)
}

// ellipsize shortens s to at most max bytes on a rune boundary. Thin wrapper
// over types.Ellipsize, which is where the rune-boundary logic lives so a
// classifier's outbound input cap and this preview cannot disagree about it.
func ellipsize(s string, max int) string {
	return types.Ellipsize(s, max)
}

// classifierContent builds the captured content for one classifier call: the
// text it classified, plus the prompt it was given as a system block.
//
// The input is byte-identical to a block of the client's own request that
// capture already stored, so it addresses to the same content row and costs one
// reference, not a second body — and the classifier row is then joinable to the
// client request that triggered it. The prompt block is constant across calls
// for a given config, so it addresses to a single row forever and makes the
// classifier's row self-contained: "which rubric produced this verdict" is
// answerable without reconstructing a config from its epoch.
//
// Gated on the same storage.capture_content switch as everything else that
// writes conversation text to disk — a second switch for derived calls is a
// switch nobody keeps in sync. nil means nothing captured, so the store's
// no-content fast path still applies when capture is off.
func (p *Pipeline) classifierContent(call *types.ClassifierCallInfo) *store.CapturedContent {
	if !p.captureContent {
		return nil
	}
	var c store.CapturedContent
	if call.SystemPrompt != "" {
		c.Request = append(c.Request, store.Block{
			Kind: "text", Body: []byte(call.SystemPrompt), Role: "system",
		})
	}
	if call.Input != "" {
		// msg_index 1 so it sits after the prompt block, mirroring how
		// CaptureRequest indexes the system prompt ahead of the messages.
		c.Request = append(c.Request, store.Block{
			Kind: "text", Body: []byte(call.Input), Role: "user", MsgIndex: 1,
		})
	}
	return contentOrNil(c)
}

// recordFailed stores a full requests row for a request that never reached
// (or was refused before reaching) an upstream: a pre-guardrail rejection or
// a routing failure (#5 — "nothing invisible": these are real client
// requests that merely failed, not a separate invisible class). It mirrors
// the upstream-failure recording below it, with no route/provider/model
// resolved yet (routing is exactly what failed) and cost left at its zero
// value, which is correct — nothing reached a provider to spend anything on.
// arbitererrors.StatusFor is the same status mapping internal/http uses to
// answer the client, so the row's status_code always matches what the
// client was actually told.
func (p *Pipeline) recordFailed(ctx context.Context, traceID, sessionKey, format, model string, start time.Time, err error, content store.CapturedContent, signals ...types.Signals) {
	var sig types.Signals
	if len(signals) > 0 {
		sig = signals[0]
	}
	p.record(store.Event{
		TraceID:              traceID,
		SessionKey:           sessionKey,
		Format:               format,
		Model:                model,
		AliasUsed:            p.aliasName(model),
		Domain:               sig.Domain,
		Effort:               sig.Effort,
		CostClass:            sig.CostClass,
		Confidence:           sig.Confidence,
		RequestKind:          sig.RequestKind,
		RequiredCapabilities: sig.RequiredCapabilities,
		ArrivalTs:            start,
		LatencyMs:            time.Since(start).Milliseconds(),
		StatusCode:           arbitererrors.StatusFor(err),
		Error:                err.Error(),
		Content:              contentOrNil(content),
		Headers:              headersFromContext(ctx),
	})
}
