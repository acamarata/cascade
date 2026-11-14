package log

import (
	"errors"
)

// Request represents a request to the ask operation.
type Request struct {
	Prompt string
}

// AskRunner is a function that takes a Request and returns a response string or an error.
type AskRunner func(Request) (string, error)

// AskBuildsRequestFromPrompt creates a Request from the given prompt and passes it to the runner.
// It returns an error if the prompt is empty.
func AskBuildsRequestFromPrompt(prompt string, runner AskRunner) (string, error) {
	if prompt == "" {
		return "", errors.New("empty prompt")
	}
	req := Request{Prompt: prompt}
	return runner(req)
}
