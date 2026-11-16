package ledger

import (
	"errors"
	"os"
	"strings"
)

// ErrEmptyTranscript is returned when the transcript path or content is empty or whitespace-only.
var ErrEmptyTranscript = errors.New("empty transcript")

// ErrInvalidPath is returned when the path does not point to a readable regular file.
var ErrInvalidPath = errors.New("invalid path")

// ErrMalformedTranscript is returned when the transcript content is malformed.
var ErrMalformedTranscript = errors.New("malformed transcript")

// SportDocs reads a transcript file at the given path and returns its contents as bytes.
// An empty or whitespace-only path returns nil, ErrEmptyTranscript.
// A nonexistent, unreadable, or directory path returns nil, ErrInvalidPath.
// Empty or whitespace-only content returns nil, ErrEmptyTranscript.
func SportDocs(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrEmptyTranscript
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, ErrInvalidPath
	}

	if !info.Mode().IsRegular() {
		return nil, ErrInvalidPath
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrInvalidPath
	}

	if strings.TrimSpace(string(data)) == "" {
		return nil, ErrEmptyTranscript
	}

	return data, nil
}
