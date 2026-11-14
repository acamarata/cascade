package docket

import (
	"errors"
	"testing"
)

// TestCredentialClassNotInLaneReturnsD4555 verifies that a credential resource
// not present in lane.CredClasses returns ErrInvalidResources.
// It covers nil caps, empty caps, non-matching caps, and differently cased names.
func TestCredentialClassNotInLaneReturnsD4555(t *testing.T) {
	credResource := Resources{"cred:github_read"}

	cases := []struct {
		name      string
		resources Resources
		caps      []string
		wantErr   bool
		msg       string
	}{
		{"nil caps", credResource, nil, true, "expected ErrInvalidResources"},
		{"empty caps", credResource, []string{}, true, "expected ErrInvalidResources"},
		{"nonmatching caps", credResource, []string{"github_write", "gitlab_read"}, true, "expected ErrInvalidResources"},
		{"differently cased cred", Resources{"cred:GitHub_Read"}, []string{"github_read"}, true, "expected ErrInvalidResources for differently cased cred"},
		{"differently cased cap", credResource, []string{"GitHub_Read"}, true, "expected ErrInvalidResources for differently cased cap"},
		{"duplicate caps", credResource, []string{"github_read", "github_read"}, true, "expected ErrInvalidResources for duplicate caps"},
		{"wildcard in caps", credResource, []string{"*"}, true, "expected ErrInvalidResources for wildcard cap"},
		{"invalid cap chars", credResource, []string{"github.read"}, true, "expected ErrInvalidResources for invalid cap chars"},
		{"cap starts with number", credResource, []string{"1github"}, true, "expected ErrInvalidResources for cap starting with number"},
		{"matching case passes", credResource, []string{"github_read"}, false, "expected nil error for matching cred"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lane := LaneCaps{CredClasses: tc.caps}
			err := ValidateResources(tc.resources, lane)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidResources) {
					t.Fatalf("%s, got %v", tc.msg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s, got %v", tc.msg, err)
			}
		})
	}
}
