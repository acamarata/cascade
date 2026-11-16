//go:build !windows

// Purpose: retryCreate retries a spurious ENOENT only for an open that
// creates, and only a bounded number of times; EINTR is still retried.

package agents

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

// failingOpen returns errs in order, then a good fd, and counts calls.
func failingOpen(errs ...error) (func() (int, error), *int) {
	calls := 0
	return func() (int, error) {
		calls++
		if calls <= len(errs) {
			return -1, errs[calls-1]
		}
		return 7, nil
	}, &calls
}

func TestRetryCreateRetriesENOENTWhenCreating(t *testing.T) {
	open, calls := failingOpen(unix.ENOENT, unix.EINTR, unix.ENOENT)
	fd, err := retryCreate(unix.O_RDWR|unix.O_CREAT, open)
	if err != nil || fd != 7 || *calls != 4 {
		t.Fatalf("fd %d err %v calls %d; want fd 7, no error, 4 calls", fd, err, *calls)
	}
}

func TestRetryCreateBoundsENOENT(t *testing.T) {
	errs := make([]error, createENOENTRetries+5)
	for i := range errs {
		errs[i] = unix.ENOENT
	}
	open, calls := failingOpen(errs...)
	_, err := retryCreate(unix.O_RDWR|unix.O_CREAT, open)
	if !errors.Is(err, unix.ENOENT) || *calls != createENOENTRetries+1 {
		t.Fatalf("err %v calls %d; want ENOENT after %d calls", err, *calls, createENOENTRetries+1)
	}
}

func TestRetryCreateKeepsENOENTWithoutCreate(t *testing.T) {
	open, calls := failingOpen(unix.ENOENT)
	_, err := retryCreate(unix.O_RDONLY, open)
	if !errors.Is(err, unix.ENOENT) || *calls != 1 {
		t.Fatalf("err %v calls %d; want ENOENT at once without O_CREAT", err, *calls)
	}
}

func TestRetryCreateKeepsOtherErrors(t *testing.T) {
	open, calls := failingOpen(unix.EACCES)
	_, err := retryCreate(unix.O_RDWR|unix.O_CREAT, open)
	if !errors.Is(err, unix.EACCES) || *calls != 1 {
		t.Fatalf("err %v calls %d; want EACCES at once", err, *calls)
	}
}
