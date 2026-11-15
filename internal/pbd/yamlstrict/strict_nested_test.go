package yamlstrict

import (
	"errors"
	"reflect"
	"testing"
)

// nestedShape is the destination for the nil and empty collection cases.
type nestedShape struct {
	Name  string            `yaml:"name"`
	Items []string          `yaml:"items"`
	Attrs map[string]string `yaml:"attrs"`
}

// TestNestedCollectionsAndAliases covers duplicate keys inside nested
// collections and anchors, aliases, scalar key comparison by Value, and the
// nil versus empty shape of decoded map and slice fields.
func TestNestedCollectionsAndAliases(t *testing.T) {
	t.Run("duplicates", testNestedDuplicates)
	t.Run("empty collections", testNestedEmptyCollections)
	t.Run("unknown kind leaves destination", testNestedUnknownKind)
}

func testNestedDuplicates(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr error
	}{
		{"duplicate in sequence mapping", "items:\n  - a: 1\n    a: 2\n", ErrDuplicateKey},
		{"duplicate in anchored mapping", "base: &b\n  k: 1\n  k: 2\n", ErrDuplicateKey},
		{"string and integer key", "\"1\": a\n1: b\n", ErrDuplicateKey},
		{"integer and string key", "1: a\n'1': b\n", ErrDuplicateKey},
		{"alias to valid mapping", "base: &b\n  k: 1\nother: *b\n", nil},
		{"same keys in separate mappings", "x:\n  k: 1\ny:\n  k: 2\n", nil},
		{"same keys in sequence items", "items:\n  - k: 1\n  - k: 2\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out map[string]any
			err := LoadStrict("epic", []byte(tt.data), &out)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("LoadStrict() error = %v, want nil", err)
				}
				if len(out) == 0 {
					t.Fatalf("LoadStrict() decoded nothing for %q", tt.data)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("LoadStrict() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func testNestedEmptyCollections(t *testing.T) {
	tests := []struct {
		name        string
		data        string
		itemsNil    bool
		attrsNil    bool
		wantItemLen int
		wantAttrLen int
	}{
		{"omitted", "name: n\n", true, true, 0, 0},
		{"null", "name: n\nitems: null\nattrs: null\n", true, true, 0, 0},
		{"tilde null", "name: n\nitems: ~\nattrs: ~\n", true, true, 0, 0},
		{"explicit empty", "name: n\nitems: []\nattrs: {}\n", false, false, 0, 0},
		{"populated", "name: n\nitems: [a]\nattrs: {k: v}\n", false, false, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out nestedShape
			if err := LoadStrict("ticket", []byte(tt.data), &out); err != nil {
				t.Fatalf("LoadStrict() error = %v", err)
			}
			if out.Name != "n" {
				t.Errorf("Name = %q, want %q", out.Name, "n")
			}
			if (out.Items == nil) != tt.itemsNil || len(out.Items) != tt.wantItemLen {
				t.Errorf("Items = %#v, want nil=%v len=%d", out.Items, tt.itemsNil, tt.wantItemLen)
			}
			if (out.Attrs == nil) != tt.attrsNil || len(out.Attrs) != tt.wantAttrLen {
				t.Errorf("Attrs = %#v, want nil=%v len=%d", out.Attrs, tt.attrsNil, tt.wantAttrLen)
			}
		})
	}
}

func testNestedUnknownKind(t *testing.T) {
	out := nestedShape{Name: "keep", Items: []string{"x"}}
	before := nestedShape{Name: "keep", Items: []string{"x"}}
	err := LoadStrict("plan", []byte("name: n\nitems: []\nattrs: {}\n"), &out)
	if !errors.Is(err, ErrInvalidYAMLInput) {
		t.Fatalf("LoadStrict() error = %v, want ErrInvalidYAMLInput", err)
	}
	if !reflect.DeepEqual(out, before) {
		t.Fatalf("destination mutated: got %#v, want %#v", out, before)
	}
}
