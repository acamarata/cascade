package v1

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func FuzzMemoryFrontmatter(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, _, err := parseMemoryFrontmatter(data)
		if err != nil {
			if _, ok := cascade.KindOf(err); !ok {
				t.Fatalf("frontmatter parser returned an untyped error: %v", err)
			}
		}
	})
}

func FuzzConfigTranslate(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, err := translateV1Config(data)
		if err != nil {
			if _, ok := cascade.KindOf(err); !ok {
				t.Fatalf("config translator returned an untyped error: %v", err)
			}
		}
	})
}
