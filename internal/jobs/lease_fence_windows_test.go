//go:build windows

package jobs

import (
	"fmt"
	"math"
	"testing"

	"golang.org/x/sys/windows"
)

// fakeProcessHandle is the handle the recording fake hands out. It is
// never a real handle of the test process and is never really closed.
const fakeProcessHandle = windows.Handle(0x5a5a)

// openCall records one livenessOpenProcess call's arguments.
type openCall struct {
	access  uint32
	inherit bool
	pid     uint32
}

// handleSeams is a recording fake for the three Windows liveness seams:
// it returns the injected results and records every call's arguments.
type handleSeams struct {
	openErr  error
	exitCode uint32
	exitErr  error
	opens    []openCall
	queries  []windows.Handle
	closes   []windows.Handle
}

// installHandleSeams swaps the three seams for f and restores the
// production calls via t.Cleanup. Tests that call it never run in
// parallel, because the seams are package state.
func installHandleSeams(t *testing.T, f *handleSeams) {
	t.Helper()
	origOpen, origQuery, origClose := livenessOpenProcess, livenessGetExitCodeProcess, livenessCloseHandle
	t.Cleanup(func() {
		livenessOpenProcess, livenessGetExitCodeProcess, livenessCloseHandle = origOpen, origQuery, origClose
	})
	livenessOpenProcess = func(access uint32, inherit bool, pid uint32) (windows.Handle, error) {
		f.opens = append(f.opens, openCall{access, inherit, pid})
		if f.openErr != nil {
			return 0, f.openErr
		}
		return fakeProcessHandle, nil
	}
	livenessGetExitCodeProcess = func(h windows.Handle, code *uint32) error {
		f.queries = append(f.queries, h)
		*code = f.exitCode
		return f.exitErr
	}
	livenessCloseHandle = func(h windows.Handle) error {
		f.closes = append(f.closes, h)
		return nil
	}
}

// TestWindowsLivenessProbeOpenProcessErrors proves only an OpenProcess
// ERROR_INVALID_PARAMETER reads dead; access denied and every other
// error read alive. A failed open is never queried or closed.
func TestWindowsLivenessProbeOpenProcessErrors(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{windows.ERROR_INVALID_PARAMETER, false},
		{windows.ERROR_ACCESS_DENIED, true},
		{windows.ERROR_INVALID_HANDLE, true},
		{windows.ERROR_NOT_ENOUGH_MEMORY, true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.err), func(t *testing.T) {
			f := &handleSeams{openErr: tc.err}
			installHandleSeams(t, f)
			if got := (windowsLivenessProbe{}).IsAlive(4242); got != tc.want {
				t.Errorf("IsAlive(4242) with OpenProcess %v = %v, want %v", tc.err, got, tc.want)
			}
			want := openCall{windows.PROCESS_QUERY_LIMITED_INFORMATION, false, 4242}
			if len(f.opens) != 1 || f.opens[0] != want {
				t.Errorf("OpenProcess calls = %+v, want exactly [%+v]", f.opens, want)
			}
			if len(f.queries) != 0 || len(f.closes) != 0 {
				t.Errorf("GetExitCodeProcess calls %v, CloseHandle calls %v, want none", f.queries, f.closes)
			}
		})
	}
}

// TestWindowsLivenessProbeExitCode proves an opened handle is dead only
// when GetExitCodeProcess succeeds with a code other than STILL_ACTIVE,
// alive on any query error, and closed exactly once in every case.
func TestWindowsLivenessProbeExitCode(t *testing.T) {
	cases := []struct {
		name string
		code uint32
		err  error
		want bool
	}{
		{"still active", 259, nil, true},
		{"exit 0", 0, nil, false},
		{"exit 1", 1, nil, false},
		{"access denied", 0, windows.ERROR_ACCESS_DENIED, true},
		{"invalid handle", 0, windows.ERROR_INVALID_HANDLE, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &handleSeams{exitCode: tc.code, exitErr: tc.err}
			installHandleSeams(t, f)
			if got := (windowsLivenessProbe{}).IsAlive(4242); got != tc.want {
				t.Errorf("IsAlive(4242) with exit (%d, %v) = %v, want %v", tc.code, tc.err, got, tc.want)
			}
			if len(f.queries) != 1 || f.queries[0] != fakeProcessHandle {
				t.Errorf("GetExitCodeProcess calls = %v, want exactly [%#x]", f.queries, fakeProcessHandle)
			}
			if len(f.closes) != 1 || f.closes[0] != fakeProcessHandle {
				t.Errorf("CloseHandle calls = %v, want exactly [%#x]", f.closes, fakeProcessHandle)
			}
		})
	}
}

// TestWindowsLivenessProbeUnprobeableNeverOpens proves a pgid that
// cannot name a Windows pid reads alive without calling any seam, even
// when OpenProcess would have answered "no such process".
func TestWindowsLivenessProbeUnprobeableNeverOpens(t *testing.T) {
	for _, pgid := range []int64{0, -1, math.MinInt64, math.MaxUint32 + 1, 1<<32 + 1} {
		t.Run(fmt.Sprint(pgid), func(t *testing.T) {
			f := &handleSeams{openErr: windows.ERROR_INVALID_PARAMETER}
			installHandleSeams(t, f)
			if !(windowsLivenessProbe{}).IsAlive(pgid) {
				t.Errorf("IsAlive(%d) = false, want true for an unprobeable pgid", pgid)
			}
			if n := len(f.opens) + len(f.queries) + len(f.closes); n != 0 {
				t.Errorf("IsAlive(%d) made %d seam calls, want 0", pgid, n)
			}
		})
	}
}
