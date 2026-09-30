package types

import "fmt"

// ContentShapeError reports a safe structural diagnostic for a message
// `content` field that matched neither wire shape a client may send (a bare
// string or an array of content parts). It deliberately carries no value or
// message text — only the message's position, the field path, and the JSON
// type actually observed — so it can be logged and safely surfaced in
// structured fields without leaking conversation content. See #75.
type ContentShapeError struct {
	// MessageIndex is the message's position within the request's messages
	// array. It starts as -1 (unknown) when raised from inside a single
	// message's UnmarshalJSON, which has no visibility into its own index;
	// the request-level UnmarshalJSON is what fills it in, since only it
	// iterates the array.
	MessageIndex int
	// Field is the field path relative to the message, e.g. "content".
	Field string
	// GotType is the JSON type actually observed (e.g. "object", "number",
	// "boolean", "null") — never the value itself.
	GotType string
}

func (e *ContentShapeError) Error() string {
	return fmt.Sprintf("messages[%d].%s: got %s; expected string or array", e.MessageIndex, e.Field, e.GotType)
}

// jsonValueKind reports the JSON type of a trimmed value from its leading
// byte alone. It exists so a shape-mismatch diagnostic can name the type a
// client actually sent without unmarshaling (and thereby capturing) the
// value itself.
func jsonValueKind(trimmed []byte) string {
	if len(trimmed) == 0 {
		return "empty"
	}
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		if trimmed[0] == '-' || (trimmed[0] >= '0' && trimmed[0] <= '9') {
			return "number"
		}
		return "unknown"
	}
}
