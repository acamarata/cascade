package evidence

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestSourceTypeValidAndDecode(t *testing.T) {
	for _, st := range []SourceType{SourceGit, SourceFile, SourceTestLog, SourceArtifact, SourceURL} {
		if !st.Valid() {
			t.Errorf("%q.Valid() = false, want true", st)
		}
	}
	if _, err := DecodeSourceType("bogus"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("DecodeSourceType(bogus) = %v, want typed invalid-input", err)
	}
	if _, err := DecodeSourceType(""); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("DecodeSourceType(\"\") = %v, want typed invalid-input", err)
	}
}

func TestNewEvidenceIDPrefixAndLength(t *testing.T) {
	id, err := NewEvidenceID()
	if err != nil {
		t.Fatalf("NewEvidenceID: %v", err)
	}
	if !strings.HasPrefix(id, EvidenceIDPrefix) {
		t.Errorf("NewEvidenceID() = %q, want prefix %q", id, EvidenceIDPrefix)
	}
	if got, want := len(id), len(EvidenceIDPrefix)+26; got != want {
		t.Errorf("len(NewEvidenceID()) = %d, want %d", got, want)
	}
	id2, _ := NewEvidenceID()
	if id == id2 {
		t.Error("two NewEvidenceID calls returned the same id")
	}
}

func TestContentHash(t *testing.T) {
	got := ContentHash([]byte(""))
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got != want {
		t.Errorf("ContentHash(\"\") = %q, want %q", got, want)
	}
	if ContentHash([]byte("a")) == ContentHash([]byte("b")) {
		t.Error("ContentHash collided for distinct inputs")
	}
}

func validEvidence() Evidence {
	return Evidence{
		ID:      "EVD-TEST",
		ClaimID: "CLM-TEST",
		Source: Source{
			Type:       SourceGit,
			Repository: "/tmp/repo",
			Commit:     "deadbeef",
			Path:       "file.go",
			Locator:    Locator{Kind: LocatorLines, Start: 1, End: 2},
		},
		ContentHash: "abc",
		DataClass:   DataClassInternal,
	}
}

func TestEvidenceValidate(t *testing.T) {
	if err := validEvidence().Validate(); err != nil {
		t.Fatalf("valid evidence: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(e Evidence) Evidence
	}{
		{"empty claim_id", func(e Evidence) Evidence { e.ClaimID = ""; return e }},
		{"unknown source type", func(e Evidence) Evidence { e.Source.Type = "bogus"; return e }},
		{"unknown data_class", func(e Evidence) Evidence { e.DataClass = "bogus"; return e }},
		{"empty content_hash", func(e Evidence) Evidence { e.ContentHash = ""; return e }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.mutate(validEvidence()).Validate()
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Errorf("Validate() = %v, want typed invalid-input", err)
			}
		})
	}
}
