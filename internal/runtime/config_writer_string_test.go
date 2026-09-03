package runtime

// Purpose: tests WriteSetResult.String() (config_writer.go) — the
//   fmt.Stringer implementation internal/output.Writer.Result relies on
//   for a clean human-mode "key = value" line instead of Go's default
//   %v struct dump. Previously 0% direct coverage.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: none — pure in-memory struct, no filesystem/network.
// SPORT: runtime/toml-edit-engine (ADD, placeholder per T-8 sport_updates).

import "testing"

func TestWriteSetResult_String(t *testing.T) {
	tests := []struct {
		name string
		res  *WriteSetResult
		want string
	}{
		{
			name: "string value",
			res:  &WriteSetResult{KeyPath: "logging.level", Value: "debug"},
			want: "logging.level = debug",
		},
		{
			name: "integer value",
			res:  &WriteSetResult{KeyPath: "retrieval.fusion.k", Value: int64(80)},
			want: "retrieval.fusion.k = 80",
		},
		{
			name: "boolean value",
			res:  &WriteSetResult{KeyPath: "elevation.allow_remote", Value: true},
			want: "elevation.allow_remote = true",
		},
		{
			name: "nil value",
			res:  &WriteSetResult{KeyPath: "some.key", Value: nil},
			want: "some.key = <nil>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.res.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
