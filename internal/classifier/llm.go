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
	target    *Target
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

	// onlyIfUnset, when true, makes the merge skip this classifier — no
	// upstream call at all — unless its axis is still empty after every
	// classifier declared before it. The gate lives here so the merge can
	// query it without knowing the concrete type.
	onlyIfUnset bool

	// maxInputChars caps the text sent as the user message, in characters.
	// 0 means unlimited; NewLLMClassifierFull applies the default, so a
	// zero here is only reachable by asking for it.
	maxInputChars int
}

// NewLLMClassifier creates an LLM-backed classifier with the default input cap.
// target names where the classification call goes: either a configured alias
// (pinned or group) or a declared provider model name — resolved the same way
// a client-named alias would be, including a group alias's member selection
// and fallback siblings. labels carries each category's optional rubric
// description; escape names the label that means "no category fits", whose
// verdict fills no axis at all. instructions, when non-empty, replaces the
// default framing sentence. fallback is used whenever the call fails outright;
// timeout <= 0 defaults to 10s.
//
// Kept alongside NewLLMClassifierFull the way NewHeuristicClassifier sits beside
// its Full variant: the short form is what most call sites want, and leaving it
// unchanged keeps every existing test exercising the same construction.
func NewLLMClassifier(name, axis string, resolver *router.AliasResolver, target *Target, u upstream.Client, providers map[string]types.ProviderConfig, labels []types.Label, escape, instructions string, fallback Classifier, timeout time.Duration) *LLMClassifier {
	return NewLLMClassifierFull(name, axis, resolver, target, u, providers, labels, escape, instructions, fallback, timeout, 0, false)
}

// NewLLMClassifierFull is NewLLMClassifier with an explicit input cap:
// maxInputChars bounds the text sent as the user message (see
// effectiveMaxInputChars — 0 takes the default, negative means unlimited).
func NewLLMClassifierFull(name, axis string, resolver *router.AliasResolver, target *Target, u upstream.Client, providers map[string]types.ProviderConfig, labels []types.Label, escape, instructions string, fallback Classifier, timeout time.Duration, maxInputChars int, onlyIfUnset bool) *LLMClassifier {
	if axis == "" {
		axis = AxisDomain
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &LLMClassifier{
		name:          name,
		axis:          axis,
		resolver:      resolver,
		target:        target,
		upstream:      u,
		providers:     providers,
		labels:        labels,
		escape:        escape,
		instructions:  instructions,
		fallback:      fallback,
		timeout:       timeout,
		onlyIfUnset:   onlyIfUnset,
		maxInputChars: effectiveMaxInputChars(maxInputChars),
	}
}

// gated and gateAxes implement onlyIfUnsetClassifier: the merge gates this
// classifier behind earlier classifiers on the axis it fills.
func (c *LLMClassifier) gated() bool { return c.onlyIfUnset }

func (c *LLMClassifier) gateAxes() []string { return []string{c.axis} }

// Classify tries the LLM call first; on any failure it defers entirely to
// the wrapped fallback classifier's result, only adding the failed attempt's
// diagnostics to Signals.ClassifierCalls — see modelBacked for that policy.
func (c *LLMClassifier) Classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	return modelBacked(ctx, req, c.fallback,
		func() (*types.Label, *types.ClassifierCallInfo, bool) {
			call, label, ok := c.tryClassify(ctx, req)
			return label, call, ok
		},
		func(sig *types.Signals, label *types.Label) {
			sig.Confidence = 1.0
			// Reported per axis as well, so the classifier's own stored row
			// carries a confidence. It is always 1.0 here — a matched label is
			// a certain verdict by construction, which is exactly the
			// limitation a decision model removes — but without it the row
			// read back as 0.0%.
			sig.AxisConfidence[c.axis] = 1.0
			c.fillAxis(sig, label)
		})
}

// fillAxis writes the chosen label onto whichever Signals field this instance
// fills. An escape verdict (label == nil) fills the axis with the reserved
// sentinel types.UnmatchedValue instead of leaving it empty — see
// MergedClassifier.Classify for what that buys: a `when: {domain: unmatched}`
// rule can now match it, and a real value from any other classifier always
// outscores it in the merge.
//
// The axis still counts as classified — Confidence 1.0, one recorded call —
// so the sentinel reads as "the model said nothing fits", not as "no
// classifier ran". Capabilities is the one axis excluded: it is an additive
// set (vision AND tool_use can both apply), not a single contested value, so
// an escape verdict there leaves RequiredCapabilities empty, same as before —
// see types.UnmatchedValue's own doc for why.
func (c *LLMClassifier) fillAxis(sig *types.Signals, label *types.Label) {
	if label == nil {
		// An escape verdict fills no value on an ADDITIVE axis: capabilities
		// and tags are sets with no single "nothing matched" value (see
		// types.UnmatchedValue's doc). Writing the sentinel onto a set axis
		// would inject a bogus tag/capability named "unmatched".
		if c.axis != AxisCapabilities && c.axis != AxisTags {
			c.fillAxisValue(sig, types.UnmatchedValue)
		}
		return
	}
	c.fillAxisValue(sig, label.Name)
}

// fillAxisValue writes value onto whichever Signals field this instance
// fills — the one place both a real label and the escape sentinel go through,
// so the two can never disagree about which field an axis name maps to.
func (c *LLMClassifier) fillAxisValue(sig *types.Signals, value string) {
	switch c.axis {
	case AxisDifficulty:
		sig.Difficulty = value
	case AxisCostClass:
		sig.CostClass = value
	case AxisCapabilities:
		sig.RequiredCapabilities = []string{value}
	case AxisTags:
		sig.Tags = []string{value}
	default:
		sig.Domain = value
	}
}

// tryClassify attempts the upstream call. ok=false means it failed outright
// (nothing usable came back); call is still non-nil in that case so the
// failure has diagnostics. ok=true means label is the label the model actually
// chose — nil when it chose the escape label, which is a successful verdict
// that fills no axis.
func (c *LLMClassifier) tryClassify(ctx context.Context, req *types.NormalizedRequest) (call *types.ClassifierCallInfo, label *types.Label, ok bool) {
	if c.target == nil || len(c.labels) == 0 {
		return nil, nil, false
	}

	// Nothing to classify means no call. Asking a model to classify an empty
	// string does not return "no signal" — it returns a verdict, at whatever
	// confidence the model feels, indistinguishable in the store from one
	// reached on real text. Skipping costs nothing and is the only way the
	// absence stays visible as an absence.
	text := classifierInput(req, c.maxInputChars)
	if text == "" {
		return nil, nil, false
	}

	candidates, err := c.candidates()
	if err != nil || len(candidates) == 0 {
		return &types.ClassifierCallInfo{Error: errString(err, "no route for classifier alias")}, nil, false
	}

	// The input and the prompt are carried on every call record, success or
	// failure: a wrong verdict is only debuggable against the text that
	// produced it, and a failed call's prompt is how you tell "the rubric is
	// ambiguous" from "the provider was down".
	prompt := c.systemPrompt()
	classifyReq := &types.NormalizedRequest{
		SystemPrompt: prompt,
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
				Input: text, SystemPrompt: prompt,
			}
			continue
		}

		reply := strings.TrimSpace(extractText(resp))
		matched, isLabel := c.matchVerdict(reply)
		lastCall = &types.ClassifierCallInfo{
			Provider: route.Provider, Model: route.Model, LatencyMs: latency,
			Usage: resp.Usage, StatusCode: 200, RawReply: reply,
			Input: text, SystemPrompt: prompt,
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

// Target is where a classifier's own upstream call goes. It is either a
// configured alias (pinned or group) or a declared provider model name —
// never a force alias (a force alias selects no model, so resolving it would
// require running classification, which is the recursion this type exists to
// forbid). This is the router's vocabulary, deliberately: a classifier never
// picks where traffic goes, it only names which client-facing target its
// own call uses.
type Target struct {
	Alias string // configured alias name (pinned/group); empty when Model is set
	Model string // declared provider model name; empty when Alias is set
}

// routes resolves the target into candidate routes (primary + group
// fallbacks for an alias), mirroring the resolution a client naming the
// alias directly would get. resolver must be non-nil when Target.Alias is
// set; providers must hold the resolved provider.
func (t *Target) routes(resolver *router.AliasResolver, providers map[string]types.ProviderConfig) ([]types.Route, error) {
	if t == nil {
		return nil, fmt.Errorf("classifier has no target (need alias or model)")
	}
	if t.Alias != "" {
		provider, model, ok, err := resolver.Resolve(t.Alias)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("alias %q is not configured", t.Alias)
		}
		cfg, ok := providers[provider]
		if !ok {
			return nil, fmt.Errorf("alias %q resolved to unconfigured provider %q", t.Alias, provider)
		}
		primary := types.Route{Provider: provider, Model: model, Config: cfg, Rationale: fmt.Sprintf("classifier alias %q", t.Alias)}
		fallbacks := resolver.GroupFallbacks(t.Alias, router.AliasMember{Provider: provider, Model: model})
		return append([]types.Route{primary}, fallbacks...), nil
	}
	// A literal model name: find the provider that declares it.
	provider, ok := providerForModel(providers, t.Model)
	if !ok {
		return nil, fmt.Errorf("model %q is not a declared model of any configured provider", t.Model)
	}
	cfg := providers[provider]
	return []types.Route{{Provider: provider, Model: t.Model, Config: cfg, Rationale: fmt.Sprintf("classifier model %q", t.Model)}}, nil
}

// providerForModel finds the provider that declares model. Providers are
// checked in sorted order so the result is stable.
func providerForModel(providers map[string]types.ProviderConfig, model string) (string, bool) {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, m := range providers[name].Models {
			if m == model {
				return name, true
			}
		}
	}
	return "", false
}

// candidates resolves the configured alias into a primary route plus its
// group-fallback siblings, mirroring the same resolution a client naming
// this alias directly would get (see router.AliasResolver).
func (c *LLMClassifier) candidates() ([]types.Route, error) {
	return c.target.routes(c.resolver, c.providers)
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
