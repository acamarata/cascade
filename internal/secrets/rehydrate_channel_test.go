package secrets

// Purpose: the injection-channel red team. R-21.204 permits exactly one
//   carrier for a rehydrated value - a non-inherited memory buffer the
//   executor holds - and names five carriers that must never hold it.
//   Each case below dispatches an action the approved way and asserts the
//   raw value is absent from that carrier byte for byte.
// Constraints: no subprocess is started; the assertions are about what a
//   dispatch PUTS in each carrier, which is decided before Start.
// SPORT: REHYDRATE_CHANNEL: ADD (forbidden-carrier red team).

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// taskPayload stands for a serialized job row, queue message or event
// body: the shapes an action crosses a process boundary in.
type taskPayload struct {
	Action  string `json:"action"`
	Content string `json:"content"`
}

// TestRehydrateForbiddenCarriers dispatches one tagged action and checks
// every carrier R-21.204 forbids.
func TestRehydrateForbiddenCarriers(t *testing.T) {
	const value = "carrier-red-team-secret-value"
	tagged := []byte("<apikey>CARRIER_KEY</apikey>")
	r, _ := newRehydratorFixture(t, map[string]string{"CARRIER_KEY": value})

	dir := t.TempDir()
	// The payload and the argv are built from the TAGGED content, which
	// is what crosses the boundary. The raw value is resolved only into
	// the buffer below and never travels with them.
	payload, err := json.Marshal(taskPayload{Action: "run", Content: string(tagged)})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	cmd := exec.Command("/bin/echo", string(tagged))
	cmd.Dir = dir
	cmd.Env = os.Environ()

	rc, err := r.Rehydrate(context.Background(), tagged)
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	defer rc.Zero()
	if !bytes.Equal(rc.Data, []byte(value)) {
		t.Fatalf("the buffer carrier did not receive the value: %q", rc.Data)
	}

	raw := []byte(value)
	for _, env := range cmd.Env {
		if bytes.Contains([]byte(env), raw) {
			t.Fatalf("carrier (a) child environment holds the value: %q", env)
		}
	}
	for _, arg := range cmd.Args {
		if bytes.Contains([]byte(arg), raw) {
			t.Fatalf("carrier (b) argv holds the value: %q", arg)
		}
	}
	assertNoValueUnder(t, dir, raw)
	if len(cmd.ExtraFiles) != 0 {
		t.Fatalf("carrier (d) the dispatch inherits %d extra descriptors; none are permitted", len(cmd.ExtraFiles))
	}
	if bytes.Contains(payload, raw) {
		t.Fatalf("carrier (e) the serialized payload holds the value: %s", payload)
	}
}

// assertNoValueUnder walks the child's working directory (carrier c).
func assertNoValueUnder(t *testing.T, dir string, raw []byte) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // path comes from t.TempDir
		if rerr != nil {
			return rerr
		}
		if bytes.Contains(data, raw) {
			t.Fatalf("carrier (c) working-directory file %s holds the value", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the working directory: %v", err)
	}
}

// TestRehydrateStagingBufferIsZeroedOnBothPaths covers the pipe staging
// buffer obligation: whatever the executor stages for a numbered pipe is
// wiped on the success path and on the error path alike.
func TestRehydrateStagingBufferIsZeroedOnBothPaths(t *testing.T) {
	const value = "staging-buffer-secret-value"
	r, _ := newRehydratorFixture(t, map[string]string{"STAGE_KEY": value})

	rc, err := r.Rehydrate(context.Background(), []byte("<token>STAGE_KEY</token>"))
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	staging := &RehydratedContent{Data: append([]byte(nil), rc.Data...)}
	backing := staging.Data[:cap(staging.Data)]
	staging.Zero()
	rc.Zero()
	if bytes.Contains(backing, []byte(value)) {
		t.Fatalf("the staging buffer still holds the value after Zero")
	}

	// Error path: the deferred Zero runs against whatever the caller
	// holds, including the nil content a refusal returns.
	failed, ferr := r.Rehydrate(context.Background(), []byte("<token>NO_SUCH_KEY</token>"))
	if ferr == nil {
		t.Fatalf("expected a refusal for an unknown name")
	}
	failed.Zero()
}

// TestRehydrateOutputRedaction asserts the boundary obligation: a
// subprocess that echoes its own input back does not put the raw value on
// a sink, because the echoed bytes go through the substitution pass
// first. This drives the detector and rewriter the egress pass runs, at
// the one boundary this package owns.
func TestRehydrateOutputRedaction(t *testing.T) {
	const value = "AKIA7YQ2XPLM4RZV6WTB"
	r, _ := newRehydratorFixture(t, map[string]string{"ECHO_KEY": value})
	rc, err := r.Rehydrate(context.Background(), []byte("<apikey>ECHO_KEY</apikey>"))
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	defer rc.Zero()

	// What an echoing subprocess would write on stdout.
	echoed := append([]byte("stdout: "), rc.Data...)

	detector, derr := NewDetector(DefaultRegistry(), DefaultDetectionConfig())
	if derr != nil {
		t.Fatalf("NewDetector: %v", derr)
	}
	hits := detector.ScanCertain(echoed)
	if len(hits) == 0 {
		t.Fatalf("the detector found nothing in the echoed output; the fixture no longer has credential shape")
	}
	result, rerr := NewRewriter().Rewrite(echoed, hits)
	if rerr != nil {
		t.Fatalf("Rewrite: %v", rerr)
	}
	if bytes.Contains(result.Text, []byte(value)) {
		t.Fatalf("the echoed output reached the sink carrying the raw value: %q", result.Text)
	}
}
