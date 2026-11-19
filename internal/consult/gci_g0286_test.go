package consult

import (
	"errors"
	"strings"
	"testing"
)

func TestGCIBlockOrdering(t *testing.T) {
	tests := []struct {
		name      string
		sections  []GCISection
		omissions string
		want      string
		wantErr   error
	}{
		{
			name: "regular before binding",
			sections: []GCISection{
				{Name: "A", Body: "a"},
				{Name: "B", Body: "b", Binding: true},
			},
			want: "# B\nb\n\n# A\na",
		},
		{
			name: "multiple bindings and regulars preserve order",
			sections: []GCISection{
				{Name: "R1", Body: "r1"},
				{Name: "B1", Body: "b1", Binding: true},
				{Name: "R2", Body: "r2"},
				{Name: "B2", Body: "b2", Binding: true},
			},
			want: "# B1\nb1\n\n# B2\nb2\n\n# R1\nr1\n\n# R2\nr2",
		},
		{
			name: "blank name",
			sections: []GCISection{
				{Name: " ", Body: "b"},
			},
			want:    "",
			wantErr: ErrInvalidGCISection,
		},
		{
			name: "blank body",
			sections: []GCISection{
				{Name: "A", Body: "\t\n"},
			},
			want:    "",
			wantErr: ErrInvalidGCISection,
		},
		{
			name: "duplicate name",
			sections: []GCISection{
				{Name: "A", Body: "a"},
				{Name: "A", Body: "a2"},
			},
			want:    "",
			wantErr: ErrInvalidGCISection,
		},
		{
			name:     "empty sections",
			sections: []GCISection{},
			want:     "",
			wantErr:  nil,
		},
		{
			name:      "nil sections",
			sections:  nil,
			omissions: "something",
			want:      "",
			wantErr:   nil,
		},
		{
			name: "budget exceeded",
			sections: []GCISection{
				{Name: "A", Body: strings.Repeat("a", 5000)},
			},
			want:    "",
			wantErr: ErrBudgetExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AssembleGCIBlock(tt.sections, tt.omissions)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("AssembleGCIBlock() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("AssembleGCIBlock() got = %q, want %q", got, tt.want)
			}
		})
	}
}
