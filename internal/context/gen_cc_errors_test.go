package context

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// assertKindInvalidInput fails unless err carries the taxonomy's
// KindInvalidInput. Every rejection this generator makes is a statement
// about its caller's input, so no other kind is acceptable here.
func assertKindInvalidInput(t *testing.T, err error) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a taxonomy error", err)
	}
	if ce.Kind != cascade.KindInvalidInput {
		t.Fatalf("error kind = %v, want %v", ce.Kind, cascade.KindInvalidInput)
	}
}

// validateCase is one input for TestValidateMergedContext.
type validateCase struct {
	name    string
	mc      MergedContext
	wantErr bool
}

// validAcceptedCases are the contexts the generator must accept.
func validAcceptedCases() []validateCase {
	return []validateCase{
		{
			name: "empty context is fine",
			mc:   MergedContext{},
		},
		{
			name: "empty context with a nil provenance map is fine",
			mc:   MergedContext{Provenance: nil},
		},
		{
			name: "well formed single section",
			mc: MergedContext{
				Sections:   []MergedSection{{Heading: "A", Content: "## A", Role: TierGCI, Ordinal: 0}},
				Provenance: map[string]TierRole{"A": TierGCI},
			},
		},
		{
			name: "preamble needs no provenance entry",
			mc: MergedContext{
				Sections:   []MergedSection{{Content: "intro", Role: TierGCI, Ordinal: 0}},
				Provenance: map[string]TierRole{},
			},
		},
	}
}

// refusedShapeCases are the refusals about a context's shape: a missing
// provenance map, or a section carrying a role outside the five-tier model.
func refusedShapeCases() []validateCase {
	return []validateCase{
		{
			name: "sections without provenance are refused",
			mc: MergedContext{
				Sections: []MergedSection{{Heading: "A", Content: "## A", Role: TierGCI}},
			},
			wantErr: true,
		},
		{
			name: "invalid role is refused",
			mc: MergedContext{
				Sections:   []MergedSection{{Heading: "A", Content: "## A", Role: TierRole(0)}},
				Provenance: map[string]TierRole{"A": TierGCI},
			},
			wantErr: true,
		},
		{
			name: "out of range role is refused",
			mc: MergedContext{
				Sections:   []MergedSection{{Heading: "A", Content: "## A", Role: TierRole(9)}},
				Provenance: map[string]TierRole{"A": TierGCI},
			},
			wantErr: true,
		},
	}
}

// refusedOrdinalCases are the refusals about a section's ordinal and about a
// heading whose provenance entry is missing or names a different tier. Split
// from refusedShapeCases only to keep each function short.
func refusedOrdinalCases() []validateCase {
	return []validateCase{
		{
			name: "negative ordinal is refused",
			mc: MergedContext{
				Sections:   []MergedSection{{Heading: "A", Content: "## A", Role: TierGCI, Ordinal: -1}},
				Provenance: map[string]TierRole{"A": TierGCI},
			},
			wantErr: true,
		},
		{
			name: "descending ordinals are refused",
			mc: MergedContext{
				Sections: []MergedSection{
					{Heading: "A", Content: "## A", Role: TierPRI, Ordinal: 3},
					{Heading: "B", Content: "## B", Role: TierGCI, Ordinal: 0},
				},
				Provenance: map[string]TierRole{"A": TierPRI, "B": TierGCI},
			},
			wantErr: true,
		},
		{
			name: "unrecorded heading is refused",
			mc: MergedContext{
				Sections:   []MergedSection{{Heading: "A", Content: "## A", Role: TierGCI}},
				Provenance: map[string]TierRole{"B": TierGCI},
			},
			wantErr: true,
		},
		{
			name: "heading carried by the losing tier is refused",
			mc: MergedContext{
				Sections:   []MergedSection{{Heading: "A", Content: "## A", Role: TierPRI, Ordinal: 3}},
				Provenance: map[string]TierRole{"A": TierGCI},
			},
			wantErr: true,
		},
	}
}

func TestValidateMergedContext(t *testing.T) {
	tests := validAcceptedCases()
	tests = append(tests, refusedShapeCases()...)
	tests = append(tests, refusedOrdinalCases()...)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMergedContext(tc.mc)
			switch {
			case tc.wantErr && err == nil:
				t.Fatal("want an error, got nil")
			case !tc.wantErr && err != nil:
				t.Fatalf("want no error, got %v", err)
			case tc.wantErr:
				assertKindInvalidInput(t, err)
			}
		})
	}
}

// TestGenerateRefusesEveryInvalidContext proves the writer actually consults
// the validator instead of only the validator being tested.
func TestGenerateRefusesEveryInvalidContext(t *testing.T) {
	bad := MergedContext{
		Sections:   []MergedSection{{Heading: "A", Content: "## A", Role: TierPRI, Ordinal: 3}},
		Provenance: map[string]TierRole{"A": TierGCI},
	}
	files, err := (&CCInstructionWriter{}).Generate(bad)
	if err == nil {
		t.Fatal("Generate accepted a context its validator rejects")
	}
	if files != nil {
		t.Errorf("Generate returned %d files alongside an error", len(files))
	}
	assertKindInvalidInput(t, err)
}
