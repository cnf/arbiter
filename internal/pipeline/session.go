package pipeline

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/cnf/arbiter/pkg/types"
)

// minSessionTextLen is the minimum amount of conversation text required
// before a content-derived session key is considered distinctive enough to
// pin on. Below this, two unrelated conversations (e.g. both opening with
// "hi", 2 chars) would collide, and pinning them together is worse than not
// pinning at all — so SessionKey reports ok=false instead.
//
// Deliberately measured on user+assistant text only, NOT on the system
// prompt: a configured system_prompt guardrail prepends a constant to
// SystemPrompt on every request, so counting it would make the gate pass
// unconditionally and defeat its purpose. The system prompt is still mixed
// into the hash (it adds distinctiveness when the client supplied a real
// one, and is harmless when it's the injected constant).
const minSessionTextLen = 16

// SessionKey derives the key used to look up/record a session's affinity
// pin. If hint (from an inbound header) is non-empty, it's used directly.
// Otherwise a key is derived by hashing the stable prefix of the
// conversation that exists on every turn: system prompt + the first
// text-bearing user turn. ok is false when there isn't enough conversation
// text to be distinctive — the caller must treat that as "never pin this
// request", which is the safe failure mode (a false pin merges unrelated
// conversations).
//
// Deliberately does NOT include the first assistant reply (an earlier draft
// did): the reply doesn't exist yet on the opening turn, so including it
// would make turn 1 hash differently from turn 2 onward and the pin
// recorded at turn 1 would never be reused. The first user turn is stable
// across the whole conversation, which is what pinning requires.
func SessionKey(hint string, req *types.NormalizedRequest) (key string, ok bool) {
	if hint != "" {
		return hint, true
	}

	userText := types.FirstUserText(req)
	if len(userText) < minSessionTextLen {
		return "", false
	}

	sum := sha256.Sum256([]byte(req.SystemPrompt + "\x00" + userText))
	return hex.EncodeToString(sum[:]), true
}
