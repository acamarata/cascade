package yamlstrict

import (
	"errors"
	"testing"
)

// TestLoadStrictValidKinds verifies that LoadStrict succeeds for each valid kind.
func TestLoadStrictValidKinds(t *testing.T) {
	type sample struct {
		Key string `yaml:"key"`
	}
	kinds := []string{"epic", "ticket", "sport"}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			out := &sample{}
			err := LoadStrict(kind, []byte("key: value"), out)
			if err != nil {
				t.Fatalf("LoadStrict(%q) unexpected error: %v", kind, err)
			}
			if out.Key != "value" {
				t.Fatalf("LoadStrict(%q) Key = %q, want %q", kind, out.Key, "value")
			}
		})
	}
}

// TestLoadStrictNestedDuplicate verifies duplicate detection in nested mappings.
func TestLoadStrictNestedDuplicate(t *testing.T) {
	type inner struct {
		A string `yaml:"a"`
	}
	type outer struct {
		Nested inner `yaml:"nested"`
	}
	data := []byte("nested:\n  a: 1\n  a: 2\n")
	out := &outer{}
	err := LoadStrict("epic", data, out)
	if !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("expected ErrDuplicateKey, got %v", err)
	}
}

// TestLoadStrictDecodeFailure verifies that type mismatch during decode returns ErrInvalidYAMLInput.
func TestLoadStrictDecodeFailure(t *testing.T) {
	type sample struct {
		Num int `yaml:"num"`
	}
	data := []byte("num: notanumber\n")
	out := &sample{}
	err := LoadStrict("epic", data, out)
	if !errors.Is(err, ErrInvalidYAMLInput) {
		t.Fatalf("expected ErrInvalidYAMLInput, got %v", err)
	}
}

// TestCheckDuplicateKeysNilNode verifies walkNode handles nil input gracefully.
func TestCheckDuplicateKeysNilNode(t *testing.T) {
	err := CheckDuplicateKeys([]byte("null"))
	if err != nil {
		t.Fatalf("CheckDuplicateKeys(null) unexpected error: %v", err)
	}
}

// TestCheckDuplicateKeysSequence verifies walkNode traverses sequences.
func TestCheckDuplicateKeysSequence(t *testing.T) {
	data := []byte("- a: 1\n- b: 2\n")
	err := CheckDuplicateKeys(data)
	if err != nil {
		t.Fatalf("CheckDuplicateKeys sequence unexpected error: %v", err)
	}
}

// TestCheckDuplicateKeysInvalidYAML verifies malformed YAML returns wrapped ErrInvalidYAMLInput.
func TestCheckDuplicateKeysInvalidYAML(t *testing.T) {
	data := []byte("key: [unterminated")
	err := CheckDuplicateKeys(data)
	if !errors.Is(err, ErrInvalidYAMLInput) {
		t.Fatalf("expected ErrInvalidYAMLInput, got %v", err)
	}
}

// TestCheckDuplicateKeysAlias verifies walkNode follows aliases without infinite loops.
func TestCheckDuplicateKeysAlias(t *testing.T) {
	data := []byte("base: &anchor\n  x: 1\nderived:\n  <<: *anchor\n  y: 2\n")
	err := CheckDuplicateKeys(data)
	if err != nil {
		t.Fatalf("CheckDuplicateKeys alias unexpected error: %v", err)
	}
}

// TestCheckDuplicateKeysDuplicateInSequence verifies duplicate detection inside sequences.
func TestCheckDuplicateKeysDuplicateInSequence(t *testing.T) {
	data := []byte("- id: 1\n  id: 2\n")
	err := CheckDuplicateKeys(data)
	if !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("expected ErrDuplicateKey, got %v", err)
	}
}
