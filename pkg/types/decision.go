package types

// DecisionRequest is the body sent to a decision-model endpoint (TypeSafe's
// System One / Jev, served by OpenRouter at /api/alpha/decisions).
//
// It is deliberately NOT an OpenAI-shaped chat request: the endpoint takes a
// `state` plus a set of typed `questions` and answers them all in one round
// trip. Modelling it as a chat completion would mean inventing a
// NormalizedResponse that carries JSON in a text block — a masquerade that
// would then have to be seen through by every reader of the store.
type DecisionRequest struct {
	// Model is the decision-model slug, e.g. "~typesafe/jev-latest". A
	// "~author/family-latest" slug resolves upstream to a concrete version,
	// which the response reports back in DecisionResponse.Model.
	Model string `json:"model"`
	// State is what the questions are asked about. It may be a string, an
	// object or an array; this field carries whichever the caller built.
	State interface{} `json:"state"`
	// Questions maps a caller-chosen name to one typed question. Every
	// question is answered in the same call.
	Questions map[string]DecisionQuestion `json:"questions"`
}

// DecisionQuestion is one typed question. Type selects which primitive is
// asked, and the remaining fields are that primitive's own vocabulary — the
// three do not share a criteria shape, so this is a union by convention
// rather than three Go types (the wire shape is a flat object either way).
type DecisionQuestion struct {
	// Type is "choice", "score" or "noul".
	Type string `json:"type"`
	// Instructions is an optional natural-language statement of the question.
	// Optional on the wire for all three primitives, but a Choice or Score
	// is close to useless without it: `criteria` says what the options are,
	// `instructions` says what is being decided between them.
	Instructions string `json:"instructions,omitempty"`

	// Criteria is the option set for a "choice" (map of label ->
	// description) or the ordered levels for a "score" (a list, low -> high).
	// Unused by "noul".
	//
	// A map and a list are both valid here because the primitives genuinely
	// differ: a choice is unordered (the model picks one, and the response
	// carries a probability per option), while a score is ordered and its
	// answer is a fractional position along that order. Go cannot express
	// that as one typed field, so the caller supplies the shape its primitive
	// needs and config validation enforces it per type.
	Criteria interface{} `json:"criteria,omitempty"`
}

// The three primitives. Named so a caller does not have to spell a bare
// string, and so config validation and construction cannot drift apart.
const (
	DecisionChoice = "choice"
	DecisionScore  = "score"
	DecisionNoul   = "noul"
)

// DecisionResponse is what a decision-model call returns: one answer per
// question asked, all in the same body.
type DecisionResponse struct {
	// Model is the concrete model that served the request. For a
	// "~family-latest" slug this is how the caller learns which version
	// actually answered — the same transparent-reporting role actual_model
	// plays for a meta-router alias like openrouter/auto.
	Model   string                    `json:"model"`
	Answers map[string]DecisionAnswer `json:"answers"`
	Usage   DecisionUsage             `json:"usage"`

	// Raw is the response body exactly as the endpoint returned it. Not part
	// of the wire shape (never marshalled back) — it is carried so a stored
	// call record can hold what actually came back, which is the only thing
	// that debugs a wrong verdict when the typed fields disagree with it.
	Raw string `json:"-"`
}

// DecisionUsage is the decisions endpoint's usage shape.
//
// Its own type rather than types.Usage, because that one is the internal
// representation and carries no JSON tags at all — decoding this endpoint's
// snake_case `input_tokens` into it silently yielded zero, which is the same
// invisible-usage failure the streaming path once had. Naming the wire shape
// explicitly is what makes that a compile-time mismatch instead of a silent
// zero.
type DecisionUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Normalized converts the wire usage into the internal representation.
func (u DecisionUsage) Normalized() Usage {
	return Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}
}

// DecisionAnswer is one question's answer. Which fields are populated depends
// on the question's type, and the zero values of the others are meaningful
// only as "this primitive did not answer here".
type DecisionAnswer struct {
	// Type echoes the question's type, so an answer is self-describing in a
	// stored rationale without the question beside it.
	Type string `json:"type"`

	// Noul is the yes/no primitive's answer: the probability of "yes", 0..1.
	// A Noul answer carries NO confidence field — a confident "no" and a
	// confident "yes" are equally confident, so a caller that wants a gate
	// derives one (max(p, 1-p), which never falls below 0.5).
	Noul float64 `json:"noul,omitempty"`

	// Choice is the option the model picked, for a "choice" question. The
	// full distribution is in Probabilities; Confidence is how peaked that
	// distribution is on the winner.
	Choice string `json:"choice,omitempty"`

	// Score is the position on a "score" question's ordered levels, as a
	// FRACTIONAL value between levels (e.g. 1.6 sits between level 1 and
	// level 2). The fractional part is the point of Score over Choice for
	// anything thresholded or averaged. Legend maps each index back to the
	// level name it came from.
	Score  float64           `json:"score,omitempty"`
	Legend map[string]string `json:"legend,omitempty"`

	// Confidence is how peaked the distribution is on the answer, 0..1.
	//
	// Deliberately NOT read as "probability this is correct": it is a
	// property of the distribution the model returned for this piece of
	// state, not a calibration guarantee. It is usable as a relative
	// act-vs-escalate gate (a 0.98 pick is safe to automate, a 0.51 with
	// 0.47 on the runner-up is a coin flip), which is the only claim this
	// codebase makes about it.
	Confidence float64 `json:"confidence,omitempty"`

	// Probabilities is the full distribution: option -> probability for a
	// "choice" (summing to 1), level index -> probability for a "score".
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}
