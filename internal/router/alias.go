package router

import (
	"fmt"
	"math/rand"

	"github.com/cnf/arbiter/pkg/types"
)

// maxAliasDepth bounds alias→alias resolution. Aliases may name other
// aliases (REQUIREMENTS §1: "resolvable recursively/by reference from
// anywhere a provider/model target is expected today"), but a cycle must
// never hang a request — config-load validation rejects cycles outright, and
// this is the runtime backstop if one somehow slips through.
const maxAliasDepth = 4

// Alias is a client-facing virtual model. Three shapes, distinguished by
// which fields are set:
//
//   - Force alias: Force is non-empty. It overrides the named axes before
//     rule matching and picks no model of its own — the rules then decide
//     where the request lands.
//   - Pinned alias: Type == "pinned". Names one concrete provider/model.
//   - Group alias: Type == "group". Names an ordered set of candidate
//     members to route within; the first is the primary and the rest are its
//     ordered fallback chain.
type Alias struct {
	Name string
	// Force overrides axes before rule matching. Values are lists because
	// some axes are list-valued (capabilities) — a scalar-valued axis just
	// gets a one-element list. Keys are canonical axis names (see
	// types.KnownAxes); the config layer maps deprecated spellings onto them.
	Force map[string][]string

	Type     string // "pinned" | "group"; empty when Force is set
	Provider string // type == "pinned"
	Model    string // type == "pinned"

	Members []AliasMember // type == "group"
	Select  string        // "random" (P1); "cheapest_input"/"fastest" in P2
}

// AliasMember is one candidate within a group alias. Provider may itself be
// another alias name, which is resolved recursively.
type AliasMember struct {
	Provider string
	Model    string
}

// AliasResolver resolves alias names against the configured alias set and
// provider table. It is the single place that turns a virtual model name
// into a concrete provider/model.
type AliasResolver struct {
	aliases   map[string]Alias
	providers map[string]types.ProviderConfig
	// pick chooses a member from a group. Injected so the strategy can
	// change (random now; cost- or latency-aware in Phase 2) without
	// touching resolution.
	pick func([]AliasMember) AliasMember
}

// NewAliasResolver builds a resolver. A nil pick defaults to random
// selection, which is the only strategy Phase 1 defines.
func NewAliasResolver(aliases map[string]Alias, providers map[string]types.ProviderConfig, pick func([]AliasMember) AliasMember) *AliasResolver {
	if pick == nil {
		pick = randomPick
	}
	return &AliasResolver{aliases: aliases, providers: providers, pick: pick}
}

// Has reports whether name is a configured alias.
func (r *AliasResolver) Has(name string) bool {
	_, ok := r.aliases[name]
	return ok
}

// Force returns the axis overrides for a force-style alias, and whether name
// is one. A force alias always reports ok=true (its Force map may be empty,
// meaning "force nothing" — the full-auto alias); a pinned/group alias or an
// unknown name reports ok=false.
func (r *AliasResolver) Force(name string) (map[string][]string, bool) {
	a, ok := r.aliases[name]
	if !ok || a.Force == nil {
		return nil, false
	}
	return a.Force, true
}

// Resolve expands an alias name into a concrete provider/model. ok is false
// when name isn't a configured alias — the caller then treats it as a
// literal provider/model reference or errors. Alias→alias references are
// followed up to maxAliasDepth; exceeding it is an error, since it means a
// cycle that config validation should have caught.
func (r *AliasResolver) Resolve(name string) (provider, model string, ok bool, err error) {
	provider, model, ok, err = r.resolve(name, 0)
	return provider, model, ok, err
}

func (r *AliasResolver) resolve(name string, depth int) (string, string, bool, error) {
	a, ok := r.aliases[name]
	if !ok {
		return "", "", false, nil
	}
	if depth > maxAliasDepth {
		return "", "", true, fmt.Errorf("alias %q: resolution exceeded %d levels (cycle?)", name, maxAliasDepth)
	}

	switch {
	case a.Force != nil:
		// A force alias names no model — it only shapes the axes that rule
		// matching then runs on. Reaching here means something targeted it as
		// a concrete model, which is a config error rather than something to
		// guess at (a client naming it simply proceeds to classify+rules).
		return "", "", true, fmt.Errorf("alias %q is a force-alias and selects no provider/model; rules must target a pinned or group alias", name)

	case a.Type == "pinned":
		if _, ok := r.providers[a.Provider]; !ok {
			return "", "", true, fmt.Errorf("alias %q: provider %q is not configured", name, a.Provider)
		}
		return a.Provider, a.Model, true, nil

	case a.Type == "group":
		if len(a.Members) == 0 {
			return "", "", true, fmt.Errorf("alias %q: group has no members", name)
		}
		member := r.pick(a.Members)
		// A member's Provider may itself be an alias name.
		innerProvider, innerModel, isAlias, err := r.resolve(member.Provider, depth+1)
		if err != nil {
			return "", "", true, err
		}
		if !isAlias {
			if _, ok := r.providers[member.Provider]; !ok {
				return "", "", true, fmt.Errorf("alias %q: member provider %q is not configured", name, member.Provider)
			}
			return member.Provider, member.Model, true, nil
		}
		// Member pointed at another alias: prefer the alias's model, but a
		// member that names its own model overrides it.
		model := innerModel
		if member.Model != "" {
			model = member.Model
		}
		return innerProvider, model, true, nil

	default:
		return "", "", true, fmt.Errorf("alias %q: unknown type %q (want \"pinned\", \"group\", or a force block)", name, a.Type)
	}
}

// GroupFallbacks returns the non-selected members of a group alias as
// candidate routes, in declared order, so the selected member's siblings
// form its fallback chain. A non-group or unknown name yields nothing — the
// caller falls back to the global provider list.
func (r *AliasResolver) GroupFallbacks(name string, selected AliasMember) []types.Route {
	a, ok := r.aliases[name]
	if !ok || a.Type != "group" {
		return nil
	}
	routes := make([]types.Route, 0, len(a.Members))
	for _, m := range a.Members {
		if m == selected {
			continue
		}
		cfg, ok := r.providers[m.Provider]
		if !ok {
			continue // nested alias or unconfigured; resolved elsewhere
		}
		routes = append(routes, types.Route{Provider: m.Provider, Model: m.Model, Config: cfg})
	}
	return routes
}

func randomPick(members []AliasMember) AliasMember {
	return members[rand.Intn(len(members))]
}