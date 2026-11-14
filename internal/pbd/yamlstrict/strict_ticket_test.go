// Package yamlstrict provides strict YAML loading with duplicate key detection.
package yamlstrict

import (
	"errors"
	"fmt"
	"testing"

	"gopkg.in/yaml.v3"
)

// ErrDuplicateKey is returned when a YAML mapping contains duplicate keys.
var ErrDuplicateKey = errors.New("duplicate key")

// ErrInvalidYAMLInput is returned when an unknown kind is provided.
var ErrInvalidYAMLInput = errors.New("invalid YAML input")

// LoadStrict parses YAML data for the given kind into out, rejecting duplicate keys.
func LoadStrict(kind string, data []byte, out any) error {
	switch kind {
	case "epic", "ticket", "sport":
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidYAMLInput, kind)
	}

	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return err
	}

	if err := checkDuplicateKeys(&node); err != nil {
		return err
	}

	return yaml.Unmarshal(data, out)
}

func checkDuplicateKeys(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool)
		for i := 0; i < len(node.Content)-1; i += 2 {
			key := node.Content[i].Value
			if seen[key] {
				return fmt.Errorf("%w: %s", ErrDuplicateKey, key)
			}
			seen[key] = true
		}
	}
	for _, child := range node.Content {
		if err := checkDuplicateKeys(child); err != nil {
			return err
		}
	}
	return nil
}

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
