package repo

import (
	"context"
	"testing"
)

// seedCorroborationDB seeds repo-1 with a live inventory carrying a
// detected Go language, a build command, and CI presence, then returns a
// Store to Corroborate against.
func seedCorroborationDB(t *testing.T) *Store {
	t.Helper()
	db := openTestDB(t)
	seedRepository(t, db, "repo-1")
	inv := Inventory{
		Repository: RepositoryRef{ID: "repo-1"},
		Languages: []LanguageFacts{
			{Language: LanguageGo, Detected: true, Commands: Commands{Build: "go build ./..."}},
		},
		CI: CIFacts{GitHubActions: true},
	}
	if err := NewStore(db).Upsert(context.Background(), inv); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	return NewStore(db)
}

// TestFactCorroborationRequired is table-driven over corroborationCases
// (below) to stay under the 50-line function cap while covering the
// full corroborated/contradicted/uncorroborated matrix in one place.
func TestFactCorroborationRequired(t *testing.T) {
	store := seedCorroborationDB(t)
	ctx := context.Background()
	for _, c := range corroborationCases() {
		t.Run(c.name, func(t *testing.T) {
			f := validFact()
			f.RepositoryID = c.repositoryID
			f.Subject = c.subject
			f.Fact = c.fact
			verdict, reason, err := Corroborate(ctx, store, f)
			if err != nil {
				t.Fatalf("Corroborate: %v", err)
			}
			if verdict != c.wantVerdict {
				t.Fatalf("verdict = %v, want %v", verdict, c.wantVerdict)
			}
			if c.wantVerdict != VerdictContradicted && reason != "" {
				t.Fatalf("reason = %q, want empty for a non-contradiction verdict", reason)
			}
			if c.wantVerdict == VerdictContradicted && reason == "" {
				t.Fatal("contradiction reason is empty")
			}
		})
	}
}

type corroborationCase struct {
	name         string
	repositoryID string
	subject      FactSubject
	fact         string
	wantVerdict  CorroborationVerdict
}

func corroborationCases() []corroborationCase {
	return []corroborationCase{
		{"corroborated", "repo-1", SubjectLanguages, "go", VerdictCorroborated},
		{"contradicted", "repo-1", SubjectLanguages, "rust", VerdictContradicted},
		{"build command corroborated", "repo-1", SubjectBuildCmd, "go build ./...", VerdictCorroborated},
		{"ci presence corroborated", "repo-1", SubjectCIPresence, "true", VerdictCorroborated},
		{"uncorroborated subject stays proposed, not a failure", "repo-1", SubjectLayout, "2 dirs, 5 files", VerdictUncorroborated},
		{"uncorroborated when no inventory exists for the repo", "no-such-repo", SubjectLanguages, "go", VerdictUncorroborated},
	}
}

func TestCorroborate_NilStoreRefused(t *testing.T) {
	if _, _, err := Corroborate(context.Background(), nil, validFact()); err == nil {
		t.Fatal("Corroborate(nil store, ...): err = nil, want typed error")
	}
}

func TestCorroborate_InvalidFactRefused(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)
	bad := validFact()
	bad.Subject = "bogus"
	if _, _, err := Corroborate(context.Background(), store, bad); err == nil {
		t.Fatal("Corroborate with an invalid fact: err = nil, want typed error")
	}
}

func TestCorroborate_BadCIPresenceValue(t *testing.T) {
	db := openTestDB(t)
	seedRepository(t, db, "repo-1")
	if err := NewStore(db).Upsert(context.Background(), Inventory{Repository: RepositoryRef{ID: "repo-1"}}); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	f := validFact()
	f.RepositoryID = "repo-1"
	f.Subject = SubjectCIPresence
	f.Fact = "yes"
	if _, _, err := Corroborate(context.Background(), store, f); err == nil {
		t.Fatal(`Corroborate with ci_presence fact "yes": err = nil, want typed error`)
	}
}

func TestAcceptRejectTransitions(t *testing.T) {
	f := validFact()

	accepted, err := Accept(f, "detector:languages")
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if accepted.State != FactAccepted || accepted.AcceptedBy != "detector:languages" {
		t.Fatalf("accepted = %+v", accepted)
	}

	if _, err := Accept(accepted, "someone"); err == nil {
		t.Fatal("Accept on an already-accepted fact: err = nil, want typed error")
	}

	f2 := validFact()
	rejected, err := Reject(f2, "detector contradiction: rust != go")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.State != FactRejected || rejected.RejectReason == "" {
		t.Fatalf("rejected = %+v", rejected)
	}

	if _, err := Accept(f, ""); err == nil {
		t.Fatal("Accept with empty acceptedBy: err = nil, want typed error")
	}
	if _, err := Reject(f2, ""); err == nil {
		t.Fatal("Reject with empty reason: err = nil, want typed error")
	}
}

func TestSupersedeAndRollback(t *testing.T) {
	v1 := validFact()
	accepted1, err := Accept(v1, "detector:languages")
	if err != nil {
		t.Fatalf("Accept v1: %v", err)
	}

	v2 := validFact()
	v2.Version = 2
	v2.Fact = "go,rust"

	superseded, accepted2, err := Supersede(accepted1, v2, "detector:languages")
	if err != nil {
		t.Fatalf("Supersede: %v", err)
	}
	if superseded.State != FactSuperseded {
		t.Fatalf("superseded.State = %v, want FactSuperseded", superseded.State)
	}
	if accepted2.State != FactAccepted || accepted2.Rollback == "" {
		t.Fatalf("accepted2 = %+v, want FactAccepted with a rollback reference", accepted2)
	}

	restored, err := Rollback(accepted2)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if restored.Version != 1 {
		t.Fatalf("restored.Version = %d, want 1", restored.Version)
	}

	if _, err := Rollback(accepted1); err == nil {
		t.Fatal("Rollback of a version-1 fact: err = nil, want typed error")
	}
}

func TestSupersede_RejectsWrongPriorState(t *testing.T) {
	v1 := validFact() // still FactProposed, not FactAccepted
	v2 := validFact()
	v2.Version = 2
	if _, _, err := Supersede(v1, v2, "detector"); err == nil {
		t.Fatal("Supersede on a non-accepted prior: err = nil, want typed error")
	}
}

func TestSupersede_RejectsVersionGap(t *testing.T) {
	accepted1, _ := Accept(validFact(), "detector")
	v3 := validFact()
	v3.Version = 3 // must be exactly prior+1 = 2
	if _, _, err := Supersede(accepted1, v3, "detector"); err == nil {
		t.Fatal("Supersede with a version gap: err = nil, want typed error")
	}
}
