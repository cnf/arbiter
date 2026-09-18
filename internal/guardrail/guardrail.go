package guardrail

import (
	"context"

	"github.com/cnf/arbiter/pkg/types"
)

// Guardrail is a pre/post-request hook that enforces policies. Pre-hooks
// run on the NormalizedRequest before routing/upstream; post-hooks run on
// the NormalizedResponse after the upstream call. Everything from prompt
// injection to rate limiting to (eventually) memory retrieval is a
// guardrail — there's no special-cased path for any of it.
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

// SystemPromptGuardrail injects or overrides system prompts. With
// override=false (the common case) it prepends the configured prompt ahead
// of whatever the client sent, so Arbiter's baseline personality/instructions
// always apply but client-supplied instructions aren't silently discarded.
// With override=true the client's system prompt is replaced entirely.
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
	if spg.override || req.SystemPrompt == "" {
		req.SystemPrompt = spg.prompt
	} else {
		req.SystemPrompt = spg.prompt + "\n\n" + req.SystemPrompt
	}
	return req, nil
}

func (spg *SystemPromptGuardrail) ApplyPost(ctx context.Context, resp *types.NormalizedResponse, route types.Route) (*types.NormalizedResponse, error) {
	// No-op for post — this guardrail only shapes outbound requests.
	return resp, nil
}

func (spg *SystemPromptGuardrail) ShouldRun(req *types.NormalizedRequest) bool {
	return true
}
