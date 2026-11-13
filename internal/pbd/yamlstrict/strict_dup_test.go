package yamlstrict

import (
	"errors"
	"testing"
)

// TestDuplicateErrorNamesKeyAndLine verifies that a duplicate key reports the
// correct key name and 1-based line of its second occurrence.
func TestDuplicateErrorNamesKeyAndLine(t *testing.T) {
	data := []byte("id: 1\nid: 2\n")
	err := CheckDuplicateKeys(data)
	if err == nil {
		t.Fatal("expected duplicate key error")
	}
	if !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("expected ErrDuplicateKey, got %v", err)
	}
	expected := `duplicate YAML key: key "id" at line 2`
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}
