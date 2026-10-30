// Purpose: the file pending-clear store (round trip, modes, corrupt-file
//
//	refusal) and the crash between the clipboard write and its confirm,
//	re-executing this test binary per the P1-SEC-33 recipe.
//
// SPORT: internal/secrets NewFilePendingClearStore/TEST (P1-SEC-09).
package secrets

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

func storePath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "data", "secrets", "pending-clears.json")
}

// Refs in the shape Write mints: 26 Crockford base32 characters.
const (
	refA = "0000000000000000000000AAA1"
	refB = "0000000000000000000000BBB2"
	refM = "000000000000000000000M0DE1"
)

// TestFilePendingClearStoreRefusesForeignRefs: a row whose ref is not a
// cascade.ID (too long, too short, lower case, a path) is refused by Put and,
// on disk, reads as a corrupt file.
func TestFilePendingClearStoreRefusesForeignRefs(t *testing.T) {
	for name, ref := range map[string]string{
		"64 chars":   strings.Repeat("A", 64),
		"27 chars":   refA + "1",
		"25 chars":   refA[1:],
		"lower case": strings.ToLower(refA),
		"excluded I": "000000000000000000000000I1",
		"path":       "../../../../../../../etc/x",
		"empty":      "",
	} {
		ctx := context.Background()
		path := storePath(t)
		if err := NewFilePendingClearStore(path).Put(ctx, row(ref, clipNow)); !rowRefused(err) {
			t.Fatalf("%s: Put = %v, want the corrupt-row refusal", name, err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: a refused Put wrote the file (%v)", name, err)
		}
		body := `{"version":1,"rows":[{"ref_id":"` + ref + `","platform":"darwin","written_at":"2026-10-29T12:00:00Z","clear_due_at":"2026-10-29T12:00:30Z"}]}`
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFilePendingClearStore(path).List(ctx); !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Fatalf("%s: List = %v, want KindIntegrity", name, err)
		}
	}
	if err := NewFilePendingClearStore(storePath(t)).Put(context.Background(), PendingClear{RefID: refA}); !rowRefused(err) {
		t.Fatalf("zero times: Put = %v, want the corrupt-row refusal", err)
	}
}

func TestFilePendingClearStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewFilePendingClearStore(storePath(t))
	if rows, err := s.List(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("empty List = %v, %v", rows, err)
	}
	a, b := row(refA, clipNow), row(refB, clipNow.Add(time.Minute))
	for _, r := range []PendingClear{a, b} {
		if err := s.Put(ctx, r); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := s.Put(ctx, a); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("duplicate Put = %v, want KindConflict", err)
	}
	if err := s.Delete(ctx, refA); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(ctx, refA); err != nil {
		t.Fatalf("Delete of an absent row = %v, want nil", err)
	}
	rows, err := s.List(ctx)
	if err != nil || len(rows) != 1 || rows[0].RefID != refB || !rows[0].ClearDueAt.Equal(b.ClearDueAt) {
		t.Fatalf("List = %+v, %v; want only %s", rows, err, refB)
	}
	if err := NewFilePendingClearStore("relative.json").Put(ctx, a); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("relative path Put = %v, want KindInvalidInput", err)
	}
}

func TestFilePendingClearStoreMode0600(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	path := storePath(t)
	if err := NewFilePendingClearStore(path).Put(context.Background(), row(refM, clipNow)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, %v; want 0700", di.Mode().Perm(), err)
	}
}

// TestFilePendingClearStoreCorruptRefuses: every corrupt shape refuses, and
// a writer over it never reaches the clipboard.
func TestFilePendingClearStoreCorruptRefuses(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      "{oops",
		"wrong version": `{"version":2,"rows":[]}`,
		"unknown field": `{"version":1,"rows":[],"x":1}`,
		"trailing data": `{"version":1,"rows":[]} {}`,
		"bad row":       `{"version":1,"rows":[{"ref_id":"../x","platform":"","written_at":"2026-10-29T12:00:00Z","clear_due_at":"2026-10-29T12:00:30Z"}]}`,
	} {
		path := storePath(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		s := NewFilePendingClearStore(path)
		if _, err := s.List(context.Background()); !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Fatalf("%s: List = %v, want KindIntegrity", name, err)
		}
		r := newRig()
		w, err := newClipboardWriterWithOps(runtime.NewFixedClock(clipNow), r.aw, r.fx, fakeVerifier{verb: ClipboardApprovalVerb}, s, r.sched, r.ops, 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Write(context.Background(), []byte("tok"), []byte("v")); err == nil || r.tr.count("set") != 0 {
			t.Fatalf("%s: Write = %v, sets = %d; want a refusal before set", name, err, r.tr.count("set"))
		}
		if body, _ := os.ReadFile(path); name == "not json" && string(body) != "{oops" {
			t.Fatalf("%s: the corrupt file was rewritten", name)
		}
	}
}

const clipCrashEnv, clipCrashCode = "CASCADE_CLIP_CRASH_CHILD", 3

// crashOps records each effect as a line in a file; setValue then kills
// the process before ConfirmEffect.
type crashOps struct{ log string }

func (c crashOps) platform() string { return "fake" }
func (c crashOps) probe() error     { return nil }
func (c crashOps) setValue(context.Context, []byte) error {
	if err := appendLine(c.log, "set"); err != nil {
		os.Exit(7)
	}
	os.Exit(clipCrashCode)
	return nil
}
func (c crashOps) clearValue(context.Context) error { return appendLine(c.log, "clear") }

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(line + "\n")
	return errors.Join(err, f.Close())
}

func crashLogs(t *testing.T, dir string) (*audit.Log, *audit.EffectLog, func()) {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	log := audit.New(store, runtime.NewFixedClock(clipNow), nil)
	el, err := audit.NewEffectLog(log)
	if err != nil {
		t.Fatal(err)
	}
	return log, el, func() { _ = store.Close() }
}

// TestClipboardCrashBetweenEffectAndConfirm: a process killed after setValue
// and before the confirm leaves the open intent and the row; the re-arm
// clears it under a confirmed record and settles the intent.
func TestClipboardCrashBetweenEffectAndConfirm(t *testing.T) {
	if dir := os.Getenv(clipCrashEnv); dir != "" {
		_, el, _ := crashLogs(t, dir)
		w, err := newClipboardWriterWithOps(runtime.NewFixedClock(clipNow), &fakeAudit{}, el, fakeVerifier{verb: ClipboardApprovalVerb},
			NewFilePendingClearStore(filepath.Join(dir, "pending-clears.json")), &fakeScheduler{}, crashOps{filepath.Join(dir, "ops.log")}, 30*time.Second)
		if err != nil {
			os.Exit(5)
		}
		_ = w.Write(context.Background(), []byte("tok"), []byte("v"))
		os.Exit(6) // Write returned: the crash did not happen where intended
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestClipboardCrashBetweenEffectAndConfirm$", "-test.count=1")
	cmd.Env = append(os.Environ(), clipCrashEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != clipCrashCode {
		t.Fatalf("child = %v, want exit %d; output:\n%s", err, clipCrashCode, out)
	}
	log, el, closeStore := crashLogs(t, dir)
	defer closeStore()
	store := NewFilePendingClearStore(filepath.Join(dir, "pending-clears.json"))
	rows, err := store.List(ctx)
	pending, perr := el.PendingEffects(ctx)
	if err != nil || len(rows) != 1 || perr != nil || len(pending) != 1 || !strings.HasPrefix(pending[0].Key, "clipboard.write:"+rows[0].RefID) {
		t.Fatalf("after crash: rows = %+v (%v), open intents = %+v (%v)", rows, err, pending, perr)
	}
	ops := crashOps{filepath.Join(dir, "ops.log")}
	later := runtime.NewFixedClock(clipNow.Add(time.Hour))
	if err := rearmWithOps(ctx, later, el, store, &fakeScheduler{}, func() (clipboardOps, error) { return ops, nil }); err != nil {
		t.Fatalf("rearm: %v", err)
	}
	if phase, _, err := el.EffectState(ctx, pending[0].Key); err != nil || phase != audit.EffectUnknown {
		t.Fatalf("write intent after re-arm = %q, %v; want unknown-outcome", phase, err)
	}
	left, _ := store.List(ctx)
	open, _ := el.PendingEffects(ctx)
	effects, _ := os.ReadFile(ops.log)
	if len(left) != 0 || len(open) != 0 || string(effects) != "set\nclear\n" {
		t.Fatalf("rows = %d, open = %d, effects = %q; want the row gone, nothing open, one set and one clear", len(left), len(open), effects)
	}
	if err := log.Verify(ctx); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// TestFilePendingClearStoreIOFailures: a directory where the file belongs
// reads as corrupt, and a parent that is a regular file refuses Put with
// nothing written.
func TestFilePendingClearStoreIOFailures(t *testing.T) {
	ctx := context.Background()
	path := storePath(t)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFilePendingClearStore(path).List(ctx); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("directory at the path: List = %v, want KindIntegrity", err)
	}
	if err := NewFilePendingClearStore(path).Delete(ctx, refA); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("directory at the path: Delete = %v, want KindIntegrity", err)
	}
	parent := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewFilePendingClearStore(filepath.Join(parent, "secrets", "pending-clears.json")).Put(ctx, row(refA, clipNow)); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("parent is a file: Put = %v, want KindUnavailable", err)
	}
}

// rowRefused matches Put's invalid-row refusal by kind and message, not by
// errors.Is (which matches any error of the sentinel's kind).
func rowRefused(err error) bool {
	return cascade.HasKind(err, cascade.KindInvalidInput) && err != nil && strings.Contains(err.Error(), "pending clear row is invalid")
}
