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
