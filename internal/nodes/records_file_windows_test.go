//go:build windows

package nodes

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestIsTransientShareError: only the two conflict errnos, however
// wrapped, are transient.
func TestIsTransientShareError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"sharing violation in PathError", &os.PathError{Op: "open", Path: "x", Err: windows.ERROR_SHARING_VIOLATION}, true},
		{"access denied on rename", &os.LinkError{Op: "rename", Old: "a", New: "b", Err: windows.ERROR_ACCESS_DENIED}, true},
		{"rename access denied under cascade.Wrap", cascade.Wrap(cascade.KindUnavailable,
			&os.LinkError{Op: "rename", Old: "a", New: "b", Err: windows.ERROR_ACCESS_DENIED}, "wrapped"), true},
		{"access denied on open (real ACL)", &os.PathError{Op: "open", Path: "x", Err: windows.ERROR_ACCESS_DENIED}, false},
		{"access denied on mkdir", &os.PathError{Op: "mkdir", Path: "x", Err: windows.ERROR_ACCESS_DENIED}, false},
		{"access denied on link", &os.LinkError{Op: "link", Old: "a", New: "b", Err: windows.ERROR_ACCESS_DENIED}, false},
		{"file not found", &os.PathError{Op: "open", Path: "x", Err: windows.ERROR_FILE_NOT_FOUND}, false},
		{"lock violation", windows.ERROR_LOCK_VIOLATION, false},
		{"os.ErrNotExist", os.ErrNotExist, false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := isTransientShareError(c.err); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// openExclusive opens path with share mode 0, so any other open of it
// fails with ERROR_SHARING_VIOLATION until the handle closes: the same
// conflict a MoveFileEx replace produces, held for as long as the test
// wants.
func openExclusive(t *testing.T, path string) windows.Handle {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("CreateFile exclusive: %v", err)
	}
	return h
}

// seededBackend saves one record through the production backend, then
// returns a backend over the same file whose backoff is sleep, so a test
// can act at the first retry instead of racing a real timer.
func seededBackend(t *testing.T, sleep func(time.Duration)) (fileRecordBackend, string) {
	t.Helper()
	dataDir := t.TempDir()
	if err := NewFileRecordBackend(dataDir).Save(map[string]DeviceRecord{"n1": {NodeID: "n1"}}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	path := filepath.Join(dataDir, "nodes", "devices.json")
	return fileRecordBackend{path: path, sleep: sleep}, path
}

// TestFileRecordBackend_LoadRidesOutSharingViolation reproduces the CI
// failure for real: devices.json is held exclusively, so the first read
// gets ERROR_SHARING_VIOLATION. The handle is released at the first
// backoff, and Load must then succeed with the stored record.
func TestFileRecordBackend_LoadRidesOutSharingViolation(t *testing.T) {
	var h windows.Handle
	released := false
	sleeps := 0
	b, path := seededBackend(t, func(time.Duration) {
		sleeps++
		if !released {
			released = true
			if err := windows.CloseHandle(h); err != nil {
				t.Errorf("CloseHandle: %v", err)
			}
		}
	})
	h = openExclusive(t, path)
	got, err := b.Load()
	if !released {
		_ = windows.CloseHandle(h)
	}
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if sleeps == 0 {
		t.Fatal("Load never hit the sharing violation, so this test proved nothing")
	}
	if _, ok := got["n1"]; !ok {
		t.Fatalf("Load = %v, want record n1", got)
	}
}

// TestFileRecordBackend_LoadFailsClosedWhenHeld: a conflict that never
// clears returns the sharing violation, never an empty fleet.
func TestFileRecordBackend_LoadFailsClosedWhenHeld(t *testing.T) {
	sleeps := 0
	b, path := seededBackend(t, func(time.Duration) { sleeps++ })
	h := openExclusive(t, path)
	defer func() { _ = windows.CloseHandle(h) }()
	got, err := b.Load()
	if err == nil {
		t.Fatalf("Load succeeded with %v while the file was held", got)
	}
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("err = %v, want ERROR_SHARING_VIOLATION", err)
	}
	if sleeps != recordFileAttempts-1 {
		t.Fatalf("sleeps = %d, want %d", sleeps, recordFileAttempts-1)
	}
}

// TestFileRecordBackend_SaveRidesOutHeldTarget: another process holding
// devices.json without DELETE sharing makes the atomic rename fail (as
// ERROR_SHARING_VIOLATION or a rename ERROR_ACCESS_DENIED, by rename
// semantics). Share mode 0 guarantees the conflict on every Windows
// version. The handle closes at the first backoff, and Save must then
// publish the new record set.
func TestFileRecordBackend_SaveRidesOutHeldTarget(t *testing.T) {
	var h windows.Handle
	released := false
	sleeps := 0
	b, path := seededBackend(t, func(time.Duration) {
		sleeps++
		if !released {
			released = true
			if err := windows.CloseHandle(h); err != nil {
				t.Errorf("CloseHandle: %v", err)
			}
		}
	})
	h = openExclusive(t, path)
	err := b.Save(map[string]DeviceRecord{"n2": {NodeID: "n2"}})
	if !released {
		_ = windows.CloseHandle(h)
	}
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if sleeps == 0 {
		t.Fatal("Save never hit the rename conflict, so this test proved nothing")
	}
	got, err := b.Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if _, ok := got["n2"]; !ok {
		t.Fatalf("Load = %v, want record n2", got)
	}
}
