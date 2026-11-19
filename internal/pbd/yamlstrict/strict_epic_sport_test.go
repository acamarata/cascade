package yamlstrict

import (
	"errors"
	"testing"
)

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
