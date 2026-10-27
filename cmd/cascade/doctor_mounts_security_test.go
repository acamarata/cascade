// Purpose: tests for the audit-chain doctor check and the read-only contract
//
//	all six security doctor checks share (doctor_mounts_security.go).
//
// Constraints: every fixture lives under t.TempDir(); nothing here reads the
//
//	host keychain, a helper binary or the real HOME, depends on file mode bits,
//	timing or path separators, and values are obviously fake.
//
// SPORT: cmd/cascade doctor security mounts/ADDED (tests, P1-SEC-12).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/output"
	internalruntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// securityPaths is a temp CASCADE_HOME whose data directory exists and is empty.
func securityPaths(t *testing.T) internalruntime.PathProvider {
	t.Helper()
	paths := doctorTestPaths(t)
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	return paths
}

// guardReadOnlyCustody forces the file vault and a refusing runner on a
// read-only custody selector, so no host keychain is consulted. TestMain
// installs it on the production seam; tests install it with a call log.
func guardReadOnlyCustody(next func(secrets.Config) (secrets.Custody, error), log *callLog) func(secrets.Config) (secrets.Custody, error) {
	return func(cfg secrets.Config) (secrets.Custody, error) {
		cfg.ForceFileVault = true
		cfg.Runner = func(_ context.Context, name string, args ...string) ([]byte, error) {
			log.add(name + " " + strings.Join(args, " "))
			return nil, fmt.Errorf("no host keychain in this test")
		}
		return next(cfg)
	}
}

// callLog records the keychain runner calls a check makes.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(call string) { l.mu.Lock(); l.calls = append(l.calls, call); l.mu.Unlock() }
func (l *callLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.calls, "\n")
}

// useRecordedCustody points the approval-key check at the guarded selector.
func useRecordedCustody(t *testing.T) *callLog {
	t.Helper()
	log := &callLog{}
	previous := doctorApprovalKeyCustody
	doctorApprovalKeyCustody = guardReadOnlyCustody(previous, log)
	t.Cleanup(func() { doctorApprovalKeyCustody = previous })
	return log
}

// auditDBPath is where the store lives under paths.
func auditDBPath(paths internalruntime.PathProvider) string {
	return filepath.Join(paths.DataDir(), "cascade.db")
}

// seedAudit opens the store as a writer and appends n policy records. The
// returned driver holds the store flock like a running daemon; close it to
// release.
func seedAudit(t *testing.T, paths internalruntime.PathProvider, n int) (*sqlite.Driver, *audit.Log) {
	t.Helper()
	drv, err := sqlite.Open(context.Background(), auditDBPath(paths))
	if err != nil {
		t.Fatalf("open the fixture store: %v", err)
	}
	log := audit.New(drv, doctorTestClock(), nil)
	for i := 1; i <= n; i++ {
		ev := audit.Event{Kind: audit.KindPolicyDecide, Actor: "user", Action: "read", Verdict: "allow", ParamsHash: audit.HashParams([]byte{byte(i)})}
		if _, err := log.Append(context.Background(), ev); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	return drv, log
}

// closeStoreClean closes the writer and removes the lock sidecar, leaving
// what a cleanly stopped daemon leaves apart from the lock file: cascade.db alone.
func closeStoreClean(t *testing.T, drv *sqlite.Driver, paths internalruntime.PathProvider) {
	t.Helper()
	if err := drv.Close(); err != nil {
		t.Fatalf("close the fixture store: %v", err)
	}
	if err := os.Remove(auditDBPath(paths) + ".lock"); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove lock sidecar: %v", err)
	}
}

// dataDigest lists every file under dir with its size, mode and content hash.
func dataDigest(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		line := fmt.Sprintf("%s dir=%t size=%d mode=%v", filepath.ToSlash(rel), d.IsDir(), info.Size(), info.Mode())
		if !d.IsDir() {
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			sum := sha256.Sum256(data)
			line += " sha256=" + hex.EncodeToString(sum[:])
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatalf("digest %s: %v", dir, err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func runSecurityCheck(t *testing.T, check doctor.Check) doctor.CheckResult {
	t.Helper()
	res, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("%s: Run returned an error: %v", check.Name(), err)
	}
	return res
}

func TestDoctorAuditChainDetectsTamper(t *testing.T) {
	paths := securityPaths(t)
	drv, _ := seedAudit(t, paths, 3)
	ctx := context.Background()
	it, err := drv.Scan(ctx, "audit", "rec:")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var keys []string
	for it.Next(ctx) {
		keys = append(keys, it.Key())
	}
	_ = it.Close()
	if len(keys) != 3 {
		t.Fatalf("fixture holds %d records, want 3", len(keys))
	}
	raw, err := drv.Get(ctx, "audit", keys[1])
	if err != nil {
		t.Fatalf("get record 2: %v", err)
	}
	edited := strings.Replace(string(raw), `"actor":"user"`, `"actor":"root"`, 1)
	if edited == string(raw) {
		t.Fatal("the fixture edit changed nothing")
	}
	if err := drv.Put(ctx, "audit", keys[1], []byte(edited)); err != nil {
		t.Fatalf("put edited record: %v", err)
	}
	closeStoreClean(t, drv, paths)

	res := runSecurityCheck(t, newAuditChainDoctorCheck(paths))
	if res.Status != doctor.StatusError || !strings.Contains(res.Message, "record 2") {
		t.Fatalf("tampered record 2: status=%s message=%q, want an error naming record 2", res.Status, res.Message)
	}
	// The same verdict reaches the CLI: a doctor run over the check exits non-zero.
	_, runErr := execRootDoctor(t, testDoctorDeps(t, newAuditChainDoctorCheck(paths)), "doctor")
	if output.ExitCode(runErr) == cascade.ExitOK || runErr == nil {
		t.Fatalf("cascade doctor exited zero over a tampered audit log (err=%v)", runErr)
	}
}

func TestDoctorAuditChainNoDatabaseIsOK(t *testing.T) {
	paths := securityPaths(t)
	res := runSecurityCheck(t, newAuditChainDoctorCheck(paths))
	if res.Status != doctor.StatusOK || !strings.Contains(res.Message, "no audit log yet") {
		t.Fatalf("no database: status=%s message=%q, want OK 'no audit log yet'", res.Status, res.Message)
	}
	entries, err := os.ReadDir(paths.DataDir())
	if err != nil || len(entries) != 0 {
		t.Fatalf("the check created files in an empty data dir: %v (err %v)", entries, err)
	}
}

func TestDoctorAuditChainWhileDaemonHoldsLock(t *testing.T) {
	paths := securityPaths(t)
	drv, _ := seedAudit(t, paths, 2)
	defer func() { _ = drv.Close() }()
	if _, err := sqlite.Open(context.Background(), auditDBPath(paths)); err == nil {
		t.Fatal("the fixture store flock is not held, so this test proves nothing")
	}
	before := dirNames(t, paths.DataDir())
	res := runSecurityCheck(t, newAuditChainDoctorCheck(paths))
	if res.Status != doctor.StatusOK || !strings.Contains(res.Message, "verified") {
		t.Fatalf("lock held: status=%s message=%q, want a verified chain", res.Status, res.Message)
	}
	if after := dirNames(t, paths.DataDir()); after != before {
		t.Fatalf("the check changed the directory while the lock was held:\nbefore %s\nafter  %s", before, after)
	}
}

func dirNames(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

// TestDoctorAuditChainUnreadableFileIsError is the fail-closed probe: a
// database the process cannot read is an error, never an OK.
func TestDoctorAuditChainUnreadableFileIsError(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file mode 0000 does not deny reads on windows or to root")
	}
	paths := securityPaths(t)
	drv, _ := seedAudit(t, paths, 1)
	closeStoreClean(t, drv, paths)
	if err := os.Chmod(auditDBPath(paths), 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(auditDBPath(paths), 0o600) })
	if res := runSecurityCheck(t, newAuditChainDoctorCheck(paths)); res.Status != doctor.StatusError {
		t.Fatalf("unreadable database: status=%s message=%q, want error", res.Status, res.Message)
	}
}

// TestDoctorSecurityChecksNeverWrite runs all six checks over a populated
// data dir and proves nothing under it, and no keychain runner call, changed.
func TestDoctorSecurityChecksNeverWrite(t *testing.T) {
	paths := securityPaths(t)
	calls := useRecordedCustody(t)
	useFakeEnrolment(t, "aaaa-fake-fingerprint")
	withElevationSource(t, presenceTier)
	drv, log := seedAudit(t, paths, 3)
	seedPresenceEnrolment(t, log, "aaaa-fake-fingerprint", "fresh", "confirmed")
	closeStoreClean(t, drv, paths)
	if err := os.WriteFile(filepath.Join(paths.Root(), "config.toml"), []byte("[policy]\nautonomy_profile = \"strict\"\n"), 0o600); err != nil {
		t.Fatalf("config: %v", err)
	}
	before, beforeCalls := dataDigest(t, paths.Root()), calls.String()

	checks := []doctor.Check{newElevationCustodyDoctorCheck(paths, doctorTestClock()), newAuditChainDoctorCheck(paths),
		newPolicyDoctorCheck(paths), newApprovalKeyDoctorCheck(paths), newEgressAuditOnlyDoctorCheck(), newCallerPrincipalDoctorCheck()}
	for _, check := range checks {
		if res := runSecurityCheck(t, check); res.Status == doctor.StatusError {
			t.Errorf("%s reported an error over a healthy fixture: %s", check.Name(), res.Message)
		}
	}
	if after := dataDigest(t, paths.Root()); after != before {
		t.Fatalf("the security checks changed the data directory\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if calls.String() != beforeCalls {
		t.Fatalf("the security checks called the keychain runner: %q", calls.String())
	}
}
