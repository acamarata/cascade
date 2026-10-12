package jobs

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestDataClassStoredTextRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		word string
		tier provider.SensitivityTier
	}{
		{"public", provider.SensitivityPublic}, {"internal", provider.SensitivityInternal},
		{"confidential", provider.SensitivityRestricted}, {"secret", provider.SensitivityLocalOnly},
	} {
		j := baseJob(tc.word)
		j.DataClass = new(tc.tier)
		if err := s.PutJob(ctx, j); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := s.db.QueryRow("SELECT data_class FROM "+tableJob+" WHERE id=?", j.ID).Scan(&raw); err != nil || raw != tc.word {
			t.Fatalf("stored = %q: %v", raw, err)
		}
		got, ok, err := s.GetJob(ctx, j.ID)
		if err != nil || !ok || got.DataClass == nil || *got.DataClass != tc.tier {
			t.Fatalf("read = %+v: %v", got, err)
		}
		if err := s.PutExecution(ctx, Execution{ID: j.ID, JobID: j.ID, State: ExecutionRunning}); err != nil {
			t.Fatal(err)
		}
		a := Artifact{ID: j.ID, JobID: j.ID, ExecutionID: j.ID, ConsequenceClass: ConsequenceNormal, DataClass: new(tc.tier)}
		if err := s.PutArtifact(ctx, a); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow("SELECT data_class FROM "+tableArtifact+" WHERE id=?", a.ID).Scan(&raw); err != nil || raw != tc.word {
			t.Fatalf("artifact = %q: %v", raw, err)
		}
		artifact, ok, err := s.GetArtifact(ctx, a.ID)
		if err != nil || !ok || artifact.DataClass == nil || *artifact.DataClass != tc.tier {
			t.Fatalf("artifact read = %+v: %v", artifact, err)
		}
		// Equal values held by different pointers remain immutable-compatible.
		if err := s.PutArtifact(ctx, artifact); err != nil {
			t.Fatal(err)
		}
	}
	assertStoredClassList(t, s)
}

func assertStoredClassList(t *testing.T, s *Store) {
	t.Helper()
	list, _, err := s.ListJobs(context.Background(), JobFilter{})
	if err != nil || len(list) != 4 {
		t.Fatalf("list = %v: %v", list, err)
	}
	for _, j := range list {
		word, err := provider.DataClassName(*j.DataClass)
		if err != nil || word != j.ID {
			t.Fatalf("listed = %+v: %v", j, err)
		}
	}
}

func TestStoredJobDataClassReadsAsTier(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		word string
		tier provider.SensitivityTier
	}{{"secret", provider.SensitivityLocalOnly}, {"confidential", provider.SensitivityRestricted}} {
		if err := s.PutJob(ctx, baseJob(tc.word)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("UPDATE "+tableJob+" SET data_class=? WHERE id=?", tc.word, tc.word); err != nil {
			t.Fatal(err)
		}
		j, ok, err := s.GetJob(ctx, tc.word)
		if err != nil || !ok || j.DataClass == nil || *j.DataClass != tc.tier {
			t.Fatalf("read = %+v: %v", j, err)
		}
	}
	rows, _, err := s.ListJobs(ctx, JobFilter{})
	if err != nil || len(rows) != 2 {
		t.Fatalf("list = %v: %v", rows, err)
	}
	for _, j := range rows {
		word, err := provider.DataClassName(*j.DataClass)
		if err != nil || word != j.ID {
			t.Fatalf("listed = %+v: %v", j, err)
		}
	}
}

func TestUnsetDataClassFailsClosed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	j := baseJob("unset")
	j.DataClass = nil
	if err := s.PutJob(ctx, j); !cascade.HasKind(err, cascade.KindInvalidInput) || err.Error() == "" {
		t.Fatalf("nil job: %v", err)
	}
	a := Artifact{ID: "unset", JobID: "unset", ExecutionID: "unset", ConsequenceClass: ConsequenceNormal}
	if err := s.PutArtifact(ctx, a); !cascade.HasKind(err, cascade.KindInvalidInput) || err.Error() == "" {
		t.Fatalf("nil artifact: %v", err)
	}
	for _, table := range []string{tableJob, tableArtifact} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s rows = %d: %v", table, count, err)
		}
	}
}
