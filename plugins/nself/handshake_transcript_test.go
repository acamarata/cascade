// Purpose (this file): Art.2 real-counterpart replay. version.json and
//
//	config-get-<KEY>.txt (plugins/nself/testdata/transcripts/nself-1.3.5/,
//	provenance in testdata/README.md) are recordings of the REAL nself
//	1.3.5 binary, never a hand-authored dialect. TestHandshakeRealTranscript
//	replays those exact bytes through a scriptedRunner; TestHandshakeLiveNself
//	forks the real binary when it is on PATH.
//
// SPORT: plugins/nself handshake (TEST) — P1-E25-W5-S103-T1.
package nself

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const transcriptDir = "testdata/transcripts/nself-1.3.5"

// loadTranscriptExitCodes reads exit-codes.txt ("<KEY> <rc>" per line),
// the real binary's exit status for each `nself config get <KEY>`.
func loadTranscriptExitCodes(t *testing.T) map[string]int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(transcriptDir, "exit-codes.txt"))
	if err != nil {
		t.Fatalf("read exit-codes.txt: %v", err)
	}
	codes := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("exit-codes.txt line %q is not \"<KEY> <rc>\"", line)
		}
		rc, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatalf("exit-codes.txt line %q: %v", line, err)
		}
		codes[fields[0]] = rc
	}
	return codes
}

// loadTranscriptScript builds a scriptedRunner from the recorded files:
// each key's captured stdout bytes, and its recorded exit code (a non-zero
// rc replays as the probeFailedError the real runner returns).
func loadTranscriptScript(t *testing.T) *scriptedRunner {
	t.Helper()
	script := &scriptedRunner{out: map[string][]byte{}, err: map[string]error{}}
	codes := loadTranscriptExitCodes(t)

	version, err := os.ReadFile(filepath.Join(transcriptDir, "version.json"))
	if err != nil {
		t.Fatalf("read version.json: %v", err)
	}
	script.out["version --json"] = version

	for _, key := range handshakeConfigKeys {
		data, err := os.ReadFile(filepath.Join(transcriptDir, "config-get-"+key+".txt"))
		if err != nil {
			t.Fatalf("read config-get-%s.txt: %v", key, err)
		}
		rc, ok := codes[key]
		if !ok {
			t.Fatalf("exit-codes.txt has no rc for %s", key)
		}
		argKey := "config get " + key
		if rc != 0 {
			script.err[argKey] = &probeFailedError{Binary: nselfBinary, ExitCode: rc}
			continue
		}
		script.out[argKey] = data
	}
	return script
}

// TestHandshakeRealTranscript replays the captured bytes exactly, proving
// the handshake's decode/propose path against real output, not a dialect
// this package invented.
func TestHandshakeRealTranscript(t *testing.T) {
	dir := nselfProjectDir(t)
	origRunner := activeRunner
	activeRunner = loadTranscriptScript(t)
	t.Cleanup(func() { activeRunner = origRunner })
	withGetenv(t, true, "")

	resp, err := runHandshake(context.Background(), handshakeModePropose, dir)
	if err != nil {
		t.Fatalf("runHandshake over the real transcript: %v", err)
	}
	wire := map[string]string{}
	for _, e := range resp.Proposed {
		wire[e.Path] = e.Literal
	}
	prefix := "plugins." + handshakeOwner + "."
	// From testdata/README.md's table: POSTGRES_HOST/PORT/DB set, Redis
	// and MinIO disabled (unset ports/bucket, false booleans), pgvector
	// unset (extensions key not found on a fresh init).
	want := map[string]string{
		prefix + "postgres_host": `"postgres"`,
		prefix + "postgres_port": "5432",
		prefix + "postgres_db":   `"cascadefixture"`,
		prefix + "pgvector":      "false",
		prefix + "redis":         "false",
		prefix + "s3":            "false",
	}
	for path, literal := range want {
		if got := wire[path]; got != literal {
			t.Errorf("proposed[%s] = %q, want %q", path, got, literal)
		}
	}
	for _, absent := range []string{prefix + "redis_port", prefix + "s3_bucket", prefix + "s3_port"} {
		if _, ok := wire[absent]; ok {
			t.Errorf("proposed[%s] present, want absent (the transcript's key was unset)", absent)
		}
	}
}

// TestHandshakeLiveNself forks the REAL nself binary when it is on PATH; a
// build machine without it names the binary and skips rather than failing
// silently. It creates the contract's throwaway `cascadefixture` project
// under t.TempDir() and runs PROPOSE mode only: nothing is applied.
func TestHandshakeLiveNself(t *testing.T) {
	nselfPath, err := exec.LookPath(nselfBinary)
	if err != nil {
		t.Skipf("nself binary not on PATH, skipping: %v", err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", t.TempDir())
	dir := t.TempDir()
	cmd := exec.Command(nselfPath, "init", "--non-interactive", "--name", "cascadefixture")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nself init --non-interactive failed: %v\n%s", err, out)
	}

	origRunner, origApplier := activeRunner, configApplier
	rec := &recordingApplier{}
	activeRunner, configApplier = execRunner{}, rec
	t.Cleanup(func() { activeRunner, configApplier = origRunner, origApplier })
	withGetenv(t, true, "")

	version, err := probeVersion(context.Background(), dir)
	if err != nil || version.Version != minNselfVersion {
		t.Fatalf("live nself version = %q, err %v; want 1.3.5", version.Version, err)
	}
	resp, err := runHandshake(context.Background(), handshakeModePropose, dir)
	if err != nil {
		t.Fatalf("runHandshake against the real %s: %v", nselfPath, err)
	}
	wire := map[string]string{}
	for _, e := range resp.Proposed {
		wire[e.Path] = e.Literal
	}
	prefix := "plugins." + handshakeOwner + "."
	if resp.Status != "proposed" || wire[prefix+"postgres_db"] != `"cascadefixture"` || wire["runtime.profile"] != `"server"` {
		t.Fatalf("live propose = %+v, want status proposed with postgres_db \"cascadefixture\" and runtime.profile", resp)
	}
	if rec.calls != 0 {
		t.Fatalf("PROPOSE mode called ApplyDiff %d times", rec.calls)
	}
}

// FuzzNselfVersionJSON fuzzes decodeVersionInfo. Oracle: never panics; a
// successfully decoded Version string, when non-empty, is exactly the
// string under the EXACT key "version" of an independent decode into a
// generic map (decodeVersionInfo invents nothing and never reads a key
// that only case-folds to "version": that is how {"VERSION":"9.9.9"}
// cleared the version floor; the review's crasher {"Version":"0"} is in
// testdata/fuzz/FuzzNselfVersionJSON/). Seeded from the real transcript's
// version.json (06-FORGE-SPEC §5 rule 7).
func FuzzNselfVersionJSON(f *testing.F) {
	if data, err := os.ReadFile(filepath.Join(transcriptDir, "version.json")); err == nil {
		f.Add(data)
	}
	f.Add([]byte(`{"version":"1.3.5"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := decodeVersionInfo(data)
		if err != nil {
			return
		}
		if v.Version == "" {
			return
		}
		var generic map[string]any
		if jsonErr := json.Unmarshal(data, &generic); jsonErr != nil {
			t.Fatalf("decodeVersionInfo reported version=%q from bytes JSON rejects", v.Version)
		}
		if s, ok := generic["version"].(string); !ok || s != v.Version {
			t.Fatalf("decodeVersionInfo() = %q, generic decode disagrees: %v", v.Version, generic["version"])
		}
	})
}
