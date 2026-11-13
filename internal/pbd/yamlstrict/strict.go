package yamlstrict

import (
	"bytes"
	"errors"
	"reflect"

	"gopkg.in/yaml.v3"
)

// ErrDuplicateKey is returned when a YAML document contains duplicate keys.
var ErrDuplicateKey = errors.New("duplicate YAML key")

// ErrInvalidYAMLInput is returned when the input to LoadStrict is invalid.
var ErrInvalidYAMLInput = errors.New("invalid YAML input")

// LoadStrict parses YAML data into the destination out, enforcing strict validation.
// kind must be exactly "epic", "ticket", or "sport".
// data must not be nil, empty, or purely whitespace.
// out must be a non-nil pointer to a concrete type.
func LoadStrict(kind string, data []byte, out any) error {
	if kind != "epic" && kind != "ticket" && kind != "sport" {
		return ErrInvalidYAMLInput
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrInvalidYAMLInput
	}
	if out == nil {
		return ErrInvalidYAMLInput
	}

	v := reflect.ValueOf(out)
	if v.Kind() != reflect.Ptr {
		return ErrInvalidYAMLInput
	}
	if v.IsNil() {
		return ErrInvalidYAMLInput
	}

	// Strict parsing using yaml.Node to detect malformed YAML and duplicate keys.
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	err := dec.Decode(&doc)
	if err != nil {
		return ErrInvalidYAMLInput
	}

	// Check for multiple documents
	var doc2 yaml.Node
	if err := dec.Decode(&doc2); err == nil {
		return ErrInvalidYAMLInput
	}

	// Check for empty or null document
	if len(doc.Content) == 0 {
		return ErrInvalidYAMLInput
	}
	if doc.Content[0].Kind == yaml.ScalarNode && doc.Content[0].Tag == "!!null" {
		return ErrInvalidYAMLInput
	}

	// Validate the document structure (e.g. check for duplicate keys, non-scalar keys).
	if err := validateNode(&doc); err != nil {
		return err
	}

	// If all strict checks pass, decode into the actual destination.
	dec2 := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec2.Decode(out); err != nil {
		return ErrInvalidYAMLInput
	}

	return nil
}

func validateNode(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.MappingNode {
		seen := make(map[string]bool)
		for i := 0; i < len(n.Content); i += 2 {
			keyNode := n.Content[i]
			if keyNode.Kind != yaml.ScalarNode {
				return ErrInvalidYAMLInput
			}
			if seen[keyNode.Value] {
				return ErrDuplicateKey
			}
			seen[keyNode.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := validateNode(child); err != nil {
			return err
		}
	}
	return nil
}
