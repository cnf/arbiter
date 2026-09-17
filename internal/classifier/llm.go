package classifier

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/upstream"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// defaultFraming is the instruction sentence used when a classifier configures
// no `instructions:`. Deliberately one line: the framing is the least
// interesting part of the prompt, and everything that carries accuracy — the
// per-label rubrics — sits below it.
const defaultFraming = "Classify the user's message into exactly one of these categories."

// replyContract is appended to every classification prompt and is NOT
// overridable by config. Parsing one word instead of JSON is what makes a
// half-parsed reply impossible; a configurable reply format would let a rubric
// edit break the one property the parser depends on.
const replyContract = "Reply with the single matching word and nothing else — no punctuation, no explanation."

// LLMClassifier fills one axis (domain, in practice — nothing here prevents
// another axis, but only domain is built today) by asking an upstream model
// to pick from a configured label set, rather than guessing from keywords.
// It never returns an error from Classify: any failure (timeout, upstream
// error, an unparseable reply) falls through to a wrapped heuristic
// classifier instead, and the failure is still recorded via
// Signals.ClassifierCalls for debugging. This matters because
// MergedClassifier aborts its entire merge — every axis, not just this one —
// on any sub-classifier error.
type LLMClassifier struct {
	name      string
	axis      string
	resolver  *router.AliasResolver
	alias     string
	upstream  upstream.Client
	providers map[string]types.ProviderConfig

	// labels carries each category's optional rubric description, and escape
	// names the label meaning "none of these apply" (empty for none).
	labels []types.Label
	escape string

	// instructions replaces defaultFraming when non-empty.
	instructions string

	fallback Classifier
	timeout  time.Duration
}

// NewLLMClassifier creates an LLM-backed classifier. alias names a configured
// alias (pinned or group) that routes the classification call — resolved the
// same way a client-named alias would be, including a group alias's member
// selection and fallback siblings. labels carries each category's optional
// rubric description; escape names the label that means "no category fits",
// whose verdict fills no axis at all. instructions, when non-empty, replaces
// the default framing sentence. fallback is used whenever the call fails
// outright; timeout <= 0 defaults to 10s.
func NewLLMClassifier(name, axis string, resolver *router.AliasResolver, alias string, u upstream.Client, providers map[string]types.ProviderConfig, labels []types.Label, escape, instructions string, fallback Classifier, timeout time.Duration) *LLMClassifier {
	if axis == "" {
		axis = AxisDomain
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &LLMClassifier{
		name:         name,
		axis:         axis,
		resolver:     resolver,
		alias:        alias,
		upstream:     u,
		providers:    providers,
		labels:       labels,
		escape:       escape,
		instructions: instructions,
		fallback:     fallback,
		timeout:      timeout,
	}
}

// Classify tries the LLM call first; on any failure it defers entirely to
// the wrapped fallback classifier's result, only adding the failed attempt's
// diagnostics to Signals.ClassifierCalls.
func (c *LLMClassifier) Classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	call, label, ok := c.tryClassify(ctx, req)

	if !ok {
		sig, err := c.runFallback(ctx, req)
		if err != nil {
			// The fallback itself failed (a config/programmer error, not a
			// network one — HeuristicClassifier never errors). Still return
			// the LLM call's own diagnostics rather than losing them.
			sig = types.Signals{}
		}
		if call != nil {
			sig.ClassifierCalls = append(sig.ClassifierCalls, call)
		}
		return sig, nil
	}

	sig := types.Signals{Confidence: 1.0, ClassifierCalls: []*types.ClassifierCallInfo{call}}
	c.fillAxis(&sig, label)
	return sig, nil
}

// fillAxis writes the chosen label onto whichever Signals field this instance
// fills. An escape verdict leaves every axis empty, which is the point: a
// policy router's `when: {domain: ...}` rules then simply do not match and a
// chained router takes over, instead of the operator having to write a rule for
// a literal "unknown" domain.
//
// The axis still counts as classified — Confidence 1.0, one recorded call — so
// the empty value reads as "the model said nothing fits", not as "no classifier
// ran". MergedClassifier keys its per-axis pick on a non-empty value, so an
// escape verdict leaves whichever other classifier fills that axis to win.
func (c *LLMClassifier) fillAxis(sig *types.Signals, label *types.Label) {
	if label == nil {
		return
	}
	switch c.axis {
	case AxisEffort:
		sig.Effort = label.Name
	case AxisCostClass:
		sig.CostClass = label.Name
	case AxisCapabilities:
		sig.RequiredCapabilities = []string{label.Name}
	default:
		sig.Domain = label.Name
	}
}

// runFallback calls the wrapped classifier, defensively treating a nil
// fallback (shouldn't happen — config validation requires one) as "no
// signal" rather than panicking.
func (c *LLMClassifier) runFallback(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	if c.fallback == nil {
		return types.Signals{}, nil
	}
	return c.fallback.Classify(ctx, req)
}

// tryClassify attempts the upstream call. ok=false means it failed outright
// (nothing usable came back); call is still non-nil in that case so the
// failure has diagnostics. ok=true means label is the label the model actually
// chose — nil when it chose the escape label, which is a successful verdict
// that fills no axis.
func (c *LLMClassifier) tryClassify(ctx context.Context, req *types.NormalizedRequest) (call *types.ClassifierCallInfo, label *types.Label, ok bool) {
	if c.resolver == nil || c.alias == "" || len(c.labels) == 0 {
		return nil, nil, false
	}

	candidates, err := c.candidates()
	if err != nil || len(candidates) == 0 {
		return &types.ClassifierCallInfo{Error: errString(err, "no route for classifier alias")}, nil, false
	}

	text := types.LastUserText(req)
	classifyReq := &types.NormalizedRequest{
		SystemPrompt: c.systemPrompt(),
		Messages:     []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock(text)}}},
		MaxTokens:    16,
	}

	cctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var lastCall *types.ClassifierCallInfo
	for _, route := range candidates {
		start := time.Now()
		classifyReq.Model = route.Model
		resp, err := c.upstream.Send(cctx, route, classifyReq)
		latency := time.Since(start).Milliseconds()

		if err != nil {
			lastCall = &types.ClassifierCallInfo{
				Provider: route.Provider, Model: route.Model, LatencyMs: latency,
				StatusCode: upstreamErrorStatus(err), Error: err.Error(),
			}
			continue
		}

		reply := strings.TrimSpace(extractText(resp))
		matched, isLabel := c.matchVerdict(reply)
		lastCall = &types.ClassifierCallInfo{
			Provider: route.Provider, Model: route.Model, LatencyMs: latency,
			Usage: resp.Usage, StatusCode: 200, RawReply: reply,
		}
		if !isLabel {
			lastCall.Error = fmt.Sprintf("reply %q did not match any configured label", reply)
			continue
		}
		return lastCall, matched, true
	}
	return lastCall, nil, false
}

// matchVerdict resolves a reply to the label it names. isLabel=false means the
// reply matched nothing usable, which counts as a failed call and falls back.
//
// A returned nil label with isLabel=true is the escape verdict. A bare "none" is
// accepted as escape whenever an escape label is configured, so a rubric that
// describes "nothing fits" without spelling a keyword still yields a successful
// verdict rather than an unparseable reply.
func (c *LLMClassifier) matchVerdict(reply string) (label *types.Label, isLabel bool) {
	l, ok := types.FindLabel(c.labels, reply)
	if !ok {
		if c.escape != "" && strings.EqualFold(reply, "none") {
			return nil, true
		}
		return nil, false
	}
	if c.escape != "" && strings.EqualFold(l.Name, c.escape) {
		return nil, true
	}
	return &l, true
}

// candidates resolves the configured alias into a primary route plus its
// group-fallback siblings, mirroring the same resolution a client naming
// this alias directly would get (see router.AliasResolver).
func (c *LLMClassifier) candidates() ([]types.Route, error) {
	provider, model, ok, err := c.resolver.Resolve(c.alias)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("alias %q is not configured", c.alias)
	}
	cfg, ok := c.providers[provider]
	if !ok {
		return nil, fmt.Errorf("alias %q resolved to unconfigured provider %q", c.alias, provider)
	}
	primary := types.Route{Provider: provider, Model: model, Config: cfg, Rationale: fmt.Sprintf("classifier alias %q", c.alias)}
	fallbacks := c.resolver.GroupFallbacks(c.alias, router.AliasMember{Provider: provider, Model: model})
	return append([]types.Route{primary}, fallbacks...), nil
}

// systemPrompt assembles the three-part prompt: framing (configurable), the
// label set with its rubrics (configurable, and where accuracy lives), then the
// reply contract (fixed, always last).
//
// Labels are sorted before joining because Go map iteration is randomized, so an
// unsorted set would emit different prompt bytes on every call and make the
// logged prompt impossible to diff against the previous one. It buys no prompt
// cache hit — a prompt this short is below every provider's minimum cacheable
// length, so it is not cached either way — which makes stability here purely
// about comparing one call's prompt to the next.
func (c *LLMClassifier) systemPrompt() string {
	framing := c.instructions
	if framing == "" {
		framing = defaultFraming
	}

	sorted := make([]types.Label, len(c.labels))
	copy(sorted, c.labels)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b strings.Builder
	b.WriteString(framing)
	b.WriteString("\n\n")
	for i, l := range sorted {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- ")
		b.WriteString(l.Name)
		if l.Description != "" {
			b.WriteString(": ")
			b.WriteString(l.Description)
		}
	}
	if c.escape != "" {
		fmt.Fprintf(&b, "\n- none: none of the above apply (this means %s).", c.escape)
	}
	b.WriteString("\n\n")
	b.WriteString(replyContract)
	return b.String()
}

// extractText concatenates every text block in a response — a classification
// reply is expected to be a single short text block, but this doesn't assume
// it.
func extractText(resp *types.NormalizedResponse) string {
	var b strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func errString(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

// upstreamErrorStatus extracts the upstream's own status code from err, or
// 502 for a transport failure that never got one — the same "no response is
// a 502, not a 0" rule the pipeline applies to real client traffic.
func upstreamErrorStatus(err error) int {
	var ue *arbitererrors.UpstreamError
	if errors.As(err, &ue) && ue.StatusCode >= 400 && ue.StatusCode <= 599 {
		return ue.StatusCode
	}
	return 502
}
