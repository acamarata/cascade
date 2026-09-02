// Purpose: read-verb-never-mutates tests, split out of config_test.go per
//
//	R-14.117/Art.10.3 (300-line file cap) as part of this ticket's R-14
//	CR fix — the legacy-file fixture and its assertions pushed
//	config_test.go over the cap. Covers blocking fix 1(c) (get/list/
//	validate/daemon-boot never mutate a legacy config.toml) and the
//	blocking-fix-3 redaction position (get/list echo a secret-shaped
//	value already on disk unredacted — documented, not fixed, per
//	docs/config-reference.md § "Secret screening applies to every write
//	path, not just set").
package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// legacyHandAnnotatedConfig is a config.toml with everything a real
// hand-edited file has and a schema_version-stamped one never does: no
// schema_version key at all (the "legacy" case), comments above and
// beside values, single- and double-quoted strings side by side, an
// unusual key order (elevation before runtime, reversed from how `cascade
// config set` would ever emit them), and a blank-line style that is not
// canonical TOML formatting. Any of these details changing (comment
// dropped, quote style flipped, keys re-sorted) after a read-only verb
// runs is exactly the R-14 CR defect (P1-E03-W1-S05-T8, blocking fix 1).
const legacyHandAnnotatedConfig = `# hand-written config.toml — predates schema_version
# (this comment must survive every read verb byte-for-byte)

[elevation]
allow_remote = false  # break-glass, left off on purpose
helper_pubkey = 'literal-single-quoted-value'

[runtime]
profile = "local"          # double-quoted, extra inline spacing on purpose


[logging]
level    =   "debug"   # unusual '=' spacing, also on purpose
`

// TestConfigCLI_ReadVerbsNeverMutateLegacyFile is the ticket contract's
// required test for blocking fix 1(c): `get`, `list`, and `validate`
// against a hand-annotated legacy file (no schema_version, comments,
// mixed quoting, non-canonical key order) must leave the file
// byte-identical. Before the fix, each of these read verbs funnelled
// through Load → readAndUpgradeTree, which rewrote the whole file via
// toml.Marshal the moment it saw a missing schema_version — destroying
// every property this fixture is designed to catch.
func TestConfigCLI_ReadVerbsNeverMutateLegacyFile(t *testing.T) {
	verbs := [][]string{
		{"config", "get", "runtime.profile"},
		{"config", "list"},
		{"config", "list", "--effective"},
		{"config", "validate"},
	}
	for _, args := range verbs {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(cfgPath, []byte(legacyHandAnnotatedConfig), 0o600); err != nil {
				t.Fatal(err)
			}
			root, _, _ := newTestRoot(t, dir)

			if _, _, err := run(t, root, args...); err != nil {
				t.Fatalf("%v: %v", args, err)
			}

			after, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatalf("read config after %v: %v", args, err)
			}
			if string(after) != legacyHandAnnotatedConfig {
				t.Fatalf("%v mutated config.toml:\n--- want ---\n%s\n--- got ---\n%s", args, legacyHandAnnotatedConfig, after)
			}
		})
	}
}

// TestConfigCLI_DaemonBootReadNeverMutatesLegacyFile covers the same
// blocking-fix-1(c) requirement for the daemon-boot path — a bare
// runtime.Load call with no CLI verb wrapping it at all — since the CR
// named "a daemon boot" alongside get/list/validate as a trigger for the
// original defect.
func TestConfigCLI_DaemonBootReadNeverMutatesLegacyFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(legacyHandAnnotatedConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(string) string { return "" }
	if _, err := runtime.Load(context.Background(), runtime.LoadOptions{
		Path:    cfgPath,
		Getenv:  getenv,
		Environ: func() []string { return nil },
	}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != legacyHandAnnotatedConfig {
		t.Fatalf("daemon-boot Load mutated config.toml:\n--- want ---\n%s\n--- got ---\n%s", legacyHandAnnotatedConfig, after)
	}
}

// TestConfigCLI_GetList_EchoExistingSecretUnredacted pins the explicit
// position recorded in docs/config-reference.md § "Secret screening
// applies to every write path, not just set" (R-14 CR blocking fix 3): a
// secret-shaped value already present in config.toml — written before
// this fix existed, or by any future path that bypasses
// checkLiteralForSecrets/ScanTreeForSecrets — is echoed unredacted by
// `get` and `list`. This is a known, stated gap, not an oversight; this
// test exists so a future change to that behavior (in either direction)
// has to update the doc's position deliberately rather than drift past a
// silent test.
func TestConfigCLI_GetList_EchoExistingSecretUnredacted(t *testing.T) {
	dir := t.TempDir()
	const secret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	cfg := "[registry]\npubkey_path = \"" + secret + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	root, out, _ := newTestRoot(t, dir)

	if _, _, err := run(t, root, "config", "get", "registry.pubkey_path"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(out.String(), secret) {
		t.Fatalf("get did not echo the on-disk secret verbatim (documented behavior): %q", out.String())
	}

	out.Reset()
	if _, _, err := run(t, root, "config", "list"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), secret) {
		t.Fatalf("list did not echo the on-disk secret verbatim (documented behavior): %q", out.String())
	}
}
