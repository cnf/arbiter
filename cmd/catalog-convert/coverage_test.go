package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/config"
)

// Coverage classification. The load-bearing distinction is the third state: a
// declared model with no data is only a FAILURE if the operator did not declare
// it expected. Without that split the strict gate is either always-green (so it
// catches nothing) or always-red (so nobody reads it).

func rowsOf(pairs ...string) []config.ModelCatalogEntry {
	var out []config.ModelCatalogEntry
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, config.ModelCatalogEntry{Provider: pairs[i], Model: pairs[i+1]})
	}
	return out
}

// TestCoverageClassifiesMatchedAndUnexpected is the base case: a declared model
// with a row matches, one without and not listed as manual is unexpected.
func TestCoverageClassifiesMatchedAndUnexpected(t *testing.T) {
	declared := map[string][]string{
		"claude": {"claude/claude-sonnet-5", "claude/claude-missing"},
	}
	rows := rowsOf("claude", "claude/claude-sonnet-5")

	rep := buildCoverage(declared, rows, nil)
	c := rep.Providers["claude"]
	if len(c.Matched) != 1 || c.Matched[0] != "claude/claude-sonnet-5" {
		t.Errorf("Matched = %v, want [claude/claude-sonnet-5]", c.Matched)
	}
	if len(c.Unexpected) != 1 || c.Unexpected[0] != "claude/claude-missing" {
		t.Errorf("Unexpected = %v, want [claude/claude-missing]", c.Unexpected)
	}
	if rep.Totals.Unexpected != 1 {
		t.Errorf("Totals.Unexpected = %d, want 1", rep.Totals.Unexpected)
	}
}

// TestCoverageManualIsNotAFailure is the whole reason manual: exists. A model the
// operator knows is unmatchable must be reported without failing the run, or the
// gate goes red forever and stops being read.
func TestCoverageManualIsNotAFailure(t *testing.T) {
	declared := map[string][]string{
		"openrouter": {"openrouter/@preset/deepseek-flash"},
	}

	rep := buildCoverage(declared, nil, []string{"openrouter/@preset/deepseek-flash"})
	c := rep.Providers["openrouter"]
	if len(c.Manual) != 1 {
		t.Errorf("Manual = %v, want the preset listed as manual", c.Manual)
	}
	if len(c.Unexpected) != 0 {
		t.Errorf("Unexpected = %v, want none — a manual entry must not be a failure", c.Unexpected)
	}
	if rep.Totals.Unexpected != 0 {
		t.Errorf("Totals.Unexpected = %d, want 0", rep.Totals.Unexpected)
	}
}

// TestCoverageManualEntryThatActuallyMatchedIsHarmless proves a stale manual:
// entry does not break anything: if the model starts matching, it reports as
// matched. The list is a statement of expectation, not an override.
func TestCoverageManualEntryThatActuallyMatchedIsHarmless(t *testing.T) {
	declared := map[string][]string{
		"claude": {"claude/claude-sonnet-5"},
	}
	rows := rowsOf("claude", "claude/claude-sonnet-5")

	rep := buildCoverage(declared, rows, []string{"claude/claude-sonnet-5"})
	c := rep.Providers["claude"]
	if len(c.Matched) != 1 {
		t.Errorf("Matched = %v, want the model classified as matched", c.Matched)
	}
	if len(c.Manual) != 0 || len(c.Unexpected) != 0 {
		t.Errorf("Manual = %v, Unexpected = %v, want both empty", c.Manual, c.Unexpected)
	}
}

// TestCoverageMatchIsExact is the property that keeps the report honest: it must
// classify using the same exact comparison Arbiter's lookup makes, or the report
// could claim a model is covered while the runtime finds nothing.
func TestCoverageMatchIsExact(t *testing.T) {
	declared := map[string][]string{
		"claude": {"claude/claude-sonnet-5"},
	}
	// A bare row exists, but the declared name is namespaced — these must NOT be
	// considered a match, which is precisely the bug this whole change fixes.
	rows := rowsOf("claude", "claude-sonnet-5")

	rep := buildCoverage(declared, rows, nil)
	if got := rep.Providers["claude"].Unexpected; len(got) != 1 {
		t.Errorf("Unexpected = %v, want the model unmatched — a bare row must not satisfy a namespaced declaration", got)
	}
}

// TestCoverageCrossProviderIsNotAMatch guards against a provider-blind match:
// the same model name under a different provider is a different row.
func TestCoverageCrossProviderIsNotAMatch(t *testing.T) {
	declared := map[string][]string{
		"claude": {"claude/claude-sonnet-5"},
	}
	rows := rowsOf("openrouter", "claude/claude-sonnet-5")

	rep := buildCoverage(declared, rows, nil)
	if got := rep.Providers["claude"].Unexpected; len(got) != 1 {
		t.Errorf("Unexpected = %v, want unmatched — a row under another provider must not count", got)
	}
}

// TestRenderCoverageJSONIsParsable is the machine-parsable requirement: a cron
// must be able to read this without scraping prose.
func TestRenderCoverageJSONIsParsable(t *testing.T) {
	declared := map[string][]string{
		"claude":     {"claude/claude-sonnet-5", "claude/claude-missing"},
		"openrouter": {"openrouter/@preset/comp"},
	}
	rows := rowsOf("claude", "claude/claude-sonnet-5")
	rep := buildCoverage(declared, rows, []string{"openrouter/@preset/comp"})

	var buf bytes.Buffer
	if err := renderCoverage(&buf, rep, "json"); err != nil {
		t.Fatalf("renderCoverage json: %v", err)
	}

	var got coverageReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("coverage json is not parsable: %v\n%s", err, buf.String())
	}
	if got.Totals.Declared != 3 || got.Totals.Matched != 1 || got.Totals.Manual != 1 || got.Totals.Unexpected != 1 {
		t.Errorf("totals = %+v, want declared 3 / matched 1 / manual 1 / unexpected 1", got.Totals)
	}
	if len(got.Providers["claude"].Unexpected) != 1 {
		t.Errorf("per-provider detail missing from json: %+v", got.Providers)
	}
}

// TestRenderCoverageNoneIsSilent supports -coverage none, for a caller that only
// wants the strict gate's exit code.
func TestRenderCoverageNoneIsSilent(t *testing.T) {
	var buf bytes.Buffer
	if err := renderCoverage(&buf, coverageReport{}, "none"); err != nil {
		t.Fatalf("renderCoverage none: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("output = %q, want empty", buf.String())
	}
}

// TestRenderCoverageRejectsUnknownFormat proves a typo'd format fails loudly
// rather than silently printing nothing, which would look like "no problems".
func TestRenderCoverageRejectsUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := renderCoverage(&buf, coverageReport{}, "yaml"); err == nil {
		t.Error("expected an error for an unknown coverage format")
	}
}

// TestRenderCoverageTextNamesUnexpected proves the text form surfaces the
// failure prominently — it is what a human reads when the cron goes red.
func TestRenderCoverageTextNamesUnexpected(t *testing.T) {
	declared := map[string][]string{"claude": {"claude/claude-missing"}}
	rep := buildCoverage(declared, nil, nil)

	var buf bytes.Buffer
	if err := renderCoverage(&buf, rep, "text"); err != nil {
		t.Fatalf("renderCoverage text: %v", err)
	}
	if !strings.Contains(buf.String(), "UNEXPECTED") || !strings.Contains(buf.String(), "claude/claude-missing") {
		t.Errorf("text report does not name the failure:\n%s", buf.String())
	}
}

// TestDeclaredModelsReadsTheConfigList proves the coverage input comes from the
// config's own models: lists, which is what makes the report about what the
// operator actually declared rather than what the catalog happens to contain.
func TestDeclaredModelsReadsTheConfigList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "arbiter.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  claude:
    type: "anthropic"
    endpoint: "https://example.invalid"
    key: "not-a-real-key"
    models:
      - "claude/claude-sonnet-5"
      - "claude/claude-opus-5"
  openrouter:
    models: ["openrouter/free"]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := declaredModels(path)
	if err != nil {
		t.Fatalf("declaredModels: %v", err)
	}
	if len(got["claude"]) != 2 {
		t.Errorf("claude models = %v, want 2", got["claude"])
	}
	if len(got["openrouter"]) != 1 || got["openrouter"][0] != "openrouter/free" {
		t.Errorf("openrouter models = %v, want [openrouter/free]", got["openrouter"])
	}
}

// TestFloatTokenLimitsDoNotDropTheEntry is a regression test for a real data-loss
// bug the live run exposed: the upstream list carries some token limits as
// floats (xai's grok entries say 2000000.0). With a typed int field the WHOLE
// entry fails to decode, so the model is dropped entirely — losing its cost and
// capabilities along with the limit — and the only symptom is an "undecodable
// entry" line that reads like upstream corruption rather than a field-type
// mismatch on our side. 14 chat-mode models were being lost this way.
func TestFloatTokenLimitsDoNotDropTheEntry(t *testing.T) {
	raw := `{
	  "xai/grok-4-fast": {
	    "litellm_provider": "xai",
	    "mode": "chat",
	    "input_cost_per_token": 1.25e-06,
	    "max_input_tokens": 2000000.0,
	    "max_output_tokens": 2000000.0
	  }
	}`
	entries, malformed, err := parseLitellm(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parseLitellm: %v", err)
	}
	if len(malformed) != 0 {
		t.Fatalf("entry dropped as malformed: %v — a float token limit must not make the whole entry undecodable", malformed)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	m := mapping{Providers: map[string]providerMap{"xai": {LitellmProvider: "xai"}}}
	rows, _ := testBuild(entries, m, []string{"xai"})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 — the model must survive with its cost", len(rows))
	}
	if rows[0].InputCostPerMTok != 1.25 {
		t.Errorf("InputCostPerMTok = %v, want 1.25 (cost must not be lost with the limit)", rows[0].InputCostPerMTok)
	}
	if rows[0].MaxInputTokens == nil || *rows[0].MaxInputTokens != 2000000 {
		t.Errorf("MaxInputTokens = %v, want 2000000", rows[0].MaxInputTokens)
	}
}

// TestTokenLimitRejectsNonWholeNumber pins the other half: a genuinely
// fractional limit is treated as absent rather than rounded. A token limit is
// not an estimate, and inventing one would put a wrong figure behind a
// client-visible field.
func TestTokenLimitRejectsNonWholeNumber(t *testing.T) {
	if got := tokenLimit(nump("1500.5")); got != nil {
		t.Errorf("tokenLimit(1500.5) = %v, want nil (absent, not rounded)", *got)
	}
	if got := tokenLimit(nump("")); got != nil {
		t.Errorf("tokenLimit(empty) = %v, want nil", *got)
	}
	if got := tokenLimit(nump("1500")); got == nil || *got != 1500 {
		t.Errorf("tokenLimit(1500) = %v, want 1500", got)
	}
}
