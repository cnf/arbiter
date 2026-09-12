package guardrail

import (
	"context"
	"fmt"
	"sync"
	"time"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
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

// RateLimitGuardrail enforces simple per-minute/per-day request-count caps
// using an in-memory sliding counter. This is a single-process limiter —
// fine for Arbiter's one-binary-per-deployment model, but it resets on
// restart and doesn't share state across replicas. Good enough for v1;
// anything sturdier (Redis-backed, etc.) is a later guardrail, not a
// rewrite of this one, since ApplyPre swaps in independently.
type RateLimitGuardrail struct {
	name      string
	perMinute int
	perDay    int

	mu          sync.Mutex
	minuteStart time.Time
	minuteCount int
	dayStart    time.Time
	dayCount    int
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

// ApplyPre increments the request counters and rejects the request once
// either window's cap is exceeded. A cap of 0 means "unlimited" for that
// window, so config doesn't need a magic large number to opt out.
func (rlg *RateLimitGuardrail) ApplyPre(ctx context.Context, req *types.NormalizedRequest) (*types.NormalizedRequest, error) {
	rlg.mu.Lock()
	defer rlg.mu.Unlock()

	now := time.Now()
	if now.Sub(rlg.minuteStart) >= time.Minute {
		rlg.minuteStart = now
		rlg.minuteCount = 0
	}
	if now.Sub(rlg.dayStart) >= 24*time.Hour {
		rlg.dayStart = now
		rlg.dayCount = 0
	}

	if rlg.perMinute > 0 && rlg.minuteCount >= rlg.perMinute {
		msg := fmt.Sprintf("per-minute rate limit exceeded (%d/%d)", rlg.minuteCount, rlg.perMinute)
		return nil, arbitererrors.NewGuardrailError(msg, 429, nil)
	}
	if rlg.perDay > 0 && rlg.dayCount >= rlg.perDay {
		msg := fmt.Sprintf("per-day rate limit exceeded (%d/%d)", rlg.dayCount, rlg.perDay)
		return nil, arbitererrors.NewGuardrailError(msg, 429, nil)
	}

	rlg.minuteCount++
	rlg.dayCount++
	return req, nil
}

func (rlg *RateLimitGuardrail) ApplyPost(ctx context.Context, resp *types.NormalizedResponse, route types.Route) (*types.NormalizedResponse, error) {
	// No-op for post — counting happens on the way in, not the way out.
	return resp, nil
}

func (rlg *RateLimitGuardrail) ShouldRun(req *types.NormalizedRequest) bool {
	return true
}
