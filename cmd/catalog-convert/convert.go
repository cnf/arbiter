package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/cnf/arbiter/internal/config"
)

// litellmEntry is the subset of a row in LiteLLM's
// model_prices_and_context_window.json that this converter consumes. The list
// is keyed by model name and rows carry many more fields than these; unknown
// ones are ignored.
type litellmEntry struct {
	InputCostPerToken  float64 `json:"input_cost_per_token"`
	OutputCostPerToken float64 `json:"output_cost_per_token"`
	LitellmProvider    string  `json:"litellm_provider"`
	Mode               string  `json:"mode"`
}

// providerMap says how one Arbiter provider's models are found in the LiteLLM
// list. LiteLLM keys rows by model name, not by Arbiter's provider/model pair,
// so the correspondence has to be stated rather than guessed; KeyPrefix covers
// the entries that namespace the model (e.g. "openrouter/...").
type providerMap struct {
	LitellmProvider string   `yaml:"litellm_provider"`
	KeyPrefix       string   `yaml:"key_prefix,omitempty"`
	LatencyMsP50    int      `yaml:"latency_ms_p50,omitempty"`
	Models          []string `yaml:"models"`
}

// mapping is the converter's input file. Latency is not in LiteLLM's list, so
// it is supplied here: per model under latency_ms_p50 (keyed "provider/model")
// or as a per-provider default.
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

// buildCatalog resolves every model named in the mapping against the LiteLLM
// list and returns the catalog rows plus a human-readable reason for each model
// that was skipped. Providers are visited in sorted order so regenerating the
// same inputs produces the same file.
//
// A model is skipped, not guessed at, when LiteLLM has no matching entry, when
// the entry is not a chat model, or when its litellm_provider contradicts the
// mapping. A skipped model simply has no catalog row, which the router already
// treats as unknown cost (ranked last) rather than an error.
func buildCatalog(entries map[string]litellmEntry, m mapping) ([]config.ModelCatalogEntry, []string) {
	providers := make([]string, 0, len(m.Providers))
	for p := range m.Providers {
		providers = append(providers, p)
	}
	sort.Strings(providers)

	var out []config.ModelCatalogEntry
	var skips []string
	for _, p := range providers {
		pm := m.Providers[p]
		if len(pm.Models) == 0 {
			skips = append(skips, fmt.Sprintf("%s: no models listed in mapping", p))
			continue
		}
		for _, model := range pm.Models {
			key := pm.KeyPrefix + model
			e, ok := entries[key]
			if !ok {
				skips = append(skips, fmt.Sprintf("%s/%s: no litellm entry for %q", p, model, key))
				continue
			}
			if e.Mode != "" && e.Mode != "chat" {
				skips = append(skips, fmt.Sprintf("%s/%s: litellm mode %q is not chat", p, model, e.Mode))
				continue
			}
			if pm.LitellmProvider != "" && e.LitellmProvider != pm.LitellmProvider {
				skips = append(skips, fmt.Sprintf("%s/%s: litellm_provider is %q, mapping expected %q", p, model, e.LitellmProvider, pm.LitellmProvider))
				continue
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
			})
		}
	}
	sort.Strings(skips)
	return out, skips
}
