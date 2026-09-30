// Purpose (this file): tests for the handshake handler (handshake.go) —
//
//	both entry points' proposed/applied diffs, the server-profile
//	env-ref gate, the secret-key floor, and the not-a-project/old-version
//	refusals.
//
// Constraints: no test here forks the real `nself` CLI; activeRunner is a
//
//	scriptedRunner double. Detection uses a REAL t.TempDir() marker
//	directory (the cheapest hermetic way to make newDetector's real
//	statFS report Detected=true), never a fake FS.
//
// SPORT: plugins/nself handshake (TEST) — P1-E25-W5-S103-T1.
package nself

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// scriptedRunner answers Run per exact argument vector; an unscripted call
// returns a probeFailedError (exit 1), matching "key unset"/"not a
// project" rather than panicking on a test's own gap.
type scriptedRunner struct {
	out   map[string][]byte
	err   map[string]error
	calls []string
}

func (r *scriptedRunner) Run(_ context.Context, _, _ string, args []string, _ time.Duration) ([]byte, error) {
	key := strings.Join(args, " ")
	r.calls = append(r.calls, key)
	if err, ok := r.err[key]; ok {
		return nil, err
	}
	if out, ok := r.out[key]; ok {
		return out, nil
	}
	return nil, &probeFailedError{Binary: nselfBinary, ExitCode: 1}
}

// nselfProjectDir builds a real .nself/build-version marker under a fresh
// t.TempDir(), the cheapest hermetic way to make the real detector answer
// Detected=true without a fake filesystem. The project is the bare
// t.TempDir() (no dot, 40+ chars on macOS and Linux): the value screen
// must accept an ordinary absolute directory as project_dir.
func nselfProjectDir(t *testing.T) string {
	t.Helper()
	return nselfProjectDirAt(t, t.TempDir())
}

// nselfProjectDirAt writes the same marker under dir (created if absent).
func nselfProjectDirAt(t *testing.T, dir string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", t.TempDir())
	marker := filepath.Join(dir, markerDirName)
	if err := os.MkdirAll(marker, 0o755); err != nil {
		t.Fatalf("mkdir marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marker, "build-version"), []byte("1"), 0o644); err != nil {
		t.Fatalf("write marker file: %v", err)
	}
	return dir
}

// fullScript is a runner scripted for every handshakeConfigKeys value set,
// version 1.3.5.
func fullScript() *scriptedRunner {
	return &scriptedRunner{out: map[string][]byte{
		"version --json":                 []byte(`{"version":"1.3.5"}`),
		"config get POSTGRES_HOST":       []byte("postgres"),
		"config get POSTGRES_PORT":       []byte("5432"),
		"config get POSTGRES_DB":         []byte("mydb"),
		"config get POSTGRES_EXTENSIONS": []byte("vector,pgcrypto"),
		"config get REDIS_ENABLED":       []byte("true"),
		"config get REDIS_PORT":          []byte("6379"),
		"config get MINIO_ENABLED":       []byte("true"),
		"config get MINIO_PORT":          []byte("9000"),
		"config get S3_BUCKET":           []byte("mybucket"),
	}}
}

func withGetenv(t *testing.T, all bool, missingName string) {
	t.Helper()
	orig := handshakeGetenv
	t.Cleanup(func() { handshakeGetenv = orig })
	handshakeGetenv = func(name string) string {
		if !all || name == missingName {
			return ""
		}
		return "x"
	}
}

func entryLiteral(entries []ConfigEntry, path string) (string, bool) {
	for _, e := range entries {
		if e.Path == path {
			return e.Literal, true
		}
	}
	return "", false
}

func TestHandshakeProposesDescriptors(t *testing.T) {
	dir := nselfProjectDir(t)
	origRunner := activeRunner
	activeRunner = fullScript()
	t.Cleanup(func() { activeRunner = origRunner })
	withGetenv(t, true, "")

	resp, err := runHandshake(context.Background(), handshakeModePropose, dir)
	if err != nil {
		t.Fatalf("runHandshake: %v", err)
	}
	if resp.Status != "proposed" {
		t.Fatalf("status = %q, want proposed", resp.Status)
	}
	wire := map[string]string{}
	for _, e := range resp.Proposed {
		wire[e.Path] = e.Literal
	}
	prefix := "plugins." + handshakeOwner + "."
	want := map[string]string{
		prefix + "project_dir":   strconv.Quote(dir),
		prefix + "postgres_host": `"postgres"`,
		prefix + "postgres_port": "5432",
		prefix + "postgres_db":   `"mydb"`,
		prefix + "pgvector":      "true",
		prefix + "redis":         "true",
		prefix + "redis_port":    "6379",
		prefix + "s3":            "true",
		prefix + "s3_bucket":     `"mybucket"`,
		prefix + "s3_port":       "9000",
		"runtime.profile":        `"server"`,
	}
	for path, literal := range want {
		if got := wire[path]; got != literal {
			t.Errorf("proposed[%s] = %q, want %q", path, got, literal)
		}
	}
}

func TestHandshakeServerProfileOnlyWhenEnvRefsResolve(t *testing.T) {
	dir := nselfProjectDir(t)
	origRunner := activeRunner
	activeRunner = fullScript()
	t.Cleanup(func() { activeRunner = origRunner })
	withGetenv(t, true, "CASCADE_STORAGE_S3_SECRET")

	resp, err := runHandshake(context.Background(), handshakeModePropose, dir)
	if err != nil {
		t.Fatalf("runHandshake: %v", err)
	}
	if resp.Status != "pending-env" {
		t.Fatalf("status = %q, want pending-env", resp.Status)
	}
	if len(resp.MissingEnv) != 1 || resp.MissingEnv[0] != "CASCADE_STORAGE_S3_SECRET" {
		t.Fatalf("missing_env = %v, want [CASCADE_STORAGE_S3_SECRET]", resp.MissingEnv)
	}
	if _, ok := entryLiteral(entriesFromWire(resp.Proposed), "runtime.profile"); ok {
		t.Fatal("runtime.profile proposed despite a missing required env-ref")
	}
}

func entriesFromWire(wire []diffEntryWire) []ConfigEntry {
	out := make([]ConfigEntry, 0, len(wire))
	for _, w := range wire {
		out = append(out, ConfigEntry(w))
	}
	return out
}

func TestHandshakePgvectorDSNOptional(t *testing.T) {
	dir := nselfProjectDir(t)
	origRunner := activeRunner
	activeRunner = fullScript()
	t.Cleanup(func() { activeRunner = origRunner })
	// Every required ref set EXCEPT the optional one, which is not in
	// requiredServerEnvRefs at all — it must never gate runtime.profile.
	withGetenv(t, true, "CASCADE_STORAGE_PGVECTOR_DSN")

	resp, err := runHandshake(context.Background(), handshakeModePropose, dir)
	if err != nil {
		t.Fatalf("runHandshake: %v", err)
	}
	if resp.Status != "proposed" {
		t.Fatalf("status = %q, want proposed (pgvector DSN is optional)", resp.Status)
	}
	if len(resp.MissingEnv) != 0 {
		t.Fatalf("missing_env = %v, want none", resp.MissingEnv)
	}
}

func TestHandshakeNeverRequestsSecretKeys(t *testing.T) {
	secretShaped := regexp.MustCompile(`(?i)PASSWORD|SECRET|_KEY|TOKEN`)
	for _, k := range handshakeConfigKeys {
		if secretShaped.MatchString(k) {
			t.Errorf("handshakeConfigKeys contains a secret-shaped name: %q", k)
		}
	}
}

func TestHandshakeNotNselfProjectNotFound(t *testing.T) {
	dir := t.TempDir() // no marker
	origRunner := activeRunner
	activeRunner = &scriptedRunner{err: map[string]error{
		"status --json": &binaryAbsentError{Binary: nselfBinary, GOOS: "test"},
	}}
	t.Cleanup(func() { activeRunner = origRunner })

	_, err := runHandshake(context.Background(), handshakeModePropose, dir)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("runHandshake(non-nself dir) err = %v, want KindNotFound", err)
	}
}

func TestHandshakeOldVersionUnsupported(t *testing.T) {
	dir := nselfProjectDir(t)
	origRunner := activeRunner
	activeRunner = &scriptedRunner{out: map[string][]byte{
		"version --json": []byte(`{"version":"1.2.0"}`),
	}}
	t.Cleanup(func() { activeRunner = origRunner })

	_, err := runHandshake(context.Background(), handshakeModePropose, dir)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("runHandshake(nself 1.2.0) err = %v, want KindUnsupported", err)
	}
	if !strings.Contains(err.Error(), "1.2.0") {
		t.Fatalf("err = %v, want it to name the found version", err)
	}
}

func TestHandshakeToolPathWritesNothing(t *testing.T) {
	dir := nselfProjectDir(t)
	origRunner, origEgress, origApplier := activeRunner, egressInterceptor, configApplier
	activeRunner = fullScript()
	spy := &spyInterceptor{}
	egressInterceptor = spy
	rec := &recordingApplier{}
	configApplier = rec
	t.Cleanup(func() { activeRunner, egressInterceptor, configApplier = origRunner, origEgress, origApplier })
	withGetenv(t, true, "")

	if _, err := (handlers{}).DispatchTool(context.Background(), toolAddCascade, rootDirInput(t, dir)); err != nil {
		t.Fatalf("DispatchTool(nself_add_cascade): %v", err)
	}
	if rec.calls != 0 {
		t.Fatalf("the tool path called ApplyDiff %d times, want 0", rec.calls)
	}
	if spy.calls != 1 {
		t.Fatalf("egress calls = %d, want 1", spy.calls)
	}
}
