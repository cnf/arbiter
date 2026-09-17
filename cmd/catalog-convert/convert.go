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
type litellmEntry struct {
	InputCostPerToken  float64 `json:"input_cost_per_token"`
	OutputCostPerToken float64 `json:"output_cost_per_token"`
	LitellmProvider    string  `json:"litellm_provider"`
	Mode               string  `json:"mode"`
}

// providerMap says how one Arbiter provider's models are found in the LiteLLM
// list: which litellm_provider they're filed under, and the namespace prefix
// (if any) LiteLLM keys them with. There is deliberately no per-model list —
// every chat-mode entry under LitellmProvider becomes a catalog row. Rows for
// models the operator hasn't declared in lanes.yaml are simply unused
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

// buildCatalog emits one catalog row per LiteLLM chat-mode entry under each
// named provider's litellm_provider, for every name in providerNames — in
// that order, so the caller controls determinism (main.go sorts it). A name
// with no mapping entry, or whose litellm_provider matches nothing in the
// price list, is skipped with a reason rather than silently producing zero
// rows for it.
//
// There is no per-model allow-list: pulling everything under a
// litellm_provider is what removes the old mapping file's maintenance burden
// (keeping a model list in sync with lanes.yaml by hand). An emitted row for
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
