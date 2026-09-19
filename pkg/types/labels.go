package types

import (
	"fmt"
	"strings"
)

// Label is one category a classifier may assign, with an optional rubric
// description. The description is where the category's *boundary* lives: a bare
// name ("code_generation") tells the model nothing about where that category
// ends and "reasoning" begins, while "the user wants code written, modified,
// refactored, or reviewed — an implementation task with a concrete code artifact
// as the answer" does. Description is empty for a label configured as a bare
// name, which is what makes the list form a strict subset of the map form rather
// than a second concept.
type Label struct {
	Name        string
	Description string
}

// ParseLabels reads a classifier's `labels:` field in either accepted shape:
//
//	labels: ["code_generation", "chat"]     # bare names, the original form
//	labels:                                  # name -> rubric description
//	  code_generation: "the user wants code written, modified, refactored..."
//	  chat: "greeting, small talk, or a question with no artifact expected."
//
// Both produce the same []Label, so nothing downstream sees two shapes. A
// YAML-decoded value arrives as []interface{} or map[string]interface{}; a nil
// value yields no labels rather than an error, so "absent" stays distinguishable
// from "present but empty" at the caller.
//
// This lives in the types package because it is the classifier's label
// vocabulary, and the two readers of that field — config validation and the main
// wiring's classifier construction — can each reach types but not one another
// (nothing under internal/ imports internal/config). One parser, so validation
// cannot accept a form the builder silently drops.
func ParseLabels(raw interface{}) ([]Label, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil

	case []interface{}:
		out := make([]Label, 0, len(v))
		for _, item := range v {
			name, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("every entry must be a label name, got %T", item)
			}
			out = append(out, Label{Name: name})
		}
		return out, nil

	case map[string]interface{}:
		out := make([]Label, 0, len(v))
		for name, desc := range v {
			s, ok := desc.(string)
			if !ok {
				return nil, fmt.Errorf("description for label %q must be a string, got %T", name, desc)
			}
			out = append(out, Label{Name: name, Description: s})
		}
		return out, nil

	default:
		return nil, fmt.Errorf("want a list of label names or a map of name -> description, got %T", raw)
	}
}

// ScoreLevels reads a decision question's `levels:` field: the ordered levels
// of a `score` question, low -> high.
//
//	levels: ["easy", "medium", "hard"]
//	levels:                                  # a description is optional
//	  - name: "easy"
//	    description: "a one-liner or a lookup"
//	  - name: "hard"
//
// ORDER IS THE DATA here, which is why a map is rejected outright rather than
// accepted like ParseLabels' map form: Go map iteration is randomized, so
// accepting one would silently pick a level order — and a score's whole meaning
// is its position on that order. A list is the only shape that can carry it.
type ScoreLevel struct {
	Name        string
	Description string
}

// ParseScoreLevels reads the levels field in either accepted list shape.
func ParseScoreLevels(raw interface{}) ([]ScoreLevel, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil

	case []interface{}:
		out := make([]ScoreLevel, 0, len(v))
		for _, item := range v {
			switch e := item.(type) {
			case string:
				out = append(out, ScoreLevel{Name: e})
			case map[string]interface{}:
				name, _ := e["name"].(string)
				if name == "" {
					return nil, fmt.Errorf("every level needs a non-empty \"name\"")
				}
				desc, _ := e["description"].(string)
				if raw, ok := e["description"]; ok {
					if _, isStr := raw.(string); !isStr {
						return nil, fmt.Errorf("description for level %q must be a string, got %T", name, raw)
					}
				}
				out = append(out, ScoreLevel{Name: name, Description: desc})
			default:
				return nil, fmt.Errorf("every level must be a name or a name+description map, got %T", item)
			}
		}
		return out, nil

	case map[string]interface{}:
		return nil, fmt.Errorf("levels must be a list, not a map — a score's order is its meaning, and a map has none")

	default:
		return nil, fmt.Errorf("want a list of level names, got %T", raw)
	}
}

// FindLabel returns the label matching name case-insensitively, with its
// canonically-configured spelling — a model's own casing (or an operator's typo
// in a reference) must never become the stored value.
func FindLabel(labels []Label, name string) (Label, bool) {
	for _, l := range labels {
		if strings.EqualFold(l.Name, name) {
			return l, true
		}
	}
	return Label{}, false
}
