package background

import (
	"testing"
)

type scopeCase struct {
	name     string
	brief    string
	wantErr  error
	callback bool
}

// scopeCasesA lists missing-scope and malformed-path briefs.
func scopeCasesA() []scopeCase {
	return []scopeCase{
		{"missing scope field", `{"apis":[],"verify":""}`, ErrMissingScope, false},
		{"null scope field", `{"scope":null,"apis":[],"verify":""}`, ErrMissingScope, false},
		{"empty scope array", `{"scope":[],"apis":[],"verify":""}`, ErrMissingScope, false},
		{"scope with empty string", `{"scope":[""],"apis":[],"verify":""}`, ErrMissingScope, false},
		{"scope with whitespace-only string", `{"scope":["   "],"apis":[],"verify":""}`, ErrMissingScope, false},
		{"duplicate scope entries", `{"scope":["a.txt","a.txt"],"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"absolute path", `{"scope":["/etc/passwd"],"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"path with backslash", `{"scope":["a\\b.txt"],"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"path with dot component", `{"scope":["./a.txt"],"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"path with dotdot component", `{"scope":["../a.txt"],"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"glob pattern", `{"scope":["*.txt"],"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"glob pattern with path", `{"scope":["dir/*.txt"],"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"path with NUL byte", "{\"scope\":[\"a.txt\x00b.txt\"],\"apis\":[],\"verify\":\"\"}", ErrInvalidBrief, false},
		{"path with whitespace", `{"scope":["a b.txt"],"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"path with tab", "{\"scope\":[\"a\tb.txt\"],\"apis\":[],\"verify\":\"\"}", ErrInvalidBrief, false},
		{"path with newline", "{\"scope\":[\"a\nb.txt\"],\"apis\":[],\"verify\":\"\"}", ErrInvalidBrief, false},
		{"directory path without filename", `{"scope":["dir/"],"apis":[],"verify":""}`, ErrInvalidBrief, false},
	}
}

// scopeCasesB lists malformed-shape briefs and valid briefs.
func scopeCasesB() []scopeCase {
	return []scopeCase{
		{"filename without extension", `{"scope":["filename"],"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"empty component in path", `{"scope":["a//b.txt"],"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"leading slash", `{"scope":["/a.txt"],"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"trailing slash", `{"scope":["a.txt/"],"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"scope is not array", `{"scope":"a.txt","apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"scope is number", `{"scope":123,"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"scope is object", `{"scope":{},"apis":[],"verify":""}`, ErrInvalidBrief, false},
		{"invalid JSON", `{"scope":["a.txt"`, ErrInvalidBrief, false},
		{"invalid UTF-8", "{\"scope\":[\"a.txt\xff\"],\"apis\":[],\"verify\":\"\"}", ErrInvalidBrief, false},
		{"unknown key", `{"scope":["a.txt"],"apis":[],"verify":"","unknown":"value"}`, ErrInvalidBrief, false},
		{"duplicate key in JSON", `{"scope":["a.txt"],"apis":[],"verify":"","scope":["b.txt"]}`, ErrInvalidBrief, false},
		{"trailing objects", `{"scope":["a.txt"],"apis":[],"verify":""}{"scope":["b.txt"]}`, ErrInvalidBrief, false},
		{"empty string brief", "", ErrInvalidBrief, false},
		{"valid single file", `{"scope":["a.txt"],"apis":[],"verify":""}`, nil, true},
		{"valid nested path", `{"scope":["dir/subdir/file.go"],"apis":[],"verify":""}`, nil, true},
		{"valid multiple files", `{"scope":["a.txt","b.go","c.js"],"apis":[],"verify":""}`, nil, true},
	}
}

// TestBriefRejectsMissingScope validates that CheckBrief returns the correct
// sentinel errors for missing, null, empty, and invalid scope fields.
func TestBriefRejectsMissingScope(t *testing.T) {
	callbackInvoked := false
	callback := func(_ string) bool {
		callbackInvoked = true
		return true
	}

	tests := append(scopeCasesA(), scopeCasesB()...)

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
