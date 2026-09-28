package main

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/config"
	arbiterhttp "github.com/cnf/arbiter/internal/http"
	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/upstream"
	"github.com/cnf/arbiter/pkg/types"
)

// configuredModels lists every model a client can name in a request:
// concrete provider models, plus every configured alias — force-aliases
// included, since REQUIREMENTS.md §1 makes aliases client-facing regardless
// of shape. An alias is advertised with Provider "alias" rather than a
// resolved target, since group/force aliases don't resolve to one fixed
// provider.
func configuredModels(cfg *config.Config) []arbiterhttp.Model {
	// Catalog rows carry what each model can do, keyed provider+model exactly
	// as the provider's declared model list is. Built into a lookup first so
	// the model list stays a single pass and an unmatched model simply has no
	// capability data (unknown, not none).
	type capInfo struct {
		modalities []string
		maxIn      *int
		maxOut     *int
		metadata   map[string]interface{}
	}
	byModel := make(map[string]capInfo, len(cfg.ModelCatalog))
	for _, e := range cfg.ModelCatalog {
		byModel[e.Provider+"\x00"+e.Model] = capInfo{
			modalities: e.InputModalities,
			maxIn:      e.MaxInputTokens,
			maxOut:     e.MaxOutputTokens,
			metadata:   e.Metadata,
		}
	}

	models := make([]arbiterhttp.Model, 0)
	for provider, providerConfig := range cfg.Providers {
		for _, model := range providerConfig.Models {
			m := arbiterhttp.Model{ID: model, Provider: provider}
			if c, ok := byModel[provider+"\x00"+model]; ok {
				m.InputModalities = c.modalities
				m.MaxInputTokens = c.maxIn
				m.MaxOutputTokens = c.maxOut
				m.Metadata = c.metadata
			}
			models = append(models, m)
		}
	}
	// Aliases carry no capability data of their own: an alias resolves to a
	// target at request time, so what it can accept depends on where it lands.
	// Left unknown rather than guessed at from the members.
	for name := range cfg.Aliases {
		models = append(models, arbiterhttp.Model{ID: name, Provider: "alias"})
	}
	slices.SortFunc(models, func(a, b arbiterhttp.Model) int {
		if a.ID != b.ID {
			return cmp.Compare(a.ID, b.ID)
		}
		return cmp.Compare(a.Provider, b.Provider)
	})
	return models
}

// buildClassifiers builds every configured classifier in two passes: every
// non-model-backed type first (indexed by name), then every type that makes its
// own upstream call ("llm", "decisions"), resolving its named `fallback` from
// that index. Two passes rather than one so a model-backed classifier's
// fallback is guaranteed to exist regardless of which one is declared first in
// config — the final list is still assembled in declared order, only the
// dependency resolution is two-phase. A fallback naming another model-backed
// classifier is rejected for "llm"; a "decisions" classifier may fall back to
// an "llm" one (see config's validateDecisionsClassifiers).
func buildClassifiers(ccs []config.ClassifierConfig, resolver *router.AliasResolver, providers map[string]types.ProviderConfig, u upstream.Client, decisions upstream.DecisionClient) ([]classifier.Classifier, error) {
	byName := make(map[string]classifier.Classifier, len(ccs))
	for _, cc := range ccs {
		if isModelBackedClassifier(cc.Type) {
			continue
		}
		c, err := buildClassifier(cc)
		if err != nil {
			return nil, fmt.Errorf("classifier %q: %w", cc.Name, err)
		}
		byName[cc.Name] = c
	}

	ordered := make([]classifier.Classifier, 0, len(ccs))
	for _, cc := range ccs {
		if !isModelBackedClassifier(cc.Type) {
			ordered = append(ordered, byName[cc.Name])
			continue
		}
		var (
			c   classifier.Classifier
			err error
		)
		switch cc.Type {
		case "decisions":
			c, err = buildDecisionsClassifier(cc, resolver, providers, decisions, byName)
		default:
			c, err = buildLLMClassifier(cc, resolver, providers, u, byName)
		}
		if err != nil {
			return nil, fmt.Errorf("classifier %q: %w", cc.Name, err)
		}
		ordered = append(ordered, c)
	}
	return ordered, nil
}

// isModelBackedClassifier reports whether a type makes its own upstream call
// and therefore needs the second construction pass (and a non-model fallback).
func isModelBackedClassifier(typeName string) bool {
	return typeName == "llm" || typeName == "decisions"
}

// buildClassifier's axis defaulting preserves pre-axis behavior: a plain
// "heuristic" classifier with no declared axis fills Domain, and the legacy
// "capability_detector" type always fills Capabilities regardless of what's
// declared (it never meant anything else). Does not handle the model-backed
// types ("llm", "decisions") — those need the resolver/providers/upstream
// client buildClassifiers threads in, which is why each has its own builder and
// its own pass.
func buildClassifier(cc config.ClassifierConfig) (classifier.Classifier, error) {
	switch cc.Type {
	case "heuristic":
		return buildHeuristicClassifier(cc, cc.Axis)
	case "capability_detector":
		// The legacy type always fills capabilities, whatever axis it declares
		// (config validation rejects a declared axis on it).
		return buildHeuristicClassifier(cc, classifier.AxisCapabilities)
	default:
		return nil, fmt.Errorf("unknown classifier type %q", cc.Type)
	}
}

// buildHeuristicClassifier builds a heuristic classifier from its `config:`
// block: keywords (or the legacy `detectors` spelling), an optional `match`
// block matching the request's own text rather than its last user message, and
// an optional `detect` list of structurally-proven capabilities.
//
// match and detect are the two ways this type does better than keyword
// guessing, and they fail differently: `match` is exact but needs the operator
// to know the client's signature, while `detect` needs nothing from the
// operator beyond naming the capability because the request either carries the
// bytes or it does not.
func buildHeuristicClassifier(cc config.ClassifierConfig, axis string) (classifier.Classifier, error) {
	keywords, err := stringListMap(cc.Config, "keywords", "detectors")
	if err != nil {
		return nil, err
	}

	var matcher *classifier.RequestMatcher
	if raw, ok := cc.Config["match"]; ok {
		patterns, err := types.ParseMatchPatterns(raw)
		if err != nil {
			return nil, fmt.Errorf("match: %w", err)
		}
		value, _ := cc.Config["value"].(string)
		kind, _ := cc.Config["request_kind"].(string)
		where, err := stringList(cc.Config, "where")
		if err != nil {
			return nil, err
		}
		decisive, _ := cc.Config["decisive"].(bool)
		matcher, err = classifier.NewRequestMatcher(patterns, value, kind, where, decisive)
		if err != nil {
			return nil, fmt.Errorf("match: %w", err)
		}
	}

	detect, err := stringList(cc.Config, "detect")
	if err != nil {
		return nil, err
	}
	longContextTokens := intFromConfig(cc.Config, "long_context_tokens")

	// A classifier with no signals at all can never produce anything, which is
	// config the operator believes is doing something — the failure mode this
	// builder's callers exist to catch. It is checked here rather than in
	// stringListMap because each of the three is individually optional: a
	// title-gen matcher has no keywords and needs none, while a keyword
	// classifier has no match and needs none either.
	if len(keywords) == 0 && matcher == nil && len(detect) == 0 {
		return nil, fmt.Errorf("classifier %q: needs at least one of \"keywords\", \"match\" or \"detect\"", cc.Name)
	}

	return classifier.NewHeuristicClassifierFull(cc.Name, axis, keywords, matcher, detect, longContextTokens), nil
}

// buildLLMClassifier builds an "llm" classifier: alias (required) names the
// configured alias its classification calls route through, labels (required,
// non-empty) is the set of values it may return — bare names, or name ->
// rubric-description pairs — escape (optional) names the label meaning "no
// category fits", instructions (optional) replaces the default framing
// sentence, fallback (required) names another classifier already built in
// buildClassifiers' first pass, and timeout is an optional Go duration
// (defaults inside NewLLMClassifier).
func buildLLMClassifier(cc config.ClassifierConfig, resolver *router.AliasResolver, providers map[string]types.ProviderConfig, u upstream.Client, byName map[string]classifier.Classifier) (classifier.Classifier, error) {
	alias, _ := cc.Config["alias"].(string)
	model, _ := cc.Config["model"].(string)
	if alias == "" && model == "" {
		return nil, fmt.Errorf(`"llm" classifier requires exactly one of "alias" or "model"`)
	}
	if alias != "" && model != "" {
		return nil, fmt.Errorf(`"llm" classifier must set exactly one of "alias" or "model", not both`)
	}
	var target *classifier.Target
	if alias != "" {
		target = &classifier.Target{Alias: alias}
	} else {
		target = &classifier.Target{Model: model}
	}
	// The same parser config validation uses, so the two cannot disagree about
	// what a labels block means (see types.ParseLabels).
	labels, err := types.ParseLabels(cc.Config["labels"])
	if err != nil {
		return nil, fmt.Errorf("invalid labels: %w", err)
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf(`"llm" classifier requires a non-empty "labels" list`)
	}
	escape, _ := cc.Config["escape"].(string)
	instructions, _ := cc.Config["instructions"].(string)
	fallbackName, _ := cc.Config["fallback"].(string)
	if fallbackName == "" {
		return nil, fmt.Errorf(`"llm" classifier requires "fallback"`)
	}
	fallback, ok := byName[fallbackName]
	if !ok {
		return nil, fmt.Errorf("fallback %q is not a configured non-llm classifier (must be declared, and must not itself be type \"llm\")", fallbackName)
	}
	var timeout time.Duration
	if raw, _ := cc.Config["timeout"].(string); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout %q: %w", raw, err)
		}
		timeout = d
	}
	return classifier.NewLLMClassifierFull(cc.Name, cc.Axis, resolver, target, u, providers, labels, escape, instructions, fallback, timeout, maxInputChars(cc), cc.OnlyIfUnset), nil
}

// buildDecisionsClassifier builds a "decisions" classifier: alias (required)
// names the configured alias its decision calls route through — which must
// resolve to a provider of type "decisions" (config validation enforces that),
// questions (required) are the typed questions asked in ONE call, each with its
// own `axis` choosing the Signals field it fills, escape (optional) names the
// label meaning "no option fits", instructions (optional) is the question's
// framing sentence, fallback (required) names another classifier already built
// in buildClassifiers' first pass, and timeout is an optional Go duration
// (defaults inside NewDecisionsClassifier).
//
// Question order is sorted by name rather than taken from map iteration, so the
// recorded prompt and the rationale are stable across calls — the same reason
// the LLM classifier sorts its labels.
func buildDecisionsClassifier(cc config.ClassifierConfig, resolver *router.AliasResolver, providers map[string]types.ProviderConfig, decisions upstream.DecisionClient, byName map[string]classifier.Classifier) (classifier.Classifier, error) {
	alias, _ := cc.Config["alias"].(string)
	model, _ := cc.Config["model"].(string)
	if alias == "" && model == "" {
		return nil, fmt.Errorf(`"decisions" classifier requires exactly one of "alias" or "model"`)
	}
	if alias != "" && model != "" {
		return nil, fmt.Errorf(`"decisions" classifier must set exactly one of "alias" or "model", not both`)
	}
	var target *classifier.Target
	if alias != "" {
		target = &classifier.Target{Alias: alias}
	} else {
		target = &classifier.Target{Model: model}
	}
	rawQuestions, ok := cc.Config["questions"].(map[string]interface{})
	if !ok || len(rawQuestions) == 0 {
		return nil, fmt.Errorf(`"decisions" classifier requires "questions"`)
	}

	names := make([]string, 0, len(rawQuestions))
	for name := range rawQuestions {
		names = append(names, name)
	}
	sort.Strings(names)

	questions := make([]classifier.DecisionQuestionConfig, 0, len(names))
	for _, qname := range names {
		q, ok := rawQuestions[qname].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("question %q must be a map", qname)
		}
		axis, _ := q["axis"].(string)
		if axis == "" {
			return nil, fmt.Errorf("question %q requires \"axis\"", qname)
		}
		// The same parser config validation uses, so the two cannot disagree
		// about what a labels block means (see types.ParseLabels).
		labels, err := types.ParseLabels(q["labels"])
		if err != nil {
			return nil, fmt.Errorf("question %q: invalid labels: %w", qname, err)
		}
		if len(labels) == 0 {
			return nil, fmt.Errorf("question %q requires a non-empty \"labels\" list", qname)
		}
		qtype, _ := q["type"].(string)
		if qtype == "" {
			qtype = types.DecisionChoice
		}
		escape, _ := q["escape"].(string)
		instructions, _ := q["instructions"].(string)
		questions = append(questions, classifier.DecisionQuestionConfig{
			Name: qname, Axis: axis, Type: qtype,
			Labels: labels, Escape: escape, Instructions: instructions,
		})
	}

	fallbackName, _ := cc.Config["fallback"].(string)
	if fallbackName == "" {
		return nil, fmt.Errorf(`"decisions" classifier requires "fallback"`)
	}
	fallback, ok := byName[fallbackName]
	if !ok {
		return nil, fmt.Errorf("fallback %q is not a configured non-model classifier (must be declared, and must not itself be a model-backed type)", fallbackName)
	}
	var timeout time.Duration
	if raw, _ := cc.Config["timeout"].(string); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout %q: %w", raw, err)
		}
		timeout = d
	}
	return classifier.NewDecisionsClassifierFull(cc.Name, resolver, target, decisions, providers, questions, fallback, timeout, maxInputChars(cc), cc.OnlyIfUnset), nil
}

// maxInputChars reads a model-backed classifier's optional `max_input_chars`.
//
// The return is passed straight to the Full constructors, which own the
// default: 0 (unset) takes the safe built-in cap, and a negative value means
// unlimited explicitly. Config validation rejects the shapes this would
// otherwise silently swallow, so an unparseable value cannot read as "unset"
// and quietly get the default while the operator believes they set a cap.
func maxInputChars(cc config.ClassifierConfig) int {
	return intFromConfig(cc.Config, "max_input_chars")
}

// buildAliasResolver builds the resolver used by policy routers to resolve
// rule targets. Aliases are optional — an empty/nil map still yields a
// resolver (Has/Resolve simply report "not an alias" for everything), so
// policy routers that only use literal provider/model targets don't need one
// at all; the resolver is nonetheless always built and passed through so a
// nil isn't threaded separately.
func buildAliasResolver(aliasesCfg map[string]config.AliasConfig, providers map[string]types.ProviderConfig, catalog []types.ModelCost) *router.AliasResolver {
	aliases := make(map[string]router.Alias, len(aliasesCfg))
	for name, a := range aliasesCfg {
		alias := router.Alias{
			Name:        name,
			Force:       a.Force,
			RequestKind: a.RequestKind,
			Type:        a.Type,
			Provider:    a.Provider,
			Model:       a.Model,
			Select:      a.Select,
		}
		for _, m := range a.Members {
			alias.Members = append(alias.Members, router.AliasMember{Provider: m.Provider, Model: m.Model})
		}
		aliases[name] = alias
	}
	var lookup router.CostLatencyLookup
	if len(catalog) > 0 {
		lookup = router.NewStaticCatalog(catalog)
	}
	return router.NewAliasResolver(aliases, providers, nil, lookup)
}

// modelCostEntries converts the config's catalog rows into the type the
// router's lookup consumes.
func modelCostEntries(entries []config.ModelCatalogEntry) []types.ModelCost {
	out := make([]types.ModelCost, 0, len(entries))
	for _, e := range entries {
		out = append(out, types.ModelCost{
			Provider:          e.Provider,
			Model:             e.Model,
			InputCostPerMTok:  e.InputCostPerMTok,
			OutputCostPerMTok: e.OutputCostPerMTok,
			LatencyMsP50:      e.LatencyMsP50,
			InputModalities:   e.InputModalities,
			MaxInputTokens:    e.MaxInputTokens,
			MaxOutputTokens:   e.MaxOutputTokens,
			Metadata:          e.Metadata,
		})
	}
	return out
}
