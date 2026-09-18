package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Capability extraction. The load-bearing rule throughout is that absence means
// UNKNOWN, never false: the upstream price list omits a flag it has no
// information about, and that is the majority case (vision is recorded on under
// half its chat models). Collapsing "no data" into "no" is precisely the failure
// that made a client refuse to send an image, so it is what these tests pin.

func boolp(b bool) *bool { return &b }

// nump is the json.Number form the litellmEntry token-limit fields now take.
func nump(n string) json.Number { return json.Number(n) }

// TestInputModalitiesNormalizesSeveralFlagsToOne pins the collapsing: the
// upstream has three different flags that all mean "takes an image or a
// document", and they must arrive as one modality list so the internal
// vocabulary stays ours.
func TestInputModalitiesNormalizesSeveralFlagsToOne(t *testing.T) {
	cases := []struct {
		name string
		e    litellmEntry
		want []string
	}{
		{
			name: "vision flag alone",
			e:    litellmEntry{SupportsVision: boolp(true)},
			want: []string{"text", "image"},
		},
		{
			name: "image_input flag alone means the same thing",
			e:    litellmEntry{SupportsImageInput: boolp(true)},
			want: []string{"text", "image"},
		},
		{
			name: "vision and pdf together",
			e:    litellmEntry{SupportsVision: boolp(true), SupportsPDFInput: boolp(true)},
			want: []string{"text", "image", "file"},
		},
		{
			name: "pdf only",
			e:    litellmEntry{SupportsPDFInput: boolp(true)},
			want: []string{"text", "file"},
		},
		{
			name: "both image flags at once still yields one image entry",
			e:    litellmEntry{SupportsVision: boolp(true), SupportsImageInput: boolp(true)},
			want: []string{"text", "image"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inputModalities(tc.e); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("inputModalities = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestInputModalitiesAbsentMeansAbsent is the rule that matters most: a row
// stating nothing about capabilities must emit no modality list at all. Emitting
// ["text"] here would be a confident claim that a model is text-only, derived
// from nothing — and for the majority of upstream rows that claim would be
// wrong.
func TestInputModalitiesAbsentMeansAbsent(t *testing.T) {
	e := litellmEntry{LitellmProvider: "openai", Mode: "chat"}
	if got := inputModalities(e); got != nil {
		t.Errorf("inputModalities = %v, want nil for a row with no capability data", got)
	}
}

// TestInputModalitiesExplicitFalseIsNotTextOnly is the other half: an explicit
// false is information, and it must not be mistaken for absence. The model is
// known to lack vision — but the upstream has not said it is text-only, so no
// modality list is emitted for it either. False is not the same as "text".
func TestInputModalitiesExplicitFalseIsNotTextOnly(t *testing.T) {
	e := litellmEntry{
		SupportsVision:   boolp(false),
		SupportsPDFInput: boolp(false),
	}
	if got := inputModalities(e); got != nil {
		t.Errorf("inputModalities = %v, want nil (explicit false is not a modality claim)", got)
	}
}

// TestExtraMetadataOmitsUnstatedFlags proves the metadata map follows the same
// absence rule: only flags the upstream actually states are carried, so a client
// reading it cannot mistake silence for a false.
func TestExtraMetadataOmitsUnstatedFlags(t *testing.T) {
	e := litellmEntry{
		SupportsFunctionCalling: boolp(true),
		SupportsReasoning:       boolp(false),
		// prompt_caching / audio_input / computer_use / parallel_tool_calls
		// deliberately unstated.
	}
	got := extraMetadata(e)

	if v, ok := got["function_calling"]; !ok || v != true {
		t.Errorf("function_calling = %v (present=%v), want true", v, ok)
	}
	if v, ok := got["reasoning"]; !ok || v != false {
		t.Errorf("reasoning = %v (present=%v), want an explicit false carried through", v, ok)
	}
	for _, absent := range []string{"prompt_caching", "audio_input", "computer_use", "parallel_tool_calls"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%s present in metadata, want omitted (the upstream did not state it)", absent)
		}
	}
}

// TestExtraMetadataNilWhenNothingStated keeps an all-unknown row from emitting
// an empty map, which would be a field in the YAML saying nothing.
func TestExtraMetadataNilWhenNothingStated(t *testing.T) {
	if got := extraMetadata(litellmEntry{}); got != nil {
		t.Errorf("extraMetadata = %v, want nil", got)
	}
}

// TestBuildCatalogCarriesCapabilities is the end-to-end extraction: a row in the
// upstream list must reach the emitted catalog row with its modalities, limits
// and metadata intact.
func TestBuildCatalogCarriesCapabilities(t *testing.T) {
	entries := map[string]litellmEntry{
		"claude-sonnet-5": {
			LitellmProvider:         "anthropic",
			Mode:                    "chat",
			InputCostPerToken:       3e-06,
			OutputCostPerToken:      15e-06,
			SupportsVision:          boolp(true),
			SupportsPDFInput:        boolp(true),
			SupportsFunctionCalling: boolp(true),
			MaxInputTokens:          nump("200000"),
			MaxOutputTokens:         nump("64000"),
		},
	}
	m := mapping{Providers: map[string]providerMap{
		"claude": {LitellmProvider: "anthropic"},
	}}

	rows, _ := testBuild(entries, m, []string{"claude"})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	got := rows[0]

	if !reflect.DeepEqual(got.InputModalities, []string{"text", "image", "file"}) {
		t.Errorf("InputModalities = %v, want [text image file]", got.InputModalities)
	}
	if got.MaxInputTokens == nil || *got.MaxInputTokens != 200000 {
		t.Errorf("MaxInputTokens = %v, want 200000", got.MaxInputTokens)
	}
	if got.MaxOutputTokens == nil || *got.MaxOutputTokens != 64000 {
		t.Errorf("MaxOutputTokens = %v, want 64000", got.MaxOutputTokens)
	}
	if got.Metadata["function_calling"] != true {
		t.Errorf("Metadata = %v, want function_calling true", got.Metadata)
	}
}

// TestBuildCatalogLeavesUnknownRowsBare proves a model the upstream knows
// nothing about still produces a usable cost row — capabilities are additive,
// not a precondition.
func TestBuildCatalogLeavesUnknownRowsBare(t *testing.T) {
	entries := map[string]litellmEntry{
		"mystery-model": {
			LitellmProvider:    "openai",
			Mode:               "chat",
			InputCostPerToken:  1e-06,
			OutputCostPerToken: 2e-06,
		},
	}
	m := mapping{Providers: map[string]providerMap{
		"gpt4": {LitellmProvider: "openai"},
	}}

	rows, _ := testBuild(entries, m, []string{"gpt4"})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.InputModalities != nil {
		t.Errorf("InputModalities = %v, want nil (nothing was stated)", got.InputModalities)
	}
	if got.Metadata != nil {
		t.Errorf("Metadata = %v, want nil (nothing was stated)", got.Metadata)
	}
	if got.InputCostPerMTok != 1 {
		t.Errorf("InputCostPerMTok = %v, want 1 — capabilities are additive, the cost row still lands", got.InputCostPerMTok)
	}
}
