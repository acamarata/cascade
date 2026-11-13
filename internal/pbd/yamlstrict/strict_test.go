package yamlstrict

import (
	"errors"
	"reflect"
	"testing"
)

// TestRejectsInvalidInput verifies that LoadStrict returns ErrInvalidYAMLInput
// for all invalid input cases and does not mutate the destination.
func TestRejectsInvalidInput(t *testing.T) {
	type sample struct {
		Key string `yaml:"key"`
	}

	tests := []struct {
		name string
		kind string
		data []byte
		out  any
	}{
		{"unknown kind", "unknown", []byte("key: value"), &sample{}},
		{"empty kind", "", []byte("key: value"), &sample{}},
		{"case mismatched kind", "Epic", []byte("key: value"), &sample{}},
		{"nil data", "epic", nil, &sample{}},
		{"empty data", "epic", []byte{}, &sample{}},
		{"whitespace data", "epic", []byte("   \n\t  "), &sample{}},
		{"empty document", "epic", []byte(""), &sample{}},
		{"null document", "epic", []byte("null"), &sample{}},
		{"malformed YAML", "sport", []byte("key: [unterminated"), &sample{}},
		{"multiple documents", "ticket", []byte("key: value\n---\nkey: value2"), &sample{}},
		{"non-scalar keys", "epic", []byte("[a]: value"), &sample{}},
		{"nil out", "epic", []byte("key: value"), nil},
		{"typed-nil out", "epic", []byte("key: value"), (*sample)(nil)},
		{"non-pointer out", "epic", []byte("key: value"), sample{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var before any
			if tt.out != nil {
				v := reflect.ValueOf(tt.out)
				if v.Kind() == reflect.Pointer && !v.IsNil() {
					before = reflect.Indirect(v).Interface()
				}
			}

			err := LoadStrict(tt.kind, tt.data, tt.out)
			if err == nil || !errors.Is(err, ErrInvalidYAMLInput) {
				t.Errorf("LoadStrict() error = %v, want errors.Is(err, ErrInvalidYAMLInput)", err)
			}

			if tt.out != nil && before != nil {
				v := reflect.ValueOf(tt.out)
				after := reflect.Indirect(v).Interface()
				if !reflect.DeepEqual(before, after) {
					t.Errorf("destination mutated: before=%v, after=%v", before, after)
				}
			}
		})
	}
}
