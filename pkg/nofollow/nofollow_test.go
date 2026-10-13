//go:build !windows

// Purpose: prove NoFollow refuses a symlink as the final component and
//
//	NonBlock keeps a read-only open of a FIFO from blocking.
//
// Inputs: a temp dir holding a file, a symlink to it and a FIFO.
// Outputs: test verdicts only.
// Constraints: the FIFO open runs under a deadline so a broken flag fails
//
//	the test instead of hanging it.
//
// SPORT: pkg/nofollow (TEST) — P1-AGT-01.

package nofollow

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNoFollowRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if f, err := os.OpenFile(link, os.O_RDONLY|NoFollow, 0); err == nil {
		_ = f.Close()
		t.Fatal("OpenFile(symlink, NoFollow) succeeded")
	}
	f, err := os.OpenFile(target, os.O_RDONLY|NoFollow, 0)
	if err != nil {
		t.Fatalf("OpenFile(regular file, NoFollow) = %v", err)
	}
	_ = f.Close()
	if NoFollow != syscall.O_NOFOLLOW || NonBlock != syscall.O_NONBLOCK {
		t.Fatal("flags do not match the platform values")
	}
}

func TestNonBlockOpensFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_RDONLY|NoFollow|NonBlock, 0)
		if err == nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("OpenFile(fifo, NonBlock) = %v", err)
		}
	case <-time.After(5 * time.Second):
		// Unblock the stuck open so the goroutine ends with the test.
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("OpenFile(fifo, NonBlock) blocked")
	}
}
