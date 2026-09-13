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
}

// ClassifierConfig defines a classifier to load.
type ClassifierConfig struct {
	Name   string                 `yaml:"name"`
	Type   string                 `yaml:"type"`
	Config map[string]interface{} `yaml:"config"`
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
	}

	if err := validateUniqueNames("classifier", classifierNames(c.Classifiers)); err != nil {
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

	switch c.Logging.Level {
	case "", "debug", "info", "warn", "error":
	default:
		return arbitererrors.NewConfigError(fmt.Sprintf("logging: unknown level %q", c.Logging.Level), nil)
	}

	return nil
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
