//go:build unix

package runtime

// Purpose: the unix-only proofs of contract:atomic-file-write. A child
//   process rewriting one file in a loop is SIGKILLed at seeded offsets and
//   the file must always hold one whole payload (with an os.WriteFile
//   control that must tear at least once), and the published mode equals
//   perm exactly under two umasks, including over a file with other bits.
// SPORT: runtime/atomic-file-write (ADD, P1-CORE-08).

import (
	"bufio"
	"bytes"
	"errors"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// killChildEnv carries "<mode>|<target>" into the re-executed test binary;
// mode is "atomic" (WriteFileAtomic) or "bare" (os.WriteFile, the control).
const killChildEnv = "CASCADE_ATOMIC_KILL_CHILD"

// killPayloads are the two payloads the child alternates: 64 KiB and one
// byte, so a torn or truncated file can never equal either.
func killPayloads() [2][]byte {
	return [2][]byte{bytes.Repeat([]byte{0xA5}, 64<<10), {0x5A}}
}

// runKillChild is the child's whole life: rewrite the target forever,
// announcing readiness after the first complete write.
func runKillChild(spec string) {
	mode, target, _ := strings.Cut(spec, "|")
	p := killPayloads()
	for i := 0; ; i++ {
		var err error
		if mode == "bare" {
			err = os.WriteFile(target, p[i%2], 0o600) // control: the write the gate forbids
		} else {
			err = WriteFileAtomic(target, p[i%2], 0o600)
		}
		if err != nil {
			os.Exit(3)
		}
		if i == 0 {
			_, _ = os.Stdout.WriteString("ready\n")
		}
	}
}

// killOnce starts a child, waits for its first write, sleeps offset, then
// SIGKILLs it and returns what the target holds afterwards.
func killOnce(t *testing.T, mode, target string, offset time.Duration) []byte {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestWriteFileAtomicSurvivesSIGKILL$")
	cmd.Env = append(os.Environ(), killChildEnv+"="+mode+"|"+target, "HOME="+home, "USERPROFILE="+home)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(out).ReadString('\n'); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("child never became ready: %v", err)
	}
	time.Sleep(offset)
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if !diedOfSIGKILL(cmd.Wait()) {
		t.Fatalf("child did not die of SIGKILL (a helper that exits on its own would hide a failed rewrite)")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target after kill: %v", err)
	}
	return got
}

// diedOfSIGKILL reports whether a Wait result is a death by SIGKILL, not a
// normal exit (such as the child's exit 3 on a failed write).
func diedOfSIGKILL(waitErr error) bool {
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

// TestDiedOfSIGKILL proves the check can fail: a normal exit, a failing
// exit and a SIGTERM death are not SIGKILL; a SIGKILL death is.
func TestDiedOfSIGKILL(t *testing.T) {
	if diedOfSIGKILL(nil) {
		t.Fatal("a nil Wait result is a normal exit, not SIGKILL")
	}
	if diedOfSIGKILL(exec.Command("sh", "-c", "exit 3").Run()) {
		t.Fatal("exit 3 reported as SIGKILL")
	}
	term := exec.Command("sh", "-c", "kill -TERM $$")
	if diedOfSIGKILL(term.Run()) {
		t.Fatal("SIGTERM death reported as SIGKILL")
	}
	if !diedOfSIGKILL(exec.Command("sh", "-c", "kill -KILL $$").Run()) {
		t.Fatal("SIGKILL death not recognised")
	}
}

// isWholePayload reports whether got is byte-for-byte one of the payloads.
func isWholePayload(got []byte) bool {
	p := killPayloads()
	return bytes.Equal(got, p[0]) || bytes.Equal(got, p[1])
}

func TestWriteFileAtomicSurvivesSIGKILL(t *testing.T) {
	if spec := os.Getenv(killChildEnv); spec != "" {
		runKillChild(spec)
		return
	}
	if testing.Short() {
		t.Skip("spawns 50+ child processes")
	}
	atomicIsolateHome(t)
	rng := rand.New(rand.NewSource(20261004)) //nolint:gosec // seeded offsets, not security
	target := filepath.Join(t.TempDir(), "state.bin")
	for i := 0; i < 50; i++ {
		got := killOnce(t, "atomic", target, time.Duration(rng.Intn(4000))*time.Microsecond)
		if !isWholePayload(got) {
			t.Fatalf("kill %d: target holds %d bytes, neither whole payload (torn write published)", i, len(got))
		}
	}
	// Control: the same loop over os.WriteFile must tear at least once, or
	// the kill harness proves nothing about atomicity.
	bare := filepath.Join(t.TempDir(), "bare.bin")
	for i := 0; i < 400; i++ {
		if got := killOnce(t, "bare", bare, time.Duration(rng.Intn(4000))*time.Microsecond); !isWholePayload(got) {
			t.Logf("control: os.WriteFile tore on kill %d (%d bytes)", i, len(got))
			return
		}
	}
	t.Fatal("control: 400 SIGKILLs never caught os.WriteFile mid-write; the harness cannot detect a torn write")
}

// replaceSeed writes a pre-existing file with bits different from perm.
func replaceSeed(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	other := os.FileMode(0o666)
	if perm == other {
		other = 0o600
	}
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, other); err != nil {
		t.Fatal(err)
	}
}

func TestWriteFileAtomicMode(t *testing.T) {
	atomicIsolateHome(t)
	for _, mask := range []int{0o022, 0o077} {
		for _, perm := range []os.FileMode{0o600, 0o644, 0o400} {
			checkModesUnderUmask(t, mask, perm)
		}
	}
	bytesAtomic := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteBytesAtomic(bytesAtomic, []byte("x")); err != nil {
		t.Fatal(err)
	}
	assertPerm(t, bytesAtomic, 0o600)
}

// checkModesUnderUmask publishes a fresh, a replacing and an exclusive
// file under mask and checks each carries exactly perm.
func checkModesUnderUmask(t *testing.T, mask int, perm os.FileMode) {
	t.Helper()
	old := syscall.Umask(mask)
	defer syscall.Umask(old)
	dir := t.TempDir()
	fresh := filepath.Join(dir, "fresh")
	if err := WriteFileAtomic(fresh, []byte("x"), perm); err != nil {
		t.Fatalf("umask %#o perm %#o: %v", mask, perm, err)
	}
	assertPerm(t, fresh, perm)
	replaced := filepath.Join(dir, "replaced")
	replaceSeed(t, replaced, perm)
	if err := WriteFileAtomic(replaced, []byte("after"), perm); err != nil {
		t.Fatalf("umask %#o perm %#o replace: %v", mask, perm, err)
	}
	assertPerm(t, replaced, perm)
	created := filepath.Join(dir, "created")
	if ok, err := CreateFileAtomic(created, []byte("x"), perm); err != nil || !ok {
		t.Fatalf("umask %#o perm %#o create: (%v, %v)", mask, perm, ok, err)
	}
	assertPerm(t, created, perm)
}
