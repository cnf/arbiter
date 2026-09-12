package guardrail

import (
	"context"

	"github.com/cnf/arbiter/pkg/types"
)

// Guardrail is a pre/post-request hook that enforces policies.
type Guardrail interface {
	Name() string
	ApplyPre(ctx context.Context, req *types.NormalizedRequest) (*types.NormalizedRequest, error)
	ApplyPost(ctx context.Context, resp *types.NormalizedResponse, route types.Route) (*types.NormalizedResponse, error)
	ShouldRun(req *types.NormalizedRequest) bool
}

// Factory creates a Guardrail from config.
type Factory func(name string, config map[string]interface{}) (Guardrail, error)

// Registry holds all registered guardrail factories.
var Registry = make(map[string]Factory)

// Register registers a guardrail factory by type name.
func Register(typeName string, factory Factory) {
	Registry[typeName] = factory
}

// SystemPromptGuardrail injects or overrides system prompts.
type SystemPromptGuardrail struct {
	name     string
	prompt   string
	override bool
}

// NewSystemPromptGuardrail creates a system prompt guardrail.
func NewSystemPromptGuardrail(name, prompt string, override bool) *SystemPromptGuardrail {
	return &SystemPromptGuardrail{
		name:     name,
		prompt:   prompt,
		override: override,
	}
}

func (spg *SystemPromptGuardrail) Name() string {
	return spg.name
}

func (spg *SystemPromptGuardrail) ApplyPre(ctx context.Context, req *types.NormalizedRequest) (*types.NormalizedRequest, error) {
	// TODO: implement system prompt injection
	return req, nil
}

func (spg *SystemPromptGuardrail) ApplyPost(ctx context.Context, resp *types.NormalizedResponse, route types.Route) (*types.NormalizedResponse, error) {
	// No-op for post
	return resp, nil
}

func (spg *SystemPromptGuardrail) ShouldRun(req *types.NormalizedRequest) bool {
	return true
}

// RateLimitGuardrail enforces rate limits.
type RateLimitGuardrail struct {
	name        string
	perMinute   int
	perDay      int
}

// NewRateLimitGuardrail creates a rate limit guardrail.
func NewRateLimitGuardrail(name string, perMinute, perDay int) *RateLimitGuardrail {
	return &RateLimitGuardrail{
		name:      name,
		perMinute: perMinute,
		perDay:    perDay,
	}
}

func (rlg *RateLimitGuardrail) Name() string {
	return rlg.name
}

func (rlg *RateLimitGuardrail) ApplyPre(ctx context.Context, req *types.NormalizedRequest) (*types.NormalizedRequest, error) {
	// TODO: implement rate limit check
	return req, nil
}

func (rlg *RateLimitGuardrail) ApplyPost(ctx context.Context, resp *types.NormalizedResponse, route types.Route) (*types.NormalizedResponse, error) {
	// No-op for post
	return resp, nil
}

func (rlg *RateLimitGuardrail) ShouldRun(req *types.NormalizedRequest) bool {
	return true
}
