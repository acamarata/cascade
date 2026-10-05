package secrets

// Purpose: quarantine key proofs - one key created once under a race, torn or
// non-regular key paths refused as found with no create attempt, a bounded
// non-blocking read, mode 0600. No test prints key bytes (a child: a digest).

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// keyChildEnv carries "<dir>|<wait|go>" into a re-executed test binary.
const keyChildEnv = "CASCADE_QUARANTINE_KEY_CHILD"

// openKeyReport opens a store per spec; reports "created=<bool> sum=<sha256>" or "error=<err>".
func openKeyReport(spec string) string {
	dir, mode, _ := strings.Cut(spec, "|")
	if mode == "wait" {
		_, _ = os.Stdout.WriteString("ready\n")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
	store, err := NewQuarantineStore(dir, fixedClock{})
	if err != nil {
		return fmt.Sprintf("error=%v", err)
	}
	return fmt.Sprintf("created=%t sum=%x", store.keyCreated, sha256.Sum256(store.key))
}

// keyChildCmd builds a key child with spec in its env and a throwaway HOME.
func keyChildCmd(t *testing.T, spec, name string, args ...string) *exec.Cmd {
	home := t.TempDir()
	cmd := exec.Command(name, append(args, "-test.count=1")...)
	cmd.Env = append(os.Environ(), keyChildEnv+"="+spec, "HOME="+home, "USERPROFILE="+home)
	return cmd
}

// childReport returns the openKeyReport line from a child's stdout.
func childReport(t *testing.T, out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "created=") || strings.HasPrefix(line, "error=") {
			return line
		}
	}
	t.Fatalf("child printed no report: %q", out)
	return ""
}

// TestQuarantineKeyCreateRace: 16 goroutines and an OS process open one empty
// dir at once; all end on the file's 32-byte key and exactly one created it.
func TestQuarantineKeyCreateRace(t *testing.T) {
	if spec := os.Getenv(keyChildEnv); spec != "" {
		_, _ = os.Stdout.WriteString(openKeyReport(spec) + "\n")
		return
	}
	dir := t.TempDir()
	cmd := keyChildCmd(t, dir+"|wait", os.Args[0], "-test.run=^TestQuarantineKeyCreateRace$")
	stdin, inErr := cmd.StdinPipe()
	stdout, outErr := cmd.StdoutPipe()
	mustDo(t, errors.Join(inErr, outErr, cmd.Start()))
	child := bufio.NewReader(stdout)
	if line, err := child.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child never became ready: %q %v", line, err)
	}
	reports, gate := make([]string, 16), make(chan struct{})
	var wg sync.WaitGroup
	for i := range reports {
		wg.Go(func() { <-gate; reports[i] = openKeyReport(dir + "|go") })
	}
	close(gate)
	_, _ = io.WriteString(stdin, "go\n")
	wg.Wait()
	rest, rerr := io.ReadAll(child)
	mustDo(t, errors.Join(cmd.Wait(), rerr))
	reports = append(reports, childReport(t, string(rest)))
	onDisk, err := os.ReadFile(filepath.Join(dir, quarantineKeyName))
	if err != nil || len(onDisk) != quarantineKeyBytes {
		t.Fatalf("key file after the race: len=%d err=%v, want %d bytes", len(onDisk), err, quarantineKeyBytes)
	}
	sum, creations := fmt.Sprintf(" sum=%x", sha256.Sum256(onDisk)), 0
	for i, report := range reports {
		if !strings.HasSuffix(report, sum) {
			t.Errorf("opener %d (16 is the child process) does not hold the file's key: %s", i, report)
		}
		creations += strings.Count(report, "created=true")
	}
	if creations != 1 {
		t.Fatalf("%d openers reported creating the key, want exactly 1", creations)
	}
}

// TestQuarantineKeyTornRefuses: 0/31/33-byte and 8 MiB keys are refused (KindIntegrity), left as found.
func TestQuarantineKeyTornRefuses(t *testing.T) {
	for _, n := range []int{0, 31, 33, 8 << 20} {
		t.Run(fmt.Sprintf("%d_bytes", n), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, quarantineKeyName)
			torn := bytes.Repeat([]byte{'Q'}, n)
			mustDo(t, os.WriteFile(path, torn, 0o600))
			before := lsDir(t, dir)
			_, err := NewQuarantineStore(dir, fixedClock{})
			assertKeyRefusal(t, err, cascade.KindIntegrity, path, fmt.Sprintf("holds %d bytes", n), "move the file aside")
			if n > 0 && n <= 64 && (strings.Contains(err.Error(), string(torn)) || strings.Contains(err.Error(), fmt.Sprintf("%x", torn))) {
				t.Error("the refusal carries the key bytes")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, torn) || lsDir(t, dir) != before {
				t.Errorf("a refused open changed the key dir: %d bytes before, %d after (err %v)", n, len(after), err)
			}
		})
	}
}

// keyRefusalRows: key path states to refuse; setup returns a path that must stay absent, or "".
var keyRefusalRows = []struct {
	name, want string
	kind       cascade.Kind
	setup      func(t *testing.T, dir, p string) (mustStayAbsent string)
}{
	{"directory", "is a directory, not a regular file", cascade.KindIntegrity, func(t *testing.T, _, p string) string { mustDo(t, os.Mkdir(p, 0o700)); return "" }},
	{"symlink_to_valid_0644_key", "is a symbolic link", cascade.KindIntegrity, func(t *testing.T, _, p string) string { linkElsewhere(t, p, true); return "" }},
	{"dangling_symlink", "is a symbolic link", cascade.KindIntegrity, func(t *testing.T, _, p string) string { return linkElsewhere(t, p, false) }},
	{"fifo", "is a named pipe", cascade.KindIntegrity, func(t *testing.T, _, p string) string { mkfifoOrSkip(t, p); return "" }},
	{"unreadable_key", "could not open the quarantine key", cascade.KindUnavailable, func(t *testing.T, _, p string) string {
		skipWithoutPermBits(t)
		mustDo(t, os.WriteFile(p, make([]byte, quarantineKeyBytes), 0))
		return ""
	}},
	{"unsearchable_dir", "could not stat the quarantine key", cascade.KindUnavailable, func(t *testing.T, dir, _ string) string {
		skipWithoutPermBits(t)
		mustDo(t, os.Chmod(dir, 0o600))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		return ""
	}},
	{"unwritable_empty_dir", "could not create the quarantine key", cascade.KindUnavailable, func(t *testing.T, dir, p string) string {
		skipWithoutPermBits(t)
		makeDirGenuinelyUnwritable(t, dir)
		return p
	}},
}

// TestQuarantineKeyUnusableRefuses: each row is refused in bounded time by its step, all left as found.
func TestQuarantineKeyUnusableRefuses(t *testing.T) {
	for _, row := range keyRefusalRows {
		t.Run(row.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, quarantineKeyName)
			absent := row.setup(t, dir, path)
			before := lsDir(t, dir)
			err := within(t, 5*time.Second, func() error { _, err := NewQuarantineStore(dir, fixedClock{}); return err })
			assertKeyRefusal(t, err, row.kind, path, row.want)
			_, lerr := os.Lstat(absent)
			if after := lsDir(t, dir); before != after || absent != "" && !os.IsNotExist(lerr) {
				t.Errorf("a refused open changed the dir (%v -> %v) or created %q (%v)", before, after, absent, lerr)
			}
		})
	}
}

// TestQuarantineKeyCheckedOpenRefusesSwap: a path swapped after its Lstat fails, and a FIFO never blocks.
func TestQuarantineKeyCheckedOpenRefusesSwap(t *testing.T) {
	dir := t.TempDir()
	checkedPath := filepath.Join(dir, "checked")
	mustDo(t, os.WriteFile(checkedPath, make([]byte, quarantineKeyBytes), 0o600))
	checked, err := os.Lstat(checkedPath)
	if key, kerr := readCheckedKey(checkedPath, checked); err != nil || kerr != nil || len(key) != quarantineKeyBytes {
		t.Fatalf("the checked file itself was refused: %v %v", err, kerr)
	}
	for _, name := range []string{"regular", "fifo"} {
		t.Run(name, func(t *testing.T) {
			swapped := filepath.Join(dir, name)
			if name == "fifo" {
				mkfifoOrSkip(t, swapped)
			} else {
				mustDo(t, os.WriteFile(swapped, make([]byte, quarantineKeyBytes), 0o600))
			}
			err := within(t, 3*time.Second, func() error { _, err := readCheckedKey(swapped, checked); return err })
			assertKeyRefusal(t, err, cascade.KindIntegrity, swapped, "is not the regular file")
		})
	}
}

// TestQuarantineKeyReadIsBounded: the reader stops one byte past a key, so a big file stays unread.
func TestQuarantineKeyReadIsBounded(t *testing.T) {
	src := bytes.NewReader(make([]byte, 1<<20))
	key, err := readBoundedKey(src)
	if err != nil || len(key) != quarantineKeyBytes+1 || src.Len() != 1<<20-quarantineKeyBytes-1 {
		t.Fatalf("read %d bytes, %d left unread (err %v), want %d read", len(key), src.Len(), err, quarantineKeyBytes+1)
	}
}

// TestQuarantineKeyMode: a key created under umask 022 (set by sh for the child
// alone, echoed as proof) is mode 0600. Windows has no POSIX bits or sh.
func TestQuarantineKeyMode(t *testing.T) {
	if spec := os.Getenv(keyChildEnv); spec != "" {
		_, _ = os.Stdout.WriteString(openKeyReport(spec) + "\n")
		return
	}
	if goruntime.GOOS == "windows" {
		t.Skip("no POSIX mode bits or sh on Windows")
	}
	dir := t.TempDir()
	out, err := keyChildCmd(t, dir+"|go", "/bin/sh", "-c", `umask 022 && umask && exec "$0" "$@"`,
		os.Args[0], "-test.run=^TestQuarantineKeyMode$").Output()
	mustDo(t, err)
	umask, _, _ := strings.Cut(string(out), "\n")
	if report := childReport(t, string(out)); strings.TrimLeft(umask, "0") != "22" || !strings.HasPrefix(report, "created=true") {
		t.Fatalf("child did not run under umask 022 or did not create the key: umask %q, %s", umask, report)
	}
	if info, err := os.Stat(filepath.Join(dir, quarantineKeyName)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the created key: %v (err %v), want mode 0600", info, err)
	}
}

// assertKeyRefusal: err has kind, names path and want, and precedes any create attempt.
func assertKeyRefusal(t *testing.T, err error, kind cascade.Kind, path string, want ...string) {
	t.Helper()
	msg, want := fmt.Sprint(err), append(want, path)
	ok := cascade.HasKind(err, kind) && !strings.Contains(msg, "another opener")
	for _, w := range want {
		ok = ok && strings.Contains(msg, w)
	}
	if !ok {
		t.Fatalf("got %q, want a %v refusal from the pre-create check naming %q", msg, kind, want)
	}
}

// within runs fn and fails the test if it has not returned within limit.
func within(t *testing.T, limit time.Duration, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("did not return within %v (blocked on a FIFO?)", limit)
		return nil
	}
}

// lsDir lists dir's entries (type and name) as one comparable string.
func lsDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	mustDo(t, err)
	return fmt.Sprint(entries)
}

// mustDo fails the test on a setup error.
func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// linkElsewhere links path to another dir's key path (a 0644 key if create) and returns it.
func linkElsewhere(t *testing.T, path string, create bool) (target string) {
	target = filepath.Join(t.TempDir(), "elsewhere.key")
	if create {
		mustDo(t, os.WriteFile(target, make([]byte, quarantineKeyBytes), 0o644))
	}
	if err := os.Symlink(target, path); err != nil && goruntime.GOOS == "windows" {
		t.Skipf("no symlink privilege: %v", err)
	} else {
		mustDo(t, err)
	}
	return target
}

// mkfifoOrSkip makes a FIFO at path with mkfifo(1); Windows has none.
func mkfifoOrSkip(t *testing.T, path string) {
	if goruntime.GOOS == "windows" {
		t.Skip("no FIFOs on Windows")
	}
	if out, err := exec.Command("mkfifo", path).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v %s", err, out)
	}
}

// skipWithoutPermBits skips as root and on Windows (a DACL-locked dir cannot be listed).
func skipWithoutPermBits(t *testing.T) {
	if os.Geteuid() == 0 || goruntime.GOOS == "windows" {
		t.Skip("permission bits are not enforced here")
	}
}
