package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// A source is one upstream file of model data, read into a common intermediate
// form. The converter previously had exactly one (LiteLLM's price list) and read
// it directly; a second shape is now supported, and more are expected.
//
// The intermediate form is deliberately flat and keyed the way a *declared* model
// is keyed, because the join Arbiter performs is an exact string match. Each
// source's own key shape is normalized away here, so nothing downstream has to
// know which upstream it came from — except for the two things that genuinely
// differ and cannot be normalized: the cost unit and the modality vocabulary.
// Both are handled by the reader, which is the only place that knows them.

// modelRow is one model as a source sees it, already normalized to the catalog's
// units and vocabulary.
type modelRow struct {
	// Key is the model name as it should appear in the emitted catalog, before
	// any provider-level namespace is applied.
	Key string

	// Provider is the source's own provider identifier (litellm_provider, or
	// models.dev's provider key). A mapping entry matches against this.
	Provider string

	InputCostPerMTok  float64
	OutputCostPerMTok float64

	InputModalities []string
	MaxInputTokens  *int
	MaxOutputTokens *int
	Metadata        map[string]interface{}

	// HasCost records whether the source stated a cost at all, so "free" (0)
	// can be distinguished from "unstated". The catalog has no room for that
	// distinction today — 0 is a meaningful cost — but the coverage report and
	// any future cost handling need it.
	HasCost bool
}

// sourceKind identifies how a file is parsed. Named rather than inferred from the
// filename: a downloaded file is called whatever the operator called it, and
// guessing a parser from a name would misparse the first file that breaks the
// convention.
type sourceKind string

const (
	kindLitellm      sourceKind = "litellm"
	kindModelsDevAPI sourceKind = "modelsdev-api"
	kindModelsDev    sourceKind = "modelsdev-models"
)

// knownKinds maps the mapping file's spelling to a parser. Kept as a map so an
// unknown kind is a clear error listing the valid ones, rather than a silent
// fallthrough to some default parser.
var knownKinds = map[string]sourceKind{
	"litellm":          kindLitellm,
	"modelsdev-api":    kindModelsDevAPI,
	"modelsdev-models": kindModelsDev,
}

// sourceSpec is one entry in the mapping file's sources: list.
//
// It accepts either spelling:
//
//	sources:
//	  - litellm-prices.json            # kind sniffed from the file's content
//	  - {path: api.json, kind: modelsdev-api}
//
// The plain-string form is the convenient one and is what the list is for — a
// list of downloaded files, no ceremony. The object form exists for the case
// where sniffing cannot tell (or the operator wants to be certain), and it skips
// the sniff entirely.
type sourceSpec struct {
	// Path is relative to the mapping file's directory, matching how
	// model_catalog_file: resolves in Arbiter's own config.
	Path string `yaml:"path"`
	// Kind says how to parse it. Empty means sniff it from the file's content.
	Kind string `yaml:"kind,omitempty"`
}

// UnmarshalYAML accepts a bare string (path only, kind sniffed) or an object.
func (s *sourceSpec) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		s.Path = node.Value
		return nil
	}
	// Alias the type so decoding into it does not recurse back into this method.
	type plain sourceSpec
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*s = sourceSpec(p)
	return nil
}

// loadSource reads and parses one source file into rows.
//
// Rows are returned sorted by key so the output is deterministic regardless of
// Go's map iteration order.
func loadSource(path string, kind sourceKind) ([]modelRow, []string, sourceKind, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("open source %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	// Read the whole file: sniffing needs to look at it, and every parser wants
	// it all anyway. These files are a few megabytes at most.
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, "", fmt.Errorf("read source %s: %w", path, err)
	}
	if kind == "" {
		k, err := sniffKind(data)
		if err != nil {
			return nil, nil, "", fmt.Errorf("source %s: %w", path, err)
		}
		kind = k
	}

	var rows []modelRow
	var skips []string
	switch kind {
	case kindLitellm:
		rows, skips, err = readLitellm(bytes.NewReader(data))
	case kindModelsDevAPI:
		rows, skips, err = readModelsDevAPI(bytes.NewReader(data))
	case kindModelsDev:
		rows, skips, err = readModelsDevFlat(bytes.NewReader(data))
	default:
		return nil, nil, "", fmt.Errorf("unknown source kind %q", kind)
	}
	if err != nil {
		return nil, nil, "", fmt.Errorf("parse source %s: %w", path, err)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Provider != rows[j].Provider {
			return rows[i].Provider < rows[j].Provider
		}
		return rows[i].Key < rows[j].Key
	})
	return rows, skips, kind, nil
}

// readLitellm parses LiteLLM's model_prices_and_context_window.json.
//
// Costs in this file are USD *per token*, so they are scaled to per-million here
// — the one place that knows the unit. Getting this wrong inflates or deflates
// every cost, so it is per-source by construction and never shared.
func readLitellm(r io.Reader) ([]modelRow, []string, error) {
	entries, malformed, err := parseLitellm(r)
	if err != nil {
		return nil, nil, err
	}

	rows := make([]modelRow, 0, len(entries))
	for key, e := range entries {
		if e.Mode != "" && e.Mode != "chat" {
			continue
		}
		rows = append(rows, modelRow{
			Key:               key,
			Provider:          e.LitellmProvider,
			InputCostPerMTok:  perMTok(e.InputCostPerToken),
			OutputCostPerMTok: perMTok(e.OutputCostPerToken),
			InputModalities:   inputModalities(e),
			MaxInputTokens:    tokenLimit(e.MaxInputTokens),
			MaxOutputTokens:   tokenLimit(e.MaxOutputTokens),
			Metadata:          extraMetadata(e),
			HasCost:           e.InputCostPerToken != 0 || e.OutputCostPerToken != 0,
		})
	}
	return rows, malformed, nil
}

// --- models.dev ---
//
// Two shapes from the same project:
//
//   - api.json:   {provider: {id, api, models: {modelID: modelObject}}}
//   - models.json: {modelID: modelObject}   (flat, provider-agnostic)
//
// The model object is identical between them, so only the outer walk differs.
// Note what the flat form cannot supply: a provider. It is keyed by a name like
// "anthropic/claude-sonnet-5" that *embeds* a vendor, but that vendor is not a
// models.dev provider key, so it cannot be matched against a mapping entry's
// provider — which is why the flat form is read with an empty Provider and
// matched by key shape instead.

// modelsDevModel is the subset of a models.dev model object this converter
// consumes. Fields are pointers/absent-able where the upstream omits them.
type modelsDevModel struct {
	Attachment       *bool  `json:"attachment"`
	Reasoning        *bool  `json:"reasoning"`
	ToolCall         *bool  `json:"tool_call"`
	StructuredOutput *bool  `json:"structured_output"`
	Temperature      *bool  `json:"temperature"`
	OpenWeights      *bool  `json:"open_weights"`
	Knowledge        string `json:"knowledge"`
	ReleaseDate      string `json:"release_date"`
	LastUpdated      string `json:"last_updated"`
	Modalities       *struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Limit *struct {
		Context int `json:"context"`
		Input   int `json:"input"`
		Output  int `json:"output"`
	} `json:"limit"`
	Cost *struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"cost"`
}

type modelsDevProvider struct {
	ID     string                    `json:"id"`
	Name   string                    `json:"name"`
	Models map[string]modelsDevModel `json:"models"`
}

// readModelsDevAPI parses the provider-nested form (api.json). This is the one
// that carries cost, and the one whose provider keys line up with a mapping
// entry.
func readModelsDevAPI(r io.Reader) ([]modelRow, []string, error) {
	var providers map[string]modelsDevProvider
	if err := json.NewDecoder(r).Decode(&providers); err != nil {
		return nil, nil, fmt.Errorf("decode models.dev api: %w", err)
	}

	rows := make([]modelRow, 0, len(providers))
	for pkey, pv := range providers {
		for id, m := range pv.Models {
			rows = append(rows, modelsDevRow(id, pkey, m))
		}
	}
	return rows, nil, nil
}

// readModelsDevFlat parses the flat form (models.json), whose keys are already
// "<vendor>/<model>" — the shape an aggregator like omniroute names its models
// with. Provider is left empty because this file has none: see the note above.
func readModelsDevFlat(r io.Reader) ([]modelRow, []string, error) {
	var models map[string]modelsDevModel
	if err := json.NewDecoder(r).Decode(&models); err != nil {
		return nil, nil, fmt.Errorf("decode models.dev models: %w", err)
	}

	rows := make([]modelRow, 0, len(models))
	for id, m := range models {
		rows = append(rows, modelsDevRow(id, "", m))
	}
	return rows, nil, nil
}

// modelsDevRow converts one models.dev model object into a row.
//
// Costs here are ALREADY per million tokens, so they are used as-is — no
// perMTok. Scaling them would inflate every figure a millionfold, which is the
// most dangerous thing in this file.
func modelsDevRow(id, provider string, m modelsDevModel) modelRow {
	row := modelRow{
		Key:      id,
		Provider: provider,
		Metadata: modelsDevMetadata(m),
	}
	if m.Cost != nil {
		row.InputCostPerMTok = m.Cost.Input
		row.OutputCostPerMTok = m.Cost.Output
		// A cost block present but zero means "free", which is a real figure and
		// must survive as 0 rather than be treated as unknown.
		row.HasCost = true
	}
	if m.Limit != nil {
		// limit.input is the tighter of the two when present; context is the
		// window. Prefer input, since that is what a request can actually use.
		in := m.Limit.Input
		if in == 0 {
			in = m.Limit.Context
		}
		if in != 0 {
			row.MaxInputTokens = &in
		}
		if m.Limit.Output != 0 {
			out := m.Limit.Output
			row.MaxOutputTokens = &out
		}
	}
	if m.Modalities != nil {
		row.InputModalities = modelsDevModalities(m.Modalities.Input)
	}
	return row
}

// modelsDevModalities maps models.dev's vocabulary onto Arbiter's normalized
// one. models.dev lists "pdf" where the catalog says "file"; "audio" and
// "video" have no Arbiter home at all, so they are dropped from the modality
// list rather than invented into it.
//
// Text is not inferred: like the litellm path, a row with no modality
// information stays unknown rather than being asserted as text-only.
func modelsDevModalities(in []string) []string {
	var out []string
	for _, m := range in {
		switch m {
		case "text":
			out = append(out, "text")
		case "image":
			out = append(out, "image")
		case "pdf":
			out = append(out, "file")
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// modelsDevMetadata carries the models.dev flags that have no typed field, on
// the same rule as the litellm extras: nothing in Arbiter reads them, they exist
// for a client to see. Absent means absent.
func modelsDevMetadata(m modelsDevModel) map[string]interface{} {
	out := make(map[string]interface{})
	for key, v := range map[string]*bool{
		"attachment":        m.Attachment,
		"reasoning":         m.Reasoning,
		"tool_calling":      m.ToolCall,
		"structured_output": m.StructuredOutput,
		"temperature":       m.Temperature,
		"open_weights":      m.OpenWeights,
	} {
		if v != nil {
			out[key] = *v
		}
	}
	for key, v := range map[string]string{
		"knowledge":    m.Knowledge,
		"release_date": m.ReleaseDate,
		"last_updated": m.LastUpdated,
	} {
		if v != "" {
			out[key] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sniffKind identifies a source file's shape from its content.
//
// Structural rather than name-based, because a downloaded file is called whatever
// the operator called it — guessing from a filename would misparse the first file
// that breaks the convention, and a misparse here is silent: wrong costs and
// wrong capabilities, with no error.
//
// The three shapes are distinguishable by what sits under a model entry:
//
//	litellm          flat map, entries carry "litellm_provider"
//	modelsdev-api    map of providers, each with a nested "models" object
//	modelsdev-models flat map, entries carry "modalities"
//
// Returns an error naming the shapes it knows when none match, rather than
// defaulting — a default here would pick the wrong parser for the next shape
// someone adds.
func sniffKind(data []byte) (sourceKind, error) {
	// Both flat shapes and the nested one are JSON objects; decode the top level
	// generically and look at ONE entry, since a nested provider map has many
	// top-level keys (one per provider), not one.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return "", fmt.Errorf("not a JSON object: %w", err)
	}
	if len(top) == 0 {
		return "", fmt.Errorf("file contains no entries")
	}

	// Any entry works: within one file every entry has the same shape. Iterating
	// a map is random, so this takes whichever comes first — which is fine
	// precisely because the shapes are uniform per file.
	for _, raw := range top {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil {
			continue
		}
		// Provider-nested: the entry has a "models" object of its own.
		if _, ok := entry["models"]; ok {
			return kindModelsDevAPI, nil
		}
		if _, ok := entry["litellm_provider"]; ok {
			return kindLitellm, nil
		}
		if _, ok := entry["modalities"]; ok {
			return kindModelsDev, nil
		}
	}
	return "", fmt.Errorf("cannot tell which source this is: expected entries carrying \"litellm_provider\" (kind litellm) or \"modalities\" (kind modelsdev-models), or a provider map whose entries carry a nested \"models\" (kind modelsdev-api); set kind: explicitly if the file is unusual")
}

// kindList renders the valid kind names for an error message, sorted so the
// message is stable.
func kindList() string {
	names := make([]string, 0, len(knownKinds))
	for k := range knownKinds {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// resolveSourcePath resolves a source path relative to the mapping file's
// directory, matching how model_catalog_file: resolves in Arbiter's config. An
// absolute path is used as-is.
func resolveSourcePath(mappingPath, sourcePath string) string {
	if filepath.IsAbs(sourcePath) {
		return sourcePath
	}
	return filepath.Join(filepath.Dir(mappingPath), sourcePath)
}

// sourceLabel renders a source for a skip message: the file's base name, so the
// message names something recognizable rather than a long path.
func sourceLabel(path string) string {
	if i := strings.LastIndex(path, string(filepath.Separator)); i >= 0 {
		return path[i+1:]
	}
	return path
}
