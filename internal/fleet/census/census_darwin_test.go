//go:build darwin

package census

import (
	"golang.org/x/sys/unix"

	"testing"
)

// Purpose: darwin's enumerateRaw against injected kinfoProcListFn/
//
//	procArgs2Fn fakes, so this test never depends on the machine's live
//	process table (AGENT-BRIEF.md's determinism requirement).
//
// SPORT: fleet/census (ADD, per T-1 sport_updates).

func encodeProcArgs2(t *testing.T, argv []string) []byte {
	t.Helper()
	argc := len(argv)
	buf := []byte{byte(argc), byte(argc >> 8), byte(argc >> 16), byte(argc >> 24)}
	buf = append(buf, "/usr/local/bin/exe\x00\x00\x00"...)
	for _, a := range argv {
		buf = append(buf, a...)
		buf = append(buf, 0)
	}
	buf = append(buf, "SOME_ENV=value\x00"...)
	return buf
}

func TestDarwinEnumerateRawHappyPath(t *testing.T) {
	origList, origArgs := kinfoProcListFn, procArgs2Fn
	defer func() { kinfoProcListFn, procArgs2Fn = origList, origArgs }()

	kinfoProcListFn = func() ([]unix.KinfoProc, error) {
		var p1, p2 unix.KinfoProc
		p1.Proc.P_pid = 111
		p2.Proc.P_pid = 222
		return []unix.KinfoProc{p1, p2}, nil
	}
	procArgs2Fn = func(pid int) ([]byte, error) {
		switch pid {
		case 111:
			return encodeProcArgs2(t, []string{"/usr/local/bin/claude", "--json"}), nil
		default:
			return encodeProcArgs2(t, []string{"/usr/local/bin/opencode"}), nil
		}
	}

	raws, err := enumerateRaw()
	if err != nil {
		t.Fatalf("enumerateRaw: %v", err)
	}
	if len(raws) != 2 {
		t.Fatalf("len(raws) = %d, want 2", len(raws))
	}
}

func TestDarwinEnumerateRawSkipsUnreadablePid(t *testing.T) {
	origList, origArgs := kinfoProcListFn, procArgs2Fn
	defer func() { kinfoProcListFn, procArgs2Fn = origList, origArgs }()

	kinfoProcListFn = func() ([]unix.KinfoProc, error) {
		var p unix.KinfoProc
		p.Proc.P_pid = 333
		return []unix.KinfoProc{p}, nil
	}
	procArgs2Fn = func(_ int) ([]byte, error) {
		return nil, unix.EPERM
	}

	raws, err := enumerateRaw()
	if err != nil {
		t.Fatalf("enumerateRaw: %v", err)
	}
	if len(raws) != 0 {
		t.Fatalf("raws = %+v, want empty (unreadable pid skipped)", raws)
	}
}

func TestDarwinEnumerateRawListFailureIsTyped(t *testing.T) {
	origList, origArgs := kinfoProcListFn, procArgs2Fn
	defer func() { kinfoProcListFn, procArgs2Fn = origList, origArgs }()

	kinfoProcListFn = func() ([]unix.KinfoProc, error) { return nil, unix.EIO }
	procArgs2Fn = origArgs

	if _, err := enumerateRaw(); err == nil {
		t.Fatal("enumerateRaw: want typed error, got nil")
	}
}
