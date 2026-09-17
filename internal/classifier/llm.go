package classifier

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/upstream"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

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
	labels    []string
	fallback  Classifier
	timeout   time.Duration
}

// NewLLMClassifier creates an LLM-backed classifier. alias names a configured
// alias (pinned or group) that routes the classification call — resolved the
// same way a client-named alias would be, including a group alias's member
// selection and fallback siblings. fallback is used whenever the call fails
// outright; timeout <= 0 defaults to 10s.
func NewLLMClassifier(name, axis string, resolver *router.AliasResolver, alias string, u upstream.Client, providers map[string]types.ProviderConfig, labels []string, fallback Classifier, timeout time.Duration) *LLMClassifier {
	if axis == "" {
		axis = AxisDomain
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &LLMClassifier{
		name:      name,
		axis:      axis,
		resolver:  resolver,
		alias:     alias,
		upstream:  u,
		providers: providers,
		labels:    labels,
		fallback:  fallback,
		timeout:   timeout,
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
	switch c.axis {
	case AxisEffort:
		sig.Effort = label
	case AxisCostClass:
		sig.CostClass = label
	case AxisCapabilities:
		sig.RequiredCapabilities = []string{label}
	default:
		sig.Domain = label
	}
	return sig, nil
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
// failure has diagnostics. ok=true means label is a value from c.labels the
// model actually chose.
func (c *LLMClassifier) tryClassify(ctx context.Context, req *types.NormalizedRequest) (call *types.ClassifierCallInfo, label string, ok bool) {
	if c.resolver == nil || c.alias == "" || len(c.labels) == 0 {
		return nil, "", false
	}

	candidates, err := c.candidates()
	if err != nil || len(candidates) == 0 {
		return &types.ClassifierCallInfo{Error: errString(err, "no route for classifier alias")}, "", false
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
		matched, isLabel := matchLabel(reply, c.labels)
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
	return lastCall, "", false
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

// systemPrompt asks for exactly one word so parsing stays a plain string
// match — no JSON, no schema, nothing that can half-parse.
func (c *LLMClassifier) systemPrompt() string {
	return fmt.Sprintf(
		"Classify the user's message into exactly one of these categories: %s.\n"+
			"Reply with the single matching word and nothing else — no punctuation, no explanation.",
		strings.Join(c.labels, ", "))
}

// matchLabel compares reply against labels case-insensitively and returns
// the canonically-configured spelling, not the model's own casing.
func matchLabel(reply string, labels []string) (string, bool) {
	lower := strings.ToLower(reply)
	for _, l := range labels {
		if strings.ToLower(l) == lower {
			return l, true
		}
	}
	return "", false
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
