package jobs

import (
	"fmt"
	"syscall"
	"testing"
)

// TestWinHandleVerdict pins the Windows handle-probe classification on
// every OS: only an OpenProcess ERROR_INVALID_PARAMETER (87), bare or
// wrapped, or a successful GetExitCodeProcess with a code other than
// STILL_ACTIVE (259) reads as dead. Every other error reads as alive.
func TestWinHandleVerdict(t *testing.T) {
	cases := []struct {
		name    string
		openErr error
		exitErr error
		code    uint32
		want    bool
	}{
		{"open invalid parameter", syscall.Errno(87), nil, 0, false},
		{"open wrapped invalid parameter", fmt.Errorf("open: %w", syscall.Errno(87)), nil, 0, false},
		{"open access denied", syscall.Errno(5), nil, 0, true},
		{"open invalid handle", syscall.Errno(6), nil, 0, true},
		{"open not enough memory", syscall.Errno(8), nil, 0, true},
		{"exit code still active", nil, nil, 259, true},
		{"exit code 0", nil, nil, 0, false},
		{"exit code 1", nil, nil, 1, false},
		{"exit query access denied", nil, syscall.Errno(5), 0, true},
		{"exit query invalid handle", nil, syscall.Errno(6), 259, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := winHandleVerdict(tc.openErr, tc.exitErr, tc.code); got != tc.want {
				t.Errorf("winHandleVerdict(%v, %v, %d) = %v, want %v", tc.openErr, tc.exitErr, tc.code, got, tc.want)
			}
		})
	}
}
