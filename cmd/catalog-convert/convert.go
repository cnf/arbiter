package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/cnf/arbiter/internal/config"
)

// litellmEntry is the subset of a row in LiteLLM's
// model_prices_and_context_window.json that this converter consumes. The list
// is keyed by model name and rows carry many more fields than these; unknown
// ones are ignored.
//
// Capability fields are POINTERS on purpose. The upstream list omits a flag it
// has no information about, and for the majority of its chat models that is the
// case (vision support is recorded on under half). A plain bool would collapse
// "no information" into "no", which is a confidently wrong answer — so the
// pointer is what lets the converter emit nothing at all rather than a false.
type litellmEntry struct {
	InputCostPerToken  float64 `json:"input_cost_per_token"`
	OutputCostPerToken float64 `json:"output_cost_per_token"`
	LitellmProvider    string  `json:"litellm_provider"`
	Mode               string  `json:"mode"`

	// CacheReadInputTokenCost / CacheCreationInputTokenCost are LiteLLM's own
	// field names (see docs.litellm.ai/docs/proxy/custom_pricing) for a
	// cache-hit read and a cache-creation write respectively, in the same
	// USD-per-token unit as the two costs above. Absent (0) means the
	// upstream states no cache pricing for this model.
	CacheReadInputTokenCost     float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost float64 `json:"cache_creation_input_token_cost"`

	SupportsVision            *bool `json:"supports_vision"`
	SupportsImageInput        *bool `json:"supports_image_input"`
	SupportsPDFInput          *bool `json:"supports_pdf_input"`
	SupportsFunctionCalling   *bool `json:"supports_function_calling"`
	SupportsReasoning         *bool `json:"supports_reasoning"`
	SupportsPromptCaching     *bool `json:"supports_prompt_caching"`
	SupportsAudioInput        *bool `json:"supports_audio_input"`
	SupportsComputerUse       *bool `json:"supports_computer_use"`
	SupportsParallelToolCalls *bool `json:"supports_parallel_function_calling"`

	// Token limits are json.Number, not int, because the upstream list carries
	// some as floats (xai's grok entries say 2000000.0). A typed int makes the
	// WHOLE entry undecodable, so the model is dropped entirely — losing its
	// cost and capabilities along with the limit, and reported only as an
	// "undecodable entry" that reads like upstream corruption rather than a
	// field-type mismatch on our side.
	MaxInputTokens  json.Number `json:"max_input_tokens"`
	MaxOutputTokens json.Number `json:"max_output_tokens"`
}

// providerMap says how one Arbiter provider's models are found in the LiteLLM
// list: which litellm_provider they're filed under, and the namespace prefix
// (if any) LiteLLM keys them with. There is deliberately no per-model list —
// every chat-mode entry under LitellmProvider becomes a catalog row. Rows for
// models the operator hasn't declared in arbiter.yaml are simply unused
// (config.go treats a generated-file row naming an undeclared model as inert,
// not a config error) — see the "catalog is a superset" note in README.md.
type providerMap struct {
	// Source names which entry in the mapping file's sources: list this
	// provider's models come from. Empty means: pick the first source in
	// sources: order that actually yields a match for this provider (see
	// pickSource) — not simply the first source listed, so sources: order
	// stops being a hidden default-binding order and stays what its own docs
	// promise: precedence for a model that appears in more than one source.
	Source string `yaml:"source,omitempty"`
	// Provider is the provider key this provider's models are filed under,
	// used for whichever source kind is picked unless overridden below. Most
	// mappings only ever need this one field: a provider's litellm_provider
	// and models.dev provider key are the same string often enough (e.g.
	// "anthropic", "openai") that naming it twice is pure duplication. Set
	// LitellmProvider/ModelsDevProvider instead only for the source where the
	// key genuinely differs.
	Provider string `yaml:"provider,omitempty"`
	// LitellmProvider is the litellm_provider this provider's models are filed
	// under, for a source of kind litellm. Overrides Provider for that kind.
	LitellmProvider string `yaml:"litellm_provider,omitempty"`
	// ModelsDevProvider is the models.dev provider key, for a source of kind
	// modelsdev-api. Separate from LitellmProvider rather than reusing it,
	// because the two name different namespaces and a single field would make a
	// litellm value silently match nothing in a models.dev source. Overrides
	// Provider for that kind.
	ModelsDevProvider string `yaml:"models_dev_provider,omitempty"`
	// KeyPrefix strips a namespacing prefix LiteLLM puts on the *input* key
	// (e.g. "openrouter/anthropic/claude-3.5-sonnet" -> "anthropic/claude-3.5-sonnet").
	KeyPrefix string `yaml:"key_prefix,omitempty"`
	// Namespace prepends a prefix to the *emitted* model name. The two are
	// separate axes because they fix opposite mismatches: a provider whose
	// litellm keys are longer than the declared names needs KeyPrefix, while one
	// whose litellm keys are bare but declared namespaced needs Namespace.
	//
	// Arbiter joins a catalog row to a declared model on the exact string, so a
	// provider that serves its models under a namespace (an omniroute-style
	// prefixing proxy, or a self-hosted proxy fronting another vendor's API) must
	// have that namespace on the emitted row or nothing ever matches.
	//
	// Applied after KeyPrefix, so the two compose: strip the input prefix, then
	// add the output one.
	Namespace    string `yaml:"namespace,omitempty"`
	LatencyMsP50 int    `yaml:"latency_ms_p50,omitempty"`
}

// mapping is the converter's input file. Latency is not in LiteLLM's list, so
// it is supplied here: per model under latency_ms_p50 (keyed "provider/model",
// using the *emitted* model name — i.e. after KeyPrefix is stripped) or as a
// per-provider default.
//
// Manual names the declared models that are expected to have NO upstream data.
// They are reported in the coverage summary but do not count as failures — see
// reportCoverage. This is what keeps a strict gate meaningful: a model the
// operator knows is unmatchable (their own preset, a model from a source not yet
// wired up) would otherwise fail every run, and a gate that always fails is one
// nobody reads.
type mapping struct {
	// Sources lists the upstream files to read, in precedence order: the first
	// source that yields a model wins, and a later source never overrides an
	// entry already emitted. Paths resolve relative to the mapping file's own
	// directory (like model_catalog_file: in Arbiter's config), so a mapping
	// sitting next to its downloads can name them plainly.
	//
	// Order is the whole precedence rule — there is no merge and no per-field
	// override, deliberately: a rule you can predict without reading the
	// implementation is what keeps a silent-failure tool trustworthy.
	Sources      []sourceSpec           `yaml:"sources,omitempty"`
	Providers    map[string]providerMap `yaml:"providers"`
	LatencyMsP50 map[string]int         `yaml:"latency_ms_p50,omitempty"`
	Manual       []string               `yaml:"manual,omitempty"`
}

// catalogOut is the emitted document. It reuses config.ModelCatalogEntry so the
// generated shape cannot drift from what the config loader parses.
type catalogOut struct {
	ModelCatalog []config.ModelCatalogEntry `yaml:"model_catalog"`
}

// sampleSpecKey is LiteLLM's schema-documentation pseudo-entry. It is not a
// model and its values are prose, so it is never a catalog row.
const sampleSpecKey = "sample_spec"

// parseLitellm decodes the price list into the fields we consume. Rows are
// decoded one at a time (rather than all at once) so a single malformed entry
// is reported and skipped instead of failing the whole file.
func parseLitellm(r io.Reader) (map[string]litellmEntry, []string, error) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("parse litellm price list: %w", err)
	}

	entries := make(map[string]litellmEntry, len(raw))
	var skips []string
	for key, msg := range raw {
		if key == sampleSpecKey {
			continue
		}
		var e litellmEntry
		if err := json.Unmarshal(msg, &e); err != nil {
			skips = append(skips, fmt.Sprintf("%s: undecodable entry (%v)", key, err))
			continue
		}
		entries[key] = e
	}
	sort.Strings(skips)
	return entries, skips, nil
}

// perMTok converts a LiteLLM USD-per-token figure to USD per million tokens,
// the catalog's unit. The result is rounded because scaling a small binary
// float up by 1e6 reintroduces representation error (3e-06 * 1e6 lands on
// 3.0000000000000004), which would otherwise be emitted verbatim.
func perMTok(perToken float64) float64 {
	return math.Round(perToken*1e6*1e10) / 1e10
}

// inputModalities normalizes LiteLLM's per-capability booleans into one
// modality list. Several upstream flags mean the same thing to a client
// ("supports_vision" and "supports_image_input" both mean it takes an image),
// and collapsing them here is what keeps the internal vocabulary ours — a
// second upstream source can then be added without touching any consumer.
//
// Returns nil when the upstream says nothing about any of them, which is the
// signal to emit no field at all. Text is not inferred: a row with no modality
// information is unknown, not text-only, and guessing "text" for it would put a
// confident claim behind an absence of data.
func inputModalities(e litellmEntry) []string {
	var out []string
	if truthy(e.SupportsVision) || truthy(e.SupportsImageInput) {
		out = append(out, "image")
	}
	if truthy(e.SupportsPDFInput) {
		out = append(out, "file")
	}
	// Only meaningful once something else was stated: seeing an explicit
	// capability flag means the row describes this model, so text support is
	// implied alongside it.
	if len(out) > 0 {
		out = append([]string{"text"}, out...)
	}
	return out
}

// extraMetadata collects the capabilities that have no typed field yet but are
// worth carrying. Same absence rule as the modalities: nothing is emitted for a
// flag the upstream does not state.
//
// These ride in the free-form metadata map rather than becoming fields of their
// own, because nothing in Arbiter reads them — they exist for a client to see.
// Promoting one to a typed field is the move when something starts branching on
// it.
func extraMetadata(e litellmEntry) map[string]interface{} {
	m := make(map[string]interface{})
	for key, v := range map[string]*bool{
		"function_calling":    e.SupportsFunctionCalling,
		"reasoning":           e.SupportsReasoning,
		"prompt_caching":      e.SupportsPromptCaching,
		"audio_input":         e.SupportsAudioInput,
		"computer_use":        e.SupportsComputerUse,
		"parallel_tool_calls": e.SupportsParallelToolCalls,
	} {
		if v != nil {
			m[key] = *v
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// tokenLimit converts a json.Number limit into the *int the catalog carries,
// returning nil when the upstream said nothing.
//
// It accepts both spellings the upstream uses — "2000000" and "2000000.0" are
// the same limit, and the float form is common enough that rejecting it silently
// loses data. A genuinely fractional value is treated as absent rather than
// rounded: a token limit is not an estimate, and inventing one would put a wrong
// figure behind a client-visible field.
func tokenLimit(n json.Number) *int {
	if n == "" {
		return nil
	}
	if v, err := n.Int64(); err == nil {
		i := int(v)
		return &i
	}
	f, err := n.Float64()
	if err != nil || f != math.Trunc(f) {
		return nil
	}
	i := int(f)
	return &i
}

// truthy reports whether an optional bool is present and true. A nil pointer
// (the upstream said nothing) is not true, and must not be confused with an
// explicit false — see inputModalities.
func truthy(b *bool) bool { return b != nil && *b }

// latencyOverride resolves the per-model latency for one emitted model, keyed
// "provider/model" in the mapping file.
//
// It accepts both spellings of the model name, which matters only when the
// provider sets a namespace. Without that, a namespace would make the lookup key
// "claude" + "/" + "claude/claude-sonnet-5" — a doubled prefix that matches
// nothing, silently orphaning every override. Namespace and latency keys are
// written independently in the mapping file, so neither shape can be assumed.
//
// The namespaced form is tried first: when a namespace is set, the emitted name
// is the namespace-qualified one, so a key written as "claude/claude-sonnet-5"
// is the intuitive spelling and should win if both happen to be present.
func (m mapping) latencyOverride(provider, model string) (int, bool) {
	if v, ok := m.LatencyMsP50[provider+"/"+model]; ok {
		return v, true
	}
	// Also try with the provider's own namespace stripped back off, so a key
	// written against the bare litellm name keeps working.
	for _, pm := range m.Providers {
		if pm.Namespace == "" || !strings.HasPrefix(model, pm.Namespace) {
			continue
		}
		if v, ok := m.LatencyMsP50[provider+"/"+strings.TrimPrefix(model, pm.Namespace)]; ok {
			return v, true
		}
	}
	return 0, false
}

// providerKeyFor resolves which provider key value applies to a source of the
// given kind, and whether that kind needs one at all.
//
// LitellmProvider/ModelsDevProvider are the per-kind overrides for the ~10% of
// mappings where the two upstreams genuinely use different keys for the same
// provider; Provider is the common-case fallback used when only one string is
// needed. kindModelsDev (the flat aggregator shape) has no provider dimension
// at all, so it never needs a key — see buildCatalog's flat handling.
//
// key=="" with needsKey==true means this kind requires a key and this
// providerMap supplied none — the caller's signal to skip with a clear reason
// rather than silently matching nothing.
func (pm providerMap) providerKeyFor(kind sourceKind) (key string, needsKey bool) {
	if kind == kindModelsDev {
		return "", false
	}
	if kind == kindModelsDevAPI {
		if pm.ModelsDevProvider != "" {
			return pm.ModelsDevProvider, true
		}
		return pm.Provider, true
	}
	// kindLitellm, or an unset/unrecognized kind: same as the pre-sources
	// behavior, which never distinguished "litellm" from "unknown" here.
	if pm.LitellmProvider != "" {
		return pm.LitellmProvider, true
	}
	return pm.Provider, true
}

// buildCatalog emits one catalog row per model a provider's configured source
// yields, for every name in providerNames — in that order, so the caller
// controls determinism (main.go sorts it). A name with no mapping entry, or
// whose source matched nothing, is skipped with a reason rather than silently
// producing zero rows for it.
//
// First-source-wins: a model already emitted for this provider is skipped by a
// later source and never overridden. Precedence is the mapping file's sources:
// order and nothing else, so there is no merge logic and no per-field override —
// a rule you can predict without reading the implementation.
//
// There is no per-model allow-list: pulling everything under a provider key is
// what removes the old mapping file's maintenance burden (keeping a model list
// in sync with arbiter.yaml by hand). An emitted row for a model the operator
// hasn't declared is simply unused — see README.md's "Model pricing catalog"
// section.
//
// sources is the loaded form of the mapping file's sources: list, keyed by the
// path as written. A provider's Source names one of them; empty means the
// first source that actually yields a match for this provider (see pickSource).
func buildCatalog(sources map[string][]modelRow, sourceOrder []string, kinds map[string]sourceKind, m mapping, providerNames []string) ([]config.ModelCatalogEntry, []string) {
	var out []config.ModelCatalogEntry
	var skips []string

	for _, p := range providerNames {
		pm, ok := m.Providers[p]
		if !ok {
			skips = append(skips, fmt.Sprintf("%s: no mapping entry (add it under providers: in the mapping file)", p))
			continue
		}

		sourceKey, rows, err := pickSource(pm, sources, sourceOrder, kinds)
		if err != nil {
			skips = append(skips, fmt.Sprintf("%s: %v", p, err))
			continue
		}

		// Which provider key in the source this provider's models are filed
		// under. The kinds name it differently, so the mapping says which — a
		// single field would make a litellm value silently match nothing in a
		// models.dev source. providerKeyFor also accepts the common-case
		// provider: field, falling back to it when no kind-specific override
		// is set.
		//
		// The flat modelsdev-models shape has NO provider dimension at all: its
		// keys are already "<vendor>/<model>", which is exactly how a prefixing
		// aggregator names its models. So no provider key is required there, and
		// every row is eligible — which is what makes such an aggregator work
		// through the generic machinery rather than needing special-casing.
		sourceKind := kinds[sourceKey]
		wantProvider, needsKey := pm.providerKeyFor(sourceKind)
		flat := !needsKey
		if wantProvider == "" && needsKey {
			skips = append(skips, fmt.Sprintf("%s: mapping entry names no provider key for source %s (set provider, or litellm_provider/models_dev_provider)", p, sourceLabel(sourceKey)))
			continue
		}

		// Sort this provider's rows before emitting, rather than trusting the
		// loader to have done it: buildCatalog is called with hand-built rows in
		// tests and could be called from elsewhere, and a map-ordered input
		// would make the output non-deterministic — the same inputs producing a
		// differently-ordered catalog on each run, which defeats diffing the
		// generated file.
		ordered := make([]modelRow, 0, len(rows))
		for _, row := range rows {
			if !flat && row.Provider != wantProvider {
				continue
			}
			ordered = append(ordered, row)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key < ordered[j].Key })

		matched := 0
		seen := make(map[string]bool)
		for _, row := range ordered {
			model := row.Key
			if pm.KeyPrefix != "" {
				if !strings.HasPrefix(row.Key, pm.KeyPrefix) {
					skips = append(skips, fmt.Sprintf("%s: key %q (provider %q) does not start with configured key_prefix %q", p, row.Key, wantProvider, pm.KeyPrefix))
					continue
				}
				model = strings.TrimPrefix(row.Key, pm.KeyPrefix)
			}
			// Namespace the emitted name so it matches how the provider's models
			// are declared in arbiter.yaml. Applied after the key_prefix strip
			// and before the latency lookup, so latency_ms_p50 keys stay the
			// same shape as the emitted model.
			model = pm.Namespace + model

			// First-source-wins. A model already emitted for this provider is
			// left alone, so ordering the sources: list is the whole precedence
			// mechanism.
			if seen[model] {
				continue
			}
			seen[model] = true

			latency := pm.LatencyMsP50
			if v, ok := m.latencyOverride(p, model); ok {
				latency = v
			}
			out = append(out, config.ModelCatalogEntry{
				Provider:              p,
				Model:                 model,
				InputCostPerMTok:      row.InputCostPerMTok,
				OutputCostPerMTok:     row.OutputCostPerMTok,
				CacheReadCostPerMTok:  row.CacheReadCostPerMTok,
				CacheWriteCostPerMTok: row.CacheWriteCostPerMTok,
				LatencyMsP50:          latency,
				InputModalities:       row.InputModalities,
				MaxInputTokens:        row.MaxInputTokens,
				MaxOutputTokens:       row.MaxOutputTokens,
				Metadata:              row.Metadata,
			})
			matched++
		}
		if matched == 0 {
			skips = append(skips, fmt.Sprintf("%s: no models found for provider key %q in source %s", p, wantProvider, sourceLabel(sourceKey)))
		}
	}
	sort.Strings(skips)
	return out, skips
}

// pickSource resolves a provider's source entry. An explicit Source: is a
// direct, non-searching lookup — a typo there is an error, not a silent
// fallback to the first source, because that would build a catalog from the
// wrong file, exactly the kind of silent wrongness this tool has to avoid.
//
// An unset Source: searches sources: in order for the first one that actually
// yields a match for this provider — using whichever provider key applies to
// each candidate's kind (see providerKeyFor), or "any row" for the flat
// modelsdev-models kind, which has no provider dimension to match on. This is
// what makes sources: order a pure precedence list rather than also being a
// hidden default-binding order: a provider mapping that only sets the
// litellm-side key no longer silently binds to a models.dev source that
// happens to be listed first and then fails downstream with a confusing "no
// provider key" skip. A provider that matches nothing in any source still
// falls back to the first listed source, so the caller's existing "no models
// found" skip reports a single, sensible source name rather than an empty one.
func pickSource(pm providerMap, sources map[string][]modelRow, order []string, kinds map[string]sourceKind) (string, []modelRow, error) {
	if len(order) == 0 {
		return "", nil, fmt.Errorf("mapping declares no sources: (add a sources: list naming the upstream files)")
	}
	if pm.Source != "" {
		rows, ok := sources[pm.Source]
		if !ok {
			return "", nil, fmt.Errorf("source %q is not listed in sources: (valid: %s)", pm.Source, strings.Join(order, ", "))
		}
		return pm.Source, rows, nil
	}

	for _, candidate := range order {
		key, needsKey := pm.providerKeyFor(kinds[candidate])
		if needsKey && key == "" {
			// This kind needs a key and the mapping gave none for it — not a
			// match, but also not this provider's problem to report against
			// THIS source; buildCatalog's own check (against whichever source
			// is eventually picked) is what surfaces that.
			continue
		}
		rows := sources[candidate]
		if !needsKey {
			// Flat shape: any row at all counts as a match.
			if len(rows) > 0 {
				return candidate, rows, nil
			}
			continue
		}
		for _, row := range rows {
			if row.Provider == key {
				return candidate, rows, nil
			}
		}
	}
	// Nothing matched anywhere: fall back to the first source, unchanged from
	// the pre-content-aware behavior, so the "no models found" skip below
	// still names one concrete source.
	first := order[0]
	return first, sources[first], nil
}
