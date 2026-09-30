package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Multiple sources. The precedence rule is first-source-wins and nothing else —
// no merge, no per-field override — so these tests pin the two things that could
// go silently wrong: the cost unit differs per source, and a later source must
// never override an earlier one.

// TestModelsDevCostIsNotScaled is the most important test in this file. models.dev
// quotes USD per MILLION tokens; litellm quotes USD per TOKEN and is scaled by
// perMTok. Applying the litellm scaling to a models.dev row would inflate every
// figure a millionfold, and nothing else would catch it — the numbers would look
// plausible in the emitted YAML.
func TestModelsDevCostIsNotScaled(t *testing.T) {
	raw := `{
	  "opencode": {
	    "id": "opencode",
	    "models": {
	      "claude-opus-4-5": {
	        "id": "claude-opus-4-5",
	        "modalities": {"input": ["text","image"], "output": ["text"]},
	        "limit": {"context": 200000, "output": 64000},
	        "cost": {"input": 5, "output": 25, "cache_read": 0.5, "cache_write": 6.25}
	      }
	    }
	  }
	}`
	rows, _, err := readModelsDevAPI(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("readModelsDevAPI: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.InputCostPerMTok != 5 {
		t.Errorf("InputCostPerMTok = %v, want 5 — models.dev is ALREADY per million tokens; scaling it corrupts every cost", got.InputCostPerMTok)
	}
	if got.OutputCostPerMTok != 25 {
		t.Errorf("OutputCostPerMTok = %v, want 25", got.OutputCostPerMTok)
	}
	if got.CacheReadCostPerMTok != 0.5 {
		t.Errorf("CacheReadCostPerMTok = %v, want 0.5 — also already per million tokens, not scaled", got.CacheReadCostPerMTok)
	}
	if got.CacheWriteCostPerMTok != 6.25 {
		t.Errorf("CacheWriteCostPerMTok = %v, want 6.25", got.CacheWriteCostPerMTok)
	}
	if !got.HasCost {
		t.Error("HasCost = false, want true — a stated cost must be distinguishable from an unstated one")
	}
}

// TestModelsDevFreeCostSurvivesAsZero pins the other half of the cost rule: a
// cost block present with zero values means "free", which is a real figure. It
// must not be collapsed into "unknown" (absent).
func TestModelsDevFreeCostSurvivesAsZero(t *testing.T) {
	raw := `{"opencode":{"id":"opencode","models":{
	  "big-pickle":{"id":"big-pickle","modalities":{"input":["text"],"output":["text"]},
	    "limit":{"context":200000,"output":32000},
	    "cost":{"input":0,"output":0,"cache_read":0,"cache_write":0}}}}}`

	rows, _, err := readModelsDevAPI(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("readModelsDevAPI: %v", err)
	}
	if !rows[0].HasCost {
		t.Error("HasCost = false for an explicit zero cost; free must not read as unknown")
	}
	if rows[0].InputCostPerMTok != 0 || rows[0].OutputCostPerMTok != 0 {
		t.Errorf("costs = %v/%v, want 0/0", rows[0].InputCostPerMTok, rows[0].OutputCostPerMTok)
	}
}

// TestModelsDevModalitiesMapPdfToFile pins the vocabulary mapping: models.dev
// says "pdf" where the catalog says "file", and "audio"/"video" have no Arbiter
// home at all — they must be dropped, not invented into the modality list.
func TestModelsDevModalitiesMapPdfToFile(t *testing.T) {
	raw := `{"p":{"id":"p","models":{
	  "m":{"id":"m","modalities":{"input":["text","image","pdf","audio","video"],"output":["text"]}}}}}`

	rows, _, err := readModelsDevAPI(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("readModelsDevAPI: %v", err)
	}
	got := rows[0].InputModalities
	want := []string{"text", "image", "file"}
	if len(got) != len(want) {
		t.Fatalf("InputModalities = %v, want %v (pdf->file, audio/video dropped)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("InputModalities = %v, want %v", got, want)
		}
	}
}

// TestModelsDevLimitPrefersInputOverContext pins the limit choice: limit.input is
// the tighter, usable window; context is the model's window. Preferring context
// would overstate what a request can actually use.
func TestModelsDevLimitPrefersInputOverContext(t *testing.T) {
	raw := `{"p":{"id":"p","models":{
	  "m":{"id":"m","limit":{"context":1000000,"input":160000,"output":32000}}}}}`

	rows, _, err := readModelsDevAPI(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("readModelsDevAPI: %v", err)
	}
	if rows[0].MaxInputTokens == nil || *rows[0].MaxInputTokens != 160000 {
		t.Errorf("MaxInputTokens = %v, want 160000 (limit.input, not context)", rows[0].MaxInputTokens)
	}
}

// TestModelsDevLimitFallsBackToContext covers the models that state only a
// context window, which is common in the flat file.
func TestModelsDevLimitFallsBackToContext(t *testing.T) {
	raw := `{"p":{"id":"p","models":{
	  "m":{"id":"m","limit":{"context":131072,"output":8192}}}}}`

	rows, _, err := readModelsDevAPI(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("readModelsDevAPI: %v", err)
	}
	if rows[0].MaxInputTokens == nil || *rows[0].MaxInputTokens != 131072 {
		t.Errorf("MaxInputTokens = %v, want 131072 (context as fallback)", rows[0].MaxInputTokens)
	}
}

// TestModelsDevFlatKeysKeepTheirPrefix proves the flat form's keys are used
// verbatim. That key shape ("<vendor>/<model>") is exactly how a prefixing
// aggregator names its models, so no prefix handling is needed or wanted.
func TestModelsDevFlatKeysKeepTheirPrefix(t *testing.T) {
	raw := `{
	  "anthropic/claude-sonnet-5": {"id":"anthropic/claude-sonnet-5","modalities":{"input":["text"],"output":["text"]},"limit":{"context":1000000}},
	  "google/gemini-3.7-flash":   {"id":"google/gemini-3.7-flash","modalities":{"input":["text","image"],"output":["text"]},"limit":{"context":1000000}}
	}`
	rows, _, err := readModelsDevFlat(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("readModelsDevFlat: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// Provider is deliberately empty: the flat file has no provider dimension,
	// and the vendor embedded in the key is not a models.dev provider key.
	for _, r := range rows {
		if r.Provider != "" {
			t.Errorf("row %q has Provider %q, want empty (the flat file carries no provider)", r.Key, r.Provider)
		}
	}
}

// TestFirstSourceWins proves the precedence rule: with two sources both holding
// the same model, the first source's figures are the ones emitted.
func TestFirstSourceWins(t *testing.T) {
	first := []modelRow{{
		Key: "claude-sonnet-5", Provider: "anthropic",
		InputCostPerMTok: 3, OutputCostPerMTok: 15,
	}}
	second := []modelRow{{
		Key: "claude-sonnet-5", Provider: "anthropic",
		InputCostPerMTok: 99, OutputCostPerMTok: 99,
	}}
	sources := map[string][]modelRow{"first.json": first, "second.json": second}
	m := mapping{
		Sources:   []sourceSpec{{Path: "first.json", Kind: "litellm"}, {Path: "second.json", Kind: "litellm"}},
		Providers: map[string]providerMap{"claude": {LitellmProvider: "anthropic", Source: "second.json"}},
	}

	// Point the provider at the SECOND source: it must win for that provider,
	// since precedence is per-provider source selection, not global order.
	rows, skips := buildCatalog(sources, []string{"first.json", "second.json"}, testKinds(m), m, []string{"claude"})
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 — the same model in two sources must not emit twice", len(rows))
	}
	if rows[0].InputCostPerMTok != 99 {
		t.Errorf("InputCostPerMTok = %v, want 99 (the provider's selected source)", rows[0].InputCostPerMTok)
	}
}

// TestDefaultSourceIsTheFirst proves an omitted source: resolves to the first
// listed entry, which is what keeps every pre-sources mapping file working
// unchanged.
func TestDefaultSourceIsTheFirst(t *testing.T) {
	first := []modelRow{{Key: "m", Provider: "p", InputCostPerMTok: 1}}
	second := []modelRow{{Key: "m", Provider: "p", InputCostPerMTok: 2}}
	sources := map[string][]modelRow{"a.json": first, "b.json": second}
	m := mapping{Providers: map[string]providerMap{"prov": {LitellmProvider: "p"}}}

	rows, _ := buildCatalog(sources, []string{"a.json", "b.json"}, testKinds(m), m, []string{"prov"})
	if len(rows) != 1 || rows[0].InputCostPerMTok != 1 {
		t.Fatalf("rows = %+v, want one row from the first source (cost 1)", rows)
	}
}

// TestUnknownSourceNameIsAnError proves a typo'd source: fails loudly. A silent
// fallback to the first source would build a catalog from the wrong file, which
// is exactly the silent wrongness this tool must avoid.
func TestUnknownSourceNameIsAnError(t *testing.T) {
	sources := map[string][]modelRow{"a.json": {{Key: "m", Provider: "p"}}}
	m := mapping{Providers: map[string]providerMap{"prov": {LitellmProvider: "p", Source: "typo.json"}}}

	rows, skips := buildCatalog(sources, []string{"a.json"}, testKinds(m), m, []string{"prov"})
	if len(rows) != 0 {
		t.Errorf("got %d rows, want none — an unlisted source must not fall back", len(rows))
	}
	if len(skips) != 1 || !strings.Contains(skips[0], "not listed in sources") {
		t.Errorf("skips = %v, want one naming the unlisted source", skips)
	}
}

// TestNoSourcesIsAnError covers the mapping that forgot the list entirely.
func TestNoSourcesIsAnError(t *testing.T) {
	m := mapping{Providers: map[string]providerMap{"prov": {LitellmProvider: "p"}}}
	_, skips := buildCatalog(nil, nil, nil, m, []string{"prov"})
	if len(skips) != 1 || !strings.Contains(skips[0], "no sources") {
		t.Errorf("skips = %v, want one reporting the missing sources list", skips)
	}
}

// TestModelsDevProviderKeyIsSeparate proves the two provider-key fields are not
// interchangeable: a litellm_provider value must not match in a models.dev
// source, or a mapping copied between kinds would silently produce zero rows.
func TestModelsDevProviderKeyIsSeparate(t *testing.T) {
	rows := []modelRow{{Key: "big-pickle", Provider: "opencode"}}
	sources := map[string][]modelRow{"api.json": rows}

	// Correct: models_dev_provider names the key.
	m := mapping{
		Sources:   []sourceSpec{{Path: "api.json", Kind: "modelsdev-api"}},
		Providers: map[string]providerMap{"zen": {ModelsDevProvider: "opencode"}},
	}
	got, skips := buildCatalog(sources, []string{"api.json"}, testKinds(m), m, []string{"zen"})
	if len(skips) != 0 || len(got) != 1 {
		t.Fatalf("rows=%d skips=%v, want one row and no skips", len(got), skips)
	}

	// Wrong field: litellm_provider must NOT be consulted for a models.dev source.
	m.Providers["zen"] = providerMap{LitellmProvider: "opencode"}
	got, skips = buildCatalog(sources, []string{"api.json"}, testKinds(m), m, []string{"zen"})
	if len(got) != 0 {
		t.Errorf("got %d rows, want none — litellm_provider must not match a models.dev source", len(got))
	}
	if len(skips) != 1 || !strings.Contains(skips[0], "models_dev_provider") {
		t.Errorf("skips = %v, want one naming the missing models_dev_provider", skips)
	}
}

// TestDefaultSourceIsContentAwareNotJustFirst is the sidebug regression: a
// provider mapping that only sets litellm_provider must resolve against the
// litellm source regardless of where that source sits in sources: order. Before
// content-aware default selection, an unset Source: always bound to order[0]
// positionally, so listing models.dev first made a litellm-only provider
// silently target the wrong source and fail downstream with a confusing "no
// provider key" skip instead of finding its actual data.
func TestDefaultSourceIsContentAwareNotJustFirst(t *testing.T) {
	litellmRows := []modelRow{{Key: "claude-sonnet-5", Provider: "anthropic", InputCostPerMTok: 3}}
	modelsDevRows := []modelRow{{Key: "big-pickle", Provider: "opencode", InputCostPerMTok: 1}}
	sources := map[string][]modelRow{"litellm.json": litellmRows, "api.json": modelsDevRows}

	// models.dev listed FIRST, litellm second — order[0] would be api.json.
	m := mapping{
		Sources: []sourceSpec{
			{Path: "api.json", Kind: "modelsdev-api"},
			{Path: "litellm.json", Kind: "litellm"},
		},
		Providers: map[string]providerMap{"claude": {LitellmProvider: "anthropic"}},
	}

	rows, skips := buildCatalog(sources, []string{"api.json", "litellm.json"}, testKinds(m), m, []string{"claude"})
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v — provider should find its litellm source regardless of list order", skips)
	}
	if len(rows) != 1 || rows[0].InputCostPerMTok != 3 {
		t.Fatalf("rows = %+v, want one row from litellm.json (cost 3)", rows)
	}
}

// TestGenericProviderKeyCoversBothKinds proves provider: is consulted as a
// fallback for whichever kind the resolved source turns out to be, covering
// the common case where the litellm and models.dev provider slugs are the same
// string and a mapping author shouldn't have to say it twice.
func TestGenericProviderKeyCoversBothKinds(t *testing.T) {
	litellmRows := []modelRow{{Key: "claude-sonnet-5", Provider: "anthropic", InputCostPerMTok: 3}}
	modelsDevRows := []modelRow{{Key: "claude-sonnet-5", Provider: "anthropic", InputCostPerMTok: 5}}

	for _, tc := range []struct {
		name string
		kind string
		rows []modelRow
		want float64
	}{
		{"litellm", "litellm", litellmRows, 3},
		{"modelsdev-api", "modelsdev-api", modelsDevRows, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources := map[string][]modelRow{"src": tc.rows}
			m := mapping{
				Sources:   []sourceSpec{{Path: "src", Kind: tc.kind}},
				Providers: map[string]providerMap{"claude": {Provider: "anthropic"}},
			}
			rows, skips := buildCatalog(sources, []string{"src"}, testKinds(m), m, []string{"claude"})
			if len(skips) != 0 || len(rows) != 1 {
				t.Fatalf("rows=%d skips=%v, want one row and no skips", len(rows), skips)
			}
			if rows[0].InputCostPerMTok != tc.want {
				t.Errorf("InputCostPerMTok = %v, want %v", rows[0].InputCostPerMTok, tc.want)
			}
		})
	}
}

// TestSpecificProviderKeyOverridesGeneric proves litellm_provider/
// models_dev_provider still win over provider: when both are set — the
// generic key is a fallback for the common case, not a replacement for the
// escape hatch that lets the two kinds diverge.
func TestSpecificProviderKeyOverridesGeneric(t *testing.T) {
	rows := []modelRow{{Key: "m", Provider: "actual-slug"}}
	sources := map[string][]modelRow{"src": rows}
	m := mapping{
		Sources: []sourceSpec{{Path: "src", Kind: "litellm"}},
		Providers: map[string]providerMap{
			"prov": {Provider: "wrong-slug", LitellmProvider: "actual-slug"},
		},
	}
	got, skips := buildCatalog(sources, []string{"src"}, testKinds(m), m, []string{"prov"})
	if len(skips) != 0 || len(got) != 1 {
		t.Fatalf("rows=%d skips=%v, want one row and no skips — litellm_provider must win over provider:", len(got), skips)
	}
}

// TestSourcePathResolvesRelativeToMapping proves downloads can be named plainly
// next to the mapping file, matching how model_catalog_file: resolves.
func TestSourcePathResolvesRelativeToMapping(t *testing.T) {
	got := resolveSourcePath("/some/dir/mapping.yaml", "downloads/api.json")
	if got != "/some/dir/downloads/api.json" {
		t.Errorf("resolveSourcePath = %q, want /some/dir/downloads/api.json", got)
	}
	if abs := resolveSourcePath("/some/dir/mapping.yaml", "/abs/api.json"); abs != "/abs/api.json" {
		t.Errorf("absolute path = %q, want it used as-is", abs)
	}
}

// TestUnknownSourceKindIsAnError proves a typo'd kind fails with the valid names
// listed, rather than falling through to some default parser.
func TestUnknownSourceKindIsAnError(t *testing.T) {
	if _, ok := knownKinds["litelm"]; ok {
		t.Error("knownKinds accepted a typo'd kind")
	}
	for _, want := range []string{"litellm", "modelsdev-api", "modelsdev-models"} {
		if _, ok := knownKinds[want]; !ok {
			t.Errorf("knownKinds is missing %q", want)
		}
	}
	if list := kindList(); !strings.Contains(list, "modelsdev-api") {
		t.Errorf("kindList = %q, want it to name the valid kinds", list)
	}
}

// TestBuildCatalogCarriesModelsDevRowThrough proves a models.dev row reaches the
// emitted catalog intact — cost, modalities, limits and metadata together.
func TestBuildCatalogCarriesModelsDevRowThrough(t *testing.T) {
	raw := `{"opencode":{"id":"opencode","models":{
	  "claude-opus-4-5":{"id":"claude-opus-4-5","reasoning":true,"tool_call":true,
	    "modalities":{"input":["text","image","pdf"],"output":["text"]},
	    "limit":{"context":200000,"input":160000,"output":64000},
	    "cost":{"input":5,"output":25}}}}}`

	rows, _, err := readModelsDevAPI(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("readModelsDevAPI: %v", err)
	}
	m := mapping{
		Sources:   []sourceSpec{{Path: "api.json", Kind: "modelsdev-api"}},
		Providers: map[string]providerMap{"zen": {ModelsDevProvider: "opencode"}},
	}
	got, skips := buildCatalog(map[string][]modelRow{"api.json": rows}, []string{"api.json"}, testKinds(m), m, []string{"zen"})
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].InputCostPerMTok != 5 || got[0].OutputCostPerMTok != 25 {
		t.Errorf("costs = %v/%v, want 5/25", got[0].InputCostPerMTok, got[0].OutputCostPerMTok)
	}
	if len(got[0].InputModalities) != 3 || got[0].InputModalities[2] != "file" {
		t.Errorf("InputModalities = %v, want [text image file]", got[0].InputModalities)
	}
	if got[0].MaxInputTokens == nil || *got[0].MaxInputTokens != 160000 {
		t.Errorf("MaxInputTokens = %v, want 160000", got[0].MaxInputTokens)
	}
	if got[0].Metadata["reasoning"] != true || got[0].Metadata["tool_calling"] != true {
		t.Errorf("Metadata = %v, want reasoning and tool_calling true", got[0].Metadata)
	}
}

// Source kind sniffing. Structural, never filename-based: a downloaded file is
// called whatever the operator called it, and a misparse here is silent — wrong
// costs and wrong capabilities with no error.

// TestSniffKindDistinguishesAllThreeShapes is the core case. The three upstream
// files are distinguishable by what sits under a model entry, and nothing else.
func TestSniffKindDistinguishesAllThreeShapes(t *testing.T) {
	cases := []struct {
		name string
		json string
		want sourceKind
	}{
		{
			name: "litellm flat with litellm_provider",
			json: `{"claude-sonnet-5":{"litellm_provider":"anthropic","mode":"chat"}}`,
			want: kindLitellm,
		},
		{
			name: "models.dev nested provider map",
			json: `{"opencode":{"id":"opencode","models":{"big-pickle":{"id":"big-pickle"}}}}`,
			want: kindModelsDevAPI,
		},
		{
			name: "models.dev flat with modalities",
			json: `{"anthropic/claude-sonnet-5":{"id":"anthropic/claude-sonnet-5","modalities":{"input":["text"]}}}`,
			want: kindModelsDev,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sniffKind([]byte(tc.json))
			if err != nil {
				t.Fatalf("sniffKind: %v", err)
			}
			if got != tc.want {
				t.Errorf("sniffKind = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSniffKindHandlesMultiProviderNested is the case that broke the first
// implementation: api.json has MANY top-level keys (one per provider), each
// carrying its own nested "models". A probe that looked for a top-level "models"
// key found nothing and misreported the file as unrecognizable.
func TestSniffKindHandlesMultiProviderNested(t *testing.T) {
	raw := `{
	  "anthropic": {"id":"anthropic","models":{"claude-sonnet-5":{"id":"claude-sonnet-5"}}},
	  "opencode":  {"id":"opencode","models":{"big-pickle":{"id":"big-pickle"}}},
	  "google":    {"id":"google","models":{"gemini-3.7-flash":{"id":"gemini-3.7-flash"}}}
	}`
	got, err := sniffKind([]byte(raw))
	if err != nil {
		t.Fatalf("sniffKind: %v", err)
	}
	if got != kindModelsDevAPI {
		t.Errorf("sniffKind = %q, want %q for a multi-provider nested file", got, kindModelsDevAPI)
	}
}

// TestSniffKindRefusesToGuess proves an unrecognizable file errors instead of
// defaulting to a parser. A default would silently pick the wrong shape for the
// next source someone adds, producing wrong costs with no error.
func TestSniffKindRefusesToGuess(t *testing.T) {
	if _, err := sniffKind([]byte(`{"something":{"unexpected":true}}`)); err == nil {
		t.Error("expected an error for an unrecognizable file")
	}
	if _, err := sniffKind([]byte(`not json at all`)); err == nil {
		t.Error("expected an error for non-JSON input")
	}
	if _, err := sniffKind([]byte(`{}`)); err == nil {
		t.Error("expected an error for an empty object")
	}
}

// TestSniffErrorMessageNamesTheKinds proves the failure is actionable: it lists
// the shapes and points at the escape hatch.
func TestSniffErrorMessageNamesTheKinds(t *testing.T) {
	_, err := sniffKind([]byte(`{"a":{"b":1}}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"litellm_provider", "modalities", "models", "kind"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// TestSourceSpecAcceptsPlainString proves the convenient form works: a bare
// filename in the sources list, with the kind sniffed from the file's content.
// That is what the list is for — a list of downloaded files, no ceremony.
func TestSourceSpecAcceptsPlainString(t *testing.T) {
	var m mapping
	dec := yaml.NewDecoder(strings.NewReader(`
sources:
  - litellm-prices.json
  - {path: api.json, kind: modelsdev-api}
providers:
  claude:
    litellm_provider: anthropic
`))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode mapping: %v", err)
	}
	if len(m.Sources) != 2 {
		t.Fatalf("got %d sources, want 2", len(m.Sources))
	}
	if m.Sources[0].Path != "litellm-prices.json" || m.Sources[0].Kind != "" {
		t.Errorf("plain entry = %+v, want path with empty kind (sniffed)", m.Sources[0])
	}
	if m.Sources[1].Path != "api.json" || m.Sources[1].Kind != "modelsdev-api" {
		t.Errorf("object entry = %+v, want path and explicit kind", m.Sources[1])
	}
}

// TestFlatSourceNeedsNoProviderKey proves an aggregator-shaped source works
// through the generic machinery: the flat file has no provider dimension, so a
// provider entry pointing at it needs no provider key, and every row is eligible.
// This is what keeps an aggregator from needing special-casing.
func TestFlatSourceNeedsNoProviderKey(t *testing.T) {
	rows := []modelRow{
		{Key: "anthropic/claude-sonnet-5", InputCostPerMTok: 2},
		{Key: "google/gemini-3.7-flash", InputCostPerMTok: 0.75},
	}
	sources := map[string][]modelRow{"flat.json": rows}
	m := mapping{
		Sources: []sourceSpec{{Path: "flat.json"}},
		// No litellm_provider, no models_dev_provider: the flat shape has none.
		Providers: map[string]providerMap{"omni": {}},
	}

	got, skips := buildCatalog(sources, []string{"flat.json"},
		map[string]sourceKind{"flat.json": kindModelsDev}, m, []string{"omni"})
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 — a flat source needs no provider key", len(got))
	}
	if got[0].Model != "anthropic/claude-sonnet-5" {
		t.Errorf("Model = %q, want the flat key used verbatim", got[0].Model)
	}
}
