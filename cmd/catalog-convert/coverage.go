package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cnf/arbiter/internal/config"
)

// Coverage reporting: for every model the config declares, did the generated
// catalog actually produce a row for it?
//
// This exists because the failure it detects is otherwise SILENT. The converter
// can write hundreds of rows and report success while the models that matter get
// nothing — the join is on an exact provider+model string, so a naming mismatch
// (a namespace the provider needs, a prefix the source doesn't carry) produces
// rows that are never read. Without this, the only way to notice is to go
// digging through the generated file by hand.

// coverage is the per-provider outcome, in three states.
//
// The third state is the whole point. A model can be unmatched because it is
// genuinely unmatchable (an operator's own preset, a model whose source isn't
// wired up yet) or because something is wrong. Collapsing those into one
// "missing" bucket makes the gate either useless (never fails) or unusable
// (always fails, so nobody reads it) — so an operator declares the expected ones
// under manual: in the mapping file, and only the rest are failures.
type coverage struct {
	// Matched is declared models that produced a catalog row.
	Matched []string `json:"matched"`
	// Manual is declared models that produced no row and are declared expected
	// to produce none, via the mapping file's manual: list.
	Manual []string `json:"manual"`
	// Unexpected is declared models that produced no row and were not declared
	// as manual. These are the failures.
	Unexpected []string `json:"unexpected"`
}

// coverageReport is the machine-parsable summary. Keys are sorted on render so
// the output is stable across runs and diffable.
type coverageReport struct {
	Providers map[string]coverage `json:"providers"`
	Totals    coverageTotals      `json:"totals"`
}

type coverageTotals struct {
	Declared   int `json:"declared"`
	Matched    int `json:"matched"`
	Manual     int `json:"manual"`
	Unexpected int `json:"unexpected"`
}

// declaredModels reads each provider's `models:` list out of arbiter.yaml.
//
// Deliberately a partial parse, like providerNamesFromConfig: a full config.Load
// would require every env var the real config references to be resolvable and
// would choke on a model_catalog_file that doesn't exist yet — which on a first
// run is exactly the file being generated.
func declaredModels(configPath string) (map[string][]string, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", configPath, err)
	}
	var doc struct {
		Providers map[string]struct {
			Models []string `yaml:"models"`
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", configPath, err)
	}
	out := make(map[string][]string, len(doc.Providers))
	for name, p := range doc.Providers {
		out[name] = p.Models
	}
	return out, nil
}

// buildCoverage classifies every declared model against the rows that were
// emitted. A model counts as matched when a row exists for that provider with
// exactly that model string — the same exact comparison Arbiter's lookup makes,
// so the report cannot disagree with the runtime.
//
// The model string is used VERBATIM as the report's key and as the key manual:
// is matched against. Declared model names are already provider-qualified in
// practice (a provider serving "claude/claude-sonnet-5", or "openrouter/auto"),
// so prefixing the provider again would produce a doubled key like
// "openrouter/openrouter/auto" and match nothing an operator would ever write.
func buildCoverage(declared map[string][]string, rows []config.ModelCatalogEntry, manual []string) coverageReport {
	have := make(map[string]bool, len(rows))
	for _, r := range rows {
		have[r.Provider+"\x00"+r.Model] = true
	}
	manualSet := make(map[string]bool, len(manual))
	for _, m := range manual {
		manualSet[m] = true
	}

	rep := coverageReport{Providers: make(map[string]coverage, len(declared))}
	for provider, models := range declared {
		var c coverage
		for _, model := range models {
			switch {
			case have[provider+"\x00"+model]:
				c.Matched = append(c.Matched, model)
			case manualSet[model]:
				c.Manual = append(c.Manual, model)
			default:
				c.Unexpected = append(c.Unexpected, model)
			}
		}
		sort.Strings(c.Matched)
		sort.Strings(c.Manual)
		sort.Strings(c.Unexpected)
		rep.Providers[provider] = c
		rep.Totals.Declared += len(models)
		rep.Totals.Matched += len(c.Matched)
		rep.Totals.Manual += len(c.Manual)
		rep.Totals.Unexpected += len(c.Unexpected)
	}
	return rep
}

// renderCoverage writes the report in the requested format. "none" is a no-op,
// for a caller that only wants the strict gate.
func renderCoverage(w io.Writer, rep coverageReport, format string) error {
	switch format {
	case "none":
		return nil
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return fmt.Errorf("encode coverage json: %w", err)
		}
		return nil
	case "text":
		names := make([]string, 0, len(rep.Providers))
		for p := range rep.Providers {
			names = append(names, p)
		}
		sort.Strings(names)
		for _, p := range names {
			c := rep.Providers[p]
			total := len(c.Matched) + len(c.Manual) + len(c.Unexpected)
			_, _ = fmt.Fprintf(w, "%s: %d/%d matched", p, len(c.Matched), total)
			if len(c.Manual) > 0 {
				_, _ = fmt.Fprintf(w, " (%d manual: %s)", len(c.Manual), joinLimited(c.Manual))
			}
			if len(c.Unexpected) > 0 {
				_, _ = fmt.Fprintf(w, " (%d UNEXPECTED: %s)", len(c.Unexpected), joinLimited(c.Unexpected))
			}
			_, _ = fmt.Fprintln(w)
		}
		_, _ = fmt.Fprintf(w, "total: %d declared, %d matched, %d manual, %d unexpected\n",
			rep.Totals.Declared, rep.Totals.Matched, rep.Totals.Manual, rep.Totals.Unexpected)
		return nil
	default:
		return fmt.Errorf("unknown coverage format %q (want text, json, or none)", format)
	}
}

// joinLimited renders a list compactly, capping it so one provider with dozens
// of misses cannot swamp the summary line. The cap is stated rather than silent.
func joinLimited(items []string) string {
	const max = 5
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:max], ", "), len(items)-max)
}
