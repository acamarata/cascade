// Purpose: exercises run() (main's testable core) over injected args and a
//
//	buffer-backed output.Writer — never the real process streams or
//	os.Args, so nothing here needs a subprocess.
//
// Inputs: fixture artifacts borrowed from pkg/plugin/registry/generator's
//
//	testdata; an env var carrying a test-only Ed25519 key.
//
// Outputs: none — pass/fail only.
// SPORT: internal/tools/registry-gen tests (ADD) — P1-E24-W5-S50-T5.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/output"
)

const fixturesDir = "../../../pkg/plugin/registry/generator/testdata/fixtures"

// testClock stamps published_at; the half second proves the truncation.
var testClock = stubClock{t: time.Date(2026, 1, 1, 12, 34, 56, 5e8, time.UTC)}

type stubClock struct{ t time.Time }

func (c stubClock) Now() time.Time { return c.t }

func testWriter() (*output.Writer, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	return output.New(&stdout, &stderr, false, false, false, true), &stdout, &stderr
}

func TestRunRefusesPlaintextKey(t *testing.T) {
	w, _, stderr := testWriter()
	code := runAt(w, testClock, []string{"--artifacts", fixturesDir, "--out", t.TempDir(), "--key-ref", "REG_GEN_TEST_KEY", "--key", "not-a-real-key-but-still-refused"})
	if code == 0 {
		t.Fatalf("run returned 0 with a plaintext --key flag set")
	}
	if strings.Contains(stderr.String(), "not-a-real-key-but-still-refused") {
		t.Errorf("stderr leaked the plaintext key value: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "plaintext key material") {
		t.Errorf("stderr %q does not name the refusal reason", stderr.String())
	}
}

func TestRunRequiresFlags(t *testing.T) {
	w, _, stderr := testWriter()
	code := runAt(w, testClock, []string{"--artifacts", fixturesDir})
	if code == 0 {
		t.Fatal("run returned 0 with --out and --key-ref missing")
	}
	if !strings.Contains(stderr.String(), "required") {
		t.Errorf("stderr %q does not explain the missing flags", stderr.String())
	}
}

func TestRunHappyPathWritesIndex(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	t.Setenv("REG_GEN_TEST_KEY", base64.StdEncoding.EncodeToString(priv))

	outDir := t.TempDir()
	w, _, stderr := testWriter()
	code := runAt(w, testClock, []string{
		"--artifacts", fixturesDir, "--sequence", "7",
		"--out", outDir,
		"--base-url", "https://cdn.example.com/plugins/",
		"--key-ref", "REG_GEN_TEST_KEY",
	})
	if code != 0 {
		t.Fatalf("run = %d, stderr: %s", code, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(outDir, "index.json"))
	if err != nil || !strings.Contains(string(data), `"sequence":7,"published_at":"2026-01-01T12:34:56Z"`) {
		t.Errorf("index.json lacks the flag's sequence and the clock-stamped published_at: %s (err %v)", data, err)
	}
}

func TestRunMissingKeyRefEnvFails(t *testing.T) {
	_ = os.Unsetenv("REG_GEN_TEST_KEY_UNSET")
	w, _, stderr := testWriter()
	code := runAt(w, testClock, []string{
		"--artifacts", fixturesDir, "--sequence", "1",
		"--out", t.TempDir(),
		"--key-ref", "REG_GEN_TEST_KEY_UNSET",
	})
	if code == 0 {
		t.Fatal("run returned 0 with an unset key-ref env var")
	}
	if stderr.Len() == 0 {
		t.Error("expected a diagnostic on stderr")
	}
}

// TestEnvVaultGetFallsBackToHexAndFailsOnGarbage drives envVault.Get's
// second decode branch (the hex fallback): a value that fails base64 decoding falls
// through to hex.DecodeString, and a value that is neither must refuse
// rather than return zero-value key bytes.
func TestEnvVaultGetFallsBackToHexAndFailsOnGarbage(t *testing.T) {
	t.Setenv("REG_GEN_TEST_GARBAGE_KEY", "not-base64-and-not-hex!!")
	if _, err := (envVault{}).Get(context.Background(), "REG_GEN_TEST_GARBAGE_KEY"); err == nil {
		t.Fatal("Get returned nil error for a value that is neither valid base64 nor valid hex")
	}
}

// TestEnvVaultGetAcceptsHexKey proves the hex fallback succeeds on real hex
// key material. The 3-byte value hex-encodes to 6 characters, a length
// base64.StdEncoding always rejects (not a multiple of 4), so this always
// forces the hex fallback.
func TestEnvVaultGetAcceptsHexKey(t *testing.T) {
	raw := []byte{0xAA, 0xBB, 0xCC}
	t.Setenv("REG_GEN_TEST_HEX_KEY", hex.EncodeToString(raw))
	got, err := (envVault{}).Get(context.Background(), "REG_GEN_TEST_HEX_KEY")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("Get returned %x, want %x", got, raw)
	}
}

func TestRunRejectsUnknownFlag(t *testing.T) {
	w, _, stderr := testWriter()
	code := runAt(w, testClock, []string{"--not-a-real-flag"})
	if code != 2 {
		t.Fatalf("run = %d, want 2 for an unparseable flag set", code)
	}
	if stderr.Len() == 0 {
		t.Error("expected a diagnostic on stderr for a flag parse failure")
	}
}

// TestRunGenerateIndexErrorRefusesPartialOutput proves the supply-chain
// requirement that a malformed or unreadable input never produces a
// partial index.json: GenerateIndex fails before signing or writing ever
// run, so the output path must stay untouched.
func TestRunGenerateIndexErrorRefusesPartialOutput(t *testing.T) {
	outDir := t.TempDir()
	w, _, stderr := testWriter()
	code := runAt(w, testClock, []string{
		"--artifacts", filepath.Join(t.TempDir(), "does-not-exist"), "--sequence", "1",
		"--out", outDir,
		"--key-ref", "REG_GEN_TEST_KEY_UNUSED",
	})
	if code != 1 {
		t.Fatalf("run = %d, want 1 for an unreadable artifacts dir", code)
	}
	if stderr.Len() == 0 {
		t.Error("expected a diagnostic on stderr")
	}
	if _, err := os.Stat(filepath.Join(outDir, "index.json")); !os.IsNotExist(err) {
		t.Errorf("index.json must not be written when GenerateIndex fails, stat err = %v", err)
	}
}

// TestRunMkdirAllFailureRefusesOutput drives generate's os.MkdirAll error
// branch: --out names a path under a file, so MkdirAll cannot create it,
// and the failure must never leave a half-written index behind.
func TestRunMkdirAllFailureRefusesOutput(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(fixedSeed45())
	t.Setenv("REG_GEN_TEST_KEY_MKDIR", base64.StdEncoding.EncodeToString(priv))

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(blocker, "sub")

	w, _, stderr := testWriter()
	code := runAt(w, testClock, []string{
		"--artifacts", fixturesDir, "--sequence", "1",
		"--out", outDir,
		"--key-ref", "REG_GEN_TEST_KEY_MKDIR",
	})
	if code != 1 {
		t.Fatalf("run = %d, want 1 when MkdirAll cannot create %q under a file", code, outDir)
	}
	if stderr.Len() == 0 {
		t.Error("expected a diagnostic on stderr")
	}
	// blocker is a regular file, so any stat under it fails (ENOTDIR):
	// a failure proves no index was written.
	if _, err := os.Stat(filepath.Join(outDir, "index.json")); err == nil {
		t.Error("index.json must not exist under a blocked path")
	}
}

// TestRunWriteIndexFailureRefusesPartialOutput drives WriteIndex's own
// failure branch: the output directory exists (MkdirAll is a no-op) but is
// not writable, so the temp-file write fails and no index.json may appear.
func TestRunWriteIndexFailureRefusesPartialOutput(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not block writes")
	}
	// makeDirGenuinelyUnwritable (dirunwritable_unix_test.go /
	// dirunwritable_windows_test.go) applies a real, platform-appropriate
	// write denial: POSIX chmod on unix, an explicit DACL DENY ACE on
	// Windows (windows-parity-pass-5), where os.Chmod only toggles a
	// cosmetic FILE_ATTRIBUTE_READONLY that never blocks writes into a
	// directory. The Windows helper proves the denial actually took
	// effect by attempting a real write before returning, so this test
	// cannot pass for the wrong reason. See custody_filevault_test.go's
	// TestFileVaultAvailableFalseOnUnwritableDir for the same case.
	priv := ed25519.NewKeyFromSeed(fixedSeed45())
	t.Setenv("REG_GEN_TEST_KEY_RO", base64.StdEncoding.EncodeToString(priv))

	outDir := t.TempDir()
	makeDirGenuinelyUnwritable(t, outDir)

	w, _, stderr := testWriter()
	code := runAt(w, testClock, []string{
		"--artifacts", fixturesDir, "--sequence", "1",
		"--out", outDir,
		"--key-ref", "REG_GEN_TEST_KEY_RO",
	})
	if code != 1 {
		t.Fatalf("run = %d, want 1 when the output directory is not writable", code)
	}
	if stderr.Len() == 0 {
		t.Error("expected a diagnostic on stderr")
	}
	if _, err := os.Stat(filepath.Join(outDir, "index.json")); !os.IsNotExist(err) {
		t.Errorf("index.json must not exist after a write failure, stat err = %v", err)
	}
}

// TestRunHappyPathDoesNotLeakSigningKey proves the supply-chain requirement
// that signing key material never reaches stdout, stderr, or the emitted
// index, on a run that succeeds end to end.
func TestRunHappyPathDoesNotLeakSigningKey(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(fixedSeed45())
	keyB64 := base64.StdEncoding.EncodeToString(priv)
	t.Setenv("REG_GEN_TEST_KEY_LEAK", keyB64)

	outDir := t.TempDir()
	w, stdout, stderr := testWriter()
	code := runAt(w, testClock, []string{
		"--artifacts", fixturesDir, "--sequence", "1",
		"--out", outDir,
		"--base-url", "https://cdn.example.com/plugins/",
		"--key-ref", "REG_GEN_TEST_KEY_LEAK",
	})
	if code != 0 {
		t.Fatalf("run = %d, stderr: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), keyB64) || strings.Contains(stderr.String(), keyB64) {
		t.Fatal("signing key material leaked to stdout or stderr")
	}
	data, err := os.ReadFile(filepath.Join(outDir, "index.json"))
	if err != nil {
		t.Fatalf("read index.json: %v", err)
	}
	if strings.Contains(string(data), keyB64) {
		t.Fatal("signing key material leaked into the emitted index")
	}
}

// fixedSeed45 returns a fixed, non-production Ed25519 seed shared by the
// error-path tests above, so each does not repeat the same byte-fill loop.
func fixedSeed45() []byte {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return seed
}

// TestRegistryGenRequiresSequenceFlag: a missing or zero --sequence exits 2
// before any key is read or index written, with --key-ref and with
// --derive-from-release (both keys are unset, so a later refusal exits 1).
func TestRegistryGenRequiresSequenceFlag(t *testing.T) {
	t.Setenv("MINISIGN_RELEASE_SECKEY_B64", "")
	for _, mode := range [][]string{{"--key-ref", "REG_GEN_TEST_KEY_UNSET_SEQ"}, {"--derive-from-release"}} {
		for _, seq := range [][]string{nil, {"--sequence", "0"}} {
			outDir := t.TempDir()
			w, _, stderr := testWriter()
			args := append(append([]string{"--artifacts", fixturesDir, "--out", outDir}, mode...), seq...)
			if code := runAt(w, testClock, args); code != 2 || !strings.Contains(stderr.String(), "--sequence is required") {
				t.Errorf("run(%v) = %d, stderr %q; want 2 naming --sequence", args, code, stderr.String())
			}
			if _, err := os.Stat(filepath.Join(outDir, "index.json")); !os.IsNotExist(err) {
				t.Errorf("run(%v) wrote index.json, stat err = %v", args, err)
			}
		}
	}
}
