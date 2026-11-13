// Package consult provides prompt phase orchestration utilities.
package consult

import (
	"errors"
	"strings"
)

// ErrPrePromptingFailed is returned when pre-prompting validation fails.
var ErrPrePromptingFailed = errors.New("pre-prompting failed")

// ErrInvalidGenerator is returned when the generator function is nil.
var ErrInvalidGenerator = errors.New("invalid generator")

// Generator is a function that processes a prompt string and returns a result or an error.
type Generator func(string) (string, error)

// PostResult holds the output of a completed prompt phase run.
type PostResult struct {
	// Output is the primary generated text.
	Output string
	// Summary is a condensed version of the output.
	Summary string
	// Extras contains any additional metadata or supplementary text.
	Extras string
}

// RunPromptPhases validates inputs, formats the prompt with allowed context and rule,
// and invokes the generator. It returns ErrPrePromptingFailed if any input is blank
// after trimming whitespace.
func RunPromptPhases(allowedContext, rule, prompt string, generate Generator) (PostResult, error) {
	if strings.TrimSpace(allowedContext) == "" || strings.TrimSpace(rule) == "" || strings.TrimSpace(prompt) == "" {
		return PostResult{}, ErrPrePromptingFailed
	}
	if generate == nil {
		return PostResult{}, ErrInvalidGenerator
	}

	formatted := "[" + allowedContext + "] [" + rule + "] " + prompt

	output, err := generate(formatted)
	if err != nil {
		return PostResult{}, err
	}

	return PostResult{
		Output:  strings.TrimSpace(output),
		Summary: "cleanup done",
		Extras:  "post-complete",
	}, nil
}
