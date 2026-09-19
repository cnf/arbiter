package classifier

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/upstream"
	"github.com/cnf/arbiter/pkg/types"
)

// DecisionsClassifier fills one or more axes by asking a decision model typed
// questions, instead of asking a chat model to emit one word and parsing it.
//
// It is the structural fix for the LLM classifier's two load-bearing hacks:
// there is no reply to match (a `choice` is constrained to the labels the
// config defines, so an off-list reply is impossible rather than a failed
// call), and there is no reply contract to protect. What it adds beyond that
// is a real probability per answer, which Confidence: 1.0 could never be.
//
// It asks EVERY configured question in ONE upstream call and reports a
// confidence per axis (Signals.AxisConfidence), which is the whole reason a
// decision model maps onto Arbiter's multi-axis Signals: several axes, one
// request, and no single "how sure was this call" number pretending to describe
// all of them.
//
// Like LLMClassifier it never returns an error from Classify: a failure falls
// through to a wrapped fallback classifier, with the attempt still recorded via
// Signals.ClassifierCalls. MergedClassifier aborts its entire merge — every
// axis, not just this one — on any sub-classifier error, so a classification
// failure must not become a request failure.
type DecisionsClassifier struct {
	name      string
	resolver  *router.AliasResolver
	alias     string
	client    upstream.DecisionClient
	providers map[string]types.ProviderConfig

	// questions are the typed questions asked in one call, in config order.
	// Order is kept so the recorded prompt and the rationale are stable.
	questions []decisionQuestion

	fallback Classifier
	timeout  time.Duration
}

// DecisionQuestionConfig is one question as config and construction express it.
// Exported because cmd/arbiter builds these from the classifier's `config:`
// map, and the two must not drift into different notions of a question.
type DecisionQuestionConfig struct {
	// Name is the key the question is sent under and read back by. Defaults
	// to the axis when unset, so a one-question config needs no name.
	Name string
	// Axis is the Signals field the answer fills.
	Axis string
	// Type is the primitive: "choice" (the only one built so far).
	Type string
	// Labels is the option set for a choice, each with its optional rubric.
	Labels []types.Label
	// Escape names the label meaning "no option fits". Sent as the choice's
	// `other` option; its verdict fills no axis.
	Escape string
	// Instructions is what is being decided between the options.
	Instructions string
}

// decisionQuestion is a configured question plus the vocabulary needed to
// interpret its answer.
type decisionQuestion struct {
	name         string
	axis         string
	qtype        string
	labels       []types.Label
	escape       string
	instructions string
}

// NewDecisionsClassifier creates a decision-model-backed classifier. alias
// names a configured alias (pinned or group) that routes the call, resolved the
// same way a client-named alias would be, including a group alias's member
// selection and fallback siblings. questions is the set asked in one call;
// fallback is used whenever the call fails outright; timeout <= 0 defaults to
// 10s.
func NewDecisionsClassifier(name string, resolver *router.AliasResolver, alias string, client upstream.DecisionClient, providers map[string]types.ProviderConfig, questions []DecisionQuestionConfig, fallback Classifier, timeout time.Duration) *DecisionsClassifier {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	qs := make([]decisionQuestion, 0, len(questions))
	for _, q := range questions {
		qs = append(qs, decisionQuestion{
			name:         q.Name,
			axis:         q.Axis,
			qtype:        q.Type,
			labels:       q.Labels,
			escape:       q.Escape,
			instructions: q.Instructions,
		})
	}
	return &DecisionsClassifier{
		name:      name,
		resolver:  resolver,
		alias:     alias,
		client:    client,
		providers: providers,
		questions: qs,
		fallback:  fallback,
		timeout:   timeout,
	}
}

// Classify asks every question in one call. A whole-call failure defers
// entirely to the fallback classifier; a per-question failure does not, because
// MergedClassifier resolves each axis independently and a partially-answered
// call is still strictly better than the heuristic for the axes it did answer.
func (c *DecisionsClassifier) Classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	verdicts, call, ok := c.tryDecide(ctx, req)
	if !ok {
		sig, err := c.runFallback(ctx, req)
		if err != nil {
			// The fallback itself failed (a config/programmer error, not a
			// network one — HeuristicClassifier never errors). Still return
			// the decision call's own diagnostics rather than losing them.
			sig = types.Signals{}
		}
		if call != nil {
			sig.ClassifierCalls = append(sig.ClassifierCalls, call)
		}
		return sig, nil
	}

	sig := types.Signals{
		AxisConfidence:  make(map[string]float64, len(verdicts)),
		ClassifierCalls: []*types.ClassifierCallInfo{call},
	}
	for _, v := range verdicts {
		// The overall Confidence is the highest per-axis value: it answers
		// "how sure was this classifier", which for a multi-axis call can only
		// be the most certain thing it concluded. AxisConfidence is what a
		// merge compares, per axis.
		sig.AxisConfidence[v.axis] = v.confidence
		if v.confidence > sig.Confidence {
			sig.Confidence = v.confidence
		}
		fillAxis(&sig, v.axis, v.value, v.capabilities)
	}
	return sig, nil
}

// fillAxis writes one answered axis onto Signals. An escape verdict arrives
// here as an empty value, which is the point: a policy router's
// `when: {domain: ...}` rules then simply do not match and a chained router
// takes over, instead of the operator writing a rule for a literal "unknown"
// domain.
func fillAxis(sig *types.Signals, axis, value string, capabilities []string) {
	if value == "" && len(capabilities) == 0 {
		return
	}
	switch axis {
	case AxisEffort:
		sig.Effort = value
	case AxisCostClass:
		sig.CostClass = value
	case AxisCapabilities:
		sig.RequiredCapabilities = capabilities
	default:
		sig.Domain = value
	}
}

// axisVerdict is one answered axis.
type axisVerdict struct {
	axis         string
	value        string
	capabilities []string
	confidence   float64
}

// runFallback calls the wrapped classifier, defensively treating a nil
// fallback (shouldn't happen — config validation requires one) as "no signal"
// rather than panicking.
func (c *DecisionsClassifier) runFallback(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	if c.fallback == nil {
		return types.Signals{}, nil
	}
	return c.fallback.Classify(ctx, req)
}

// tryDecide attempts the decision call. ok=false means it failed outright
// (nothing usable came back) and the fallback should supply every axis; call is
// still non-nil in that case so the failure has diagnostics.
func (c *DecisionsClassifier) tryDecide(ctx context.Context, req *types.NormalizedRequest) (verdicts []axisVerdict, call *types.ClassifierCallInfo, ok bool) {
	if c.resolver == nil || c.alias == "" || c.client == nil || len(c.questions) == 0 {
		return nil, nil, false
	}

	candidates, err := c.candidates()
	if err != nil || len(candidates) == 0 {
		return nil, &types.ClassifierCallInfo{Error: errString(err, "no route for classifier alias")}, false
	}

	// The input and the prompt are carried on every call record, success or
	// failure: a wrong verdict is only debuggable against the text that
	// produced it, and a failed call's prompt is how you tell "the rubric is
	// ambiguous" from "the endpoint was down".
	text := types.LastUserText(req)
	decisionReq := &types.DecisionRequest{
		State:     text,
		Questions: c.wireQuestions(),
	}
	prompt := c.promptSummary()

	cctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var last *types.ClassifierCallInfo
	for _, route := range candidates {
		start := time.Now()
		resp, err := c.client.Decide(cctx, route, decisionReq)
		latency := time.Since(start).Milliseconds()

		if err != nil {
			last = &types.ClassifierCallInfo{
				Provider: route.Provider, Model: route.Model, LatencyMs: latency,
				StatusCode: upstreamErrorStatus(err), Error: err.Error(),
				Input: text, SystemPrompt: prompt,
			}
			continue
		}

		info := &types.ClassifierCallInfo{
			Provider: route.Provider, Model: route.Model, LatencyMs: latency,
			Usage: resp.Usage.Normalized(), StatusCode: 200, RawReply: resp.Raw,
			Input: text, SystemPrompt: prompt,
		}

		answered, dropped := c.resolveAnswers(resp)
		if len(answered) == 0 {
			// A 200 that answered nothing usable is a failed call: acting on it
			// would fill no axis at all and silently look like "the model said
			// nothing fits" for every question at once.
			info.Error = strings.Join(dropped, "; ")
			if info.Error == "" {
				info.Error = "decision response answered none of the configured questions"
			}
			last = info
			continue
		}

		info.Verdict = c.verdictSummary(answered)
		// Per-call axes, so the classifier's own row carries every axis this
		// call filled rather than only the merged domain.
		info.Axes = make(map[string]string, len(answered))
		info.AxisConfidence = make(map[string]float64, len(answered))
		for _, v := range answered {
			if v.value != "" {
				info.Axes[v.axis] = v.value
			}
			info.AxisConfidence[v.axis] = v.confidence
		}
		if len(dropped) > 0 {
			// Recorded, not hidden: a partially-answered call is a real outcome
			// worth seeing, and why a question was dropped is what tells you
			// whether the rubric or the endpoint is at fault.
			info.Error = strings.Join(dropped, "; ")
		}
		return answered, info, true
	}
	return nil, last, false
}

// resolveAnswers turns one response into per-axis verdicts, plus a
// human-readable reason for every question it could not use.
func (c *DecisionsClassifier) resolveAnswers(resp *types.DecisionResponse) ([]axisVerdict, []string) {
	var (
		verdicts []axisVerdict
		dropped  []string
	)
	for _, q := range c.questions {
		answer, ok := resp.Answers[q.name]
		if !ok {
			dropped = append(dropped, fmt.Sprintf("no answer for question %q", q.name))
			continue
		}
		label, isOption := q.matchChoice(answer.Choice)
		if !isOption {
			// A choice outside the option set should be impossible — the
			// primitive is constrained to the criteria we sent. Treat a breach
			// as a dropped question rather than acting on an unknown label, the
			// same rule the LLM classifier applies to an off-list reply.
			dropped = append(dropped, fmt.Sprintf("question %q chose %q, which is not a configured option", q.name, answer.Choice))
			continue
		}
		v := axisVerdict{axis: q.axis, confidence: answer.Confidence}
		if label != nil {
			v.value = label.Name
		}
		verdicts = append(verdicts, v)
	}
	return verdicts, dropped
}

// matchChoice resolves a choice answer to the label it names. isOption=false
// means the answer matched no configured option.
//
// A returned nil label with isOption=true is the escape verdict — "none of
// these apply", which fills no axis.
//
// An escape verdict is reached two ways, and only these two:
//   - the answer names the configured escape label (matched case-insensitively
//     through FindLabel, so the model's casing does not matter), or
//   - no escape label is configured and the answer is a literal "other" — the
//     option this codebase adds in that case, since a config with no escape
//     still needs a way to say "none of these".
//
// A literal "other" is deliberately NOT accepted as escape when an escape label
// IS configured: in that config the operator has named their own option, and
// treating a different string as the same verdict would make the axis's
// emptiness depend on which synonym the model happened to pick.
func (q decisionQuestion) matchChoice(choice string) (label *types.Label, isOption bool) {
	l, ok := types.FindLabel(q.labels, choice)
	if !ok {
		if q.escape == "" && strings.EqualFold(choice, "other") {
			return nil, true
		}
		return nil, false
	}
	if q.escape != "" && strings.EqualFold(l.Name, q.escape) {
		return nil, true
	}
	return &l, true
}

// choiceCriteria builds the criteria map a choice question sends: every
// configured label with its rubric description.
//
// The escape label is sent under ITS OWN NAME — it is already in the criteria
// above, carrying the operator's own wording. An earlier version also emitted a
// separate `other` option alongside it, which meant a config declaring
// `none: ...` was sent BOTH `none` and `other`, two options meaning the same
// thing with different rubrics — and a reply of `none` filled the axis while a
// reply of `other` did not.
//
// `other` is added only when NO escape label is configured. Without it a
// decision model is forced into the closest listed option even when none of
// them fit, which is exactly the failure an escape label exists to prevent —
// so a config that declares no escape still gets a way to say "none of these".
func (q decisionQuestion) choiceCriteria() map[string]string {
	criteria := make(map[string]string, len(q.labels)+1)
	for _, l := range q.labels {
		criteria[l.Name] = l.Description
	}
	if q.escape == "" {
		criteria["other"] = "none of the above apply"
		return criteria
	}
	// A configured escape label with no description would be sent as an
	// undescribed option, which is the weakest possible rubric for the one
	// option that decides whether any axis is filled at all.
	if l, ok := types.FindLabel(q.labels, q.escape); ok && l.Description == "" {
		criteria[l.Name] = "none of the above apply"
	}
	return criteria
}

// wireQuestions builds the questions map for one request. Every question goes
// in the same call, which is the point of a decision model.
func (c *DecisionsClassifier) wireQuestions() map[string]types.DecisionQuestion {
	out := make(map[string]types.DecisionQuestion, len(c.questions))
	for _, q := range c.questions {
		out[q.name] = types.DecisionQuestion{
			Type:         q.qtype,
			Instructions: q.instructions,
			Criteria:     q.choiceCriteria(),
		}
	}
	return out
}

// promptSummary is what gets stored as the call's SystemPrompt. A decision
// request has no system prompt — it has typed questions — so this records the
// questions as they were actually asked, which is the equivalent evidence:
// without it a verdict cannot be read against the criteria that produced it.
//
// Everything is sorted because Go map iteration is randomized: an unsorted
// summary would emit different bytes on every call and make one call's recorded
// questions impossible to diff against the next.
func (c *DecisionsClassifier) promptSummary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "decision questions (%d, asked in one call):", len(c.questions))
	for _, q := range c.questions {
		b.WriteString("\n\n")
		b.WriteString(q.name)
		b.WriteString(" [")
		b.WriteString(q.qtype)
		b.WriteString("] -> axis ")
		b.WriteString(q.axis)
		if q.instructions != "" {
			b.WriteString("\n  ")
			b.WriteString(q.instructions)
		}
		criteria := q.choiceCriteria()
		names := make([]string, 0, len(criteria))
		for name := range criteria {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			b.WriteString("\n  - ")
			b.WriteString(name)
			if desc := criteria[name]; desc != "" {
				b.WriteString(": ")
				b.WriteString(desc)
			}
		}
	}
	return b.String()
}

// verdictSummary renders every answered axis with its probability, e.g.
// `domain="code_generation" (0.98), cost_class="budget" (0.81)`. Sorted so two
// identical verdicts render identically — the rationale is diffed between
// calls.
func (c *DecisionsClassifier) verdictSummary(verdicts []axisVerdict) string {
	byAxis := make(map[string]axisVerdict, len(verdicts))
	for _, v := range verdicts {
		byAxis[v.axis] = v
	}
	axes := make([]string, 0, len(byAxis))
	for axis := range byAxis {
		axes = append(axes, axis)
	}
	sort.Strings(axes)

	parts := make([]string, 0, len(axes))
	for _, axis := range axes {
		v := byAxis[axis]
		value := v.value
		if value == "" {
			// The escape verdict: reported explicitly rather than as an empty
			// string, so the rationale reads as a decision the model made.
			value = "none of the options fit"
		}
		parts = append(parts, fmt.Sprintf("%s=%q (%.2f)", axis, value, v.confidence))
	}
	return "decisions classifier answered " + strings.Join(parts, ", ")
}

// candidates resolves the configured alias into a primary route plus its
// group-fallback siblings, mirroring the same resolution a client naming this
// alias directly would get (see router.AliasResolver).
func (c *DecisionsClassifier) candidates() ([]types.Route, error) {
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
