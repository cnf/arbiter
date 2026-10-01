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
	target    *Target
	client    upstream.DecisionClient
	providers map[string]types.ProviderConfig

	// questions are the typed questions asked in one call, in config order.
	// Order is kept so the recorded prompt and the rationale are stable.
	questions []decisionQuestion

	fallback Classifier
	timeout  time.Duration

	// onlyIfUnset, when true, makes the merge skip this classifier — no
	// upstream call at all — unless any axis one of its questions fills is
	// still empty after every classifier declared before it. The gate lives
	// here so the merge can query it without knowing the concrete type.
	onlyIfUnset bool

	// maxInputChars caps the text sent as the decision call's state, in
	// characters. 0 means unlimited; the constructor applies the default, so
	// a zero here is only reachable by asking for it.
	maxInputChars int
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
	// Type is the primitive: "choice", "noul" or "score".
	Type string
	// Labels is the option set for a choice, each with its optional rubric.
	// Unused by "noul"/"score".
	Labels []types.Label
	// Escape names the label meaning "no option fits". Sent as the choice's
	// `other` option; its verdict fills no axis. Unused by "noul"/"score".
	Escape string
	// Value is "noul"'s axis value: applied when the model answers yes
	// (Noul > 0.5), and nothing is applied on no — the axis is left exactly
	// as it was, open for a later classifier to fill. Unused by "choice".
	Value string
	// Levels is "score"'s ordered rubric, low -> high. The answer's
	// fractional position is snapped to the nearest level, and that
	// level's Name is what fills the axis. Unused by "choice"/"noul".
	Levels []types.ScoreLevel
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
	value        string
	levels       []types.ScoreLevel
	instructions string
}

// NewDecisionsClassifier creates a decision-model-backed classifier. target
// names where the call goes — a configured alias (pinned or group) or a
// declared provider model name, resolved the same way a client-named alias
// would be, including a group alias's member selection and fallback siblings.
// questions is the set asked in one call; fallback is used whenever the call
// fails outright; timeout <= 0 defaults to 10s.
func NewDecisionsClassifier(name string, resolver *router.AliasResolver, target *Target, client upstream.DecisionClient, providers map[string]types.ProviderConfig, questions []DecisionQuestionConfig, fallback Classifier, timeout time.Duration) *DecisionsClassifier {
	return NewDecisionsClassifierFull(name, resolver, target, client, providers, questions, fallback, timeout, 0, false)
}

// NewDecisionsClassifierFull is NewDecisionsClassifier with an explicit input
// cap: maxInputChars bounds the state sent to the decision endpoint (see
// effectiveMaxInputChars — 0 takes the default, negative means unlimited).
func NewDecisionsClassifierFull(name string, resolver *router.AliasResolver, target *Target, client upstream.DecisionClient, providers map[string]types.ProviderConfig, questions []DecisionQuestionConfig, fallback Classifier, timeout time.Duration, maxInputChars int, onlyIfUnset bool) *DecisionsClassifier {
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
			value:        q.Value,
			levels:       q.Levels,
			instructions: q.Instructions,
		})
	}
	return &DecisionsClassifier{
		name:          name,
		resolver:      resolver,
		target:        target,
		client:        client,
		providers:     providers,
		questions:     qs,
		fallback:      fallback,
		timeout:       timeout,
		onlyIfUnset:   onlyIfUnset,
		maxInputChars: effectiveMaxInputChars(maxInputChars),
	}
}

// gated and gateAxes implement onlyIfUnsetClassifier: the merge gates this
// classifier behind earlier classifiers on every axis its questions fill. A
// decisions call is one round trip for several axes, so it is only worth
// skipping when EVERY axis it could answer is already set — otherwise the
// remaining unanswered axis justifies the call.
func (c *DecisionsClassifier) gated() bool { return c.onlyIfUnset }

func (c *DecisionsClassifier) gateAxes() []string {
	axes := make([]string, 0, len(c.questions))
	for _, q := range c.questions {
		if q.axis != "" {
			axes = append(axes, q.axis)
		}
	}
	return axes
}

// Classify asks every question in one call. A whole-call failure defers
// entirely to the fallback classifier; a per-question failure does not, because
// MergedClassifier resolves each axis independently and a partially-answered
// call is still strictly better than the heuristic for the axes it did answer.
// The failure policy itself is shared — see modelBacked.
func (c *DecisionsClassifier) Classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	return modelBacked(ctx, req, c.fallback,
		func() ([]axisVerdict, *types.ClassifierCallInfo, bool) {
			return c.tryDecide(ctx, req)
		},
		func(sig *types.Signals, verdicts []axisVerdict) {
			for _, v := range verdicts {
				// The overall Confidence is the highest per-axis value: it
				// answers "how sure was this classifier", which for a
				// multi-axis call can only be the most certain thing it
				// concluded. AxisConfidence is what a merge compares, per axis.
				sig.AxisConfidence[v.axis] = v.confidence
				if v.confidence > sig.Confidence {
					sig.Confidence = v.confidence
				}
				// A "noul" question that answered no is a real, recorded
				// verdict (confidence above) but fills nothing — v.skip is
				// how it differs from a choice's escape, which DOES fill the
				// axis with the unmatched sentinel. "No" is not "nothing
				// fits"; it is "this question's condition did not hold",
				// and the axis must stay open for a later classifier.
				if v.skip {
					continue
				}
				fillAxis(sig, v.axis, v.value, v.capabilities)
			}
		})
}

// fillAxis writes one answered axis onto Signals. An escape verdict arrives
// here as an empty value, which is filled with the reserved sentinel
// types.UnmatchedValue (except on the ADDITIVE axes — capabilities and tags —
// which are sets with no single "nothing matched" value; see
// types.UnmatchedValue's own doc) so a policy router's `when: {domain:
// unmatched}` rule can match it explicitly, instead of every wildcard rule
// matching a silently empty axis.
//
// Called only for a question resolveAnswers actually answered AND that wants
// to fill something — a choice's escape arrives here (as an empty value), but
// a noul's "no" verdict does not (see axisVerdict.skip) — so there is no
// "nothing happened here" case left to special-case away.
func fillAxis(sig *types.Signals, axis, value string, capabilities []string) {
	if axis == AxisCapabilities {
		sig.RequiredCapabilities = capabilities
		return
	}
	// Tags are additive like capabilities, but filled one tag at a time (a
	// question yields one string), so this APPENDS and an empty value adds
	// nothing — a choice's escape verdict ("none of the options matched")
	// means no tag, not the unmatched sentinel. Multiple tag questions in one
	// call are rejected by config validation in phase 1; the append is the
	// per-classifier accumulation phase 3 will allow.
	if axis == AxisTags {
		if value != "" {
			sig.Tags = append(sig.Tags, value)
		}
		return
	}
	if value == "" {
		value = types.UnmatchedValue
	}
	switch axis {
	case AxisDifficulty:
		sig.Difficulty = value
	case AxisCostClass:
		sig.CostClass = value
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
	// skip is true for a "noul" question that answered no: a real, recorded
	// verdict, but one that fills nothing — see Classify's use of it, and
	// fillAxis's doc for why this is not the same as a choice's escape.
	skip bool
}

// tryDecide attempts the decision call. ok=false means it failed outright
// (nothing usable came back) and the fallback should supply every axis; call is
// still non-nil in that case so the failure has diagnostics.
func (c *DecisionsClassifier) tryDecide(ctx context.Context, req *types.NormalizedRequest) (verdicts []axisVerdict, call *types.ClassifierCallInfo, ok bool) {
	if c.target == nil || c.client == nil || len(c.questions) == 0 {
		return nil, nil, false
	}

	// Nothing to decide on means no call. This is the case that produced a
	// burst of identical "none of the options fit" verdicts at ~0.8
	// confidence: an agentic request whose last user turn was tool_result-only
	// gave the endpoint an empty state, and it answered anyway. A verdict on
	// nothing is worse than no verdict, because it is stored and rendered like
	// any other.
	text := classifierInput(req, c.maxInputChars)
	if text == "" {
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
		if q.qtype == types.DecisionNoul {
			verdicts = append(verdicts, q.resolveNoul(answer))
			continue
		}
		if q.qtype == types.DecisionScore {
			v, err := q.resolveScore(answer)
			if err != nil {
				dropped = append(dropped, fmt.Sprintf("question %q: %v", q.name, err))
				continue
			}
			verdicts = append(verdicts, v)
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

// resolveNoul turns a "noul" answer into a verdict. On yes (p > 0.5) it fills
// the axis with the question's configured Value; on no it fills nothing —
// skip is set so Classify leaves the axis exactly as it was, open for a later
// classifier (unlike a choice's escape, which fills the axis with the
// unmatched sentinel — see fillAxis's doc for why the two are not the same).
//
// Confidence is derived as max(p, 1-p): a Noul answer carries no confidence
// field of its own (see DecisionAnswer.Noul's doc), because a confident "no"
// and a confident "yes" are equally confident — this is the one honest number
// that is true of both.
func (q decisionQuestion) resolveNoul(answer types.DecisionAnswer) axisVerdict {
	p := answer.Noul
	confidence := p
	if confidence < 1-p {
		confidence = 1 - p
	}
	if p > 0.5 {
		return axisVerdict{axis: q.axis, value: q.value, confidence: confidence}
	}
	return axisVerdict{axis: q.axis, confidence: confidence, skip: true}
}

// resolveScore turns a "score" answer into a verdict by snapping its
// fractional position to the nearest configured level and filling the axis
// with that level's Name — a score only needs to collapse to one of the
// axis's configured string values, same as a choice's winning label.
//
// The raw fractional position is lost in that collapse (1.6 and 1.9 both
// round to index 2), which is a real precision loss, not an oversight: the
// value this classifier produces is an axis string, and a `when:` rule
// matches axis strings. A future extension could keep the raw score on
// ClassifierCallInfo for a threshold-based `when:` rule, but that is not
// needed to make "score" usable today.
//
// Confidence is read directly from the answer (unlike "noul", a "score"
// answer DOES carry its own Confidence — see DecisionAnswer.Score's doc).
func (q decisionQuestion) resolveScore(answer types.DecisionAnswer) (axisVerdict, error) {
	if len(q.levels) == 0 {
		return axisVerdict{}, fmt.Errorf("question has no configured levels")
	}
	i := int(answer.Score + 0.5)
	if i < 0 {
		i = 0
	}
	if max := len(q.levels) - 1; i > max {
		i = max
	}
	return axisVerdict{axis: q.axis, value: q.levels[i].Name, confidence: answer.Confidence}, nil
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

// scoreCriteria builds the ordered criteria list a score question sends: one
// string per configured level, low -> high. A level's Description is sent
// when configured (the Jev reference shape is a plain criteria sentence per
// level); an undescribed level falls back to its bare Name rather than
// sending an empty string, which would be the weakest possible rubric for
// the level a model is meant to match against.
func (q decisionQuestion) scoreCriteria() []string {
	out := make([]string, 0, len(q.levels))
	for _, l := range q.levels {
		if l.Description != "" {
			out = append(out, l.Description)
			continue
		}
		out = append(out, l.Name)
	}
	return out
}

// wireQuestions builds the questions map for one request. Every question goes
// in the same call, which is the point of a decision model.
func (c *DecisionsClassifier) wireQuestions() map[string]types.DecisionQuestion {
	out := make(map[string]types.DecisionQuestion, len(c.questions))
	for _, q := range c.questions {
		wq := types.DecisionQuestion{
			Type:         q.qtype,
			Instructions: q.instructions,
		}
		// "noul" has no criteria — it is a bare yes/no question, answered
		// from Instructions alone (see types.DecisionQuestion.Criteria's doc:
		// "Unused by noul").
		switch q.qtype {
		case types.DecisionNoul:
		case types.DecisionScore:
			// Order IS the data for a score (see types.ScoreLevel's doc), so
			// this sends the ordered list of level descriptions rather than
			// choiceCriteria()'s alphabetically-sorted map, which would
			// silently discard the order a score's whole meaning depends on.
			wq.Criteria = q.scoreCriteria()
		default:
			wq.Criteria = q.choiceCriteria()
		}
		out[q.name] = wq
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
		if q.qtype == types.DecisionNoul {
			// No criteria to list — a noul question is answered from
			// Instructions alone. The axis value it applies on "yes" is
			// still worth recording, so a verdict is readable without the
			// config beside it.
			fmt.Fprintf(&b, "\n  - yes -> %s=%q", q.axis, q.value)
			continue
		}
		if q.qtype == types.DecisionScore {
			// Order is the data here too, so levels are listed in their
			// configured low -> high order rather than sorted by name.
			for i, l := range q.levels {
				b.WriteString("\n  - ")
				fmt.Fprintf(&b, "[%d] %s", i, l.Name)
				if l.Description != "" {
					b.WriteString(": ")
					b.WriteString(l.Description)
				}
			}
			continue
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
		switch {
		case v.skip:
			// A "noul" question that answered no: a real verdict, but
			// distinct wording from a choice's escape — "no" is not "nothing
			// fits", it is "this question's condition did not hold".
			parts = append(parts, fmt.Sprintf("%s=no (%.2f)", axis, v.confidence))
		case v.value == "":
			// The escape verdict: reported explicitly rather than as an empty
			// string, so the rationale reads as a decision the model made.
			parts = append(parts, fmt.Sprintf("%s=%q (%.2f)", axis, "none of the options fit", v.confidence))
		default:
			parts = append(parts, fmt.Sprintf("%s=%q (%.2f)", axis, v.value, v.confidence))
		}
	}
	return "decisions classifier answered " + strings.Join(parts, ", ")
}

// candidates resolves the configured target into a primary route plus its
// group-fallback siblings, mirroring the same resolution a client naming
// this alias directly would get (see router.AliasResolver).
func (c *DecisionsClassifier) candidates() ([]types.Route, error) {
	return c.target.routes(c.resolver, c.providers)
}
