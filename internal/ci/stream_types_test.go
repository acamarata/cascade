// Purpose: the dispatcher's contract types: the CheckpointIDFor golden, the
// additive risk-to-requirement mapping and the stream-only predicate.
//
// SPORT: internal.ci.CheckpointIDFor/TESTED (P1-CI-01).
package ci

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestTelemetryRefusesUnwritableLayout(t *testing.T) {
	for _, blockMode := range []bool{false, true} {
		home := t.TempDir()
		mode := telemetryModePath("linux", home)
		message := "creating the go telemetry directory"
		if blockMode {
			if err := os.MkdirAll(mode, 0o700); err != nil {
				t.Fatal(err)
			}
			message = "turning go telemetry off"
		} else {
			writeRepoFile(t, home, ".config", "not a directory")
		}
		assertStreamError(t, disableGoTelemetry("linux", home), cascade.KindUnavailable, message)
	}
	home := t.TempDir()
	if err := disableGoTelemetry("linux", home); err != nil {
		t.Fatal(err)
	}
	mode, err := os.ReadFile(filepath.Join(home, ".config", "go", "telemetry", "mode"))
	if err != nil || string(mode) != "off" {
		t.Fatalf("mode = %q, %v; want off", mode, err)
	}
}

// assertStreamError checks both the taxonomy and the failing operation.
func assertStreamError(t *testing.T, err error, kind cascade.Kind, message string) {
	t.Helper()
	if !cascade.HasKind(err, kind) || !strings.Contains(err.Error(), message) {
		t.Fatalf("error = %v, want %s containing %q", err, kind, message)
	}
}

func TestAsKindPreservesExistingTaxonomy(t *testing.T) {
	if err := asKind(nil, cascade.KindUnavailable, "storage"); err != nil {
		t.Fatalf("nil became %v", err)
	}
	cause := errors.New("disk failed")
	err := asKind(cause, cascade.KindUnavailable, "storage")
	assertStreamError(t, err, cascade.KindUnavailable, "storage")
	if !errChainHas(err, cause) {
		t.Fatalf("wrapped error lost its cause: %v", err)
	}
	typed := fmt.Errorf("outer: %w", cascade.New(cascade.KindIntegrity, "corrupt dispatch"))
	if got := asKind(typed, cascade.KindUnavailable, "storage"); got != typed {
		t.Fatalf("typed error replaced: %v", got)
	}
	assertStreamError(t, typed, cascade.KindIntegrity, "corrupt dispatch")
}

func TestLocalExecutorNamesEveryMissingDependency(t *testing.T) {
	deps := LocalExecutorDeps{CIDB: openTestDB(t), Clock: newTestClock(), Exec: &lockedExec{},
		Commands: allKindCommands(), RunRoot: t.TempDir(), ModCache: t.TempDir()}
	for field, remove := range map[string]func(*LocalExecutorDeps){
		"CIDB":     func(d *LocalExecutorDeps) { d.CIDB = nil },
		"Clock":    func(d *LocalExecutorDeps) { d.Clock = nil },
		"Exec":     func(d *LocalExecutorDeps) { d.Exec = nil },
		"Commands": func(d *LocalExecutorDeps) { d.Commands = nil },
		"RunRoot":  func(d *LocalExecutorDeps) { d.RunRoot = "" },
		"ModCache": func(d *LocalExecutorDeps) { d.ModCache = "" },
	} {
		t.Run(field, func(t *testing.T) {
			bad := deps
			remove(&bad)
			ex, err := NewLocalSubJobExecutor(bad)
			assertStreamError(t, err, cascade.KindInvalidInput, "requires "+field)
			if ex != nil {
				t.Fatal("incomplete dependencies returned an executor")
			}
		})
	}
	if ex, err := NewLocalSubJobExecutor(deps); err != nil || ex == nil {
		t.Fatalf("valid dependencies: %v, %v", ex, err)
	}
}

func TestCheckpointIDGolden(t *testing.T) {
	const want = "a1024273aaa8a53d91fd99970ee09d63a33cfa00d3429042e59401141998271a" // sha256("job-1|aaaabbbb|cccc0000")
	got := CheckpointIDFor("job-1", "aaaabbbb", "cccc0000")
	if got != want {
		t.Fatalf("CheckpointIDFor = %s, want golden %s", got, want)
	}
	if CheckpointIDFor("job-1", "aaaabbbb", "cccc0001") == got || CheckpointIDFor("job-2", "aaaabbbb", "cccc0000") == got {
		t.Fatal("CheckpointIDFor must change when the job or the tree changes")
	}
}

func TestRequirementForRiskIsAdditive(t *testing.T) {
	order := []jobs.RiskClass{jobs.RiskClassLow, jobs.RiskClassNormal, jobs.RiskClassHigh, jobs.RiskClassCritical}
	count := func(r CIRequirement) int {
		n := 0
		for _, k := range allRequirementKinds {
			if r.Requires(k) {
				n++
			}
		}
		return n
	}
	prev := 0
	for _, c := range order {
		r, err := requirementForRisk(c)
		if err != nil {
			t.Fatalf("requirementForRisk(%s): %v", c, err)
		}
		if r.isZero() {
			t.Fatalf("class %s mapped to the zero requirement, which would read as all-required", c)
		}
		if n := count(r); n < prev {
			t.Fatalf("class %s requires %d kinds, fewer than the lower class's %d", c, n, prev)
		} else {
			prev = n
		}
	}
	if low, _ := requirementForRisk(jobs.RiskClassLow); count(low) != 2 {
		t.Fatalf("low requires %d kinds, want 2 (format, lint)", count(low))
	}
	if high, _ := requirementForRisk(jobs.RiskClassHigh); high != AllRequired {
		t.Fatalf("high = %+v, want every kind", high)
	}
	if _, err := requirementForRisk("bogus"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("unknown class: err = %v, want KindInvalidInput", err)
	}
}

func TestStreamOnlyFollowsDeclaredUntracked(t *testing.T) {
	if (CandidateSnapshot{}).StreamOnly() {
		t.Fatal("a snapshot without untracked paths must not be stream-only")
	}
	if !(CandidateSnapshot{Untracked: []string{"a.txt"}}).StreamOnly() {
		t.Fatal("a snapshot with declared untracked paths must be stream-only")
	}
}

func TestStreamErrorKindsAreFrozen(t *testing.T) {
	want := map[*cascade.Error]cascade.Kind{
		ErrCheckpointStale: cascade.KindConflict, ErrAlreadyRunning: cascade.KindConflict, ErrLateResult: cascade.KindConflict,
		ErrScopeViolation: cascade.KindPolicyDenied, ErrSensitivityLowered: cascade.KindPolicyDenied,
		ErrTreeHashMismatch: cascade.KindIntegrity,
	}
	for e, kind := range want {
		if e.Kind != kind {
			t.Fatalf("%v has kind %v, want %v", e, e.Kind, kind)
		}
	}
}
