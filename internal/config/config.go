package config

import (
	"github.com/cnf/arbiter/pkg/types"
)

// Config holds the entire Arbiter configuration.
type Config struct {
	Version string `yaml:"version"`

	Providers map[string]ProviderConfig `yaml:"providers"`
	Classifiers []ClassifierConfig `yaml:"classifiers"`
	Routers []RouterConfig `yaml:"routers"`
	Guardrails GuardrailsConfig `yaml:"guardrails"`
	Logging LoggingConfig `yaml:"logging"`
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

// Validate checks that the config is well-formed.
func (c *Config) Validate() error {
	// TODO: implement validation
	return nil
}

// Load loads config from a YAML file.
func Load(path string) (*Config, error) {
	// TODO: implement YAML loading
	return &Config{}, nil
}
