package docket

import (
	"errors"
	"testing"
)

// TestValidatePlainTextRejectsMarkdownFence verifies that ValidatePlainText
// rejects strings containing Markdown ATX headings or fenced code blocks at
// the start of a line, while accepting strings that only look similar but
// fall outside the exact rejection regexp.
func TestValidatePlainTextRejectsMarkdownFence(t *testing.T) {
	shouldReject := []string{
		"```go",
		"~~~go",
		"# heading",
		" ## heading",
		"text\n# heading",
	}
	for _, v := range shouldReject {
		err := ValidatePlainText("title", v)
		if !errors.Is(err, ErrPlainText) {
			t.Errorf("expected ErrPlainText for %q, got %v", v, err)
		}
	}

	shouldAccept := []string{
		"#hashtag",
		"    # heading",
	}
	for _, v := range shouldAccept {
		err := ValidatePlainText("title", v)
		if err != nil {
			t.Errorf("expected nil for %q, got %v", v, err)
		}
	}
}
