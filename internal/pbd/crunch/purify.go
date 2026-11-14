package crunch

import (
	"errors"
	"strings"
)

// ErrT0411 indicates that a purification stage was bypassed or an input was invalid.
var ErrT0411 = errors.New("T0411")

// Stage represents a single purification step that transforms a string.
type Stage func(string) (string, error)

// PurifyWebchat applies the redact stage then the sys1b stage to the given transcript.
// Both stages must be non-nil and the transcript must contain non-whitespace characters.
// If any stage is bypassed due to invalid inputs or blank outputs, ErrT0411 is returned.
func PurifyWebchat(transcript string, redact, sys1b Stage) (string, error) {
	if redact == nil || sys1b == nil {
		return "", ErrT0411
	}
	if strings.TrimSpace(transcript) == "" {
		return "", ErrT0411
	}

	redacted, err := redact(transcript)
	if err != nil {
		return "", ErrT0411
	}
	if strings.TrimSpace(redacted) == "" {
		return "", ErrT0411
	}

	sys1bOut, err := sys1b(redacted)
	if err != nil {
		return "", ErrT0411
	}
	if strings.TrimSpace(sys1bOut) == "" {
		return "", ErrT0411
	}

	return sys1bOut, nil
}
