package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

type evidencePublishFailure struct {
	provider.Store
	err error
}

func (s *evidencePublishFailure) Put(ctx context.Context, namespace, key string, value []byte) error {
	if namespace == EvidenceNamespace && s.err != nil {
		return s.err
	}
	return s.Store.Put(ctx, namespace, key, value)
}

func evidenceFixture(t *testing.T) (*EvidenceLedger, *Store) {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, home)
	}
	l, s, _ := newLedgerFixture(t, time.Unix(1700000000, 0).UTC())
	return l, s
}

func appendEvidence(t *testing.T, l *EvidenceLedger, key string, class *provider.SensitivityTier) EvidenceRecord {
	t.Helper()
	rec := passRecord(EvidenceBuild, key)
	rec.DataClass = class
	out, err := l.Append(context.Background(), rec, baseAuth())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func evidenceSQL(t *testing.T, s *Store, statement string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceQueryAll(t *testing.T) {
	l, s := evidenceFixture(t)
	ctx := context.Background()
	want := []EvidenceRecord{}
	for i := 0; i < 3; i++ {
		want = append(want, appendEvidence(t, l, fmt.Sprint(i), new(provider.SensitivityInternal)))
	}
	job := baseJob("job-b")
	if err := s.PutJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	l.authz = nil
	rec := passRecord(EvidenceLint, "other")
	rec.JobID = job.ID
	if _, err := l.Append(ctx, rec, AppendAuthorization{}); err != nil {
		t.Fatal(err)
	}
	got, err := l.QueryAll(ctx, "job-1")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("QueryAll = %+v, %v; want %+v", got, err, want)
	}
	if _, err := l.QueryAll(ctx, "job-c"); err != ErrNoEvidence || err.Error() != ErrNoEvidence.Error() {
		t.Fatalf("empty: %v", err)
	}
	evidenceSQL(t, s, `ALTER TABLE jobs_evidence RENAME TO unavailable_evidence`)
	if _, err := l.QueryAll(ctx, "job-1"); err == nil || !strings.Contains(err.Error(), "jobs_evidence") {
		t.Fatalf("query failure lost: %v", err)
	}
}

func TestRedactForCeiling(t *testing.T) {
	classes := []*provider.SensitivityTier{new(provider.SensitivityPublic), new(provider.SensitivityInternal), new(provider.SensitivityRestricted), new(provider.SensitivityLocalOnly), new(provider.SensitivityTier(99))}
	rows := make([]EvidenceRecord, 0, len(classes))
	for i, class := range classes {
		rec := passRecord(EvidenceBuild, fmt.Sprint(i))
		rec.Seq = int64(i + 1)
		rec.DataClass = class
		got, redacted := RedactForCeiling(rec, EvidenceReadCeiling)
		want := rec
		if i >= 2 {
			want.ArtifactRef = ""
			want.ResultHash = ""
			want.AttestorIdentity = ""
			want.IdempotencyKey = ""
		}
		if redacted != (i >= 2) || !reflect.DeepEqual(got, want) {
			t.Fatalf("class %v: %+v, redacted %v", class, got, redacted)
		}
		rows = append(rows, got)
	}
	if len(rows) != 5 || rows[0].Seq != 1 || rows[4].Seq != 5 {
		t.Fatal("lost rows or order")
	}
}

func TestEvidenceChangedPublished(t *testing.T) {
	l, s := evidenceFixture(t)
	storage := &evidencePublishFailure{Store: storetest.NewMemStore()}
	bus := events.New(storage, l.clock)
	t.Cleanup(func() { _ = bus.Close() })
	l.WithBus(bus)
	rec := appendEvidence(t, l, "first", new(provider.SensitivityInternal))
	appendEvidence(t, l, "first", new(provider.SensitivityPublic))
	l.authz = NewProducerAuthz(s, func() bool { return false }, nil)
	if _, err := l.Append(context.Background(), passRecord(EvidenceBuild, "denied"), baseAuth()); err == nil {
		t.Fatal("authorization admitted")
	}
	l.authz = nil
	evidenceSQL(t, s, `CREATE TRIGGER refuse_class BEFORE INSERT ON jobs_evidence_class BEGIN SELECT RAISE(ABORT, 'class-insert-failure'); END`)
	if _, err := l.Append(context.Background(), passRecord(EvidenceBuild, "failed"), baseAuth()); err == nil {
		t.Fatal("transaction admitted")
	}
	evs, err := bus.Replay(context.Background(), EvidenceNamespace, 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	var changed EvidenceChanged
	if err := json.Unmarshal(evs[0].Payload, &changed); err != nil {
		t.Fatal(err)
	}
	if evs[0].Kind != EventEvidenceChanged || changed != (EvidenceChanged{rec.JobID, rec.Seq, rec.Kind, rec.Outcome, "internal"}) {
		t.Fatalf("changed = %+v", changed)
	}
	evidenceSQL(t, s, `DROP TRIGGER refuse_class`)
	storage.err = errors.New("injected publish failure")
	appendEvidence(t, l, "publish-fails", nil)
	l.WithBus(nil)
	appendEvidence(t, l, "without-bus", nil)
	rows, err := l.QueryAll(context.Background(), rec.JobID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows = %d, %v", len(rows), err)
	}
}
