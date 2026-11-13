package docket

import (
	"errors"
	"testing"
)

func TestWaveDropsToZeroOnGCSlugAnswer(t *testing.T) {
	tests := []struct {
		name     string
		ev       SlugAnswer
		wantWave int
		wantErr  error
	}{
		{
			name:     "gc-slug-answer not finished",
			ev:       SlugAnswer{Slug: "gc-slug-answer", Finished: false},
			wantWave: 0,
			wantErr:  nil,
		},
		{
			name:     "arbitrary nonblank slug not finished",
			ev:       SlugAnswer{Slug: "some arbitrary  slug", Finished: false},
			wantWave: 0,
			wantErr:  nil,
		},
		{
			name:     "repeated call same slug",
			ev:       SlugAnswer{Slug: "gc-slug-answer", Finished: false},
			wantWave: 0,
			wantErr:  nil,
		},
		{
			name:     "blank slug returns error",
			ev:       SlugAnswer{Slug: "   ", Finished: false},
			wantWave: 0,
			wantErr:  ErrInvalidSlug,
		},
		{
			name:     "empty slug returns error",
			ev:       SlugAnswer{Slug: "", Finished: false},
			wantWave: 0,
			wantErr:  ErrInvalidSlug,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotWave, gotErr := ApplySlugAnswer(tt.ev)
			if gotWave != tt.wantWave {
				t.Errorf("ApplySlugAnswer() wave = %v, want %v", gotWave, tt.wantWave)
			}
			if !errors.Is(gotErr, tt.wantErr) {
				t.Errorf("ApplySlugAnswer() err = %v, want %v", gotErr, tt.wantErr)
			}
		})
	}
}
