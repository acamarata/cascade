package forge

import (
	"bytes"
	"testing"
)

func TestClipContract(t *testing.T) {
	tests := []struct {
		name    string
		body    []byte
		want    []byte
		wantErr error
	}{
		{
			name: "single newline at end",
			body: []byte("hello\n"),
			want: []byte("hello\n"),
		},
		{
			name: "multiple lines clips at last newline",
			body: []byte("line1\nline2\npartial"),
			want: []byte("line1\nline2\n"),
		},
		{
			name: "newline in middle only",
			body: []byte("abc\ndef"),
			want: []byte("abc\n"),
		},
		{
			name:    "no newline returns ErrNoLineBoundary",
			body:    []byte("no newline here"),
			wantErr: ErrNoLineBoundary,
		},
		{
			name:    "empty body returns ErrNoLineBoundary",
			body:    []byte{},
			wantErr: ErrNoLineBoundary,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ClipContract(tt.body)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Errorf("ClipContract() error = %v, wantErr %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ClipContract() unexpected error = %v", err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Errorf("ClipContract() = %q, want %q", got, tt.want)
			}
		})
	}
}
