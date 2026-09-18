// Command catalog-convert turns LiteLLM's published price list
// (model_prices_and_context_window.json) into an Arbiter model_catalog file.
//
// LiteLLM keys its price list by model name and quotes costs in USD per single
// token; Arbiter's catalog is keyed by provider/model and quotes USD per
// million tokens. Neither the key mapping nor the unit change is guessable, so
// this tool takes a small mapping file stating which litellm_provider each
// Arbiter provider corresponds to, and it converts the units. Latency is not
// in LiteLLM's list at all, so it is supplied by hand in that same mapping
// file. There is no per-model list to maintain: every chat-mode entry under a
// mapped litellm_provider becomes a catalog row.
//
// The generated file is meant to be referenced from arbiter.yaml as
// model_catalog_file:.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "catalog-convert: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("catalog-convert", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mapPath := fs.String("mapping", "", "Path to the mapping YAML (arbiter provider -> litellm_provider). Required.")
	configPath := fs.String("config", "", "Path to arbiter.yaml. When set, catalog rows are generated for every provider it declares (skipping any with no mapping entry) instead of every provider the mapping file lists.")
	outPath := fs.String("out", "", "Write the catalog to this path instead of stdout.")
	strict := fs.Bool("strict", false, "Exit non-zero if any provider had no usable entries, or any declared model went unmatched (see -coverage).")
	coverageFmt := fs.String("coverage", "text", "Coverage report format: text, json, or none. Requires -config (that is what declares the models).")
	coverageOut := fs.String("coverage-out", "", "Write the coverage report to this path instead of stderr. Use with -coverage json so the report is pure JSON with no interleaved log lines.")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: catalog-convert -mapping mapping.yaml [-config arbiter.yaml] [-out catalog.yaml] [-strict] [-coverage text|json|none] <model_prices_and_context_window.json>\n\n")
		_, _ = fmt.Fprintf(stderr, "The litellm price list is read from the single positional argument, or stdin when it is omitted.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *mapPath == "" {
		fs.Usage()
		return fmt.Errorf("-mapping is required")
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return fmt.Errorf("expected at most one positional argument (the litellm price list), got %d", fs.NArg())
	}

	// Read the mapping before opening any input, so a bad mapping fails fast
	// without having consumed a possibly-large JSON stream.
	mapData, err := os.ReadFile(*mapPath)
	if err != nil {
		return fmt.Errorf("read mapping %s: %w", *mapPath, err)
	}
	var m mapping
	dec := yaml.NewDecoder(bytes.NewReader(mapData))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return fmt.Errorf("parse mapping %s: %w", *mapPath, err)
	}
	if len(m.Providers) == 0 {
		return fmt.Errorf("mapping %s declares no providers", *mapPath)
	}

	providerNames, err := resolveProviderNames(*configPath, m)
	if err != nil {
		return err
	}

	// Load every source named in the mapping, in order. The positional argument
	// is kept as a compatibility path: with no sources: list, a single file on
	// the command line is read as litellm, which is what every pre-sources
	// invocation did.
	sources := make(map[string][]modelRow, len(m.Sources))
	// Resolved per source at load time — sniffed when the mapping left kind:
	// unset — so buildCatalog never has to re-derive a shape from a path.
	kinds := make(map[string]sourceKind, len(m.Sources))
	var sourceOrder []string
	var malformed []string
	for _, s := range m.Sources {
		if s.Path == "" {
			return fmt.Errorf("mapping has a sources: entry with no path")
		}
		// An empty kind is sniffed from the file's content — see sniffKind.
		var kind sourceKind
		if s.Kind != "" {
			k, ok := knownKinds[s.Kind]
			if !ok {
				return fmt.Errorf("source %s: unknown kind %q (want one of %s)", s.Path, s.Kind, kindList())
			}
			kind = k
		}
		resolved := resolveSourcePath(*mapPath, s.Path)
		rows, bad, kindUsed, err := loadSource(resolved, kind)
		if err != nil {
			return err
		}
		kinds[s.Path] = kindUsed
		for _, b := range bad {
			malformed = append(malformed, fmt.Sprintf("%s: %s", sourceLabel(s.Path), b))
		}
		sources[s.Path] = rows
		sourceOrder = append(sourceOrder, s.Path)
	}

	if len(sourceOrder) == 0 {
		// No sources: list — fall back to the positional argument (or stdin) as
		// a single litellm source, so existing invocations keep working.
		in := io.Reader(os.Stdin)
		name := "stdin"
		if fs.NArg() == 1 {
			f, err := os.Open(fs.Arg(0))
			if err != nil {
				return fmt.Errorf("open %s: %w", fs.Arg(0), err)
			}
			defer func() { _ = f.Close() }()
			in = f
			name = fs.Arg(0)
		}
		rows, bad, err := readLitellm(in)
		if err != nil {
			return fmt.Errorf("parse source %s: %w", sourceLabel(name), err)
		}
		malformed = bad
		sources[name] = rows
		kinds[name] = kindLitellm
		sourceOrder = append(sourceOrder, name)
	} else if fs.NArg() > 0 {
		return fmt.Errorf("the mapping declares sources:, so no positional price list is expected (remove it, or drop sources: from the mapping)")
	}

	for _, s := range malformed {
		_, _ = fmt.Fprintln(stderr, "skipped "+s)
	}

	rows, skips := buildCatalog(sources, sourceOrder, kinds, m, providerNames)
	for _, s := range skips {
		_, _ = fmt.Fprintln(stderr, "skipped "+s)
	}

	if len(rows) == 0 {
		return fmt.Errorf("no catalog rows produced; check the mapping and that the sources are the expected files")
	}

	sourceName := sourceLabel(sourceOrder[0])
	if len(sourceOrder) > 1 {
		sourceName = fmt.Sprintf("%s (+%d more)", sourceLabel(sourceOrder[0]), len(sourceOrder)-1)
	}

	var buf bytes.Buffer
	if _, err := buf.WriteString(header(sourceName, len(rows), len(skips)+len(malformed))); err != nil {
		return err
	}
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(catalogOut{ModelCatalog: rows}); err != nil {
		return fmt.Errorf("encode catalog: %w", err)
	}
	if err := enc.Close(); err != nil {
		return err
	}

	if *outPath == "" {
		if _, err := stdout.Write(buf.Bytes()); err != nil {
			return err
		}
	} else if err := writeCatalogFile(*outPath, buf.Bytes()); err != nil {
		return err
	}

	// The summary goes BEFORE the coverage report, deliberately: the coverage
	// report is the thing a cron parses, so it must be the last thing written to
	// stderr. Interleaving a "wrote N rows" line after it makes `2>` capture
	// both and the JSON unparsable.
	_, _ = fmt.Fprintf(stderr, "wrote %d catalog rows", len(rows))
	if n := len(skips) + len(malformed); n > 0 {
		_, _ = fmt.Fprintf(stderr, ", skipped %d", n)
	}
	_, _ = fmt.Fprintln(stderr)

	// Coverage: did every model the config declares actually get a row? This is
	// the check that catches a silent naming mismatch — the converter can write
	// hundreds of rows and report success while the models that matter match
	// nothing, because the join is on an exact provider+model string.
	//
	// Only meaningful with -config, which is what declares the models.
	unexpected := 0
	if *configPath != "" {
		declared, err := declaredModels(*configPath)
		if err != nil {
			return err
		}
		rep := buildCoverage(declared, rows, m.Manual)
		unexpected = rep.Totals.Unexpected
		// A report destined for a parser goes to its own file when asked, so the
		// skip lines and summary above cannot contaminate it. Without this, a
		// cron doing `2>report.json` gets JSON with log lines in front of it,
		// which is unparsable — and the failure looks like corrupt output rather
		// than a flag that was needed.
		dest := io.Writer(stderr)
		if *coverageOut != "" {
			f, err := os.Create(*coverageOut)
			if err != nil {
				return fmt.Errorf("create coverage report %s: %w", *coverageOut, err)
			}
			if err := renderCoverage(f, rep, *coverageFmt); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("close coverage report %s: %w", *coverageOut, err)
			}
			dest = nil
		}
		if dest != nil {
			if err := renderCoverage(dest, rep, *coverageFmt); err != nil {
				return err
			}
		}
	} else if *coverageFmt != "none" {
		_, _ = fmt.Fprintln(stderr, "coverage: skipped (no -config, so there are no declared models to check against)")
	}

	// The strict gate. Three classes of finding, deliberately not treated alike:
	//
	//   - A malformed ENTRY is always a failure. It means data was dropped from
	//     the source itself, which is never expected.
	//   - An UNEXPECTED coverage miss is a failure. A declared model with no data
	//     and no manual: entry saying that is expected is the real signal this
	//     gate exists for.
	//   - A provider-level SKIP is informational once coverage is available. A
	//     provider the mapping file doesn't mention is simply out of scope —
	//     during a build-out that is the normal state, and the coverage report
	//     already shows the consequence (its models appear as manual or
	//     unexpected). Failing on it would make the gate red on every run for a
	//     reason nobody intends to fix, and a gate that always fails is one
	//     nobody reads — which is worse than no gate.
	//
	// Without -config there is no coverage to check, so skips are all there is to
	// go on and they keep their original meaning.
	if *strict {
		if len(malformed) > 0 {
			return fmt.Errorf("strict: %d undecodable entry/entries in the source — see the skipped lines above", len(malformed))
		}
		if unexpected > 0 {
			return fmt.Errorf("strict: %d declared model(s) unmatched and not listed under manual: (see the coverage report above)", unexpected)
		}
		if *configPath == "" && len(skips) > 0 {
			return fmt.Errorf("strict: %d issue(s) — see the skipped lines above", len(skips))
		}
	}
	return nil
}

// writeCatalogFile writes the catalog atomically: a temporary file in the same
// directory, fsynced, then renamed over the target. A plain os.WriteFile
// truncates the target first, so a crash mid-write leaves a truncated catalog —
// and this file is meant to be regenerated on a schedule by a cron while the
// config that reads it stays live, which makes that window real rather than
// theoretical. Rename is atomic within a filesystem, so a reader sees either the
// previous catalog or the new one, never a partial one.
//
// The temp file is created in the target's own directory deliberately: a rename
// across filesystems is not atomic (and fails outright on some), so a temp file
// in /tmp would defeat the point.
func writeCatalogFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	// Best-effort cleanup: after a successful rename this is a no-op, and on any
	// failure path it keeps the directory free of debris.
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	// fsync before rename so the contents are durable, not just the name. Without
	// it a crash can leave the rename applied but the data not yet on disk.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	// Match the permissions a plain write would have produced.
	if err := os.Chmod(tmp, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, path, err)
	}
	tmp = "" // renamed; nothing to clean up
	return nil
}

// resolveProviderNames decides which Arbiter provider names to generate
// catalog rows for: every provider arbiter.yaml declares, when configPath is
// set, else every provider the mapping file itself lists (today's
// mapping-drives-everything behavior, kept for standalone use — e.g. CI
// generating a catalog with no access to a real arbiter.yaml).
func resolveProviderNames(configPath string, m mapping) ([]string, error) {
	if configPath == "" {
		names := make([]string, 0, len(m.Providers))
		for p := range m.Providers {
			names = append(names, p)
		}
		sort.Strings(names)
		return names, nil
	}
	return providerNamesFromConfig(configPath)
}

// providerNamesFromConfig reads just the top-level `providers:` map's keys
// out of a arbiter.yaml — not a full config.Load, deliberately: this tool needs
// only provider names, and a full load would (a) require every env var the
// real config references to be resolvable, and (b) choke on a arbiter.yaml that
// names a model_catalog_file which doesn't exist yet — exactly the file this
// tool is about to generate on a first run.
func providerNamesFromConfig(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var doc struct {
		Providers map[string]interface{} `yaml:"providers"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	names := make([]string, 0, len(doc.Providers))
	for p := range doc.Providers {
		names = append(names, p)
	}
	sort.Strings(names)
	return names, nil
}

// header is a comment banner for the generated file. Regeneration is expected
// (the file is inert on write and picked up by POST /admin/reload), so the
// header names its actual source rather than pretending to be hand-maintained.
//
// The source is named from the path it was read from, not hardcoded: the tool
// reads whatever price list it is given, and more than one upstream exists, so a
// fixed filename would become a lie the moment a second source is used.
func header(source string, rows, skipped int) string {
	return fmt.Sprintf(`# Generated by catalog-convert from %s.
# Do not edit by hand; edits are lost on regeneration. Per-model overrides
# belong in arbiter.yaml's inline model_catalog:, which wins over this file.
# Costs are USD per million tokens.
# %d rows, %d skipped (see the converter's stderr for reasons).
`, source, rows, skipped)
}
