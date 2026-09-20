// Package config loads and validates arbiter.yaml, Arbiter's single
// configuration file. Provider API keys are written as ${ENV_VAR}
// placeholders in the YAML and substituted from the process environment at
// load time, so secrets never live in the config file itself.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
	"gopkg.in/yaml.v3"
)

// Config holds the entire Arbiter configuration.
type Config struct {
	Version string `yaml:"version"`

	Providers   map[string]ProviderConfig `yaml:"providers"`
	Classifiers []ClassifierConfig        `yaml:"classifiers"`
	Routers     []RouterConfig            `yaml:"routers"`
	Guardrails  GuardrailsConfig          `yaml:"guardrails"`
	Routing     RoutingConfig             `yaml:"routing"`
	Logging     LoggingConfig             `yaml:"logging"`
	Aliases     map[string]AliasConfig    `yaml:"aliases,omitempty"`

	SessionAffinity SessionAffinityConfig `yaml:"session_affinity,omitempty"`

	Admin AdminConfig `yaml:"admin,omitempty"`

	Storage StorageConfig `yaml:"storage,omitempty"`

	// ModelCatalog supplies static cost/latency figures for group-alias
	// selection strategies (cheapest_input, cheapest_output, fastest). A
	// provider/model with no entry is "unknown cost" and ranks last rather
	// than erroring.
	ModelCatalog []ModelCatalogEntry `yaml:"model_catalog,omitempty"`

	// ModelCatalogFile names a second catalog file, usually a generated
	// artifact (see the catalog converter tool). It is read relative to the
	// config file's directory. Rows here are *defaults*: when both this file
	// and the inline ModelCatalog declare the same provider/model, the inline
	// row replaces the file's row entirely.
	//
	// Writes to this file are deliberately inert — the config watcher filters
	// on the config file's basename, so a regenerated catalog is only picked
	// up by an explicit reload.
	ModelCatalogFile string `yaml:"model_catalog_file,omitempty"`
}

// StorageConfig controls the sqlite event store — a persisted, queryable
// record of completed requests (Phase 3). An empty Path disables the store,
// which is the default so local dev and tests need no database file.
type StorageConfig struct {
	// Path is the sqlite database file. Empty means "no event store": every
	// request still runs, but nothing is persisted.
	Path string `yaml:"path,omitempty"`

	// CaptureContent stores prompt and response bodies, content-addressed and
	// deduplicated, alongside the request metadata. Off by default — this is
	// the one setting that writes conversation text to disk.
	CaptureContent bool `yaml:"capture_content,omitempty"`

	// ContentTTL is how long captured content is kept, as a Go duration
	// ("72h", "7d" is NOT valid — use "168h"). Empty or "0" means never
	// expire: retention is opt-in so an upgrade cannot silently begin deleting
	// data. Request metadata is not on this clock — it stays useful far longer
	// than conversation text does.
	ContentTTL string `yaml:"content_ttl,omitempty"`
}

// ModelCatalogEntry is one row of the static cost/latency catalog. Costs are
// US dollars per million tokens; latency is a p50 estimate in milliseconds.
//
// A row also carries what the model can *do*, not just what it costs, because
// clients gate on capability metadata: a client that sees none assumes the
// model is text-only and refuses to send an image at all. The information comes
// from the upstream price list (see cmd/catalog-convert), which carries far more
// than prices.
type ModelCatalogEntry struct {
	Provider          string  `yaml:"provider"`
	Model             string  `yaml:"model"`
	InputCostPerMTok  float64 `yaml:"input_cost_per_mtok"`
	OutputCostPerMTok float64 `yaml:"output_cost_per_mtok"`
	LatencyMsP50      int     `yaml:"latency_ms_p50,omitempty"`

	// InputModalities lists what the model accepts: "text", "image", "file".
	//
	// ABSENT MEANS UNKNOWN, and unknown is not the same as none. The upstream
	// list records vision support for under half its chat models, so treating
	// absence as "no" would be a confidently wrong answer for the majority —
	// and it is exactly the failure that motivated this field. A model known to
	// be text-only carries ["text"]; a model nothing is known about carries no
	// field at all.
	InputModalities []string `yaml:"input_modalities,omitempty"`

	// MaxInputTokens / MaxOutputTokens are the model's context and completion
	// limits, when the upstream states them. Pointers so "unstated" stays
	// distinguishable from a genuine zero.
	MaxInputTokens  *int `yaml:"max_input_tokens,omitempty"`
	MaxOutputTokens *int `yaml:"max_output_tokens,omitempty"`

	// Metadata is free-form, operator-supplied extra data. NOTHING READS IT:
	// it exists to be carried into the /models response, and that is the whole
	// contract. Anything with a consumer gets a typed field instead — an
	// untyped map that two packages branch on is the trap this codebase has
	// already hit once (the classifier's `labels:`), and a map that is only
	// ever forwarded cannot drift into that.
	Metadata map[string]interface{} `yaml:"metadata,omitempty"`
}

// SessionAffinityConfig controls how requests are pinned to whichever
// provider/model last served their conversation, so multi-turn
// conversations keep hitting the same upstream (preserving prompt-cache
// reuse) instead of re-routing every turn.
type SessionAffinityConfig struct {
	// Header names the inbound request header carrying a client-supplied
	// session identifier. Defaults to "X-Session-Id" if unset.
	Header string `yaml:"header,omitempty"`
	// DefaultTTL is how long a pin survives without being reused (idle
	// timeout, refreshed on every hit), parsed as a Go duration. Defaults to
	// 5m if unset.
	DefaultTTL string `yaml:"default_ttl,omitempty"`
}

// AdminConfig governs Arbiter's /admin/* surface. Arbiter deliberately
// implements no authentication of its own: access control is Caddy's job
// (forward_auth) and the network's (a tailnet, or a loopback/unix-socket
// bind), and this block only carries the one in-app affordance Arbiter
// needs to cooperate with that — a header-presence gate.
type AdminConfig struct {
	// ForwardAuthHeader, when set, requires every /admin/* request to carry
	// this header; a request without it is rejected 401. It is a *presence*
	// check only — Arbiter cannot verify that Caddy actually set the header,
	// so this is sound only while Arbiter is unreachable except through the
	// proxy that sets it (see the --bind/--socket flags). Unset means
	// /admin/* is ungated (a development convenience, not a safe default for
	// a listener reachable from anywhere).
	ForwardAuthHeader string `yaml:"forward_auth_header,omitempty"`
}

// RoutingConfig holds cross-cutting routing behavior that isn't the job of
// any single router: what to do when a routed provider fails.
type RoutingConfig struct {
	// FallbackProviders names providers to try — in order — when the routed
	// provider fails with a retriable error (429/5xx). Each is tried once;
	// providers in cooldown (from a recent 429's Retry-After) are skipped.
	FallbackProviders []string `yaml:"fallback_providers,omitempty"`
}

// ProviderConfig defines an upstream provider.
type ProviderConfig struct {
	Type     string            `yaml:"type"`
	Endpoint string            `yaml:"endpoint"`
	Key      string            `yaml:"key"`
	Models   []string          `yaml:"models"`
	Headers  map[string]string `yaml:"headers,omitempty"`
	Timeout  string            `yaml:"timeout,omitempty"`
	RetryMax int               `yaml:"retry_max,omitempty"`
	// CacheTTL overrides session_affinity.default_ttl for pins served by
	// this provider (e.g. to match its own prompt-cache expiry).
	CacheTTL string `yaml:"cache_ttl,omitempty"`
}

// ClassifierConfig defines a classifier to load.
type ClassifierConfig struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	// Axis names the types.Signals field this classifier instance fills
	// ("domain", "effort", "cost_class", "capabilities"). Empty defaults to
	// "domain" for type "heuristic" and "capabilities" for the legacy type
	// "capability_detector" — matching pre-axis behavior so existing configs
	// need no change.
	Axis   string                 `yaml:"axis,omitempty"`
	Config map[string]interface{} `yaml:"config"`
}

// AliasConfig defines a client-facing virtual model. Exactly one of Force
// (a force-alias) or Type (a pinned/group alias) should be set.
type AliasConfig struct {
	// Force overrides classification axes before rule matching, e.g.
	// { domain: ["code_generation"] }. Mutually exclusive with Type.
	Force map[string][]string `yaml:"force,omitempty"`

	Type     string `yaml:"type,omitempty"` // "pinned" | "group"
	Provider string `yaml:"provider,omitempty"`
	Model    string `yaml:"model,omitempty"`

	Members []AliasMemberConfig `yaml:"members,omitempty"`
	Select  string              `yaml:"select,omitempty"` // "random" | "ordered" | "cheapest_input" | "cheapest_output" | "fastest"
}

// AliasMemberConfig is one candidate within a group alias. Provider may
// itself name another alias, resolved recursively.
type AliasMemberConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model,omitempty"`
}

// RouterConfig defines a router to load.
type RouterConfig struct {
	Name   string                 `yaml:"name"`
	Type   string                 `yaml:"type"`
	Config map[string]interface{} `yaml:"config"`
}

// GuardrailsConfig defines pre/post guardrails.
type GuardrailsConfig struct {
	Pre  []GuardrailConfig `yaml:"pre"`
	Post []GuardrailConfig `yaml:"post"`
}

// GuardrailConfig defines a single guardrail.
type GuardrailConfig struct {
	Name   string                 `yaml:"name"`
	Type   string                 `yaml:"type"`
	Config map[string]interface{} `yaml:"config"`
}

// LoggingConfig defines logging behavior.
type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
	Output string `yaml:"output"`
}

// envVarPattern matches ${VAR_NAME} placeholders.
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv replaces ${VAR_NAME} placeholders with their environment
// values. A placeholder for an unset variable is left untouched (rather
// than silently becoming "") so a missing secret is easy to spot in a
// dumped config instead of quietly turning into an empty API key.
func expandEnv(raw []byte) []byte {
	return envVarPattern.ReplaceAllFunc(raw, func(match []byte) []byte {
		name := envVarPattern.FindSubmatch(match)[1]
		if val, ok := os.LookupEnv(string(name)); ok {
			return []byte(val)
		}
		return match
	})
}

// Validate checks that the config is well-formed: every named component
// (classifier/router/guardrail) has a registered type, every router's
// default/fallback providers exist, and every guardrail/classifier/router
// name is unique within its list (config wiring elsewhere looks components
// up by name).
func (c *Config) Validate() error {
	if len(c.Providers) == 0 {
		return arbitererrors.NewConfigError("no providers configured", nil)
	}
	for name, p := range c.Providers {
		if p.Type != "anthropic" && p.Type != "openai" && p.Type != "ollama" && p.Type != "decisions" {
			return arbitererrors.NewConfigError(fmt.Sprintf("provider %q: unknown type %q (want \"anthropic\", \"openai\", \"ollama\", or \"decisions\")", name, p.Type), nil)
		}
		if p.Endpoint == "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("provider %q: missing endpoint", name), nil)
		}
		if p.Timeout != "" {
			if _, err := time.ParseDuration(p.Timeout); err != nil {
				return arbitererrors.NewConfigError(fmt.Sprintf("provider %q: invalid timeout %q", name, p.Timeout), err)
			}
		}
		if p.CacheTTL != "" {
			if _, err := time.ParseDuration(p.CacheTTL); err != nil {
				return arbitererrors.NewConfigError(fmt.Sprintf("provider %q: invalid cache_ttl %q", name, p.CacheTTL), err)
			}
		}
	}

	if c.SessionAffinity.DefaultTTL != "" {
		if _, err := time.ParseDuration(c.SessionAffinity.DefaultTTL); err != nil {
			return arbitererrors.NewConfigError(fmt.Sprintf("session_affinity: invalid default_ttl %q", c.SessionAffinity.DefaultTTL), err)
		}
	}

	if c.Storage.ContentTTL != "" {
		if _, err := time.ParseDuration(c.Storage.ContentTTL); err != nil {
			return arbitererrors.NewConfigError(fmt.Sprintf("storage: invalid content_ttl %q (note \"7d\" is not a Go duration; use \"168h\")", c.Storage.ContentTTL), err)
		}
	}

	if err := validateUniqueNames("classifier", classifierNames(c.Classifiers)); err != nil {
		return err
	}
	if err := validateClassifierAxes(c.Classifiers); err != nil {
		return err
	}
	if err := c.validateLLMClassifiers(); err != nil {
		return err
	}
	if err := c.validateDecisionsClassifiers(); err != nil {
		return err
	}
	if err := validateClassifierMatch(c.Classifiers); err != nil {
		return err
	}
	if err := validateClassifierInputCap(c.Classifiers); err != nil {
		return err
	}
	if err := validateUniqueNames("router", routerNames(c.Routers)); err != nil {
		return err
	}
	if len(c.Routers) == 0 {
		return arbitererrors.NewConfigError("no routers configured", nil)
	}

	seenFallback := make(map[string]bool, len(c.Routing.FallbackProviders))
	for _, fb := range c.Routing.FallbackProviders {
		if _, ok := c.Providers[fb]; !ok {
			return arbitererrors.NewConfigError(fmt.Sprintf("routing: fallback provider %q is not configured", fb), nil)
		}
		if seenFallback[fb] {
			return arbitererrors.NewConfigError(fmt.Sprintf("routing: duplicate fallback provider %q", fb), nil)
		}
		seenFallback[fb] = true
	}

	allGuardrails := append(append([]GuardrailConfig{}, c.Guardrails.Pre...), c.Guardrails.Post...)
	if err := validateUniqueNames("guardrail", guardrailNames(allGuardrails)); err != nil {
		return err
	}

	if err := c.validateAliases(); err != nil {
		return err
	}

	if err := c.validateModelCatalog(); err != nil {
		return err
	}

	switch c.Logging.Level {
	case "", "debug", "info", "warn", "error":
	default:
		return arbitererrors.NewConfigError(fmt.Sprintf("logging: unknown level %q", c.Logging.Level), nil)
	}

	return nil
}

// knownAxisSet mirrors types.KnownAxes, plus the deprecated spellings that
// the config layer maps onto the canonical names — a force alias may use
// either during the deprecation window.
var knownAxisSet = func() map[string]bool {
	set := make(map[string]bool, len(types.KnownAxes)+2)
	for _, a := range types.KnownAxes {
		set[a] = true
	}
	set["intent"] = true           // deprecated spelling of "domain"
	set["cost_sensitivity"] = true // deprecated spelling of "cost_class"
	return set
}()

// canonicalAxisSet is the subset of knownAxisSet that a classifier's `axis`
// field may use. Deprecated spellings are accepted for force aliases and when
// a policy rule's `when` clause (hand-parsed with its own dual-key handling)
// but a classifier axis is new config, so it only accepts current names —
// otherwise a typo'd axis would silently default to filling Domain.
var canonicalAxisSet = func() map[string]bool {
	set := make(map[string]bool, len(types.KnownAxes))
	for _, a := range types.KnownAxes {
		set[a] = true
	}
	return set
}()

// validateClassifierAxes checks each classifier's declared axis is a current
// axis name. The legacy "capability_detector" type always fills capabilities,
// so declaring an axis on it is rejected rather than silently ignored. A
// "decisions" classifier fills the axis its question declares, so an axis on
// the classifier itself would be a second, silently-ignored answer to the same
// question — rejected for the same reason.
func validateClassifierAxes(cs []ClassifierConfig) error {
	for _, c := range cs {
		if c.Axis == "" {
			continue
		}
		if c.Type == "capability_detector" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"capability_detector\" always fills capabilities and must not set axis", c.Name), nil)
		}
		if c.Type == "decisions" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"decisions\" fills the axis its question declares and must not set axis", c.Name), nil)
		}
		if !canonicalAxisSet[c.Axis] {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: unknown axis %q (want one of %v)", c.Name, c.Axis, types.KnownAxes), nil)
		}
	}
	return nil
}

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
		if alias == "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"llm\" requires \"alias\"", cc.Name), nil)
		}
		if _, ok := c.Aliases[alias]; !ok {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: alias %q is not configured", cc.Name, alias), nil)
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
// "choice". Only that primitive is built, and accepting "score" or "noul" in
// config while the builder ignores them would be exactly the silent no-op this
// package exists to catch.
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
		if alias == "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"decisions\" requires \"alias\"", cc.Name), nil)
		}
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
			if qtype != types.DecisionChoice {
				return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: question %q has type %q, which is not supported yet (want %q)", cc.Name, qname, qtype, types.DecisionChoice), nil)
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
		kind, _ := cc.Config["kind"].(string)
		if value == "" && kind == "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: match requires \"value\" (the axis value a hit fills) or \"kind\" (the request kind a hit records)", cc.Name), nil)
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

// validateAliases checks the aliases block: names unique and disjoint from
// provider names (an unqualified lookup must be unambiguous), pinned/group
// members reference a configured provider and one of its declared models
// (unless the member instead names another alias, resolved recursively),
// force keys are known axis names, and the alias graph has no cycles.
func (c *Config) validateAliases() error {
	if len(c.Aliases) == 0 {
		return nil
	}

	// Since a declared model name takes routing precedence over an alias, an
	// alias sharing a model's name would be silently unreachable — reject it.
	declaredModels := make(map[string]string)
	for provider, pc := range c.Providers {
		for _, m := range pc.Models {
			declaredModels[m] = provider
		}
	}

	for name, a := range c.Aliases {
		if name == "" {
			return arbitererrors.NewConfigError("alias: entry missing name", nil)
		}
		if _, ok := c.Providers[name]; ok {
			return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: name collides with a configured provider", name), nil)
		}
		if provider, ok := declaredModels[name]; ok {
			return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: name collides with a model declared by provider %q (an explicit model name takes precedence, so this alias would be unreachable)", name, provider), nil)
		}

		switch {
		case a.Force != nil:
			// A declared force block, even an empty one (`force: {}`) — the
			// latter is the "full auto" alias: force nothing, let every axis
			// classify and the rules decide.
			if a.Type != "" {
				return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: force alias must not also set type", name), nil)
			}
			for axis := range a.Force {
				if !knownAxisSet[axis] {
					return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: force names unknown axis %q", name, axis), nil)
				}
			}

		case a.Type == "pinned":
			if err := c.validateAliasMember(name, AliasMemberConfig{Provider: a.Provider, Model: a.Model}); err != nil {
				return err
			}

		case a.Type == "group":
			if len(a.Members) == 0 {
				return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: group has no members", name), nil)
			}
			if !validSelect(a.Select) {
				return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: unknown select %q (want one of %s)", name, a.Select, strings.Join(selectStrategies, ", ")), nil)
			}
			for _, m := range a.Members {
				if err := c.validateAliasMember(name, m); err != nil {
					return err
				}
			}

		default:
			return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: must set force, or type \"pinned\"/\"group\"", name), nil)
		}
	}

	return c.detectAliasCycles()
}

// selectStrategies are the values a group alias's `select:` accepts. The
// cost/latency ones require catalog entries to be useful; without them they
// degrade to first-listed (see router.selectMember). "ordered" is the one
// strategy that needs no catalog at all: it takes the members exactly as
// written, so the declared list *is* the preference order and the remaining
// members become the degradation path.
var selectStrategies = []string{"random", "cheapest_input", "cheapest_output", "fastest", "ordered"}

// validSelect reports whether s is a known strategy. Empty means the default
// (random).
func validSelect(s string) bool {
	if s == "" {
		return true
	}
	for _, v := range selectStrategies {
		if s == v {
			return true
		}
	}
	return false
}

// validateModelCatalog checks catalog rows: the provider must be configured,
// and the model must be one of that provider's declared Models. Both are
// errors — a row that can never match is a config bug, and the lookup's
// unknown-row tolerance is for genuinely absent entries, not typos. By the
// time this runs, mergeModelCatalogFile has already dropped any
// model_catalog_file row that fails this same check (a generated catalog is
// expected to be a superset), so in practice this only ever rejects the
// hand-written inline model_catalog: block — which is exactly where a typo is
// worth catching.
func (c *Config) validateModelCatalog() error {
	seen := make(map[string]int, len(c.ModelCatalog))
	for i, e := range c.ModelCatalog {
		if e.Provider == "" || e.Model == "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("model_catalog[%d]: provider and model are required", i), nil)
		}
		p, ok := c.Providers[e.Provider]
		if !ok {
			return arbitererrors.NewConfigError(fmt.Sprintf("model_catalog[%d]: provider %q is not configured", i, e.Provider), nil)
		}
		if !slicesContain(p.Models, e.Model) {
			return arbitererrors.NewConfigError(fmt.Sprintf("model_catalog[%d]: model %q is not declared by provider %q", i, e.Model, e.Provider), nil)
		}
		key := e.Provider + "\x00" + e.Model
		if prev, dup := seen[key]; dup {
			return arbitererrors.NewConfigError(fmt.Sprintf("model_catalog[%d]: duplicate row for %s/%s (already at index %d)", i, e.Provider, e.Model, prev), nil)
		}
		seen[key] = i
	}
	return nil
}

// validateAliasMember checks one pinned/group member: its Provider must be
// a configured provider, and Model must be one of that provider's declared
// Models (the Member row may set Model to override the first declared model,
// which is useful when a single provider lists several models and only one
// is the alias's representative). Empty Model is accepted when the provider
// has no declared models.
//
// If the Member itself names another alias, the caller (detectAliasCycles)
// resolves it elsewhere; here the Provider is another alias name and we skip
// the Model check.
func (c *Config) validateAliasMember(aliasName string, m AliasMemberConfig) error {
	if m.Provider == "" {
		return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: member missing provider", aliasName), nil)
	}
	if _, isAlias := c.Aliases[m.Provider]; isAlias {
		return nil
	}
	pc, ok := c.Providers[m.Provider]
	if !ok {
		return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: member provider %q is not configured", aliasName, m.Provider), nil)
	}
	if m.Model != "" && len(pc.Models) > 0 && !slicesContain(pc.Models, m.Model) {
		return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: member model %q is not declared for provider %q", aliasName, m.Model, m.Provider), nil)
	}
	return nil
}

// detectAliasCycles runs a DFS over the alias graph (edges: pinned/group
// member -> alias it names) and errors on any cycle, rather than letting one
// slip through to the runtime's maxAliasDepth backstop.
func (c *Config) detectAliasCycles() error {
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := make(map[string]int, len(c.Aliases))

	var visit func(name string) error
	visit = func(name string) error {
		a, ok := c.Aliases[name]
		if !ok {
			return nil // not an alias (a real provider); nothing to follow
		}
		switch state[name] {
		case visiting:
			return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: cycle detected", name), nil)
		case done:
			return nil
		}
		state[name] = visiting

		var members []AliasMemberConfig
		switch a.Type {
		case "pinned":
			members = []AliasMemberConfig{{Provider: a.Provider, Model: a.Model}}
		case "group":
			members = a.Members
		}
		for _, m := range members {
			if err := visit(m.Provider); err != nil {
				return err
			}
		}

		state[name] = done
		return nil
	}

	for name := range c.Aliases {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func slicesContain(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func classifierNames(cs []ClassifierConfig) []string {
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = c.Name
	}
	return names
}

func routerNames(rs []RouterConfig) []string {
	names := make([]string, len(rs))
	for i, r := range rs {
		names[i] = r.Name
	}
	return names
}

func guardrailNames(gs []GuardrailConfig) []string {
	names := make([]string, len(gs))
	for i, g := range gs {
		names[i] = g.Name
	}
	return names
}

func validateUniqueNames(kind string, names []string) error {
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if n == "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("%s: entry missing name", kind), nil)
		}
		if seen[n] {
			return arbitererrors.NewConfigError(fmt.Sprintf("%s: duplicate name %q", kind, n), nil)
		}
		seen[n] = true
	}
	return nil
}

// Load reads and parses a YAML config file, expanding ${ENV_VAR}
// placeholders before parsing, and validates the result.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, arbitererrors.NewConfigError(fmt.Sprintf("reading %s", path), err)
	}

	expanded := expandEnv(raw)

	// Strict decoding: an unknown field (e.g. a typo like "api_key" instead
	// of "key") is a config error, not a silently-ignored no-op.
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, arbitererrors.NewConfigError(fmt.Sprintf("parsing %s", path), err)
	}

	cfg.normalizeEndpoints()

	// Merge the external catalog before validation so its rows get the same
	// provider/model checks as inline ones.
	if err := cfg.mergeModelCatalogFile(path); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// catalogFile is the on-disk shape of a model_catalog_file: the same key the
// inline block uses, so a generated catalog is a drop-in config fragment.
type catalogFile struct {
	ModelCatalog []ModelCatalogEntry `yaml:"model_catalog"`
}

// mergeModelCatalogFile loads ModelCatalogFile (if set), resolved relative to
// the directory of the config file at configPath, and prepends its rows to
// ModelCatalog. Merge is row-wise: the inline catalog wins, and an inline row
// for a provider/model present in the file replaces that file row entirely
// rather than overriding it field by field.
//
// Row-wise (rather than per-field) is deliberate: costs are plain float64,
// where 0 is a meaningful value (a free model), so a field-wise merge could
// not distinguish "unset" from "free" and would let a file row resurrect a
// cost the inline row meant to clear.
func (c *Config) mergeModelCatalogFile(configPath string) error {
	if c.ModelCatalogFile == "" {
		return nil
	}

	dir := filepath.Dir(configPath)
	catPath := c.ModelCatalogFile
	if !filepath.IsAbs(catPath) {
		catPath = filepath.Join(dir, catPath)
	}

	raw, err := os.ReadFile(catPath)
	if err != nil {
		return arbitererrors.NewConfigError(fmt.Sprintf("model_catalog_file: reading %s", catPath), err)
	}

	var cf catalogFile
	dec := yaml.NewDecoder(bytes.NewReader(expandEnv(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cf); err != nil {
		return arbitererrors.NewConfigError(fmt.Sprintf("model_catalog_file: parsing %s", catPath), err)
	}

	if len(cf.ModelCatalog) == 0 {
		return nil
	}

	inline := make(map[string]bool, len(c.ModelCatalog))
	for _, e := range c.ModelCatalog {
		inline[e.Provider+"\x00"+e.Model] = true
	}

	merged := make([]ModelCatalogEntry, 0, len(cf.ModelCatalog)+len(c.ModelCatalog))
	for _, e := range cf.ModelCatalog {
		if inline[e.Provider+"\x00"+e.Model] {
			continue // an inline row replaces this one wholesale
		}
		// A generated file (catalog-convert) is expected to be a superset of
		// what arbiter.yaml declares — it pulls every model under a
		// litellm_provider, not just the ones this config happens to list —
		// so a row naming an undeclared provider/model is not the config bug
		// an inline typo would be; it's simply unused, exactly like a missing
		// row (StaticCatalog.Lookup already treats "no entry" as unknown
		// cost, ranking it last). Drop it rather than failing config load;
		// validateModelCatalog below still applies its full strictness to the
		// inline block, where a typo is still worth catching.
		if p, ok := c.Providers[e.Provider]; !ok || !slicesContain(p.Models, e.Model) {
			continue
		}
		merged = append(merged, e)
	}

	c.ModelCatalog = append(merged, c.ModelCatalog...)
	return nil
}

// Epoch returns a short, stable identifier for this *resolved* configuration:
// a hash over the config as it exists after env expansion, catalog-file merge,
// and endpoint normalization. Two loads that produce the same effective
// config yield the same epoch, across reloads and across process restarts;
// any change to a behavior-bearing field yields a new one.
//
// It exists so recorded requests can be attributed to the configuration that
// produced them — "did this config change save or cost money?" is otherwise
// unanswerable, since a reload is not a commit and the resolved config never
// exists on disk in one piece. The hash covers the merged catalog rows, so a
// regenerated catalog file is itself a new epoch.
//
// Provider API keys are blanked before hashing: rotating a secret changes no
// behavior and must not split the data. Logging level is *not* blanked, so
// changing it does start a new epoch — a deliberate simplification (it is a
// config change), not an oversight.
//
// The value is a hex-encoded SHA-256 truncated to 16 chars — long enough that
// accidental collisions across a hand-edited config are not a concern, short
// enough to read in a log line or query filter.
func (c *Config) Epoch() string {
	redacted := *c
	redacted.Providers = make(map[string]ProviderConfig, len(c.Providers))
	for name, p := range c.Providers {
		p.Key = ""
		redacted.Providers[name] = p
	}

	// yaml.Marshal emits map keys in sorted order, so the same config always
	// produces the same bytes regardless of map iteration order.
	raw, err := yaml.Marshal(&redacted)
	if err != nil {
		// Marshaling a config that just decoded successfully cannot fail; if
		// it somehow does, an empty epoch is better than panicking on the
		// startup path.
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:16]
}

// normalizeEndpoints strips a trailing slash from each provider endpoint.
// Upstream requests always build URLs as Endpoint + "/v1/messages" or
// Endpoint + "/chat/completions"; a trailing slash left in config (e.g.
// "https://api.example.com/") would otherwise produce a double slash.
func (c *Config) normalizeEndpoints() {
	for name, p := range c.Providers {
		p.Endpoint = strings.TrimRight(p.Endpoint, "/")
		c.Providers[name] = p
	}
}
