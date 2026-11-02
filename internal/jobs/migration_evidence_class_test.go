package jobs

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestEvidenceClassMigration(t *testing.T) {
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, home)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	ctx, clock := context.Background(), newTestClock()
	if err := ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	set := EvidenceMigrationSet()
	if set.SchemaVersion != 2 || len(set.Steps) != 2 || set.Steps[1].Table.Name != tableEvidenceClass {
		t.Fatalf("migration = %+v", set)
	}
	base := set
	base.SchemaVersion = 1
	base.ReaderCeiling = 1
	base.Steps = base.Steps[:1]
	if err := migrate.Apply(ctx, migrate.ApplyConfig{DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: clock}, base); err != nil {
		t.Fatal(err)
	}
	s := NewStore(db)
	if err := s.PutJob(ctx, baseJob("job-1")); err != nil {
		t.Fatal(err)
	}
	evidenceSQL(t, s, `INSERT INTO jobs_evidence VALUES ('job-1',1,'build','controller-run','a','cp','tree','daemon:d',zeroblob(32),0,0,'pass','result','key','artifact',0)`)
	for i := 0; i < 2; i++ {
		if err := ApplyEvidenceSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	l, err := NewEvidenceLedger(s, clock, &recordingWriter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := l.QueryAll(ctx, "job-1")
	if err != nil || len(rows) != 1 || *rows[0].DataClass != *baseJob("job-1").DataClass || rows[0].ArtifactRef != "artifact" {
		t.Fatalf("legacy rows = %+v, %v", rows, err)
	}
	testLegacyClassFailure(t, l, s)
	evidenceSQL(t, s, `INSERT INTO jobs_evidence_class VALUES ('job-1',1,'internal')`)
	if _, err := db.Exec(`INSERT INTO jobs_evidence_class VALUES ('job-1',1,'public')`); err == nil {
		t.Fatal("missing composite primary key")
	}
}

func testLegacyClassFailure(t *testing.T, l *EvidenceLedger, s *Store) {
	t.Helper()
	evidenceSQL(t, s, `UPDATE jobs_job SET capabilities='invalid-json' WHERE id='job-1'`)
	rows, err := l.QueryAll(context.Background(), "job-1")
	if err != nil || len(rows) != 1 || *rows[0].DataClass != provider.SensitivityLocalOnly {
		t.Fatalf("unreadable legacy job = %+v, %v", rows, err)
	}
}

func TestEvidenceEffectiveClass(t *testing.T) {
	cases := []struct {
		name, job string
		rec       *provider.SensitivityTier
		want      provider.SensitivityTier
	}{
		{"raised", "public", new(provider.SensitivityRestricted), provider.SensitivityRestricted},
		{"inherit", "internal", nil, provider.SensitivityInternal},
		{"floor", "secret", new(provider.SensitivityPublic), provider.SensitivityLocalOnly},
		{"unknown-record", "public", new(provider.SensitivityTier(77)), provider.SensitivityLocalOnly},
		{"unknown-job", "nonsense", new(provider.SensitivityPublic), provider.SensitivityLocalOnly},
		{"unreadable-job", "public", new(provider.SensitivityPublic), provider.SensitivityLocalOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, s := evidenceFixture(t)
			evidenceSQL(t, s, `UPDATE jobs_job SET data_class=? WHERE id='job-1'`, tc.job)
			if tc.name == "unreadable-job" {
				evidenceSQL(t, s, `UPDATE jobs_job SET capabilities='invalid-json' WHERE id='job-1'`)
			}
			row := appendEvidence(t, l, "class", tc.rec)
			if row.DataClass == nil || *row.DataClass != tc.want {
				t.Fatalf("class = %v, want %v", row.DataClass, tc.want)
			}
			var stored string
			if err := s.db.QueryRow(`SELECT data_class FROM jobs_evidence_class`).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			want, _ := provider.DataClassName(tc.want)
			if stored != want {
				t.Fatalf("stored = %q, want %q", stored, want)
			}
		})
	}
	t.Run("rollback", testEvidenceClassRollback)
}

func testEvidenceClassRollback(t *testing.T) {
	l, s := evidenceFixture(t)
	appendEvidence(t, l, "present", nil)
	evidenceSQL(t, s, `CREATE TRIGGER fail_class BEFORE INSERT ON jobs_evidence_class BEGIN SELECT RAISE(ABORT, 'injected-class-failure'); END`)
	_, err := l.Append(context.Background(), passRecord(EvidenceBuild, "rollback"), baseAuth())
	if err == nil || !strings.Contains(err.Error(), "injected-class-failure") {
		t.Fatalf("lost insert error: %v", err)
	}
	for _, table := range []string{tableEvidence, tableEvidenceClass} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s count = %d, %v", table, n, err)
		}
	}
}

func TestEvidenceChainHashUnchangedByClass(t *testing.T) {
	l, s := evidenceFixture(t)
	first := appendEvidence(t, l, "1", new(provider.SensitivityPublic))
	second := appendEvidence(t, l, "2", new(provider.SensitivityLocalOnly))
	appendEvidence(t, l, "3", new(provider.SensitivityInternal))
	with, err := chainHash(first)
	if err != nil {
		t.Fatal(err)
	}
	first.DataClass = nil
	without, err := chainHash(first)
	if err != nil || !bytesEqual(with, without) || !bytesEqual(second.PrevHash, without) {
		t.Fatal("class entered chain")
	}
	evidenceSQL(t, s, `UPDATE jobs_evidence_class SET data_class='unknown' WHERE seq=1`)
	if err := l.Verify(context.Background(), "job-1"); err != nil {
		t.Fatal(err)
	}
	rows, err := l.QueryAll(context.Background(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, redacted := RedactForCeiling(rows[0], EvidenceReadCeiling); !redacted {
		t.Fatal("unknown stored class exposed")
	}
	evidenceSQL(t, s, `UPDATE jobs_evidence SET result_hash='tampered' WHERE seq=1`)
	if err := l.Verify(context.Background(), "job-1"); err == nil || !strings.Contains(err.Error(), ErrEvidenceChainBroken.Error()) {
		t.Fatalf("tamper = %v", err)
	}
}
