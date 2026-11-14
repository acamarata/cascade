// Package log provides egress refusal handling and turn retry logic.
package log

import "errors"

// RefusalError represents a refusal from the egress layer.
type RefusalError struct {
	// Reason describes why the egress was refused.
	Reason string
}

// Error returns the string representation of the refusal error.
func (e RefusalError) Error() string {
	return "egress refusal: " + e.Reason
}

// TurnFunc is an injected function that executes a single turn with the given prompt.
type TurnFunc func(prompt string) (string, error)

// TurnResult holds the outcome of a Reask invocation.
type TurnResult struct {
	// Output contains the successful response text.
	Output string
	// OK indicates the turn completed successfully.
	OK bool
	// Failed indicates the turn failed without further retries.
	Failed bool
	// Continue indicates the caller should continue processing.
	Continue bool
	// Err holds any error encountered during the turn.
	Err error
}

// PlainProseNote is the default instruction appended to reask prompts.
const PlainProseNote = "Please answer in plain prose without encoded or high-entropy content."

// MaxReasks is the maximum number of additional attempts allowed after an initial failure.
const MaxReasks = 2

// ErrInvalidTurn is returned when a nil TurnFunc is provided to Reask.
var ErrInvalidTurn = errors.New("invalid turn")

// ErrInvalidPrompt is returned when an empty prompt is provided to Reask.
var ErrInvalidPrompt = errors.New("invalid prompt")

// Reask executes a turn function with the given prompt, validating inputs before execution.
// It returns immediately with an error if the turn function is nil or the prompt is empty.
func Reask(turn TurnFunc, prompt string) TurnResult {
	if turn == nil {
		return TurnResult{
			Failed:   true,
			Continue: true,
			Err:      ErrInvalidTurn,
		}
	}
	if prompt == "" {
		return TurnResult{
			Failed:   true,
			Continue: true,
			Err:      ErrInvalidPrompt,
		}
	}

	output, err := turn(prompt)
	if err != nil {
		return TurnResult{
			Failed:   true,
			Continue: false,
			Err:      err,
		}
	}

	return TurnResult{
		Output:   output,
		OK:       true,
		Failed:   false,
		Continue: false,
		Err:      nil,
	}
}
