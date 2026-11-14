package background

import (
	"errors"
	"strings"
)

// ModeFullAutonomy enables the resolver to use sensible defaults when
// a recorded intent is not found.
const ModeFullAutonomy = "bypass"

// ErrNeedsUser is returned when a question cannot be answered without
// interactive user input.
var ErrNeedsUser = errors.New("question requires user input")

// ErrInvalidQuestion is returned when a question or default is malformed.
var ErrInvalidQuestion = errors.New("invalid question or default")

// Resolver answers configuration questions using recorded intents and,
// when enabled, sensible defaults.
type Resolver struct {
	mode         string
	intents      map[string]string
	defaults     map[string]string
	defaultOrder []string
}

// NewResolver returns a Resolver configured with the given mode and
// a copy of the provided intent map. Blank keys and values are ignored.
func NewResolver(mode string, intent map[string]string) *Resolver {
	r := &Resolver{
		mode:         mode,
		intents:      make(map[string]string),
		defaults:     make(map[string]string),
		defaultOrder: []string{},
	}
	for k, v := range intent {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			continue
		}
		r.intents[k] = v
	}
	return r
}

// ResolveQuestion returns the recorded answer for question, or a sensible
// default if the resolver is in full autonomy mode. It returns ErrNeedsUser
// if the answer is unknown and autonomy is disabled.
func (r *Resolver) ResolveQuestion(question string, sensibleDefault string) (string, error) {
	if r == nil {
		return "", ErrInvalidQuestion
	}
	if question == "" || strings.TrimSpace(question) != question {
		return "", ErrInvalidQuestion
	}

	if ans, ok := r.intents[question]; ok {
		return ans, nil
	}

	if r.mode == ModeFullAutonomy {
		if prior, ok := r.defaults[question]; ok {
			return prior, nil
		}
		if strings.TrimSpace(sensibleDefault) == "" {
			return "", ErrInvalidQuestion
		}
		r.defaults[question] = sensibleDefault
		r.defaultOrder = append(r.defaultOrder, question)
		return sensibleDefault, nil
	}

	return "", ErrNeedsUser
}
