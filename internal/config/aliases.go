package config

import (
	"fmt"
	"strings"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
)

// validateAliases checks the aliases block: names unique and disjoint from
// provider names (an unqualified lookup must be unambiguous), pinned/group
// members reference a configured provider and one of its declared models
// (unless the member instead names another alias, resolved recursively),
// force keys are known axis names, and the alias graph has no cycles.
// request_kind is validated as non-empty only: kinds are an open vocabulary
// (see types.Signals.RequestKind — freeform string by design), and "title"
// vs "subagent" is deployment policy, not schema.
//
// The three shapes are EXCLUSIVE — an alias is either a destination (pinned/
// group) or a metadata declaration (force + optional request_kind), never
// both. A request_kind on a destination alias is rejected: the alias's job
// is to say WHERE the request goes, and stamping what the request IS is the
// force alias's job. (Technically stamping on the pinned/group path would
// work — the plumbing is shared — but it mixes the two concerns in one
// block, and the operator's config should not do in two lines what the shape
// split exists to keep apart.)
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
		if strings.TrimSpace(a.RequestKind) == "" && a.RequestKind != "" {
			return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: request_kind must not be empty or whitespace — omit the field if the alias says nothing about the request's kind", name), nil)
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
			if a.RequestKind != "" {
				return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: request_kind is a metadata declaration and belongs on a force alias (see docs/routing.md §\"Declaring what an alias's traffic IS\"); a pinned alias only says where the request goes, so it must not also say what the request is", name), nil)
			}
			if err := c.validateAliasMember(name, AliasMemberConfig{Provider: a.Provider, Model: a.Model}); err != nil {
				return err
			}

		case a.Type == "group":
			if a.RequestKind != "" {
				return arbitererrors.NewConfigError(fmt.Sprintf("alias %q: request_kind is a metadata declaration and belongs on a force alias (see docs/routing.md §\"Declaring what an alias's traffic IS\"); a group alias only says where the request goes, so it must not also say what the request is", name), nil)
			}
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
