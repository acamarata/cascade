package repo

import "testing"

// TestFactSubjectEnumClosed proves the seven closed subjects validate
// and nothing else does -- no `other` member, no permissive zero value.
func TestFactSubjectEnumClosed(t *testing.T) {
	valid := []FactSubject{
		SubjectLanguages, SubjectBuildCmd, SubjectTestCmd, SubjectLintCmd,
		SubjectLayout, SubjectCIPresence, SubjectHarnessFiles,
	}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("FactSubject(%q).Valid() = false, want true", s)
		}
	}
	invalid := []FactSubject{"", "other", "OTHER", "secrets"}
	for _, s := range invalid {
		if s.Valid() {
			t.Errorf("FactSubject(%q).Valid() = true, want false", s)
		}
	}
}

func validFact() InferredFact {
	return InferredFact{
		ID: "f1", RepositoryID: "repo-1", Subject: SubjectLanguages,
		Fact: "go", Source: "untrusted-source", Confidence: 0.9, Version: 1,
		State: FactProposed,
	}
}

func TestInferredFactValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(InferredFact) InferredFact
		wantErr bool
	}{
		{"valid", func(f InferredFact) InferredFact { return f }, false},
		{"missing id", func(f InferredFact) InferredFact { f.ID = ""; return f }, true},
		{"missing repo id", func(f InferredFact) InferredFact { f.RepositoryID = ""; return f }, true},
		{"bad subject", func(f InferredFact) InferredFact { f.Subject = "other"; return f }, true},
		{"missing fact", func(f InferredFact) InferredFact { f.Fact = ""; return f }, true},
		{"missing source", func(f InferredFact) InferredFact { f.Source = ""; return f }, true},
		{"confidence too high", func(f InferredFact) InferredFact { f.Confidence = 1.5; return f }, true},
		{"confidence negative", func(f InferredFact) InferredFact { f.Confidence = -0.1; return f }, true},
		{"version zero", func(f InferredFact) InferredFact { f.Version = 0; return f }, true},
		{"bad state", func(f InferredFact) InferredFact { f.State = "bogus"; return f }, true},
	}
	for _, c := range cases {
		err := c.mutate(validFact()).Validate()
		if (err != nil) != c.wantErr {
			t.Errorf("%s: Validate() err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

func TestFactStateValid(t *testing.T) {
	valid := []FactState{FactProposed, FactAccepted, FactRejected, FactSuperseded}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("FactState(%q).Valid() = false, want true", s)
		}
	}
	if FactState("bogus").Valid() {
		t.Error(`FactState("bogus").Valid() = true, want false`)
	}
}
