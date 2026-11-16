package wiregen

import (
	"errors"
	"testing"
)

func TestGenerateRejectsNonStruct(t *testing.T) {
	type ValidStruct struct {
		Name string
	}

	tests := []struct {
		name    string
		args    []any
		wantErr error
	}{
		{
			name:    "int value",
			args:    []any{42},
			wantErr: ErrNotStruct,
		},
		{
			name:    "nil value",
			args:    []any{nil},
			wantErr: ErrNotStruct,
		},
		{
			name:    "pointer to int",
			args:    []any{new(int)},
			wantErr: ErrNotStruct,
		},
		{
			name:    "valid struct followed by int",
			args:    []any{ValidStruct{}, 42},
			wantErr: ErrNotStruct,
		},
		{
			name:    "pointer to struct",
			args:    []any{&ValidStruct{}},
			wantErr: ErrNotStruct,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Generate(tt.args...)
			if got != "" {
				t.Errorf("Generate() text = %q, want empty string", got)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Generate() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
