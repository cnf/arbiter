package config

import (
	"fmt"
	"strings"
	"time"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// validateLLMClassifiers checks every "llm"-type classifier's alias, labels,
// escape label, instructions and fallback reference. This is the one place a
// classifier's otherwise-opaque `config:` map (cmd/arbiter's buildClassifier is
// what interprets it for every type) gets a load-time look from this package —
// worth the exception because a bad alias or a missing fallback would otherwise
// only surface as a silent runtime fallback (every classification call failing
// and falling through to its wrapped classifier, with no load-time signal
// that anything is wrong) rather than a config error, which is exactly the
// class of mistake this package exists to catch elsewhere.
func (c *Config) validateLLMClassifiers() error {
	classifierTypes := make(map[string]string, len(c.Classifiers)) // name -> type
	for _, cc := range c.Classifiers {
		classifierTypes[cc.Name] = cc.Type
	}

	for _, cc := range c.Classifiers {
		if cc.Type != "llm" {
			continue
		}
		alias, _ := cc.Config["alias"].(string)
		model, _ := cc.Config["model"].(string)
		if alias != "" && model != "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"llm\" must set exactly one of \"alias\" or \"model\", not both", cc.Name), nil)
		}
		if alias == "" && model == "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"llm\" requires exactly one of \"alias\" or \"model\"", cc.Name), nil)
		}
		if model != "" {
			// The model must be declared by some configured provider, or the
			// classifier's call can only fail at request time.
			if !c.modelDeclared(model) {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: model %q is not a declared model of any configured provider", cc.Name, model), nil)
			}
		}
		if alias != "" {
			if _, ok := c.Aliases[alias]; !ok {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: alias %q is not configured", cc.Name, alias), nil)
			}
			// The alias must name a pinned/group alias, never a force-alias:
			// a force alias selects no provider/model (it only shapes routing
			// axes), so routing the classification call through it would only
			// fail at request time. Same class of check validateDecisionsAlias
			// performs.
			if a, ok := c.Aliases[alias]; ok && a.Force != nil {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: alias %q is a force-alias and selects no provider/model — a classifier needs a pinned or group alias to route its calls through", cc.Name, alias), nil)
			}
		}
		// Parsed with the same function the builder uses, so a shape
		// validation accepts cannot be one construction drops. Labels may be
		// bare names or name -> rubric-description pairs.
		labels, err := types.ParseLabels(cc.Config["labels"])
		if err != nil {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: invalid labels: %v", cc.Name, err), nil)
		}
		if len(labels) == 0 {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"llm\" requires a non-empty \"labels\" list", cc.Name), nil)
		}
		seen := make(map[string]bool, len(labels))
		for _, l := range labels {
			if l.Name == "" {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: labels must not contain an empty name", cc.Name), nil)
			}
			key := strings.ToLower(l.Name)
			if seen[key] {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: label %q is declared more than once", cc.Name, l.Name), nil)
			}
			// "unmatched" is the reserved sentinel value an escape verdict
			// fills the axis with (see types.UnmatchedValue) — a real label
			// of that name would be indistinguishable in a `when:` rule from
			// "nothing matched".
			if key == types.UnmatchedValue {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: label %q is reserved — it is the sentinel value an escape verdict fills the axis with", cc.Name, l.Name), nil)
			}
			seen[key] = true
		}
		// An escape label must be one of the declared labels: it is the name
		// the model is told to reply with, so a name the model is never offered
		// could only ever be reached by coincidence.
		if escape, _ := cc.Config["escape"].(string); escape != "" {
			if !seen[strings.ToLower(escape)] {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: escape label %q is not one of the declared labels", cc.Name, escape), nil)
			}
		}
		if raw, ok := cc.Config["instructions"]; ok {
			if _, isStr := raw.(string); !isStr {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: instructions must be a string", cc.Name), nil)
			}
		}
		fallback, _ := cc.Config["fallback"].(string)
		if fallback == "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"llm\" requires \"fallback\"", cc.Name), nil)
		}
		fbType, ok := classifierTypes[fallback]
		if !ok {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: fallback %q is not a configured classifier", cc.Name, fallback), nil)
		}
		if fbType == "llm" || fbType == "decisions" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: fallback %q must not itself be a model-backed classifier (%q) — a failed classification must not become a second classification call", cc.Name, fallback, fbType), nil)
		}
		if raw, _ := cc.Config["timeout"].(string); raw != "" {
			if _, err := time.ParseDuration(raw); err != nil {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: invalid timeout %q", cc.Name, raw), err)
			}
		}
	}
	return nil
}

// validateDecisionsClassifiers checks every "decisions"-type classifier's
// alias, questions, labels, escape labels, instructions and fallback reference.
//
// Same exception as validateLLMClassifiers and for the same reason: a
// classifier's `config:` map is otherwise opaque to this package, but a bad
// alias or a missing fallback would only ever surface as a silent runtime
// fallback — every classification call failing and quietly deferring to the
// wrapped classifier, with no load-time signal that anything is wrong.
//
// The one rule that is genuinely decisions-specific: a question's type must be
// one of the three built primitives — accepting an unknown type in config
// while the builder silently ignores it would be exactly the silent no-op
// this package exists to catch.
func (c *Config) validateDecisionsClassifiers() error {
	classifierTypes := make(map[string]string, len(c.Classifiers)) // name -> type
	for _, cc := range c.Classifiers {
		classifierTypes[cc.Name] = cc.Type
	}

	for _, cc := range c.Classifiers {
		if cc.Type != "decisions" {
			continue
		}
		alias, _ := cc.Config["alias"].(string)
		model, _ := cc.Config["model"].(string)
		if alias != "" && model != "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"decisions\" must set exactly one of \"alias\" or \"model\", not both", cc.Name), nil)
		}
		if alias == "" && model == "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"decisions\" requires exactly one of \"alias\" or \"model\"", cc.Name), nil)
		}
		if model != "" {
			if !c.modelDeclared(model) {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: model %q is not a declared model of any configured provider", cc.Name, model), nil)
			}
			// A decisions classifier's call must go to a decisions-type
			// provider, whatever the target is — the endpoint is called
			// directly, not as a chat completion.
			if !c.modelOnDecisionsProvider(model) {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: model %q is not declared by a provider of type \"decisions\" — a decisions classifier needs a decisions provider (its endpoint is called directly, not as a chat completion)", cc.Name, model), nil)
			}
		}
		if alias != "" {
			if _, ok := c.Aliases[alias]; !ok {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: alias %q is not configured", cc.Name, alias), nil)
			}
			// The alias must resolve to a provider that speaks the decisions
			// protocol. A pinned alias naming an ordinary chat provider would
			// otherwise send a `state`/`questions` body to /chat/completions and
			// fail at request time with a translation error, which is a far worse
			// place to learn it than config load.
			if err := c.validateDecisionsAlias(cc.Name, alias); err != nil {
				return err
			}
		}

		raw, ok := cc.Config["questions"]
		if !ok {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"decisions\" requires \"questions\"", cc.Name), nil)
		}
		questions, ok := raw.(map[string]interface{})
		if !ok {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: \"questions\" must be a map of name -> question", cc.Name), nil)
		}
		if len(questions) == 0 {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"decisions\" requires at least one question", cc.Name), nil)
		}

		// Every question is asked in one call, so two questions filling the
		// same axis would race for it with nothing to break the tie — the
		// verdict would depend on map iteration order.
		axesSeen := make(map[string]string, len(questions))
		for qname, rawQ := range questions {
			q, ok := rawQ.(map[string]interface{})
			if !ok {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q must be a map", cc.Name, qname), nil)
			}
			qtype, _ := q["type"].(string)
			if qtype == "" {
				qtype = types.DecisionChoice
			}
			if qtype != types.DecisionChoice && qtype != types.DecisionNoul && qtype != types.DecisionScore {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q has type %q, which is not a known primitive (want %q, %q or %q)", cc.Name, qname, qtype, types.DecisionChoice, types.DecisionNoul, types.DecisionScore), nil)
			}
			axis, _ := q["axis"].(string)
			if axis == "" {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q requires \"axis\"", cc.Name, qname), nil)
			}
			if !canonicalAxisSet[axis] {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q has unknown axis %q (want one of %v)", cc.Name, qname, axis, types.KnownAxes), nil)
			}
			if other, taken := axesSeen[axis]; taken {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: questions %q and %q both fill axis %q — they are asked in one call, so neither can win", cc.Name, other, qname, axis), nil)
			}
			axesSeen[axis] = qname

			if qtype == types.DecisionNoul {
				if err := validateNoulQuestion(cc.Name, qname, q); err != nil {
					return err
				}
				continue
			}

			if qtype == types.DecisionScore {
				if err := validateScoreQuestion(cc.Name, qname, q); err != nil {
					return err
				}
				continue
			}

			// Parsed with the same function the builder uses, so a shape
			// validation accepts cannot be one construction drops.
			labels, err := types.ParseLabels(q["labels"])
			if err != nil {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: invalid labels: %v", cc.Name, qname, err), nil)
			}
			if len(labels) == 0 {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q requires a non-empty \"labels\" list", cc.Name, qname), nil)
			}
			seen := make(map[string]bool, len(labels))
			for _, l := range labels {
				if l.Name == "" {
					return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: labels must not contain an empty name", cc.Name, qname), nil)
				}
				key := strings.ToLower(l.Name)
				if seen[key] {
					return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: label %q is declared more than once", cc.Name, qname, l.Name), nil)
				}
				seen[key] = true
			}

			// "other" is the name this codebase sends for the escape option, so
			// a label of that name would collide with it: the criteria map is
			// keyed by name, and the second write would win silently.
			if seen["other"] {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: label \"other\" is reserved (it is the option name sent for the escape label)", cc.Name, qname), nil)
			}

			// "unmatched" is the reserved sentinel value an escape verdict
			// fills the axis with (see types.UnmatchedValue) — a real label
			// of that name would be indistinguishable in a `when:` rule from
			// "nothing matched".
			if seen[types.UnmatchedValue] {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: label \"unmatched\" is reserved — it is the sentinel value an escape verdict fills the axis with", cc.Name, qname), nil)
			}

			// An escape label must be one of the declared labels: it is the
			// option the model is offered, so a name never sent could only be
			// reached by coincidence.
			if escape, _ := q["escape"].(string); escape != "" {
				if !seen[strings.ToLower(escape)] {
					return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: escape label %q is not one of the declared labels", cc.Name, qname, escape), nil)
				}
			}

			if raw, ok := q["instructions"]; ok {
				if _, isStr := raw.(string); !isStr {
					return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: instructions must be a string", cc.Name, qname), nil)
				}
			}
		}

		fallback, _ := cc.Config["fallback"].(string)
		if fallback == "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"decisions\" requires \"fallback\"", cc.Name), nil)
		}
		fbType, ok := classifierTypes[fallback]
		if !ok {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: fallback %q is not a configured classifier", cc.Name, fallback), nil)
		}
		// A decisions classifier MAY fall back to an llm classifier, unlike an
		// llm classifier (see validateLLMClassifiers). The rule there exists to
		// bound chains; decisions -> llm is depth 1 and terminates, because the
		// llm classifier's own fallback must still be a non-model classifier.
		// It is also the useful direction: a chat model asked the same question
		// is exactly the escalation a failed decision call wants.
		if fbType == "decisions" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: fallback %q must not itself be type \"decisions\" (no chained decision calls)", cc.Name, fallback), nil)
		}
		if raw, _ := cc.Config["timeout"].(string); raw != "" {
			if _, err := time.ParseDuration(raw); err != nil {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: invalid timeout %q", cc.Name, raw), err)
			}
		}
	}
	return nil
}

// validateNoulQuestion checks a "noul" question's shape: it requires "value"
// (the axis value applied on yes) and rejects "labels"/"escape", which belong
// to "choice" and would otherwise be silently ignored by the builder.
//
// "noul" is deliberately not accepted on the capabilities axis yet: that axis
// is an additive set (RequiredCapabilities), and a single yes/no value does
// not yet have a defined meaning there — see fillAxis's capabilities branch,
// which expects a slice. Extending noul to gate a single capability is future
// work, not a silent default.
func validateNoulQuestion(classifierName, qname string, q map[string]interface{}) error {
	axis, _ := q["axis"].(string)
	if axis == types.AxisCapabilitiesName {
		return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: type \"noul\" does not support the capabilities axis yet", classifierName, qname), nil)
	}
	if _, ok := q["labels"]; ok {
		return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: \"labels\" is not valid for type \"noul\" (use \"value\")", classifierName, qname), nil)
	}
	if _, ok := q["escape"]; ok {
		return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: \"escape\" is not valid for type \"noul\"", classifierName, qname), nil)
	}
	value, _ := q["value"].(string)
	if value == "" {
		return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q requires a non-empty \"value\" (the axis value applied on yes)", classifierName, qname), nil)
	}
	if value == types.UnmatchedValue {
		return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: value \"unmatched\" is reserved — it is the sentinel a choice's escape verdict fills the axis with", classifierName, qname), nil)
	}
	if raw, ok := q["instructions"]; ok {
		if _, isStr := raw.(string); !isStr {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: instructions must be a string", classifierName, qname), nil)
		}
	}
	return nil
}

// validateScoreQuestion checks a "score" question's shape: it requires
// "levels" (the ordered rubric the answer's fractional position snaps onto)
// and rejects "labels"/"escape"/"value", which belong to "choice"/"noul" and
// would otherwise be silently ignored by the builder.
//
// At least two levels are required — a single-level score could never
// disagree with itself, which is not a rubric, it is a constant.
func validateScoreQuestion(classifierName, qname string, q map[string]interface{}) error {
	if _, ok := q["labels"]; ok {
		return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: \"labels\" is not valid for type \"score\" (use \"levels\")", classifierName, qname), nil)
	}
	if _, ok := q["escape"]; ok {
		return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: \"escape\" is not valid for type \"score\"", classifierName, qname), nil)
	}
	if _, ok := q["value"]; ok {
		return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: \"value\" is not valid for type \"score\" (use \"levels\")", classifierName, qname), nil)
	}
	levels, err := types.ParseScoreLevels(q["levels"])
	if err != nil {
		return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: invalid levels: %v", classifierName, qname, err), nil)
	}
	if len(levels) < 2 {
		return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q requires at least two \"levels\" (a single level cannot disagree with itself)", classifierName, qname), nil)
	}
	seen := make(map[string]bool, len(levels))
	for _, l := range levels {
		if l.Name == "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: levels must not contain an empty name", classifierName, qname), nil)
		}
		key := strings.ToLower(l.Name)
		if seen[key] {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: level %q is declared more than once", classifierName, qname, l.Name), nil)
		}
		// "unmatched" is the reserved sentinel value an escape verdict fills
		// the axis with (see types.UnmatchedValue) — a real level of that
		// name would be indistinguishable in a `when:` rule from "nothing
		// matched", which a score (unlike a choice) can never actually emit.
		if key == types.UnmatchedValue {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: level %q is reserved — it is the sentinel value an escape verdict fills the axis with", classifierName, qname, l.Name), nil)
		}
		seen[key] = true
	}
	if raw, ok := q["instructions"]; ok {
		if _, isStr := raw.(string); !isStr {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q: instructions must be a string", classifierName, qname), nil)
		}
	}
	return nil
}

// modelDeclared reports whether model is a declared model of some configured
// provider. Used to reject a classifier whose `model:` target could only fail
// at request time (no provider offers it).
func (c *Config) modelDeclared(model string) bool {
	for _, pc := range c.Providers {
		for _, m := range pc.Models {
			if m == model {
				return true
			}
		}
	}
	return false
}

// modelOnDecisionsProvider reports whether model is declared by a provider of
// type "decisions". A decisions classifier's call goes to the decision
// endpoint directly, not as a chat completion, so its target must be one.
func (c *Config) modelOnDecisionsProvider(model string) bool {
	for _, pc := range c.Providers {
		if pc.Type != "decisions" {
			continue
		}
		for _, m := range pc.Models {
			if m == model {
				return true
			}
		}
	}
	return false
}

// validateDecisionsAlias resolves a decisions classifier's alias and requires
// every target it can reach to be a provider of type "decisions" — including a
// group alias's members and any alias they resolve through.
func (c *Config) validateDecisionsAlias(classifier, alias string) error {
	seen := make(map[string]bool)
	var walk func(name string) error
	walk = func(name string) error {
		if seen[name] {
			// Cycles are rejected by validateAliases; stopping here keeps this
			// walk from looping while that error is reported.
			return nil
		}
		seen[name] = true

		a, ok := c.Aliases[name]
		if !ok {
			return nil
		}
		if a.Type == "pinned" && a.Provider != "" {
			// A member may itself name another alias, resolved recursively.
			if _, isAlias := c.Aliases[a.Provider]; isAlias {
				return walk(a.Provider)
			}
			if pc, ok := c.Providers[a.Provider]; ok && pc.Type != "decisions" {
				return arbitererrors.NewConfigError(fmt.Sprintf(
					"classifier %q: alias %q resolves to provider %q of type %q — a decisions classifier needs a provider of type \"decisions\" (its endpoint is called directly, not as a chat completion)",
					classifier, name, a.Provider, pc.Type), nil)
			}
		}
		for _, m := range a.Members {
			if err := walk(m.Provider); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(alias)
}

// validateClassifierMatch checks every classifier's optional `match:` and
// `detect:` blocks.
//
// `match` and `detect` are interpreted by cmd/arbiter's buildClassifier, which
// this package cannot reach, so a shape it would drop has to be caught here.
// The failure mode is the one this file exists to prevent: a `match` block the
// builder ignores looks exactly like a `match` block that never hits, and the
// operator concludes the signature is wrong rather than the config.
func validateClassifierMatch(cs []ClassifierConfig) error {
	for _, cc := range cs {
		// `detect` and `long_context_tokens` are only meaningful on the
		// capabilities axis. On any other axis the structural hit would be
		// discarded by fillAxis.
		if raw, ok := cc.Config["detect"]; ok {
			if cc.Type != "capability_detector" && cc.Axis != types.AxisCapabilitiesName {
				return arbitererrors.NewConfigError(fmt.Sprintf(
					"classifier %q: \"detect\" only applies to the capabilities axis (this one fills %q; use type \"capability_detector\" or axis: %q)",
					cc.Name, cc.Axis, types.AxisCapabilitiesName), nil)
			}
			names, err := matchStringList(raw, "detect")
			if err != nil {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: %v", cc.Name, err), nil)
			}
			if len(names) == 0 {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: \"detect\" must name at least one capability (want one of %v)", cc.Name, types.KnownCapabilities), nil)
			}
			for _, name := range names {
				if !containsString(types.KnownCapabilities, name) {
					return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: unknown capability %q in detect (want one of %v)", cc.Name, name, types.KnownCapabilities), nil)
				}
				// long_context is the one capability that needs a threshold:
				// without one it could never fire, and "never fires" is
				// indistinguishable from "the request was short".
				if name == types.CapLongContext {
					if n, _ := cc.Config["long_context_tokens"].(int); n <= 0 {
						return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: detect \"long_context\" requires \"long_context_tokens\" (a positive token threshold); without one it can never match", cc.Name), nil)
					}
				}
			}
		}

		raw, ok := cc.Config["match"]
		if !ok {
			continue
		}
		patterns, err := types.ParseMatchPatterns(raw)
		if err != nil {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: match: %v", cc.Name, err), nil)
		}
		if len(patterns) == 0 {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: match needs at least one pattern", cc.Name), nil)
		}
		value, _ := cc.Config["value"].(string)
		kind, _ := cc.Config["request_kind"].(string)
		if value == "" && kind == "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: match requires \"value\" (the axis value a hit fills) or \"request_kind\" (the request kind a hit records); the previous spelling \"kind\" is no longer accepted", cc.Name), nil)
		}
		if raw, ok := cc.Config["where"]; ok {
			where, err := matchStringList(raw, "where")
			if err != nil {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: %v", cc.Name, err), nil)
			}
			if _, err := types.ParseMatchTargets(where); err != nil {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: %v", cc.Name, err), nil)
			}
		}
		if raw, ok := cc.Config["decisive"]; ok {
			if _, isBool := raw.(bool); !isBool {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: decisive must be a boolean", cc.Name), nil)
			}
		}
		// A decisive matcher ends classification for everything after it, so
		// it must not be able to end it for a request it does not match.
		// Compiling each pattern is the only way to know that here.
		for _, p := range patterns {
			if _, err := types.NewTextMatcher(p.Pattern, types.MatchMode(p.Mode)); err != nil {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: match pattern %q: %v", cc.Name, p.Pattern, err), nil)
			}
		}
	}
	return nil
}

// validateClassifierInputCap checks a model-backed classifier's optional
// `max_input_chars`.
//
// The builder reads it with intFromConfig, which returns 0 for any shape it
// does not recognise — and 0 means "unset, take the default". So a value that
// is a string, a float or a map would silently become the default cap while the
// operator believes they set one. Caught here because cmd/arbiter cannot report
// it: by the time the builder runs, "unset" and "unreadable" are the same 0.
func validateClassifierInputCap(cs []ClassifierConfig) error {
	for _, cc := range cs {
		raw, ok := cc.Config["max_input_chars"]
		if !ok {
			continue
		}
		n, isInt := raw.(int)
		if !isInt {
			return arbitererrors.NewConfigError(fmt.Sprintf(
				"classifier %q: max_input_chars must be a whole number of characters (got %T); 0 is rejected too — omit the field for the default, or use a negative value to mean unlimited",
				cc.Name, raw), nil)
		}
		if n == 0 {
			// 0 would read as "unset" and take the default, so an operator
			// writing it expects either no limit or no input at all — both
			// different from what they would get.
			return arbitererrors.NewConfigError(fmt.Sprintf(
				"classifier %q: max_input_chars of 0 is ambiguous — omit the field for the default, or use a negative value for unlimited",
				cc.Name), nil)
		}
	}
	return nil
}

// matchStringList reads a config value that may be a YAML list or a single
// string, for the shapes this file validates on its own rather than through a
// types parser.
func matchStringList(raw interface{}, field string) ([]string, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case string:
		return []string{v}, nil
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s entries must be strings, got %T", field, item)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be a string or a list of strings, got %T", field, raw)
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
