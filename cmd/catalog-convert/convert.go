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

	SupportsVision            *bool `json:"supports_vision"`
	SupportsImageInput        *bool `json:"supports_image_input"`
	SupportsPDFInput          *bool `json:"supports_pdf_input"`
	SupportsFunctionCalling   *bool `json:"supports_function_calling"`
	SupportsReasoning         *bool `json:"supports_reasoning"`
	SupportsPromptCaching     *bool `json:"supports_prompt_caching"`
	SupportsAudioInput        *bool `json:"supports_audio_input"`
	SupportsComputerUse       *bool `json:"supports_computer_use"`
	SupportsParallelToolCalls *bool `json:"supports_parallel_function_calling"`
	MaxInputTokens            *int  `json:"max_input_tokens"`
	MaxOutputTokens           *int  `json:"max_output_tokens"`
}

// providerMap says how one Arbiter provider's models are found in the LiteLLM
// list: which litellm_provider they're filed under, and the namespace prefix
// (if any) LiteLLM keys them with. There is deliberately no per-model list —
// every chat-mode entry under LitellmProvider becomes a catalog row. Rows for
// models the operator hasn't declared in arbiter.yaml are simply unused
// (config.go treats a generated-file row naming an undeclared model as inert,
// not a config error) — see the "catalog is a superset" note in README.md.
type providerMap struct {
	LitellmProvider string `yaml:"litellm_provider"`
	KeyPrefix       string `yaml:"key_prefix,omitempty"`
	LatencyMsP50    int    `yaml:"latency_ms_p50,omitempty"`
}

// mapping is the converter's input file. Latency is not in LiteLLM's list, so
// it is supplied here: per model under latency_ms_p50 (keyed "provider/model",
// using the *emitted* model name — i.e. after KeyPrefix is stripped) or as a
// per-provider default.
type mapping struct {
	Providers    map[string]providerMap `yaml:"providers"`
	LatencyMsP50 map[string]int         `yaml:"latency_ms_p50,omitempty"`
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

// truthy reports whether an optional bool is present and true. A nil pointer
// (the upstream said nothing) is not true, and must not be confused with an
// explicit false — see inputModalities.
func truthy(b *bool) bool { return b != nil && *b }

// buildCatalog emits one catalog row per LiteLLM chat-mode entry under each
// named provider's litellm_provider, for every name in providerNames — in
// that order, so the caller controls determinism (main.go sorts it). A name
// with no mapping entry, or whose litellm_provider matches nothing in the
// price list, is skipped with a reason rather than silently producing zero
// rows for it.
//
// There is no per-model allow-list: pulling everything under a
// litellm_provider is what removes the old mapping file's maintenance burden
// (keeping a model list in sync with arbiter.yaml by hand). An emitted row for
// a model the operator hasn't declared is simply unused — see README.md's
// "Model pricing catalog" section.
func buildCatalog(entries map[string]litellmEntry, m mapping, providerNames []string) ([]config.ModelCatalogEntry, []string) {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []config.ModelCatalogEntry
	var skips []string
	for _, p := range providerNames {
		pm, ok := m.Providers[p]
		if !ok {
			skips = append(skips, fmt.Sprintf("%s: no mapping entry (add it under providers: in the mapping file)", p))
			continue
		}
		if pm.LitellmProvider == "" {
			skips = append(skips, fmt.Sprintf("%s: mapping entry has no litellm_provider", p))
			continue
		}

		matched := 0
		for _, key := range keys {
			e := entries[key]
			if e.LitellmProvider != pm.LitellmProvider {
				continue
			}
			if e.Mode != "" && e.Mode != "chat" {
				continue
			}
			model := key
			if pm.KeyPrefix != "" {
				if !strings.HasPrefix(key, pm.KeyPrefix) {
					skips = append(skips, fmt.Sprintf("%s: litellm key %q (litellm_provider %q) does not start with configured key_prefix %q", p, key, pm.LitellmProvider, pm.KeyPrefix))
					continue
				}
				model = strings.TrimPrefix(key, pm.KeyPrefix)
			}

			latency := pm.LatencyMsP50
			if v, ok := m.LatencyMsP50[p+"/"+model]; ok {
				latency = v
			}
			out = append(out, config.ModelCatalogEntry{
				Provider:          p,
				Model:             model,
				InputCostPerMTok:  perMTok(e.InputCostPerToken),
				OutputCostPerMTok: perMTok(e.OutputCostPerToken),
				LatencyMsP50:      latency,
				InputModalities:   inputModalities(e),
				MaxInputTokens:    e.MaxInputTokens,
				MaxOutputTokens:   e.MaxOutputTokens,
				Metadata:          extraMetadata(e),
			})
			matched++
		}
		if matched == 0 {
			skips = append(skips, fmt.Sprintf("%s: no chat-mode litellm entries found for litellm_provider %q", p, pm.LitellmProvider))
		}
	}
	sort.Strings(skips)
	return out, skips
}
