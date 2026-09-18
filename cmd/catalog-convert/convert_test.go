package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/cnf/arbiter/internal/config"
)

// sampleLitellm mirrors the real file's shape: a flat object keyed by model
// name, a `sample_spec` sentinel, per-token costs, a litellm_provider, and a
// mode.
const sampleLitellm = `{
  "sample_spec": {
    "input_cost_per_token": 0.0,
    "litellm_provider": "one of https://docs.litellm.ai/docs/providers",
    "mode": "one of: chat, embedding"
  },
  "claude-3-haiku-20250307": {
    "input_cost_per_token": 2.5e-07,
    "output_cost_per_token": 1.25e-06,
    "litellm_provider": "anthropic",
    "max_input_tokens": 200000,
    "mode": "chat"
  },
  "claude-3-opus-20250219": {
    "input_cost_per_token": 1.5e-05,
    "output_cost_per_token": 7.5e-05,
    "litellm_provider": "anthropic",
    "mode": "chat"
  },
  "openrouter/anthropic/claude-3.5-sonnet": {
    "input_cost_per_token": 3e-06,
    "output_cost_per_token": 1.5e-05,
    "litellm_provider": "openrouter",
    "mode": "chat"
  },
  "text-embedding-3-small": {
    "input_cost_per_token": 2e-08,
    "litellm_provider": "openai",
    "mode": "embedding"
  },
  "gpt-image-1": {
    "input_cost_per_token": 5e-06,
    "litellm_provider": "openai",
    "mode": "image_generation"
  }
}`

func parseSample(t *testing.T) map[string]litellmEntry {
	t.Helper()
	entries, malformed, err := parseLitellm(strings.NewReader(sampleLitellm))
	if err != nil {
		t.Fatalf("parseLitellm: %v", err)
	}
	if len(malformed) != 0 {
		t.Fatalf("unexpected malformed entries: %v", malformed)
	}
	if _, ok := entries[sampleSpecKey]; ok {
		t.Fatalf("sample_spec sentinel should be skipped, but it is present")
	}
	return entries
}

// TestPerMTokConversion is the load-bearing test: LiteLLM quotes USD per token,
// the catalog quotes USD per million. A missing or inverted scale factor would
// silently misprice every routing decision, so the arithmetic is pinned here.
func TestPerMTokConversion(t *testing.T) {
	cases := []struct {
		name     string
		perToken float64
		want     float64
	}{
		{"three dollars per M", 3e-06, 3},
		{"fifteen dollars per M", 1.5e-05, 15},
		{"quarter dollar per M", 2.5e-07, 0.25},
		{"free model", 0, 0},
		{"sub-cent per M", 2e-08, 0.02},
		{"one cent per M", 1e-08, 0.01},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := perMTok(tc.perToken)
			if got != tc.want {
				t.Errorf("perMTok(%g) = %v, want %v", tc.perToken, got, tc.want)
			}
		})
	}
}

// TestPerMTokIsExactNotNear guards against the floating-point residue that
// motivated rounding: 3e-06 * 1e6 is 3.0000000000000004 in IEEE-754, and that
// value would be emitted literally into the YAML without the round-trip.
func TestPerMTokIsExactNotNear(t *testing.T) {
	cases := []struct {
		perToken float64
		want     string
	}{
		{3e-06, "3"},
		{1.5e-05, "15"},
		{2.5e-07, "0.25"},
		{2e-08, "0.02"},
	}
	for _, tc := range cases {
		if got := strconv.FormatFloat(perMTok(tc.perToken), 'f', -1, 64); got != tc.want {
			t.Errorf("perMTok(%g) formats as %q, want %q (no binary-float residue)", tc.perToken, got, tc.want)
		}
	}
}

func TestBuildCatalogConvertsUnitsAndKeys(t *testing.T) {
	entries := parseSample(t)
	m := mapping{
		Providers: map[string]providerMap{
			"claude": {LitellmProvider: "anthropic"},
			"litellm": {
				LitellmProvider: "openrouter",
				KeyPrefix:       "openrouter/",
			},
		},
	}

	rows, skips := buildCatalog(entries, m, []string{"claude", "litellm"})
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(rows), rows)
	}

	byKey := map[string]config.ModelCatalogEntry{}
	for _, r := range rows {
		byKey[r.Provider+"/"+r.Model] = r
	}

	haiku, ok := byKey["claude/claude-3-haiku-20250307"]
	if !ok {
		t.Fatalf("missing haiku row; got %+v", rows)
	}
	if haiku.InputCostPerMTok != 0.25 || haiku.OutputCostPerMTok != 1.25 {
		t.Errorf("haiku costs = %v/%v, want 0.25/1.25", haiku.InputCostPerMTok, haiku.OutputCostPerMTok)
	}

	sonnet, ok := byKey["litellm/anthropic/claude-3.5-sonnet"]
	if !ok {
		t.Fatalf("slash-keyed model not mapped (key_prefix not stripped); got %+v", rows)
	}
	if sonnet.InputCostPerMTok != 3 || sonnet.OutputCostPerMTok != 15 {
		t.Errorf("sonnet costs = %v/%v, want 3/15", sonnet.InputCostPerMTok, sonnet.OutputCostPerMTok)
	}

	opus := byKey["claude/claude-3-opus-20250219"]
	if opus.InputCostPerMTok != 15 || opus.OutputCostPerMTok != 75 {
		t.Errorf("opus costs = %v/%v, want 15/75", opus.InputCostPerMTok, opus.OutputCostPerMTok)
	}
}

// TestBuildCatalogSkipsNonChatEntries proves a pulled-everything litellm_provider
// still excludes embedding/image entries filed under it, without needing them
// named anywhere — there is no per-model list to omit them from.
func TestBuildCatalogSkipsNonChatEntries(t *testing.T) {
	entries := parseSample(t)
	m := mapping{
		Providers: map[string]providerMap{
			"openai": {LitellmProvider: "openai"},
		},
	}

	rows, skips := buildCatalog(entries, m, []string{"openai"})
	if len(rows) != 0 {
		t.Fatalf("expected no rows (only embedding/image entries exist for openai), got %+v", rows)
	}
	if len(skips) != 1 || !strings.Contains(skips[0], "no chat-mode litellm entries") {
		t.Fatalf("got skips %v, want one 'no chat-mode litellm entries' skip", skips)
	}
}

// TestBuildCatalogSkipsProviderWithNoMappingEntry proves a providerNames entry
// absent from the mapping is reported, not silently dropped or guessed at.
func TestBuildCatalogSkipsProviderWithNoMappingEntry(t *testing.T) {
	entries := parseSample(t)
	m := mapping{Providers: map[string]providerMap{"claude": {LitellmProvider: "anthropic"}}}

	rows, skips := buildCatalog(entries, m, []string{"claude", "local"})
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 from claude", len(rows))
	}
	if len(skips) != 1 || !strings.Contains(skips[0], "local") || !strings.Contains(skips[0], "no mapping entry") {
		t.Fatalf("got skips %v, want one naming local's missing mapping entry", skips)
	}
}

// A litellm_provider match whose key doesn't start with the configured
// key_prefix is surfaced, not silently dropped — it means the prefix
// assumption is wrong for at least one entry.
func TestBuildCatalogReportsKeyPrefixMismatch(t *testing.T) {
	entries := parseSample(t)
	m := mapping{
		Providers: map[string]providerMap{
			// openrouter's one sample entry is "openrouter/anthropic/claude-3.5-sonnet";
			// this prefix doesn't match it.
			"litellm": {LitellmProvider: "openrouter", KeyPrefix: "wrong-prefix/"},
		},
	}

	rows, skips := buildCatalog(entries, m, []string{"litellm"})
	if len(rows) != 0 {
		t.Fatalf("expected no rows, got %+v", rows)
	}
	found := false
	for _, s := range skips {
		if strings.Contains(s, "key_prefix") {
			found = true
		}
	}
	if !found {
		t.Fatalf("got skips %v, want one mentioning key_prefix", skips)
	}
}

func TestBuildCatalogLatencyResolution(t *testing.T) {
	entries := parseSample(t)
	m := mapping{
		Providers: map[string]providerMap{
			"claude": {LitellmProvider: "anthropic", LatencyMsP50: 900},
		},
		LatencyMsP50: map[string]int{
			"claude/claude-3-opus-20250219": 4500, // per-model override
		},
	}

	rows, skips := buildCatalog(entries, m, []string{"claude"})
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	byKey := map[string]int{}
	for _, r := range rows {
		byKey[r.Provider+"/"+r.Model] = r.LatencyMsP50
	}
	if byKey["claude/claude-3-haiku-20250307"] != 900 {
		t.Errorf("haiku latency = %d, want provider default 900", byKey["claude/claude-3-haiku-20250307"])
	}
	if byKey["claude/claude-3-opus-20250219"] != 4500 {
		t.Errorf("opus latency = %d, want per-model override 4500", byKey["claude/claude-3-opus-20250219"])
	}
}

// TestOutputRoundTripsThroughConfig validates the real contract: what the
// converter writes must parse as an Arbiter model_catalog file and survive as
// the same numbers.
func TestOutputRoundTripsThroughConfig(t *testing.T) {
	entries := parseSample(t)
	m := mapping{
		Providers: map[string]providerMap{
			"claude": {LitellmProvider: "anthropic", LatencyMsP50: 900},
		},
	}
	rows, skips := buildCatalog(entries, m, []string{"claude"})
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}

	var buf bytes.Buffer
	_, _ = buf.WriteString(header(len(rows), len(skips)))
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(catalogOut{ModelCatalog: rows}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("close encoder: %v", err)
	}

	// The config loader wraps this shape in a `model_catalog:` key; decode it
	// back the same way config.catalogFile does.
	var round struct {
		ModelCatalog []config.ModelCatalogEntry `yaml:"model_catalog"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(buf.Bytes()))
	dec.KnownFields(true)
	if err := dec.Decode(&round); err != nil {
		t.Fatalf("generated YAML does not round-trip through the catalog shape: %v\n%s", err, buf.String())
	}
	if len(round.ModelCatalog) != len(rows) {
		t.Fatalf("round-trip changed row count: %d -> %d", len(rows), len(round.ModelCatalog))
	}
	for i := range rows {
		// reflect.DeepEqual, not ==: a catalog row now carries a modality
		// slice and a metadata map, which makes the struct non-comparable.
		if !reflect.DeepEqual(round.ModelCatalog[i], rows[i]) {
			t.Errorf("row %d changed: %+v -> %+v", i, rows[i], round.ModelCatalog[i])
		}
	}
}

func TestParseLitellmReportsMalformedEntry(t *testing.T) {
	const bad = `{
      "good": {"input_cost_per_token": 1e-06, "output_cost_per_token": 2e-06, "litellm_provider": "openai", "mode": "chat"},
      "bad": {"input_cost_per_token": "not-a-number", "litellm_provider": "openai", "mode": "chat"}
    }`
	entries, malformed, err := parseLitellm(strings.NewReader(bad))
	if err != nil {
		t.Fatalf("parseLitellm: %v", err)
	}
	if _, ok := entries["good"]; !ok {
		t.Errorf("good entry should survive a sibling's error")
	}
	if len(malformed) != 1 || !strings.Contains(malformed[0], "bad") {
		t.Fatalf("expected one malformed skip naming 'bad', got %v", malformed)
	}
}

func TestParseLitellmRejectsNonObject(t *testing.T) {
	if _, _, err := parseLitellm(strings.NewReader(`[1,2,3]`)); err == nil {
		t.Fatal("expected an error for a JSON array input")
	}
}

// TestRunEndToEnd exercises the flag parsing, the mapping file, and the emitted
// file together, which is the path the user actually invokes.
func TestRunEndToEnd(t *testing.T) {
	dir := t.TempDir()
	mapPath := dir + "/mapping.yaml"
	if err := os.WriteFile(mapPath, []byte(`
providers:
  claude:
    litellm_provider: anthropic
    latency_ms_p50: 900
  litellm:
    litellm_provider: openrouter
    key_prefix: "openrouter/"
latency_ms_p50:
  claude/claude-3-haiku-20250307: 750
`), 0o644); err != nil {
		t.Fatal(err)
	}

	jsonPath := dir + "/prices.json"
	if err := os.WriteFile(jsonPath, []byte(sampleLitellm), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := dir + "/catalog.yaml"

	var stdout, stderr bytes.Buffer
	if err := run([]string{"-mapping", mapPath, "-out", outPath, jsonPath}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\nstderr:\n%s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("with -out set, stdout should be empty; got:\n%s", stdout.String())
	}
	// claude pulls both haiku and opus (no per-model list to restrict it), plus
	// litellm's one openrouter entry: 3 rows.
	if !strings.Contains(stderr.String(), "wrote 3 catalog rows") {
		t.Errorf("stderr missing row count report:\n%s", stderr.String())
	}

	written, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read generated catalog: %v", err)
	}

	var round struct {
		ModelCatalog []config.ModelCatalogEntry `yaml:"model_catalog"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(written))
	dec.KnownFields(true)
	if err := dec.Decode(&round); err != nil {
		t.Fatalf("generated catalog does not parse: %v\n%s", err, written)
	}
	if len(round.ModelCatalog) != 3 {
		t.Fatalf("got %d rows, want 3:\n%s", len(round.ModelCatalog), written)
	}
	for _, r := range round.ModelCatalog {
		if r.Provider == "claude" && r.Model == "claude-3-haiku-20250307" {
			if r.InputCostPerMTok != 0.25 || r.LatencyMsP50 != 750 {
				t.Errorf("haiku row wrong: %+v (want input 0.25, latency 750 from per-model override)", r)
			}
		}
	}
}

// TestRunWithConfigLimitsToDeclaredProviders proves -config drives which
// providers get catalog rows: mapping.yaml can name more providers than
// arbiter.yaml declares (e.g. a shared mapping reused across deployments), and
// only the declared ones are generated for.
func TestRunWithConfigLimitsToDeclaredProviders(t *testing.T) {
	dir := t.TempDir()
	mapPath := dir + "/mapping.yaml"
	if err := os.WriteFile(mapPath, []byte(`
providers:
  claude:
    litellm_provider: anthropic
  litellm:
    litellm_provider: openrouter
    key_prefix: "openrouter/"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// arbiter.yaml declares only "claude" — "litellm" exists in the mapping but
	// not here, so it must not contribute rows, and no other arbiter.yaml field
	// needs to be valid (this is a minimal fixture, not a loadable config).
	configFilePath := dir + "/arbiter.yaml"
	if err := os.WriteFile(configFilePath, []byte(`
providers:
  claude:
    type: "anthropic"
    endpoint: "https://api.anthropic.com"
    models: ["claude-3-haiku-20250307"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonPath := dir + "/prices.json"
	if err := os.WriteFile(jsonPath, []byte(sampleLitellm), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"-mapping", mapPath, "-config", configFilePath, jsonPath}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\nstderr:\n%s", err, stderr.String())
	}
	if strings.Contains(stdout.String(), "provider: litellm") {
		t.Errorf("litellm should not appear (not declared in arbiter.yaml):\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "claude-3-haiku-20250307") {
		t.Errorf("claude's rows missing:\n%s", stdout.String())
	}
}

func TestRunStdoutByDefault(t *testing.T) {
	dir := t.TempDir()
	mapPath := dir + "/mapping.yaml"
	if err := os.WriteFile(mapPath, []byte(`
providers:
  claude:
    litellm_provider: anthropic
`), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonPath := dir + "/prices.json"
	if err := os.WriteFile(jsonPath, []byte(sampleLitellm), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"-mapping", mapPath, jsonPath}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "model_catalog:") {
		t.Errorf("stdout should carry the catalog, got:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "input_cost_per_mtok: 0.25") {
		t.Errorf("stdout missing converted cost:\n%s", stdout.String())
	}
}

func TestRunStrictFailsOnSkips(t *testing.T) {
	dir := t.TempDir()
	mapPath := dir + "/mapping.yaml"
	if err := os.WriteFile(mapPath, []byte(`
providers:
  claude:
    litellm_provider: anthropic
  openai:
    litellm_provider: openai
`), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonPath := dir + "/prices.json"
	if err := os.WriteFile(jsonPath, []byte(sampleLitellm), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"-mapping", mapPath, "-strict", jsonPath}, &stdout, &stderr); err == nil {
		t.Fatal("expected -strict to fail: openai's sample entries are all non-chat")
	}
	// claude's rows should still be emitted before the strict failure.
	if !strings.Contains(stdout.String(), "claude-3-haiku") {
		t.Errorf("non-strict rows should still be emitted before the strict failure:\n%s", stdout.String())
	}
}

func TestRunRequiresMapping(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"-mapping", ""}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error when -mapping is missing")
	}
}

func TestRunEmptyCatalogIsAnError(t *testing.T) {
	dir := t.TempDir()
	mapPath := dir + "/mapping.yaml"
	if err := os.WriteFile(mapPath, []byte(`
providers:
  openai:
    litellm_provider: openai
`), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonPath := dir + "/prices.json"
	if err := os.WriteFile(jsonPath, []byte(sampleLitellm), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"-mapping", mapPath, jsonPath}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error when no rows are produced (openai's sample entries are all non-chat)")
	}
}

// TestMappingRejectsUnknownFields keeps the mapping file honest: a typo'd key
// would otherwise be silently ignored.
func TestMappingRejectsUnknownFields(t *testing.T) {
	var m mapping
	dec := yaml.NewDecoder(strings.NewReader(`
providers:
  claude:
    litellm_provider: anthropic
    latency_ms_p500: 900
`))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err == nil {
		t.Fatal("expected an unknown-field error for the typo'd latency key")
	}
}

// Guard: the emitted YAML must be valid JSON-free utf-8 and stable across runs.
func TestBuildCatalogIsDeterministic(t *testing.T) {
	entries := parseSample(t)
	m := mapping{
		Providers: map[string]providerMap{
			"z":      {LitellmProvider: "openrouter", KeyPrefix: "openrouter/"},
			"claude": {LitellmProvider: "anthropic"},
		},
	}
	names := []string{"claude", "z"} // caller-controlled order, sorted the way main.go sorts it
	first, _ := buildCatalog(entries, m, names)
	second, _ := buildCatalog(entries, m, names)
	if len(first) != len(second) {
		t.Fatalf("row count differs between runs")
	}
	for i := range first {
		if !reflect.DeepEqual(first[i], second[i]) {
			t.Errorf("row %d differs between runs: %+v vs %+v", i, first[i], second[i])
		}
	}
	if first[0].Provider != "claude" {
		t.Errorf("providers should be visited in the given order, got first row %+v", first[0])
	}
	// Sanity: the straight-through JSON decode must agree with the fixture.
	var check map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sampleLitellm), &check); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}
}

// TestResolveProviderNamesFallsBackToMapping proves the mapping's own
// provider keys are used, sorted, when -config isn't given (standalone use).
func TestResolveProviderNamesFallsBackToMapping(t *testing.T) {
	m := mapping{Providers: map[string]providerMap{
		"z": {LitellmProvider: "openrouter"},
		"a": {LitellmProvider: "anthropic"},
	}}
	names, err := resolveProviderNames("", m)
	if err != nil {
		t.Fatalf("resolveProviderNames: %v", err)
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "z" {
		t.Fatalf("names = %v, want [a z]", names)
	}
}

// TestProviderNamesFromConfigIgnoresRestOfFile proves this reads bare
// provider names without needing the rest of arbiter.yaml (routers, aliases,
// env vars) to be valid — including a model_catalog_file that doesn't exist
// yet, the exact chicken-and-egg case a full config.Load would hit on a first
// run.
func TestProviderNamesFromConfigIgnoresRestOfFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/arbiter.yaml"
	if err := os.WriteFile(path, []byte(`
model_catalog_file: "does-not-exist-yet.yaml"
providers:
  claude:
    type: "anthropic"
    endpoint: "https://api.anthropic.com"
    key: "${SOME_VAR_NOT_SET}"
    models: ["claude-3-haiku-20250307"]
  gpt4:
    type: "openai"
    endpoint: "https://api.openai.com/v1"
    models: ["gpt-4o"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	names, err := providerNamesFromConfig(path)
	if err != nil {
		t.Fatalf("providerNamesFromConfig: %v", err)
	}
	if len(names) != 2 || names[0] != "claude" || names[1] != "gpt4" {
		t.Fatalf("names = %v, want [claude gpt4]", names)
	}
}
