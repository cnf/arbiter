package classifier

import "github.com/cnf/arbiter/pkg/types"

// defaultMaxInputChars is the cap applied to a classifier's outbound input when
// its config sets no `max_input_chars`.
//
// A classifier's input is one user message, not a conversation, so this is
// generous for the job — a rubric verdict needs a message, not a transcript.
// Unset meaning "unlimited" was the bug this exists to prevent: an uncapped
// input sent a 111KB turn to the upstream, which rejected it outright. Sized in
// characters rather than tokens because the only estimator in the tree is the
// documented char/4 heuristic (see estimateTokens), and expressing a cap in
// tokens would imply a precision nothing here has.
const defaultMaxInputChars = 8192

// classifierInput is the text a model-backed classifier classifies, capped.
//
// It is the FIRST user turn that actually has text — not the last. Three
// reasons, in order of weight:
//
//   - A classifier's subject is the request, and the request's subject is its
//     opening turn. Later turns are the conversation's history, not what the
//     request is about.
//   - The last user turn of an agentic request is routinely tool_result-only,
//     which carries no text at all. Reading it meant asking a model to classify
//     the empty string, and the model answered anyway — a fabricated verdict at
//     high confidence that rendered as a real one.
//   - It matches how the session key is derived (pipeline.SessionKey uses
//     types.FirstUserText), so a request's identity and its classification now
//     come from the same text instead of two different turns.
//
// The cap keeps the head and drops the tail, marking the cut, because what
// matters is the opening of a long first message. A cap of 0 means unlimited
// (see types.Ellipsize), which is what an unconfigured caller gets — but no
// classifier in a pipeline is built that way: see effectiveMaxInputChars.
func classifierInput(req *types.NormalizedRequest, maxChars int) string {
	return types.Ellipsize(types.FirstUserText(req), maxChars)
}

// effectiveMaxInputChars resolves a configured cap, defaulting an unset one.
//
// Negative means "explicitly unlimited" — the one way to opt out — so that an
// unset cap gets the safe default rather than silently meaning no limit. The
// distinction is the whole point: a config that forgets the field must not
// reproduce the uncapped behaviour the field exists to fix.
func effectiveMaxInputChars(configured int) int {
	switch {
	case configured > 0:
		return configured
	case configured < 0:
		return 0 // unlimited, requested explicitly
	default:
		return defaultMaxInputChars
	}
}
