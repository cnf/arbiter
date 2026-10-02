package main

import (
	"fmt"

	"github.com/cnf/arbiter/internal/config"
	"github.com/cnf/arbiter/internal/guardrail"
	"github.com/cnf/arbiter/internal/store"
)

// countSource converts a possibly-nil *store.Reader into a guardrail.CountSource.
//
// The conversion is explicit because a nil *store.Reader assigned to an interface
// produces a NON-nil interface holding a nil pointer — so the guardrail's own
// `counts == nil` check would pass and then panic on first use. Returning a true
// nil interface when there is no reader keeps the "no store configured" case
// behaving as the guardrail documents.
func countSource(r *store.Reader) guardrail.CountSource {
	if r == nil {
		return nil
	}
	return r
}

func buildGuardrail(gc config.GuardrailConfig, counts guardrail.CountSource) (guardrail.Guardrail, error) {
	switch gc.Type {
	case "system_prompt":
		prompt, _ := gc.Config["prompt"].(string)
		override, _ := gc.Config["override"].(bool)
		return guardrail.NewSystemPromptGuardrail(gc.Name, prompt, override), nil
	case "rate_limit":
		perMinute := intFromConfig(gc.Config, "per_minute")
		perDay := intFromConfig(gc.Config, "per_day")
		return guardrail.NewRateLimitGuardrail(gc.Name, perMinute, perDay, counts), nil
	case "prompt_rewrite":
		// Every value is read here and validated in the constructor, so a typo'd
		// mode or action is a config-load error rather than a guardrail that
		// silently rewrites nothing while the operator believes it strips.
		match, _ := gc.Config["match"].(string)
		mode, _ := gc.Config["mode"].(string)
		action, _ := gc.Config["action"].(string)
		replacement, _ := gc.Config["replacement"].(string)
		paragraphBoundary, _ := gc.Config["paragraph_boundary"].(string)
		blockStatus := intFromConfig(gc.Config, "block_status")
		where, err := stringList(gc.Config, "where")
		if err != nil {
			return nil, err
		}
		return guardrail.NewPromptRewriteGuardrail(
			gc.Name, match, mode, action, replacement, paragraphBoundary, blockStatus, where)
	case "unpin":
		// Clears the session-affinity pin when the request's LAST message
		// carries a marker, so a pinned conversation re-classifies and
		// re-routes. strip is optional and defaults to false: a guardrail
		// whose job is to unpin must not silently edit the prompt.
		match, _ := gc.Config["match"].(string)
		mode, _ := gc.Config["mode"].(string)
		strip, _ := gc.Config["strip"].(bool)
		return guardrail.NewUnpinGuardrail(gc.Name, match, mode, strip)
	default:
		return nil, fmt.Errorf("unknown guardrail type %q", gc.Type)
	}
}

// stringList pulls an optional list-of-strings out of a guardrail config block.
func stringList(cfg map[string]interface{}, key string) ([]string, error) {
	raw, ok := cfg[key]
	if !ok {
		return nil, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%s must be a list of strings", key)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a list of strings", key)
		}
		out = append(out, s)
	}
	return out, nil
}

// stringListMap pulls a map[string][]string out of a classifier config block,
// trying each of the given keys in turn (arbiter.yaml uses "keywords" for the
// domain classifier and "detectors" for the capability classifier — same shape,
// different name).
//
// An ABSENT map yields no keywords rather than an error, and that is
// deliberate: keywords stopped being mandatory when `match` and `detect`
// arrived. A classifier can be purely structural — a title-gen matcher needs no
// keyword list at all, and demanding one would force the operator to write a
// dummy that never fires. The caller decides whether a classifier with no
// signals of any kind is worth rejecting; see buildHeuristicClassifier.
func stringListMap(cfg map[string]interface{}, keys ...string) (map[string][]string, error) {
	var raw map[string]interface{}
	for _, k := range keys {
		if v, ok := cfg[k]; ok {
			// Present but not a map is a real mistake — a list, a scalar — and
			// must not be read as "absent".
			m, ok := v.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("%s must be a map of group -> keywords", k)
			}
			raw = m
			break
		}
	}
	if raw == nil {
		return nil, nil
	}

	out := make(map[string][]string, len(raw))
	for intent, v := range raw {
		list, ok := v.([]interface{})
		if !ok {
			continue
		}
		words := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				words = append(words, s)
			}
		}
		out[intent] = words
	}
	return out, nil
}

func intFromConfig(cfg map[string]interface{}, key string) int {
	v, ok := cfg[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}
