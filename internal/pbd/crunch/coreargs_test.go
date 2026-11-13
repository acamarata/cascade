package crunch

import (
	"reflect"
	"testing"
)

// TestExtractCoreArgsEmptyInput verifies that nil and empty argument slices
// are treated as an explicit absence, returning the zero CoreArgs and no
// error.
func TestExtractCoreArgsEmptyInput(t *testing.T) {
	cases := map[string][]string{
		"nil":   nil,
		"empty": {},
	}

	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ExtractCoreArgs(args)
			if err != nil {
				t.Fatalf("ExtractCoreArgs(%#v) returned error: %v", args, err)
			}

			want := CoreArgs{}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ExtractCoreArgs(%#v) = %#v, want %#v", args, got, want)
			}
		})
	}
}
