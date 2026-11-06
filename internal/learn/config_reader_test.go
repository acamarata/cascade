package learn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// writeLearnConfig writes a config.toml under a fresh isolated home. The
// body always starts with schema_version = 1 so runtime.Load never
// rewrites it.
func writeLearnConfig(t *testing.T, body string) (string, []byte) {
	t.Helper()
	dir := isolateHome(t)
	path := filepath.Join(dir, "config.toml")
	data := []byte("schema_version = 1\n" + body)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

// requireKindMsg asserts err carries kind and a message containing msg.
func requireKindMsg(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	if k, ok := cascade.KindOf(err); !ok || k != kind {
		t.Fatalf("error kind: got %v (ok=%v), want %v: %v", k, ok, kind, err)
	}
	if !strings.Contains(err.Error(), msg) {
		t.Fatalf("error message %q does not contain %q", err.Error(), msg)
	}
}

func TestRuntimeConfigReaderReadsTheRawFile(t *testing.T) {
	path, data := writeLearnConfig(t, "[ci.local]\nbuild = [\"go build ./...\"]\ntimeout_seconds = 120\n")
	r := RuntimeConfigReader{Path: path}
	raw, present, source, digest, err := r.Get("ci.local.build")
	if err != nil {
		t.Fatal(err)
	}
	if raw != `["go build ./..."]` || !present || source != runtime.SourceFile {
		t.Fatalf("got raw=%q present=%v source=%q", raw, present, source)
	}
	if digest != runtime.ConfigDigest(data) {
		t.Fatalf("digest %q != ConfigDigest(bytes)", digest)
	}
	raw, present, source, _, err = r.Get("ci.local.timeout_seconds")
	if err != nil || raw != "120" || !present || source != runtime.SourceFile {
		t.Fatalf("timeout: raw=%q present=%v source=%q err=%v", raw, present, source, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatalf("Load rewrote the fixture: %v", err)
	}
}

func TestRuntimeConfigReaderMissingKey(t *testing.T) {
	path, data := writeLearnConfig(t, "[ci.local]\nbuild = [\"go build ./...\"]\n")
	r := RuntimeConfigReader{Path: path}
	raw, present, source, digest, err := r.Get("ci.local.lint")
	if raw != "" || present || source != runtime.SourceDefault || digest != runtime.ConfigDigest(data) || err != nil {
		t.Fatalf("absent key: raw=%q present=%v source=%q digest=%q err=%v", raw, present, source, digest, err)
	}

	missing := RuntimeConfigReader{Path: filepath.Join(t.TempDir(), "none.toml")}
	raw, present, source, digest, err = missing.Get("ci.local.lint")
	if raw != "" || present || source != runtime.SourceDefault || digest != "" || err != nil {
		t.Fatalf("missing file: raw=%q present=%v source=%q digest=%q err=%v", raw, present, source, digest, err)
	}

	bad := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(bad, []byte("schema_version = 1\n[ci.local\nbuild = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	_, present, _, _, err = RuntimeConfigReader{Path: bad}.Get("ci.local.build")
	if present {
		t.Fatal("malformed TOML reported present")
	}
	requireKindMsg(t, err, cascade.KindInvalidInput, "malformed TOML")

	// An invalid key errors before the malformed file is read.
	_, _, _, _, err = RuntimeConfigReader{Path: bad}.Get("CI..Local")
	requireKindMsg(t, err, cascade.KindInvalidInput, "dot-separated lowercase bare-key segments")

	_, _, _, _, err = RuntimeConfigReader{}.Get("ci.local.build")
	requireKindMsg(t, err, cascade.KindInvalidInput, "needs a file path")

	t.Setenv("CASCADE_CI__LOCAL__BUILD", `["make"]`)
	raw, present, source, _, err = r.Get("ci.local.build")
	if err != nil || raw != `["go build ./..."]` || !present || source != runtime.SourceEnv {
		t.Fatalf("env override: raw=%q present=%v source=%q err=%v", raw, present, source, err)
	}
}

func TestAddStepOnAbsentListHeld(t *testing.T) {
	c := LearnedConfig{Target: "ci.local_steps", Tier: TierBehavioral,
		Change: Change{Op: OpAddStep, Path: "ci.local.build", Value: `"cascade context generate --check"`}}

	path, data := writeLearnConfig(t, "[ci.local]\nlint = [\"go vet ./...\"]\n")
	d := DiffTier(RuntimeConfigReader{Path: path}, c)
	if !d.Held || d.HoldReason != HoldAbsentListReplacesRepoDefaults || d.Materialised != "" ||
		d.Computed.Tier != TierBehavioral || d.FileDigest != runtime.ConfigDigest(data) || d.Err != nil {
		t.Fatalf("absent list: %+v", d)
	}

	path, _ = writeLearnConfig(t, "[ci.local]\nbuild = [\"go build ./...\"]\n")
	d = DiffTier(RuntimeConfigReader{Path: path}, c)
	want := `["go build ./...", "cascade context generate --check"]`
	if d.Held || d.Materialised != want || d.Computed.Tier != TierBehavioral || d.Mismatch {
		t.Fatalf("explicit list: %+v", d)
	}

	t.Setenv("CASCADE_CI__LOCAL__BUILD", `["make"]`)
	d = DiffTier(RuntimeConfigReader{Path: path}, c)
	if !d.Held || d.HoldReason != HoldEffectiveSourceNotFile {
		t.Fatalf("env override: %+v", d)
	}

	bad, _ := writeLearnConfig(t, "[ci.local\n")
	d = DiffTier(RuntimeConfigReader{Path: bad}, c)
	if !d.Held || d.HoldReason != HoldReadError || d.Computed.Tier != TierSecurity || !d.Mismatch {
		t.Fatalf("malformed file: %+v", d)
	}
	requireKindMsg(t, d.Err, cascade.KindInvalidInput, "malformed TOML")
}
