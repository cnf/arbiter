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
// The generated file is meant to be referenced from lanes.yaml as
// model_catalog_file:.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
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
	configPath := fs.String("config", "", "Path to lanes.yaml. When set, catalog rows are generated for every provider it declares (skipping any with no mapping entry) instead of every provider the mapping file lists.")
	outPath := fs.String("out", "", "Write the catalog to this path instead of stdout.")
	strict := fs.Bool("strict", false, "Exit non-zero if any provider had no usable litellm entries.")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: catalog-convert -mapping mapping.yaml [-config lanes.yaml] [-out catalog.yaml] [-strict] <model_prices_and_context_window.json>\n\n")
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

	litellmIn := io.Reader(os.Stdin)
	if fs.NArg() == 1 {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return fmt.Errorf("open %s: %w", fs.Arg(0), err)
		}
		defer func() { _ = f.Close() }()
		litellmIn = f
	}

	entries, malformed, err := parseLitellm(litellmIn)
	if err != nil {
		return err
	}
	for _, s := range malformed {
		_, _ = fmt.Fprintln(stderr, "skipped "+s)
	}

	rows, skips := buildCatalog(entries, m, providerNames)
	for _, s := range skips {
		_, _ = fmt.Fprintln(stderr, "skipped "+s)
	}

	if len(rows) == 0 {
		return fmt.Errorf("no catalog rows produced; check the mapping and that the price list is the expected file")
	}

	var buf bytes.Buffer
	if _, err := buf.WriteString(header(len(rows), len(skips)+len(malformed))); err != nil {
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
	} else if err := os.WriteFile(*outPath, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", *outPath, err)
	}

	_, _ = fmt.Fprintf(stderr, "wrote %d catalog rows", len(rows))
	if n := len(skips) + len(malformed); n > 0 {
		_, _ = fmt.Fprintf(stderr, ", skipped %d", n)
	}
	_, _ = fmt.Fprintln(stderr)

	if *strict && len(skips)+len(malformed) > 0 {
		return fmt.Errorf("strict: %d issue(s) — see the skipped lines above", len(skips)+len(malformed))
	}
	return nil
}

// resolveProviderNames decides which Arbiter provider names to generate
// catalog rows for: every provider lanes.yaml declares, when configPath is
// set, else every provider the mapping file itself lists (today's
// mapping-drives-everything behavior, kept for standalone use — e.g. CI
// generating a catalog with no access to a real lanes.yaml).
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
// out of a lanes.yaml — not a full config.Load, deliberately: this tool needs
// only provider names, and a full load would (a) require every env var the
// real config references to be resolvable, and (b) choke on a lanes.yaml that
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
// header names its source rather than pretending to be hand-maintained.
func header(rows, skipped int) string {
	return fmt.Sprintf(`# Generated by catalog-convert from LiteLLM's model_prices_and_context_window.json.
# Do not edit by hand; edits are lost on regeneration. Per-model overrides
# belong in lanes.yaml's inline model_catalog:, which wins over this file.
# Costs are USD per million tokens (converted from litellm's per-token figures).
# %d rows, %d skipped (see the converter's stderr for reasons).
`, rows, skipped)
}
