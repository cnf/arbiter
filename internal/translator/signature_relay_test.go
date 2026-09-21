package translator

import (
	"encoding/json"
	"testing"
)

// An Anthropic-format client talking to an Anthropic-wire upstream must get its
// thinking blocks back COMPLETE, signature included.
//
// The signature is not decoration: Anthropic requires a thinking block to be
// replayed with the signature it was issued with on the next request, and a
// client that loses it cannot continue the conversation. Dropping it is
// correct only on the OpenAI path, where the concept does not exist and a
// client cannot use it — on an anthropic-to-anthropic relay there is no
// translator to save it and the fingerprint is simply lost.
func TestSignatureDeltaSurvivesAnthropicToAnthropicRelay(t *testing.T) {
	const sig = "EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pki"

	raw := `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + sig + `"}}`
	var evt AnthropicStreamEvent
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	norm := AnthropicStreamEventToNormalized(&evt)
	if norm == nil {
		t.Fatal("signature_delta vanished inbound — it cannot then reach an Anthropic client")
	}

	// Round-trip back out to the Anthropic wire.
	out := NormalizedToAnthropicStreamEvent(norm)
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal outbound: %v", err)
	}
	var wire struct {
		Type  string `json:"type"`
		Delta *struct {
			Type      string `json:"type"`
			Signature string `json:"signature"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal outbound: %v", err)
	}
	if wire.Delta == nil || wire.Delta.Type != "signature_delta" {
		t.Fatalf("outbound delta = %s, want a signature_delta", b)
	}
	if wire.Delta.Signature != sig {
		t.Errorf("signature = %q, want %q — an Anthropic client cannot replay the thinking block without it",
			wire.Delta.Signature, sig)
	}
}