package learn

// Purpose: above-scope aggregation tests: hashed contributor keys and
//   aggregate values (asserted from stored rows), the finding fold, the
//   create-once 0600 salt file and every fail-closed refusal.
// SPORT: internal.learn.Aggregator/TESTED, internal.learn.EnsureAggregateSalt/TESTED (P1-CAP-03).

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/pkg/cascade"
)

func skipIfNoModeBits(t *testing.T) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("file mode bits carry no access control on windows")
	}
}

func saltPathIn(t *testing.T) string { return filepath.Join(t.TempDir(), "learn", "aggregate_salt") }

// saltFileWith writes data at a fresh salt path (dir 0700, file 0600) and returns it.
func saltFileWith(t *testing.T, data []byte) string {
	t.Helper()
	path := saltPathIn(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestAggregateSaltCreateOnceRefusesOverwrite: the salt is 32 random bytes,
// mode 0600 in a 0700 dir, created exactly once; a second call returns the
// same bytes; an existing valid file is never regenerated; an invalid one is
// refused and left untouched.
func TestAggregateSaltCreateOnceRefusesOverwrite(t *testing.T) {
	path := saltPathIn(t)
	salt, err := EnsureAggregateSalt(path)
	if err != nil || len(salt) != 32 || bytes.Equal(salt, make([]byte, 32)) {
		t.Fatalf("EnsureAggregateSalt = %x, %v, want 32 non-zero random bytes", salt, err)
	}
	if onDisk, _ := os.ReadFile(path); !bytes.Equal(onDisk, salt) {
		t.Fatalf("file holds %x, want the returned salt %x", onDisk, salt)
	}
	if goruntime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		di, _ := os.Stat(filepath.Dir(path))
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Errorf("modes = file %v dir %v, want 0600 and 0700", fi.Mode().Perm(), di.Mode().Perm())
		}
	}
	again, err := EnsureAggregateSalt(path)
	if err != nil || !bytes.Equal(again, salt) {
		t.Fatalf("second EnsureAggregateSalt = %x, %v, want the first salt unchanged", again, err)
	}
	other, err := EnsureAggregateSalt(saltPathIn(t))
	if err != nil || bytes.Equal(other, salt) {
		t.Errorf("a second location got %x (err %v), want a different random salt", other, err)
	}
	seven := bytes.Repeat([]byte{7}, 32)
	if got, err := EnsureAggregateSalt(saltFileWith(t, seven)); err != nil || !bytes.Equal(got, seven) {
		t.Errorf("existing valid file: got %x, %v, want it returned unchanged (O_EXCL, no overwrite)", got, err)
	}
	short := saltFileWith(t, []byte("tenbytes!!"))
	if _, err := EnsureAggregateSalt(short); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("10-byte file: EnsureAggregateSalt = %v, want KindIntegrity", err)
	}
	if kept, _ := os.ReadFile(short); string(kept) != "tenbytes!!" {
		t.Errorf("an invalid salt file was overwritten: now %q", kept)
	}
}

// TestAggregateSaltUnderRegularFile: a salt path below a regular file is
// KindUnavailable, and nothing is created.
func TestAggregateSaltUnderRegularFile(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureAggregateSalt(filepath.Join(blocked, "learn", "aggregate_salt")); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("salt under a regular file: EnsureAggregateSalt = %v, want KindUnavailable", err)
	}
}

// TestAggregateRefusesWithoutSalt: aggregation refuses typed when the salt
// file is absent, not 32 bytes, or group/world readable, and writes nothing.
func TestAggregateRefusesWithoutSalt(t *testing.T) {
	clock := newStepClock()
	s := newTestScorer(t, clock)
	seedScore(t, s, langScope(LanguageGo), conductor.TaskClassCode, capacity.TierOne, 9, 9, 9, clock.t)
	seedScore(t, s, "repo:repo-x-1", conductor.TaskClassCode, capacity.TierOne, 1, 0, 1, clock.t)
	cases := []struct {
		name  string
		write func(path string)
		kind  cascade.Kind
		modes bool
	}{
		{"absent", func(string) {}, cascade.KindNotFound, false},
		{"31 bytes", func(p string) { _ = os.WriteFile(p, make([]byte, 31), 0o600) }, cascade.KindIntegrity, false},
		{"33 bytes", func(p string) { _ = os.WriteFile(p, make([]byte, 33), 0o600) }, cascade.KindIntegrity, false},
		{"group readable", func(p string) { _ = os.WriteFile(p, make([]byte, 32), 0o640); _ = os.Chmod(p, 0o640) }, cascade.KindPermissionDenied, true},
		{"world readable", func(p string) { _ = os.WriteFile(p, make([]byte, 32), 0o604); _ = os.Chmod(p, 0o604) }, cascade.KindPermissionDenied, true},
	}
	for _, c := range cases {
		if c.modes {
			skipIfNoModeBits(t)
		}
		path := saltPathIn(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		c.write(path)
		sum, err := NewAggregator(s.db, clock, path).Aggregate(context.Background())
		if !cascade.HasKind(err, c.kind) || sum.Cells != 0 || len(sum.ContributorKeys) != 0 {
			t.Errorf("%s: Aggregate = %+v, %v, want an empty summary and kind %v", c.name, sum, err, c.kind)
		}
		if row, found := readStored(t, s, langScope(LanguageGo), conductor.TaskClassCode, capacity.TierOne); !found || row.alpha != 9 || row.count != 9 {
			t.Errorf("%s: sentinel lang row = %+v (found %v), want untouched", c.name, row, found)
		}
		if _, found := readStored(t, s, scopeGlobal, conductor.TaskClassCode, capacity.TierOne); found {
			t.Errorf("%s: a global row was written despite the refusal", c.name)
		}
	}
	var nilAgg *Aggregator
	if _, err := nilAgg.Aggregate(context.Background()); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("nil Aggregator = %v, want KindInvalidInput", err)
	}
}

type aggRow struct {
	alpha, beta float64
	count       int
}

// aggregateRows lists every lang:/global row as scope|class|tier -> values,
// plus all of their stored strings.
func aggregateRows(t *testing.T, s *SQLiteCapabilityScorer) (map[string]aggRow, []string) {
	t.Helper()
	rows, err := s.db.Query(`SELECT scope_key, task_class, tier, alpha, beta, observation_count FROM ` + tableCapabilityScore + ` ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out, strs := map[string]aggRow{}, []string{}
	for rows.Next() {
		var sc, tc, tier string
		var r aggRow
		if err := rows.Scan(&sc, &tc, &tier, &r.alpha, &r.beta, &r.count); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(sc, scopeRepoPrefix) { // the repo rows are the input, not the aggregate
			strs = append(strs, sc, tc, tier)
			out[sc+"|"+tc+"|"+tier] = r
		}
	}
	return out, strs
}

// aggFixture is a database seeded with four repos (two go, one python, one
// with no recorded outcome), three findings and a created salt file.
type aggFixture struct {
	s     *SQLiteCapabilityScorer
	clock *stepClock
	ids   []string
	salt  []byte
	agg   *Aggregator
}

func newAggFixture(t *testing.T) aggFixture {
	t.Helper()
	clock := newStepClock()
	f := aggFixture{s: newTestScorer(t, clock), clock: clock}
	repos := map[string]struct {
		lang      Language
		wins, bad int
	}{"repo-alpha-1": {LanguageGo, 3, 1}, "repo-beta-22": {LanguageGo, 2, 0}, "repo-gamma-3": {LanguagePython, 0, 1}, "repo-nolang-4": {"", 2, 0}}
	for id, r := range repos {
		f.ids = append(f.ids, id)
		if r.lang != "" {
			recordLangOutcome(t, f.s, "job-agg-"+id, id, r.lang)
		}
		for i := 0; i < r.wins+r.bad; i++ {
			if err := f.s.Observe(context.Background(), repoScope(id), conductor.TaskClassCode, capacity.TierOne, i < r.wins); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, fd := range []Finding{
		{"job-agg-repo-alpha-1", FamilyReview, CategorySecurity, SeverityHigh, 2},
		{"job-agg-repo-beta-22", FamilyReview, CategorySecurity, SeverityHigh, 3},
		{"job-agg-repo-beta-22", FamilyToolFailure, CategoryTimeout, SeverityLow, 4},
	} {
		if err := NewSQLiteFindingWriter(f.s.db).WriteFinding(context.Background(), fd); err != nil {
			t.Fatal(err)
		}
	}
	path := saltPathIn(t)
	var err error
	if f.salt, err = EnsureAggregateSalt(path); err != nil {
		t.Fatal(err)
	}
	f.agg = NewAggregator(f.s.db, clock, path)
	return f
}

// TestAggregateHashedIdentifiers: contributors are keyed on
// hex(blake3(salt || repo_id)); the lang and global rows hold the sum of the
// repo rows; no stored or returned string contains a repo id; findings fold
// by {family, category, severity}.
func TestAggregateHashedIdentifiers(t *testing.T) {
	f := newAggFixture(t)
	sum, err := f.agg.Aggregate(context.Background())
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	var wantKeys []string
	for _, id := range f.ids {
		h := blake3.Sum256(append(append([]byte{}, f.salt...), id...))
		wantKeys = append(wantKeys, hex.EncodeToString(h[:]))
	}
	slices.Sort(wantKeys)
	if !slices.Equal(sum.ContributorKeys, wantKeys) || len(wantKeys) != 4 {
		t.Fatalf("ContributorKeys = %v, want %v", sum.ContributorKeys, wantKeys)
	}
	rows, strs := aggregateRows(t, f.s)
	want := map[string]aggRow{
		"lang:go|code|tier-1": {5, 1, 6}, "lang:python|code|tier-1": {0, 1, 1}, "global|code|tier-1": {7, 2, 9},
	}
	if len(rows) != 3 || sum.Cells != 3 {
		t.Fatalf("aggregate rows = %v (Cells %d), want exactly %v", rows, sum.Cells, want)
	}
	for k, w := range want {
		if got := rows[k]; got.count != w.count || !near(got.alpha, w.alpha) || !near(got.beta, w.beta) {
			t.Errorf("aggregate %s = %+v, want %+v", k, got, w)
		}
	}
	for _, id := range f.ids {
		for _, str := range append(strs, sum.ContributorKeys...) {
			if strings.Contains(str, id) {
				t.Errorf("stored or returned string %q contains repo id %q", str, id)
			}
		}
	}
	wantFolds := []FindingFold{{FamilyReview, CategorySecurity, SeverityHigh, 5}, {FamilyToolFailure, CategoryTimeout, SeverityLow, 4}}
	if !slices.Equal(sum.Findings, wantFolds) {
		t.Errorf("Findings = %+v, want %+v", sum.Findings, wantFolds)
	}
}

// TestAggregateIdempotentAndDecayed: a second pass at one instant leaves the
// rows unchanged and the repo rows untouched; 30 days later the aggregates
// are rebuilt from the repo rows decayed by half; a closed db is refused.
func TestAggregateIdempotentAndDecayed(t *testing.T) {
	f, ctx := newAggFixture(t), context.Background()
	if _, err := f.agg.Aggregate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := aggregateRows(t, f.s)
	if again, err := f.agg.Aggregate(ctx); err != nil || again.Cells != 3 {
		t.Fatalf("second Aggregate = %+v, %v", again, err)
	}
	if rows2, _ := aggregateRows(t, f.s); len(rows2) != 3 || rows2["global|code|tier-1"] != rows["global|code|tier-1"] {
		t.Errorf("rows after a second pass = %v, want unchanged %v", rows2, rows)
	}
	if r, _ := readStored(t, f.s, "repo:repo-alpha-1", conductor.TaskClassCode, capacity.TierOne); r.alpha != 3 || r.beta != 1 || r.count != 4 {
		t.Errorf("repo row after aggregation = %+v, want untouched 3/1/4", r)
	}
	f.clock.advance(30 * 24 * time.Hour)
	if _, err := f.agg.Aggregate(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := aggregateRows(t, f.s); !near(got["lang:go|code|tier-1"].alpha, 2.5) || !near(got["lang:go|code|tier-1"].beta, 0.5) {
		t.Errorf("lang:go 30 days later = %+v, want alpha 2.5 beta 0.5 (repo rows decayed by half)", got["lang:go|code|tier-1"])
	}
	if err := f.s.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.agg.Aggregate(ctx); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("Aggregate on a closed db = %v, want KindUnavailable", err)
	}
}
