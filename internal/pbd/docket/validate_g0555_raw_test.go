package docket

import (
	"errors"
	"testing"
)

// TestRawValueInResourcesReturnsD4555 verifies that raw values, malformed
// resources, duplicate entries, and invalid caps all return ErrInvalidResources.
func TestRawValueInResourcesReturnsD4555(t *testing.T) {
	tests := []struct {
		name      string
		resources Resources
		lane      LaneCaps
	}{
		{"empty element", Resources{""}, LaneCaps{}},
		{"unknown prefix", Resources{"unknown:abc"}, LaneCaps{}},
		{"duplicate resources", Resources{"tool:a", "tool:a"}, LaneCaps{}},
		{"duplicate caps", Resources{}, LaneCaps{CredClasses: []string{"a", "a"}}},
		{"cred with equals", Resources{"cred:github_read=synthetic"}, LaneCaps{CredClasses: []string{"github_read"}}},
		{"URL", Resources{"tool:http://example.com"}, LaneCaps{}},
		{"path", Resources{"tool:/usr/bin"}, LaneCaps{}},
		{"whitespace", Resources{"tool:abc def"}, LaneCaps{}},
		{"extra colon", Resources{"tool:abc:def"}, LaneCaps{}},
		{"malformed caps", Resources{}, LaneCaps{CredClasses: []string{"BadCap"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resCopy := make(Resources, len(tt.resources))
			copy(resCopy, tt.resources)
			capCopy := make([]string, len(tt.lane.CredClasses))
			copy(capCopy, tt.lane.CredClasses)

			err := ValidateResources(tt.resources, tt.lane)
			if !errors.Is(err, ErrInvalidResources) {
				t.Errorf("expected ErrInvalidResources, got %v", err)
			}
			if err != nil && err.Error() != "D4555" {
				t.Errorf("expected error string D4555, got %s", err.Error())
			}

			if len(tt.resources) != len(resCopy) {
				t.Errorf("resources slice length mutated")
			}
			for i := range tt.resources {
				if tt.resources[i] != resCopy[i] {
					t.Errorf("resources[%d] mutated", i)
				}
			}
			if len(tt.lane.CredClasses) != len(capCopy) {
				t.Errorf("caps slice length mutated")
			}
			for i := range tt.lane.CredClasses {
				if tt.lane.CredClasses[i] != capCopy[i] {
					t.Errorf("caps[%d] mutated", i)
				}
			}
		})
	}
}
