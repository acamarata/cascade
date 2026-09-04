package memory

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// failingFS is the file-system double. It delegates nothing: every method
// either returns the configured failure or the benign empty answer, so a
// test states exactly which operation breaks. It lives in _test.go and is
// unexported, so no shipped path can reach it (Art.1).
type failingFS struct {
	failRead    bool
	failWrite   bool
	failRemove  bool
	failExists  bool
	failListDir bool
	present     map[string][]byte
}

var errInjected = errors.New("injected file-system failure")

func newFailingFS() *failingFS { return &failingFS{present: map[string][]byte{}} }

func (f *failingFS) ReadFile(path string) ([]byte, error) {
	if f.failRead {
		return nil, errInjected
	}
	if data, ok := f.present[path]; ok {
		return data, nil
	}
	return nil, fs.ErrNotExist
}

func (f *failingFS) WriteAtomic(path string, data []byte) error {
	if f.failWrite {
		return errInjected
	}
	f.present[path] = data
	return nil
}

func (f *failingFS) Remove(path string) error {
	if f.failRemove {
		return errInjected
	}
	if _, ok := f.present[path]; !ok {
		return fs.ErrNotExist
	}
	delete(f.present, path)
	return nil
}

func (f *failingFS) Exists(path string) (bool, error) {
	if f.failExists {
		return false, errInjected
	}
	_, ok := f.present[path]
	return ok, nil
}

func (f *failingFS) ReadDirNames(string) ([]string, error) {
	if f.failListDir {
		return nil, errInjected
	}
	return nil, nil
}

// TestFileSystemFailuresBecomeTypedErrors proves no raw OS error escapes:
// every I/O failure comes back classified, so a caller can act on it.
func TestFileSystemFailuresBecomeTypedErrors(t *testing.T) {
	ctx := context.Background()
	e := validEntry()

	cases := []struct {
		name string
		set  func(*failingFS)
		call func(*FileStore) error
	}{
		{"write fails", func(f *failingFS) { f.failWrite = true },
			func(s *FileStore) error { return s.Write(ctx, e) }},
		{"read fails", func(f *failingFS) { f.failRead = true },
			func(s *FileStore) error { _, err := s.Read(ctx, e.Kind, e.Name); return err }},
		{"tombstone check fails", func(f *failingFS) { f.failExists = true },
			func(s *FileStore) error { _, err := s.Read(ctx, e.Kind, e.Name); return err }},
		{"exists fails", func(f *failingFS) { f.failExists = true },
			func(s *FileStore) error { _, err := s.Exists(ctx, e.Kind, e.Name); return err }},
		{"listing fails", func(f *failingFS) { f.failListDir = true },
			func(s *FileStore) error { _, err := s.List(ctx, e.Kind); return err }},
		{"update read fails", func(f *failingFS) { f.failRead = true },
			func(s *FileStore) error { return s.Update(ctx, e) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sys := newFailingFS()
			c.set(sys)
			err := c.call(newFileStoreWithFS(t.TempDir(), newTestClock(), sys))
			if !errors.Is(err, ErrStoreIO) {
				t.Fatalf("error = %v, want ErrStoreIO", err)
			}
			if !cascade.HasKind(err, cascade.KindUnavailable) {
				t.Fatalf("error %v is not a KindUnavailable taxonomy error", err)
			}
			var pathErr *os.PathError
			if errors.As(err, &pathErr) {
				t.Fatalf("a raw os.PathError escaped: %v", err)
			}
		})
	}
}

// TestDeleteReportsTombstoneAndRemovalFailures covers the two write steps
// of a delete, which the read-side table above cannot reach.
func TestDeleteReportsTombstoneAndRemovalFailures(t *testing.T) {
	ctx := context.Background()
	e := validEntry()
	sys := newFailingFS()
	s := newFileStoreWithFS(t.TempDir(), newTestClock(), sys)
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	sys.failWrite = true
	if err := s.Delete(ctx, e.Kind, e.Name); !errors.Is(err, ErrStoreIO) {
		t.Fatalf("tombstone write failure = %v, want ErrStoreIO", err)
	}
	sys.failWrite, sys.failRemove = false, true
	if err := s.Delete(ctx, e.Kind, e.Name); !errors.Is(err, ErrStoreIO) {
		t.Fatalf("record removal failure = %v, want ErrStoreIO", err)
	}
}

// TestPersistReportsTombstoneClearFailure covers the resurrect path.
func TestPersistReportsTombstoneClearFailure(t *testing.T) {
	ctx := context.Background()
	e := validEntry()
	sys := newFailingFS()
	s := newFileStoreWithFS(t.TempDir(), newTestClock(), sys)
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Delete(ctx, e.Kind, e.Name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	sys.failRemove = true
	if err := s.Write(ctx, e); !errors.Is(err, ErrStoreIO) {
		t.Fatalf("tombstone clear failure = %v, want ErrStoreIO", err)
	}
}

// TestOneDamagedRecordDoesNotBreakTheStore is the isolation requirement:
// a file this build cannot parse refuses itself and nothing else.
func TestOneDamagedRecordDoesNotBreakTheStore(t *testing.T) {
	ctx := context.Background()
	s, _, base := newStore(t)
	for _, n := range []string{"good", "damaged"} {
		e := validEntry()
		e.Name = n
		if err := s.Write(ctx, e); err != nil {
			t.Fatalf("Write %s: %v", n, err)
		}
	}
	damaged := filepath.Join(base, "project", "damaged.md")
	if err := os.WriteFile(damaged, []byte("---\nname: not-quoted\n"), 0o600); err != nil {
		t.Fatalf("damaging a record: %v", err)
	}

	_, err := s.Read(ctx, KindProject, "damaged")
	if !errors.Is(err, ErrMalformedEntry) || !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("damaged record error = %v, want a KindIntegrity ErrMalformedEntry", err)
	}
	if _, err := s.Read(ctx, KindProject, "good"); err != nil {
		t.Errorf("the undamaged record became unreadable: %v", err)
	}
	names, err := s.List(ctx, KindProject)
	if err != nil || len(names) != 2 {
		t.Errorf("List = %v, %v; a damaged file must not break listing", names, err)
	}
	// A damaged record must stay deletable, or the file is stuck forever.
	if err := s.Delete(ctx, KindProject, "damaged"); err != nil {
		t.Errorf("Delete on a damaged record: %v", err)
	}
}

// TestWriteRefusesToOverwriteAnUnreadableRecord proves a write does not
// destroy a file it could not understand, which is the fail-closed choice:
// the file may have been written by a newer build.
func TestWriteRefusesToOverwriteAnUnreadableRecord(t *testing.T) {
	ctx := context.Background()
	s, _, base := newStore(t)
	e := validEntry()
	path := filepath.Join(base, "project", e.Name+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	future := strings.Replace(string(mustReadGolden(t, "entry_user.md")), "format: 1", "format: 99", 1)
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatalf("seeding a future-format record: %v", err)
	}
	if err := s.Write(ctx, e); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("Write over a future-format record = %v, want ErrUnsupportedFormat", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != future {
		t.Fatal("the refused write modified the file it could not read")
	}
}

// TestInvalidIdentityIsRefusedBeforeAnyIO proves a hostile name never
// reaches the file system: every operation on the injected file system is
// set to fail, so any call at all would surface as an I/O error instead.
func TestInvalidIdentityIsRefusedBeforeAnyIO(t *testing.T) {
	ctx := context.Background()
	sys := newFailingFS()
	sys.failRead, sys.failWrite, sys.failExists, sys.failListDir = true, true, true, true
	s := newFileStoreWithFS(t.TempDir(), newTestClock(), sys)

	for _, name := range []string{"../escape", "sub/dir", ".hidden", ""} {
		if _, err := s.Read(ctx, KindProject, name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Read(%q) = %v, want ErrInvalidName", name, err)
		}
		if err := s.Delete(ctx, KindProject, name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Delete(%q) = %v, want ErrInvalidName", name, err)
		}
		if _, err := s.Exists(ctx, KindProject, name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Exists(%q) = %v, want ErrInvalidName", name, err)
		}
	}
	for _, k := range []MemoryKind{"", "lesson", "../user"} {
		if _, err := s.Read(ctx, k, "ok-name"); !errors.Is(err, ErrInvalidKind) {
			t.Errorf("Read(kind %q) = %v, want ErrInvalidKind", k, err)
		}
		if _, err := s.List(ctx, k); !errors.Is(err, ErrInvalidKind) {
			t.Errorf("List(kind %q) = %v, want ErrInvalidKind", k, err)
		}
		if _, err := s.Exists(ctx, k, "ok-name"); !errors.Is(err, ErrInvalidKind) {
			t.Errorf("Exists(kind %q) = %v, want ErrInvalidKind", k, err)
		}
	}
}

// TestCanceledContextIsRefused proves every entry point honours
// cancellation rather than performing I/O the caller no longer wants.
func TestCanceledContextIsRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, _, _ := newStore(t)
	e := validEntry()

	calls := map[string]error{
		"Write":  s.Write(ctx, e),
		"Update": s.Update(ctx, e),
		"Delete": s.Delete(ctx, e.Kind, e.Name),
	}
	if _, err := s.Read(ctx, e.Kind, e.Name); err != nil {
		calls["Read"] = err
	}
	if _, err := s.List(ctx, e.Kind); err != nil {
		calls["List"] = err
	}
	if _, err := s.Exists(ctx, e.Kind, e.Name); err != nil {
		calls["Exists"] = err
	}
	for name, err := range calls {
		if !cascade.HasKind(err, cascade.KindCanceled) {
			t.Errorf("%s on a canceled context = %v, want a KindCanceled error", name, err)
		}
	}
}

// TestListSkipsUnrelatedFiles proves the listing reports records only, so
// a stray temp file or a hand-dropped note does not appear as a record.
func TestListSkipsUnrelatedFiles(t *testing.T) {
	ctx := context.Background()
	s, _, base := newStore(t)
	e := validEntry()
	if err := s.Write(ctx, e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dir := filepath.Join(base, "project")
	for _, junk := range []string{"notes.txt", ".memory-123.md.tmp", "no-extension", "a-record.md.tombstone.bak"} {
		if err := os.WriteFile(filepath.Join(dir, junk), nil, 0o600); err != nil {
			t.Fatalf("writing %s: %v", junk, err)
		}
	}
	names, err := s.List(ctx, KindProject)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "a-record" {
		t.Fatalf("List = %v, want just the one record", names)
	}
}
