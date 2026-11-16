package docs

import (
	"reflect"
	"testing"
)

func TestLintSafe(t *testing.T) {
	// Empty store
	s := Store{}
	violations := s.Lint()
	if violations == nil {
		t.Fatalf("expected non-nil empty slice, got nil")
	}
	if len(violations) != 0 {
		t.Fatalf("expected empty slice, got %v", violations)
	}

	// Nil Docs
	s2 := Store{Docs: nil}
	violations2 := s2.Lint()
	if violations2 == nil {
		t.Fatalf("expected non-nil empty slice, got nil")
	}
	if len(violations2) != 0 {
		t.Fatalf("expected empty slice, got %v", violations2)
	}

	tests := []struct {
		name     string
		store    Store
		expected []string
	}{
		{
			name:     "empty content",
			store:    Store{Docs: []Doc{{Name: "a", Content: "   \n\t  "}}},
			expected: []string{"doc[0]: empty content"},
		},
		{
			name:     "invalid name - empty",
			store:    Store{Docs: []Doc{{Name: "", Content: "c"}}},
			expected: []string{"doc[0]: invalid name"},
		},
		{
			name:     "invalid name - whitespace",
			store:    Store{Docs: []Doc{{Name: " ", Content: "c"}}},
			expected: []string{"doc[0]: invalid name"},
		},
		{
			name:     "invalid name - NUL",
			store:    Store{Docs: []Doc{{Name: "a\x00b", Content: "c"}}},
			expected: []string{"doc[0]: invalid name"},
		},
		{
			name:     "invalid name - CR",
			store:    Store{Docs: []Doc{{Name: "a\rb", Content: "c"}}},
			expected: []string{"doc[0]: invalid name"},
		},
		{
			name:     "invalid name - LF",
			store:    Store{Docs: []Doc{{Name: "a\nb", Content: "c"}}},
			expected: []string{"doc[0]: invalid name"},
		},
		{
			name:     "duplicate name",
			store:    Store{Docs: []Doc{{Name: "a", Content: "c"}, {Name: "a", Content: "c2"}}},
			expected: []string{"a: duplicate name"},
		},
		{
			name:     "invalid alias",
			store:    Store{Docs: []Doc{{Name: "a", Content: "c", Aliases: []string{" "}}}},
			expected: []string{"doc[0]: invalid alias"},
		},
		{
			name:     "duplicate alias - same doc",
			store:    Store{Docs: []Doc{{Name: "a", Content: "c", Aliases: []string{"b", "b"}}}},
			expected: []string{"b: duplicate alias"},
		},
		{
			name:     "duplicate alias - cross doc",
			store:    Store{Docs: []Doc{{Name: "a", Content: "c", Aliases: []string{"b"}}, {Name: "c", Content: "d", Aliases: []string{"b"}}}},
			expected: []string{"b: duplicate alias"},
		},
		{
			name:     "alias collides with name",
			store:    Store{Docs: []Doc{{Name: "a", Content: "c"}, {Name: "b", Content: "d", Aliases: []string{"a"}}}},
			expected: []string{"a: duplicate alias"},
		},
		{
			name: "multiple issues",
			store: Store{Docs: []Doc{
				{Name: "a", Content: " "},
				{Name: "a", Content: "c"},
				{Name: "b", Aliases: []string{"a", "a", " "}},
			}},
			expected: []string{
				"a: duplicate alias",
				"a: duplicate name",
				"doc[0]: empty content",
				"doc[2]: invalid alias",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.store.Lint()
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("got %v, expected %v", got, tt.expected)
			}
		})
	}
}
