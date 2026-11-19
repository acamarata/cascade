// Package yamlstrict provides strict YAML loading with duplicate key detection.
package yamlstrict

import (
	"errors"
	"testing"
)

// TestRejectsDuplicateTicketKey verifies that a nested duplicate title
// returns ErrDuplicateKey and leaves the destination unchanged.
func TestRejectsDuplicateTicketKey(t *testing.T) {
	out := map[string]string{"title": "original"}
	data := []byte("title: first\ntitle: second\n")
	err := LoadStrict("ticket", data, &out)
	if !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("expected ErrDuplicateKey, got %v", err)
	}
	if out["title"] != "original" {
		t.Fatalf("destination mutated: got %v", out)
	}
}

// TestAcceptsUniqueKeys verifies that epic, ticket and sport accept
// unique keys and replace a prefilled map with exactly id=a and title=t.
func TestAcceptsUniqueKeys(t *testing.T) {
	kinds := []string{"epic", "ticket", "sport"}
	data := []byte("id: a\ntitle: t\n")
	for _, kind := range kinds {
		out := map[string]string{"id": "old", "title": "old"}
		err := LoadStrict(kind, data, &out)
		if err != nil {
			t.Fatalf("LoadStrict(%q) returned error: %v", kind, err)
		}
		if len(out) != 2 || out["id"] != "a" || out["title"] != "t" {
			t.Fatalf("LoadStrict(%q) = %v, want id=a title=t", kind, out)
		}
	}
}
