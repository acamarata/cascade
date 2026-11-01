package repo

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

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
	invalid := []FactSubject{"", "other", "OTHER", "secrets", "Languages"}
	for _, s := range invalid {
		if s.Valid() {
			t.Errorf("FactSubject(%q).Valid() = true, want false", s)
		}
		raw := `{"proposals":[{"subject":` + strconv.Quote(string(s)) + `,"fact":"x","confidence":0.5}]}`
		got, err := ParseExtractProposal([]byte(raw))
		if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput || got != nil {
			t.Errorf("ParseExtractProposal(subject %q) = %v, %v; want nil and KindInvalidInput", s, got, err)
		}
	}
	svc, ledger := newTestLedgerService(t)
	bad := []ProposedFact{{Subject: SubjectLanguages, Fact: "go", Confidence: 0.5}, {Subject: "other", Fact: "x", Confidence: 0.5}}
	if _, err := svc.Record(context.Background(), "repo-1", bad); err == nil {
		t.Fatal("Record accepted a proposal naming subject \"other\"")
	}
	if rows, _ := ledger.List(context.Background(), LedgerFilter{RepositoryID: "repo-1"}); len(rows) != 0 {
		t.Fatalf("a refused batch appended %d rows, want 0", len(rows))
	}
}

// TestFactRecordFieldSet pins the ledger row to the decided field set plus
// identity and state, and the state set to exactly four members.
func TestFactRecordFieldSet(t *testing.T) {
	want := map[string]bool{
		"fact": true, "source": true, "confidence": true, "version": true, "accepted_by": true, "rollback": true,
		"id": true, "repository_id": true, "subject": true, "state": true, "reject_reason": true,
	}
	typ := reflect.TypeOf(InferredFact{})
	got := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		got[name] = true
		if !want[name] {
			t.Errorf("InferredFact carries undecided field %q", name)
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("InferredFact lacks decided field %q", name)
		}
	}
	states := []FactState{FactProposed, FactAccepted, FactRejected, FactSuperseded}
	if len(states) != 4 || string(FactProposed) != "proposed" || string(FactAccepted) != "accepted" ||
		string(FactRejected) != "rejected" || string(FactSuperseded) != "superseded" {
		t.Fatalf("fact states drifted: %v", states)
	}
	for _, s := range []FactState{"", "pending", "Accepted"} {
		if s.Valid() {
			t.Errorf("FactState(%q).Valid() = true, want false", s)
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
