package pipeline

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/cnf/arbiter/pkg/types"
)

// minSessionContentLen is the minimum amount of conversation text required
// before a content-derived session key is considered distinctive enough to
// pin on. Below this, two unrelated conversations (e.g. both opening with
// "hi", 2 chars) would collide, and pinning them together is worse than not
// pinning at all — so SessionKey reports ok=false instead.
//
// It is measured over the system prompt PLUS the first text-bearing user turn,
// and either may carry the whole budget. A real client's system prompt is
// thousands of characters (an agent CLI's house prompt), while its opening user
// turn is often a single word — measuring only the user turn left such a
// conversation unpinnable for its entire life, because the first user turn is
// the same short string on turn 1 and on turn 40. Hashing the two together
// keeps both sources of distinctiveness: the user turn separates two
// conversations that share a house prompt, and the system prompt separates two
// that share a terse opener.
//
// The threshold is a multi-tenant defence — merging two unrelated
// conversations is worse than not pinning — and this deployment is not that:
// one user, a handful of clients, low concurrency. It is therefore kept low
// enough to pass a real terse opener backed by a real system prompt, while
// still refusing a bare "hi" with nothing else.
const minSessionContentLen = 16

// SessionKey derives the key used to look up/record a session's affinity
// pin. If hint (from an inbound header) is non-empty, it's used directly.
// Otherwise a key is derived by hashing the stable prefix of the
// conversation that exists on every turn: system prompt + the first
// text-bearing user turn. ok is false when there isn't enough conversation
// text to be distinctive — the caller must treat that as "never pin this
// request", which is the safe failure mode (a false pin merges unrelated
// conversations).
//
// Must be called on the request as it arrived, BEFORE pre-guardrails: a
// guardrail like system_prompt prepends a constant to SystemPrompt, and hashing
// after it would mix Arbiter's own text into every key — and would change every
// key at once whenever that guardrail's prompt is edited, invalidating every
// live pin in a single step. The caller passes the client's own text.
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
	if len(req.SystemPrompt)+len(userText) < minSessionContentLen {
		return "", false
	}

	sum := sha256.Sum256([]byte(req.SystemPrompt + "\x00" + userText))
	return hex.EncodeToString(sum[:]), true
}
