//go:build !windows

// Purpose: tests for the mutual code-signature check of the darwin helper
//
//	(R12 F-3): cascade refuses a helper that is unsigned, ad-hoc signed or
//	signed by another team, refuses to run when it is itself unsigned, and
//	refuses a helper swapped between the path check and the exec because the
//	running child is verified before a single request byte is written. The
//	production codesign wrapper is tested with an injected runner.
//
// SPORT: internal/elevation helper signature tests/ADDED (P1-SHIP-15).
package elevation

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	teamA = "ABCDE12345"
	teamB = "ZZZZZ99999"
)

var errSig = errors.New("not signed by the requirement")

// scriptVerifier answers the three checks from a script and records them.
type scriptVerifier struct {
	selfErr, pathErr, pidErr error
	helperTeam               string        // when set, both checks also compare the team
	afterPath                func()        // runs after VerifyPath answered: the swap window
	checkImage               bool          // VerifyPID fails when the file is no longer what VerifyPath saw
	pidDelay                 time.Duration // stands in for codesign's run time
	watch                    string        // VerifyPID waits (bounded) for this file: an eager writer shows itself
	atPID                    func()        // runs when VerifyPID is called, before it answers
	path                     string
	pathSum                  [32]byte
	pidChecked               []int
}

func sumFile(path string) [32]byte {
	b, _ := os.ReadFile(path)
	return sha256.Sum256(b)
}

func (v *scriptVerifier) SelfTeam() (string, error) { return teamA, v.selfErr }

func (v *scriptVerifier) VerifyPath(path, team string) error {
	v.path, v.pathSum = path, sumFile(path)
	var err error
	if v.pathErr != nil || (v.helperTeam != "" && v.helperTeam != team) {
		err = errSig
	}
	if v.afterPath != nil {
		v.afterPath()
	}
	return err
}

func (v *scriptVerifier) VerifyPID(pid int, team string) error {
	v.pidChecked = append(v.pidChecked, pid)
	if v.atPID != nil {
		v.atPID()
	}
	time.Sleep(v.pidDelay)
	for end := time.Now().Add(1500 * time.Millisecond); v.watch != "" && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(v.watch); err == nil {
			break
		}
	}
	if v.checkImage && sumFile(v.path) != v.pathSum {
		return errSig
	}
	if v.pidErr != nil || (v.helperTeam != "" && v.helperTeam != team) {
		return errSig
	}
	return nil
}

// withHelperSource points the production source at exe and v for one test.
func withHelperSource(t *testing.T, exe string, v sigVerifier) Selector {
	t.Helper()
	oldExe, oldVer := helperExecutable, helperVerifier
	helperExecutable, helperVerifier = func() (string, error) { return exe, nil }, v
	t.Cleanup(func() { helperExecutable, helperVerifier = oldExe, oldVer })
	srcs := sourcesForOS("darwin", func() ElevationKeystore { return unavailableKS{} })
	return Selector{Sources: append([]CustodySource{helperSource()}, srcs...)}
}

func TestHelperKeystoreRefusesUnsignedOrForeignTeamHelper(t *testing.T) {
	cases := map[string]*scriptVerifier{
		"unsigned cascade":          {selfErr: errSig},
		"unsigned or ad-hoc helper": {pathErr: errSig},
		"foreign team helper":       {helperTeam: teamB},
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			dir, exe := install(t, true)
			// The fake answers status available/platform/cgo true and signs
			// with a file-held key: only the signature checks stand in the way.
			sel := withHelperSource(t, exe, v)
			c := sel.Select()
			if c.Tier() != CustodyNone {
				t.Fatalf("tier = %s source = %s, want none", c.Tier(), c.Source())
			}
			if _, err := c.Signer(); err == nil {
				t.Fatal("Signer() allowed an elevated path")
			}
			_, err := newKS(exe, v).Sign([]byte("payload"))
			wantHelperKind(t, err, cascade.KindUnavailable)
			if n := execCount(dir); n != 0 {
				t.Fatalf("the helper ran %d times before its signature was accepted", n)
			}
		})
	}
}

func TestHelperKeystoreAcceptsSameTeamHelper(t *testing.T) {
	dir, exe := install(t, true)
	v := &scriptVerifier{helperTeam: teamA}
	if _, err := newKS(exe, v).Sign([]byte("payload")); err != nil {
		t.Fatalf("same-team helper refused: %v", err)
	}
	if len(v.pidChecked) != 1 || execCount(dir) != 1 {
		t.Fatalf("pid checks %v, execs %d; want one of each", v.pidChecked, execCount(dir))
	}
}

func TestHelperKeystoreRefusesSwapBetweenCheckAndExec(t *testing.T) {
	dir, exe := install(t, true)
	helper := filepath.Join(dir, HelperName)
	script := "#!/bin/sh\nread line <&3\nprintf '%s' \"$line\" > \"$(dirname \"$0\")/request\"\nprintf '{\"v\":1,\"ok\":true}\\n' >&3\n"
	swaps := 0
	v := &scriptVerifier{checkImage: true, watch: filepath.Join(dir, "request"), afterPath: func() {
		tmp := helper + ".swap"
		swaps++
		if err := os.WriteFile(tmp, []byte(script+"# swap "+strconv.Itoa(swaps)+"\n"), 0o755); err != nil {
			t.Error(err)
			return
		}
		if err := os.Rename(tmp, helper); err != nil {
			t.Error(err)
		}
	}}
	sel := withHelperSource(t, exe, v)
	if c := sel.Select(); c.Tier() != CustodyNone {
		t.Fatalf("tier = %s, want none after the swap", c.Tier())
	}
	if len(v.pidChecked) == 0 || swaps == 0 {
		t.Fatalf("pid checks %d, swaps %d: the swap window was never exercised", len(v.pidChecked), swaps)
	}
	if _, err := os.Stat(filepath.Join(dir, "request")); !os.IsNotExist(err) {
		t.Fatalf("a request byte reached the unverified child (stat err %v)", err)
	}
	for _, pid := range v.pidChecked {
		deadline := time.Now().Add(3 * time.Second)
		for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if syscall.Kill(pid, 0) == nil {
			t.Fatalf("child %d survived a failed verification", pid)
		}
	}
}
