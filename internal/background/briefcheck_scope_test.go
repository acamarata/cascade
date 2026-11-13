package background

import (
	"testing"
)

// TestBriefRejectsMissingScope validates that CheckBrief returns the correct
// sentinel errors for missing, null, empty, and invalid scope fields.
func TestBriefRejectsMissingScope(t *testing.T) {
	callbackInvoked := false
	callback := func(brief string) bool {
		callbackInvoked = true
		return true
	}

	tests := []struct {
		name     string
		brief    string
		wantErr  error
		callback bool
	}{
		{
			name:    "missing scope field",
			brief:   `{"apis":[],"verify":""}`,
			wantErr: ErrMissingScope,
		},
		{
			name:    "null scope field",
			brief:   `{"scope":null,"apis":[],"verify":""}`,
			wantErr: ErrMissingScope,
		},
		{
			name:    "empty scope array",
			brief:   `{"scope":[],"apis":[],"verify":""}`,
			wantErr: ErrMissingScope,
		},
		{
			name:    "scope with empty string",
			brief:   `{"scope":[""],"apis":[],"verify":""}`,
			wantErr: ErrMissingScope,
		},
		{
			name:    "scope with whitespace-only string",
			brief:   `{"scope":["   "],"apis":[],"verify":""}`,
			wantErr: ErrMissingScope,
		},
		{
			name:    "duplicate scope entries",
			brief:   `{"scope":["a.txt","a.txt"],"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "absolute path",
			brief:   `{"scope":["/etc/passwd"],"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "path with backslash",
			brief:   `{"scope":["a\\b.txt"],"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "path with dot component",
			brief:   `{"scope":["./a.txt"],"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "path with dotdot component",
			brief:   `{"scope":["../a.txt"],"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "glob pattern",
			brief:   `{"scope":["*.txt"],"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "glob pattern with path",
			brief:   `{"scope":["dir/*.txt"],"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "path with NUL byte",
			brief:   "{\"scope\":[\"a.txt\x00b.txt\"],\"apis\":[],\"verify\":\"\"}",
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "path with whitespace",
			brief:   `{"scope":["a b.txt"],"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "path with tab",
			brief:   "{\"scope\":[\"a\tb.txt\"],\"apis\":[],\"verify\":\"\"}",
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "path with newline",
			brief:   "{\"scope\":[\"a\nb.txt\"],\"apis\":[],\"verify\":\"\"}",
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "directory path without filename",
			brief:   `{"scope":["dir/"],"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "filename without extension",
			brief:   `{"scope":["filename"],"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "empty component in path",
			brief:   `{"scope":["a//b.txt"],"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "leading slash",
			brief:   `{"scope":["/a.txt"],"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "trailing slash",
			brief:   `{"scope":["a.txt/"],"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "scope is not array",
			brief:   `{"scope":"a.txt","apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "scope is number",
			brief:   `{"scope":123,"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "scope is object",
			brief:   `{"scope":{},"apis":[],"verify":""}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "invalid JSON",
			brief:   `{"scope":["a.txt"`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "invalid UTF-8",
			brief:   "{\"scope\":[\"a.txt\xff\"],\"apis\":[],\"verify\":\"\"}",
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "unknown key",
			brief:   `{"scope":["a.txt"],"apis":[],"verify":"","unknown":"value"}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "duplicate key in JSON",
			brief:   `{"scope":["a.txt"],"apis":[],"verify":"","scope":["b.txt"]}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "trailing objects",
			brief:   `{"scope":["a.txt"],"apis":[],"verify":""}{"scope":["b.txt"]}`,
			wantErr: ErrInvalidBrief,
		},
		{
			name:    "empty string brief",
			brief:   "",
			wantErr: ErrInvalidBrief,
		},
		{
			name:     "valid single file",
			brief:    `{"scope":["a.txt"],"apis":[],"verify":""}`,
			wantErr:  nil,
			callback: true,
		},
		{
			name:     "valid nested path",
			brief:    `{"scope":["dir/subdir/file.go"],"apis":[],"verify":""}`,
			wantErr:  nil,
			callback: true,
		},
		{
			name:     "valid multiple files",
			brief:    `{"scope":["a.txt","b.go","c.js"],"apis":[],"verify":""}`,
			wantErr:  nil,
			callback: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callbackInvoked = false
			err := CheckBrief(tt.brief, callback)

			if err != tt.wantErr {
				t.Errorf("CheckBrief() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.callback != callbackInvoked {
				t.Errorf("callback invoked = %v, want %v", callbackInvoked, tt.callback)
			}
		})
	}
}
