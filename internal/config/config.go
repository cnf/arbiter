// Package config loads and validates lanes.yaml, Arbiter's single
// configuration file. Provider API keys are written as ${ENV_VAR}
// placeholders in the YAML and substituted from the process environment at
// load time, so secrets never live in the config file itself.
package config

import (
	"bytes"
	"fmt"
	"os"
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
	Select  string              `yaml:"select,omitempty"` // "random" (P1)
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
		if p.Type != "anthropic" && p.Type != "openai" && p.Type != "ollama" {
			return arbitererrors.NewConfigError(fmt.Sprintf("provider %q: unknown type %q (want \"anthropic\", \"openai\", or \"ollama\")", name, p.Type), nil)
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

	if err := validateUniqueNames("classifier", classifierNames(c.Classifiers)); err != nil {
		return err
	}
	if err := validateClassifierAxes(c.Classifiers); err != nil {
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
// so declaring an axis on it is rejected rather than silently ignored.
func validateClassifierAxes(cs []ClassifierConfig) error {
	for _, c := range cs {
		if c.Axis == "" {
			continue
		}
		if c.Type == "capability_detector" {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: type \"capability_detector\" always fills capabilities and must not set axis", c.Name), nil)
		}
		if !canonicalAxisSet[c.Axis] {
			return arbitererrors.NewConfigError(fmt.Sprintf("classifier %q: unknown axis %q (want one of %v)", c.Name, c.Axis, types.KnownAxes), nil)
		}
	}
	return nil
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
			if a.Select != "" && a.Select != "random" {
				return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: unknown select %q (want \"random\"; other strategies arrive in a later phase)", name, a.Select), nil)
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

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
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
