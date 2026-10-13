//go:build !windows

package claude

// Purpose: custody verdicts that the first review found missing: a late
//   Cancel does not relabel a canary verdict, a canary written into a file
//   the driver itself injected is found, and the one-object parsers refuse
//   trailing tokens.
// Constraints: every credential value is synthetic; the fake harness leaks
//   into its own leased tree only.
// SPORT: providers.agents.claude tests (P1-AGT-01).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestClaudeCancelKeepsCustodyVerdict(t *testing.T) {
	f := newFx(t)
	body := readCanary + `[ -n "$C" ] || exit 9` + "\nprintf '%s\\n' \"$C\"\nexec sleep 30\n"
	d, res := spawnSleeper(t, f, f.config(), body)
	eventually(t, "the canary to reach stdout", func() bool {
		rec, err := d.lookup(res.JobID)
		return err == nil && rec.scanOut.Hit()
	})
	if err := d.Cancel(context.Background(), res.JobID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cancelConfirmWindow*10)
	defer cancel()
	cr, err := d.Collect(ctx, res.JobID)
	if err != ErrCredentialCanary || cr.Output != "" || state(t, d, res.JobID) != provider.AgentRunFailed {
		t.Fatalf("Collect = %v, state %s: a Cancel must not relabel a canary verdict", err, state(t, d, res.JobID))
	}
	if strings.Contains(err.Error(), "cascade-canary-") {
		t.Fatal("the verdict carries the canary")
	}
}

func TestClaudeCanaryInInjectedFileFailsClosed(t *testing.T) {
	f := newFx(t)
	f.body = readCanary + `[ -n "$C" ] || exit 9` + "\nprintf '%s' \"$C\" >> .claude/settings.json\n" + f.body
	_, cr, err := run(t, f.driver(f.config()), f.spec())
	if err != ErrCredentialCanary || cr.Output != "" || len(cr.Artifacts) != 0 {
		t.Fatalf("Collect = %v (output %q, artifacts %q): the driver's own file is scanned too", err, cr.Output, cr.Artifacts)
	}
}

func TestDecodersRefuseTrailingTokens(t *testing.T) {
	const object = `{"seq":1,"kind":"stdout","tool_name":"x"}`
	if _, _, err := decodePayload([]byte(object)); err != nil {
		t.Fatalf("a single object: %v", err)
	}
	for name, payload := range map[string]string{"stray brace": object + "}", "stray bracket": object + "]", "second object": object + object} {
		if _, _, err := decodePayload([]byte(payload)); !errors.Is(err, ErrCallbackFrameRejected) {
			t.Fatalf("decodePayload(%s) = %v, want ErrCallbackFrameRejected", name, err)
		}
	}
	good := string(readTestdata(t, "protocol/1/result.json"))
	if _, err := parseResult([]byte(strings.TrimSpace(good))); err != nil {
		t.Fatalf("parseResult(capture): %v", err)
	}
	if _, err := parseResult([]byte(strings.TrimSpace(good) + "}")); !errors.Is(err, provider.ErrHarnessIncompatible) {
		t.Fatalf("parseResult(trailing brace) = %v, want ErrHarnessIncompatible", err)
	}
}

// TestClaudeIgnoredPathsAreScannedNotListed: git reports an ignored directory
// as one entry; it is scanned for the canary but is never an artifact, so a
// clean run lists nothing, a special file inside it does not fail the run, and
// a symlink to a directory is not walked.
func TestClaudeIgnoredPathsAreScannedNotListed(t *testing.T) {
	f := newFx(t)
	mustWrite(t, filepath.Join(f.root, ".gitignore"), ".claude/\nbuild/\nlink\n")
	mustMkdir(t, filepath.Join(f.root, "build"))
	mustWrite(t, filepath.Join(f.root, "build", "cache.bin"), "x")
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "denied"), "x")
	if err := os.Chmod(filepath.Join(outside, "denied"), 0); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.root, "add", ".gitignore")
	gitIn(t, f.root, "commit", "--quiet", "-m", "ignore")
	// The job makes the FIFO and the symlink after the pre-spawn guards ran.
	f.body = "mkfifo build/pipe\nln -s '" + outside + "' link\n" + f.body
	_, cr, err := run(t, f.driver(f.config()), f.spec())
	if err != nil || len(cr.Artifacts) != 0 {
		t.Fatalf("Collect = %v, artifacts %q: ignored paths are scanned, never listed, and never followed", err, cr.Artifacts)
	}
}

// TestClaudeLeakBeforeFailureIsStillACanaryVerdict: a job that writes the key
// value, or the canary, and then exits non-zero is a custody failure, not a
// plain exit status.
func TestClaudeLeakBeforeFailureIsStillACanaryVerdict(t *testing.T) {
	leaks := map[string]string{
		"key value": `printf '%s' "$` + KeyVarOAuthToken + `" > leaked.txt`,
		"canary":    readCanary + `printf '%s' "$C" > leaked.txt`,
	}
	for name, leak := range leaks {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.body = leak + "\nexit 1\n" + f.body
			_, cr, err := run(t, f.driver(f.config()), f.spec())
			if err != ErrCredentialCanary || cr.Output != "" {
				t.Fatalf("Collect = %v (output %q): a leak before a failing exit must be the canary verdict", err, cr.Output)
			}
		})
	}
	t.Run("a failing exit without a leak stays a plain exit failure", func(t *testing.T) {
		f := newFx(t)
		f.body = "exit 3\n" + f.body
		if _, _, err := run(t, f.driver(f.config()), f.spec()); err == nil || err == ErrCredentialCanary || !strings.Contains(err.Error(), "status 3") {
			t.Fatalf("Collect = %v, want the exit status", err)
		}
	})
}

func TestClaudeRecordAuditsBothVendorVars(t *testing.T) {
	f := newFx(t)
	d := f.driver(f.config())
	res, _, err := run(t, d, f.spec())
	if err != nil {
		t.Fatal(err)
	}
	rec, lerr := d.lookup(res.JobID)
	if lerr != nil || !reflect.DeepEqual(rec.vendorVars, []string{VendorConfigDirVar, KeyVarOAuthToken}) {
		t.Fatalf("vendorVars %q (%v): the straggler audit must name the config dir and the key variable", rec.vendorVars, lerr)
	}
}

func TestHasAnyEnvVar(t *testing.T) {
	environ := []byte("A=1\x00" + KeyVarOAuthToken + "=v\x00")
	for names, want := range map[string]bool{
		KeyVarOAuthToken: true, VendorConfigDirVar + "," + KeyVarOAuthToken: true,
		VendorConfigDirVar: false, ",": false, "A_": false, "": false,
	} {
		if got := hasAnyEnvVar(environ, strings.Split(names, ",")); got != want {
			t.Fatalf("hasAnyEnvVar(%q) = %v, want %v", names, got, want)
		}
	}
}

func TestClaudeShortKeyValueRefused(t *testing.T) {
	f := newFx(t)
	spec := f.spec()
	spec.Env.Driver[3] = KeyVarOAuthToken + "=abc1234"
	_, err := f.driver(f.config()).Spawn(context.Background(), spec)
	if !errors.Is(err, ErrSpawnEnvRejected) || !strings.Contains(err.Error(), "at least 8 bytes") || exists(f.anyRan) {
		t.Fatalf("Spawn = %v (anyRan %v): a key value too short to scan must refuse before any child", err, exists(f.anyRan))
	}
}

func TestScanIgnoredEntries(t *testing.T) {
	forms := canaryForms("cascade-canary-unit")
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "ign", "deep"))
	mustWrite(t, filepath.Join(root, "ign", "deep", "clean"), "nothing here")
	if err := syscall.Mkfifo(filepath.Join(root, "ign", "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := scanIgnored(t.Context(), root, "ign/", forms); err != nil {
		t.Fatalf("a clean ignored directory with a FIFO: %v", err)
	}
	if err := scanIgnored(t.Context(), root, "gone", forms); err != nil {
		t.Fatalf("a vanished entry: %v", err)
	}
	cases := map[string]func(){
		"content": func() { mustWrite(t, filepath.Join(root, "ign", "deep", "x"), "a cascade-canary-unit b") },
		"name":    func() { mustWrite(t, filepath.Join(root, "ign", "cascade-canary-unit"), "x") },
		"link":    func() { _ = os.Symlink("/nowhere/cascade-canary-unit", filepath.Join(root, "ign", "l")) },
	}
	for name, plant := range cases {
		plant()
		if err := scanIgnored(t.Context(), root, "ign/", forms); err != ErrCredentialCanary {
			t.Fatalf("%s in an ignored directory: %v, want ErrCredentialCanary", name, err)
		}
		_ = os.RemoveAll(filepath.Join(root, "ign", "deep", "x"))
		_ = os.Remove(filepath.Join(root, "ign", "cascade-canary-unit"))
		_ = os.Remove(filepath.Join(root, "ign", "l"))
	}
	mustWrite(t, filepath.Join(root, "ign", "denied"), "x")
	if err := os.Chmod(filepath.Join(root, "ign", "denied"), 0); err != nil {
		t.Fatal(err)
	}
	if err := scanIgnored(t.Context(), root, "ign/denied", forms); err == nil || !strings.Contains(err.Error(), "could not be scanned") {
		t.Fatalf("an unreadable regular file must fail closed: %v", err)
	}
}

func TestCustodyVerdictIdentity(t *testing.T) {
	rec := &jobRecord{channel: &callbackChannel{}}
	for name, want := range map[string]struct {
		err  error
		want bool
	}{
		"nil": {nil, false}, "egress": {provider.ErrEgressSubstitutionFailed, true}, "overflow": {ErrFilteredBufferOverflow, true},
		"canary": {ErrCredentialCanary, true}, "straggler": {ErrVendorAuthEnvLeak, false},
		"same kind, other identity": {cascade.New(cascade.KindIntegrity, "claude: seeded credential canary observed; run failed closed"), false},
	} {
		if got := rec.custodyVerdict(want.err); got != want.want {
			t.Fatalf("custodyVerdict(%s) = %v, want %v", name, got, want.want)
		}
	}
}

// TestClaudeScanErrorSurvivesAFailingExit: a job that leaves an unreadable
// file holding the key and then fails must not downgrade the scan's own
// failure to a plain exit status.
func TestClaudeScanErrorSurvivesAFailingExit(t *testing.T) {
	f := newFx(t)
	key := `"$` + KeyVarOAuthToken + `"`
	f.body = "printf '%s' " + key + " > leaked.txt\nchmod 000 leaked.txt\nexit 1\n" + f.body
	_, cr, err := run(t, f.driver(f.config()), f.spec())
	if err == nil || !strings.Contains(err.Error(), "could not be scanned") || !strings.Contains(err.Error(), "status 1") || cr.Output != "" {
		t.Fatalf("Collect = %v: the failed scan is the verdict and the exit status stays in the message", err)
	}
	rec := &jobRecord{channel: &callbackChannel{}, scanFailed: true}
	if !rec.custodyVerdict(errors.New("scan failed")) {
		t.Fatal("a failed scan is a verdict a Cancel must not relabel")
	}
}

// TestClaudeKeyNamedDirectoryInAnIgnoredTree: directory names are checked too.
func TestClaudeKeyNamedDirectoryInAnIgnoredTree(t *testing.T) {
	f := newFx(t)
	mustWrite(t, filepath.Join(f.root, ".gitignore"), "build/\n")
	mustMkdir(t, filepath.Join(f.root, "build"))
	mustWrite(t, filepath.Join(f.root, "build", "keep"), "x")
	gitIn(t, f.root, "add", ".gitignore")
	gitIn(t, f.root, "commit", "--quiet", "-m", "ignore")
	f.body = `mkdir -p "build/$` + KeyVarOAuthToken + `"` + "\n" + f.body
	if _, _, err := run(t, f.driver(f.config()), f.spec()); err != ErrCredentialCanary {
		t.Fatalf("Collect = %v, want ErrCredentialCanary for a directory named after the key value", err)
	}
}

// TestClaudeCancelKeepsAFailedScanVerdict: the scan fails closed (an unreadable
// file holding the key) and then a Cancel kills the job; the run stays failed.
func TestClaudeCancelKeepsAFailedScanVerdict(t *testing.T) {
	f := newFx(t)
	leaked := filepath.Join(f.root, "leaked.txt")
	body := "printf '%s' \"$" + KeyVarOAuthToken + "\" > leaked.txt\nchmod 000 leaked.txt\nexec sleep 30\n"
	d, res := spawnSleeper(t, f, f.config(), body)
	eventually(t, "the unreadable file", func() bool {
		fi, err := os.Lstat(leaked)
		return err == nil && fi.Mode().Perm() == 0
	})
	if err := d.Cancel(context.Background(), res.JobID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cancelConfirmWindow*10)
	defer cancel()
	_, err := d.Collect(ctx, res.JobID)
	if err == nil || !strings.Contains(err.Error(), "could not be scanned") || state(t, d, res.JobID) != provider.AgentRunFailed {
		t.Fatalf("Collect = %v, state %s: a failed scan must not be relabelled cancelled", err, state(t, d, res.JobID))
	}
}

// TestClaudeKeyNamedDirectoryInAnUntrackedTree: git lists a nested repository
// as one directory entry; the names under it are checked too.
func TestClaudeKeyNamedDirectoryInAnUntrackedTree(t *testing.T) {
	f := newFx(t)
	f.body = `git init -q nested` + "\n" + `mkdir -p "nested/$` + KeyVarOAuthToken + `"` + "\n" + f.body
	if _, _, err := run(t, f.driver(f.config()), f.spec()); err != ErrCredentialCanary {
		t.Fatalf("Collect = %v, want ErrCredentialCanary for a directory named after the key value", err)
	}
}
