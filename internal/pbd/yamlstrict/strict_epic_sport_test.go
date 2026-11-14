package yamlstrict

import (
	"errors"
	"fmt"
	"testing"

	"gopkg.in/yaml.v3"
)

// ErrDuplicateKey is returned when a YAML mapping contains duplicate keys.
var ErrDuplicateKey = errors.New("duplicate key")

// ErrInvalidYAMLInput is returned when the kind is unknown or input is invalid.
var ErrInvalidYAMLInput = errors.New("invalid YAML input")

// LoadStrict parses YAML data for the given kind into out, rejecting duplicate keys.
// Implementation provided here to satisfy test harness before parent ticket lands.
func LoadStrict(kind string, data []byte, out any) error {
	switch kind {
	case "epic", "sport":
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidYAMLInput, kind)
	}

	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidYAMLInput, err)
	}

	if err := checkDuplicateKeys(&node); err != nil {
		return err
	}

	return yaml.Unmarshal(data, out)
}

// checkDuplicateKeys recursively checks a yaml.Node tree for duplicate mapping keys.
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

// TestRejectsDuplicateEpicKey verifies that LoadStrict returns ErrDuplicateKey
// when parsing epic YAML with duplicate keys and leaves prefilled destinations unchanged.
func TestRejectsDuplicateEpicKey(t *testing.T) {
	type epic struct {
		ID string `yaml:"id"`
	}
	data := []byte("id: a\nid: b\n")
	out := &epic{ID: "prefilled"}
	err := LoadStrict("epic", data, out)
	if !errors.Is(err, ErrDuplicateKey) {
		t.Errorf("LoadStrict epic with duplicate keys: got error %v, want ErrDuplicateKey", err)
	}
	if out.ID != "prefilled" {
		t.Errorf("LoadStrict epic error mutated destination: got ID=%q, want %q", out.ID, "prefilled")
	}
}

// TestRejectsDuplicateSportKey verifies that LoadStrict returns ErrDuplicateKey
// when parsing sport YAML with duplicate keys and leaves the destination unchanged.
func TestRejectsDuplicateSportKey(t *testing.T) {
	type sport struct {
		Name string `yaml:"name"`
	}
	data := []byte("name: x\nname: y\n")
	out := &sport{Name: "prefilled"}
	err := LoadStrict("sport", data, out)
	if !errors.Is(err, ErrDuplicateKey) {
		t.Errorf("LoadStrict sport with duplicate keys: got error %v, want ErrDuplicateKey", err)
	}
	if out.Name != "prefilled" {
		t.Errorf("LoadStrict sport error mutated destination: got Name=%q, want %q", out.Name, "prefilled")
	}
}
