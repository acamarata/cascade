package runtime

// Purpose: the portable proofs of contract:atomic-file-write: the file
//   and directory syncs happen in order (counted through the syncFile and
//   syncDir seams), a failure leaves the target byte-identical with no
//   temp file behind, and CreateFileAtomic is exclusive. The unix-only
//   SIGKILL and umask proofs live in atomic_write_kill_test.go.
// SPORT: runtime/atomic-file-write (ADD, P1-CORE-08).

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// atomicIsolateHome points HOME and USERPROFILE at a fresh temp dir, so no
// test in this file can reach the real home even by accident.
func atomicIsolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// atomicTempLeft lists any temp file a publish left in dir.
func atomicTempLeft(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	var left []string
	for _, e := range entries {
		if IsAtomicTempName(e.Name()) {
			left = append(left, e.Name())
		}
	}
	return left
}

// syncEvent records one sync and what the target held at that moment.
type syncEvent struct {
	kind   string
	target string
}

// recordSyncs swaps the sync seams for counting wrappers that still call
// the real syncs, restoring them when the test ends.
func recordSyncs(t *testing.T, target string) *[]syncEvent {
	t.Helper()
	events := &[]syncEvent{}
	origFile, origDir := syncFile, syncDir
	t.Cleanup(func() { syncFile, syncDir = origFile, origDir })
	read := func() string { b, _ := os.ReadFile(target); return string(b) }
	syncFile = func(f *os.File) error {
		*events = append(*events, syncEvent{"file", read()})
		return origFile(f)
	}
	syncDir = func(dir string) error {
		*events = append(*events, syncEvent{"dir", read()})
		return origDir(dir)
	}
	return events
}

func TestWriteFileAtomicSyncs(t *testing.T) {
	atomicIsolateHome(t)
	publish := map[string]func(path string, data []byte) error{
		"WriteFileAtomic": func(p string, d []byte) error { return WriteFileAtomic(p, d, 0o600) },
		"WriteReaderAtomic": func(p string, d []byte) error {
			return WriteReaderAtomic(p, bytes.NewReader(d), 0o600)
		},
		"WriteBytesAtomic": WriteBytesAtomic,
		"CreateFileAtomic": func(p string, d []byte) error {
			if err := os.Remove(p); err != nil {
				return err
			}
			_, err := CreateFileAtomic(p, d, 0o600)
			return err
		},
	}
	for name, fn := range publish {
		t.Run(name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			events := recordSyncs(t, target)
			if err := fn(target, []byte("new")); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			want := []syncEvent{{"file", "old"}, {"dir", "new"}}
			if name == "CreateFileAtomic" {
				want[0].target = "" // the target was removed so the create can win
			}
			if len(*events) != len(want) {
				t.Fatalf("%s: syncs = %+v, want exactly %+v", name, *events, want)
			}
			for i := range want {
				if (*events)[i] != want[i] {
					t.Fatalf("%s: sync %d = %+v, want %+v (file before publish, dir after)", name, i, (*events)[i], want[i])
				}
			}
		})
	}
}

// failingReader yields some bytes, then an error, so a partial temp file
// exists at the moment of failure.
type failingReader struct{ sent bool }

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, bytes.Repeat([]byte("x"), 1024)), nil
	}
	return 0, errors.New("reader broke")
}

// assertFailureLeftTarget checks the failure contract for one write.
func assertFailureLeftTarget(t *testing.T, err error, dir, target string, want []byte) {
	t.Helper()
	if err == nil {
		t.Fatal("write succeeded, want a failure")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("error kind = %v (ok=%v), want KindUnavailable: %v", kind, ok, err)
	}
	if !strings.Contains(err.Error(), target) {
		t.Fatalf("error %q does not name the path %s", err, target)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil || !bytes.Equal(got, want) {
		t.Fatalf("target = %q (%v), want byte-identical %q", got, readErr, want)
	}
	if left := atomicTempLeft(t, dir); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestWriteFileAtomicFailureLeavesTarget(t *testing.T) {
	atomicIsolateHome(t)
	original := []byte("original contents\n")
	t.Run("failing reader", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "catalog.json")
		if err := os.WriteFile(target, original, 0o600); err != nil {
			t.Fatal(err)
		}
		err := WriteReaderAtomic(target, &failingReader{}, 0o600)
		assertFailureLeftTarget(t, err, dir, target, original)
	})
	t.Run("read-only directory", func(t *testing.T) {
		if goruntime.GOOS == "windows" {
			t.Skip("windows ignores directory permission bits for file creation")
		}
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permission bits")
		}
		dir := t.TempDir()
		target := filepath.Join(dir, "cursor.json")
		if err := os.WriteFile(target, original, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		err := WriteFileAtomic(target, []byte("replacement"), 0o600)
		assertFailureLeftTarget(t, err, dir, target, original)
	})
}

// raceCreate runs two concurrent CreateFileAtomic calls on one fresh path
// and returns each caller's created flag.
func raceCreate(t *testing.T, path string, payloads [2][]byte, perm os.FileMode) [2]bool {
	t.Helper()
	var created [2]bool
	var errs [2]error
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range payloads {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			created[i], errs[i] = CreateFileAtomic(path, payloads[i], perm)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	return created
}

func TestCreateFileAtomicIsExclusive(t *testing.T) {
	atomicIsolateHome(t)
	const perm os.FileMode = 0o640
	payloads := [2][]byte{bytes.Repeat([]byte("A"), 4096), bytes.Repeat([]byte("B"), 7)}
	for round := 0; round < 40; round++ {
		dir := t.TempDir()
		path := filepath.Join(dir, "key")
		created := raceCreate(t, path, payloads, perm)
		if created[0] == created[1] {
			t.Fatalf("round %d: created = %v, want exactly one true", round, created)
		}
		winner := 0
		if created[1] {
			winner = 1
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, payloads[winner]) {
			t.Fatalf("round %d: file = %d bytes (%v), want caller %d's %d bytes", round, len(got), err, winner, len(payloads[winner]))
		}
		assertPerm(t, path, perm)
		if left := atomicTempLeft(t, dir); len(left) != 0 {
			t.Fatalf("round %d: temp files left behind: %v", round, left)
		}
	}
	// An existing target is never replaced, and that is not an error.
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := CreateFileAtomic(path, []byte("second"), perm)
	if err != nil || created {
		t.Fatalf("CreateFileAtomic over an existing file = (%v, %v), want (false, nil)", created, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "first" {
		t.Fatalf("existing file replaced: %q", got)
	}
}

// assertPerm checks a file's permission bits on platforms that have them.
func assertPerm(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != perm {
		t.Fatalf("%s mode = %#o, want %#o", path, got, perm)
	}
}

func TestIsAtomicTempName(t *testing.T) {
	cases := map[string]bool{
		".state.json.123456.tmp": true,
		".key.99.tmp":            true,
		"state.json":             false,
		".hidden.tmp":            false,
		"state.json.tmp":         false,
	}
	for name, want := range cases {
		if got := IsAtomicTempName(name); got != want {
			t.Errorf("IsAtomicTempName(%q) = %v, want %v", name, got, want)
		}
	}
}
