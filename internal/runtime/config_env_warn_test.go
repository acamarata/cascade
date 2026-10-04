package runtime

// Purpose: proves the env-override warnings (fallback to a plain string,
//   unknown key, case collision) reach the caller's own LoadOptions.Warn,
//   one message per condition, naming variables and never values, and that
//   two concurrent Loads never see each other's warnings (BF-008). Also
//   declares every dynamic env read, so a name built at run time is never
//   mistaken for a generic override (TestReservedEnvDynamicReadsDeclared).
// Inputs: fake Environ/Getenv, files in t.TempDir(), fresh HOME per test.
// Outputs: n/a (test-only).
// Constraints: no package-global callback exists; isolation is per Load.
//   A nil Warn routes to the default slog logger (captured here with
//   slog.SetDefault, restored in t.Cleanup; that test is not parallel).
// SPORT: runtime/config (ADD, P1-CORE-16).

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func loadWithEnv(t *testing.T, path string, env map[string]string, w *warnCollector) error {
	t.Helper()
	_, err := Load(context.Background(), LoadOptions{
		Path: path, Getenv: func(string) string { return "" },
		Environ: fakeEnviron(env), Warn: w.warn,
	})
	return err
}

func cleanConfig(t *testing.T) string {
	t.Helper()
	isolateHome(t)
	path := writeConfigFile(t, t.TempDir(), "schema_version = 1\n")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEnvOverrideFallbackWarns(t *testing.T) {
	secret := "hunter2-" + "not-a-real-secret"
	cases := []struct {
		name  string
		env   map[string]string
		names []string // every one must appear in the single message
		value string   // must not appear
	}{
		{"bareword fallback", map[string]string{"CASCADE_LOGGING__LEVEL": "debug"}, []string{"CASCADE_LOGGING__LEVEL"}, "debug"},
		{"bareword secret-looking value", map[string]string{"CASCADE_REGISTRY__URL": secret}, []string{"CASCADE_REGISTRY__URL"}, secret},
		{"unknown key", map[string]string{"CASCADE_NOSUCH__KEY": "1"}, []string{"CASCADE_NOSUCH__KEY"}, "= 1"},
		{"case collision", map[string]string{"CASCADE_FOO__BAR": "1", "CASCADE_FOO__bar": "2"}, []string{"CASCADE_FOO__BAR", "CASCADE_FOO__bar"}, "= 2"},
		// One message per variable even when conditions stack.
		{"unknown key with a bareword", map[string]string{"CASCADE_NOSUCH__KEY": "plainword"}, []string{"CASCADE_NOSUCH__KEY"}, "plainword"},
		{"case collision with a bareword", map[string]string{"CASCADE_FOO__BAR": "1", "CASCADE_FOO__bar": "plainword"}, []string{"CASCADE_FOO__BAR", "CASCADE_FOO__bar"}, "plainword"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := &warnCollector{}
			if err := loadWithEnv(t, cleanConfig(t), c.env, w); err != nil {
				t.Fatalf("Load: %v", err)
			}
			msgs := w.all()
			if len(msgs) != 1 {
				t.Fatalf("Warn messages = %q, want exactly one", msgs)
			}
			for _, n := range c.names {
				if !strings.Contains(msgs[0], n) {
					t.Errorf("message %q does not name %s", msgs[0], n)
				}
			}
			if c.value == "" || strings.Contains(msgs[0], c.value) {
				t.Errorf("message %q leaks the value %q", msgs[0], c.value)
			}
		})
	}
}

func TestEnvOverrideCleanConfigNoWarn(t *testing.T) {
	w := &warnCollector{}
	env := map[string]string{
		"CASCADE_LOGGING__LEVEL":       `"debug"`,
		"CASCADE_RETRIEVAL__FUSION__K": "80",
		"CASCADE_TELEMETRY__ENABLED":   "false",
		"CASCADE_HOME":                 "/somewhere",
		"CASCADE_INIT_ANSWER":          "x",
		"CASCADE_TEST_ONLY_FLAG":       "1",
		"UNRELATED":                    "y",
	}
	if err := loadWithEnv(t, cleanConfig(t), env, w); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if msgs := w.all(); len(msgs) != 0 {
		t.Fatalf("clean config produced warnings: %q", msgs)
	}
}

func TestEnvWarnIsPerLoad(t *testing.T) {
	isolateHome(t)
	dirtyPath := writeConfigFile(t, t.TempDir(), "schema_version = 1\n")
	if err := os.Chmod(dirtyPath, 0o660); err != nil {
		t.Fatal(err)
	}
	cleanPath := cleanConfig(t)
	wantDirty := 2 // group-writable file + bareword fallback
	if osIsWindows() {
		wantDirty = 1 // permissions are not checked on windows
	}
	for round := 0; round < 20; round++ {
		a, b := &warnCollector{}, &warnCollector{}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs[0] = loadWithEnv(t, dirtyPath, map[string]string{"CASCADE_LOGGING__LEVEL": "debug"}, a)
		}()
		go func() {
			defer wg.Done()
			errs[1] = loadWithEnv(t, cleanPath, map[string]string{"CASCADE_LOGGING__LEVEL": `"debug"`}, b)
		}()
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("Load errors: %v, %v", errs[0], errs[1])
		}
		if got := a.all(); len(got) != wantDirty {
			t.Fatalf("round %d: dirty Load got %q, want %d messages", round, got, wantDirty)
		}
		if got := b.all(); len(got) != 0 {
			t.Fatalf("round %d: clean Load received foreign warnings %q", round, got)
		}
	}
}

// slogCapture records every slog record and its text rendering.
type slogCapture struct {
	mu   sync.Mutex
	recs []slog.Record
	text bytes.Buffer
}

type slogCaptureHandler struct{ c *slogCapture }

func (h slogCaptureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h slogCaptureHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h slogCaptureHandler) WithGroup(string) slog.Handler            { return h }

func (h slogCaptureHandler) Handle(ctx context.Context, r slog.Record) error {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	h.c.recs = append(h.c.recs, r.Clone())
	return slog.NewTextHandler(&h.c.text, nil).Handle(ctx, r)
}

func (c *slogCapture) take() ([]slog.Record, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	recs, text := c.recs, c.text.String()
	c.recs, c.text = nil, bytes.Buffer{}
	return recs, text
}

// TestLoadNilWarnUsesDefaultSlog proves a Load with no Warn callback (the
// daemon and most callers) never drops its warning: it reaches the default
// slog logger as one formatted message with no attributes.
func TestLoadNilWarnUsesDefaultSlog(t *testing.T) {
	skipUnlessUnix(t)
	isolateHome(t)
	path := writeConfigFile(t, t.TempDir(), "schema_version = 1\n")
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	finding, err := CheckConfigPermissions(path)
	if err != nil || !strings.Contains(finding.Reason, path) || !strings.Contains(finding.Reason, "group-writable") {
		t.Fatalf("fixture finding = %+v, %v; want a group-writable reason naming %s", finding, err, path)
	}
	want := "runtime: " + finding.Reason
	c := &slogCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slogCaptureHandler{c}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	opts := LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: func() []string { return nil }}
	if _, err := Load(context.Background(), opts); err != nil {
		t.Fatalf("Load: %v", err)
	}
	recs, text := c.take()
	if len(recs) != 1 || recs[0].Level != slog.LevelWarn {
		t.Fatalf("slog records = %d (%q), want exactly one warn record", len(recs), text)
	}
	if recs[0].Message != want || recs[0].NumAttrs() != 0 {
		t.Errorf("record message %q with %d attrs, want %q with none", recs[0].Message, recs[0].NumAttrs(), want)
	}
	if strings.Contains(text, "%s") || strings.Contains(text, "!BADKEY") {
		t.Errorf("slog output %q carries an unformatted verb or a misread argument", text)
	}
	w := &warnCollector{}
	opts.Warn = w.warn
	if _, err := Load(context.Background(), opts); err != nil {
		t.Fatalf("Load with Warn: %v", err)
	}
	if got := w.all(); len(got) != 1 || got[0] != want {
		t.Errorf("Warn got %q, want exactly [%q]", got, want)
	}
	if recs, text := c.take(); len(recs) != 0 {
		t.Errorf("slog received %q although a Warn callback was set", text)
	}
}

// dynamicEnvReads declares every env-accessor call whose first argument is
// not a compile-time constant: file:line from the repo root -> the env
// names it reads. "<caller-named>" marks a pass-through adapter whose
// caller names the variable (a constant there is scanned at that call);
// "<operator-named>" marks a name taken from a flag or from config. Every
// CASCADE_* name here must be reserved. Slices of names are not evaluated:
// their names are listed by hand.
var dynamicEnvReads = map[string][]string{
	"cmd/cascade/backup_key.go:177":          {"<operator-named>"}, // --passphrase-env
	"cmd/cascade/init_adapters.go:30":        {"<caller-named>"},
	"cmd/cascade/profile_attach.go:62":       {"<caller-named>"},
	"internal/backup/targets.go:228":         {"<operator-named>"}, // name match only: passes getenv and the S3 prefix to s3.go
	"internal/backup/targets/s3.go:69":       {"<operator-named>"}, // target S3 env prefix + suffix
	"internal/backup/targets/s3.go:70":       {"<operator-named>"},
	"internal/backup/targets/s3.go:71":       {"<operator-named>"},
	"internal/backup/targets/s3.go:72":       {"<operator-named>"},
	"internal/build/sweep_allow.go:112":      {"CASCADE_IDENTIFIER_PATTERNS"},
	"internal/ci/waitmerge_deps.go:77":       {"CASCADE_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"},
	"internal/providers/intake/core.go:183":  {"<caller-named>"},
	"internal/providers/intake/core.go:201":  {"<caller-named>"},
	"internal/runtime/config_load.go:145":    {"CASCADE_PROFILE"},
	"internal/runtime/initconfig/env.go:100": {"<caller-named>"},
	"internal/runtime/profile_server.go:119": {"<operator-named>"}, // server-profile DSN env-ref
	"internal/runtime/profile_server.go:152": {"<operator-named>"}, // server-profile s3_env_prefix + suffix
	"internal/secrets/oauth_transport.go:57": {"<caller-named>"},
	"internal/tools/registry-gen/main.go:62": {"<operator-named>"}, // --key-ref
	"plugins/cascade-pa/cmd/env.go:12":       {"<caller-named>"},
	"plugins/github/token_env.go:41":         {"CASCADE_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"},
	"plugins/github/wiki/confirm.go:43":      {"<caller-named>"},
	"plugins/nself/handshake_entries.go:129": {"CASCADE_STORAGE_POSTGRES_DSN", "CASCADE_STORAGE_REDIS_URL", "CASCADE_STORAGE_S3_ENDPOINT", "CASCADE_STORAGE_S3_BUCKET", "CASCADE_STORAGE_S3_KEY_ID", "CASCADE_STORAGE_S3_SECRET"}, // requiredServerEnvRefs, presence only
}

// TestReservedEnvDynamicReadsDeclared proves the dynamic-read table matches
// the tree exactly, that its CASCADE_* names are reserved, and that a
// seeded undeclared dynamic read fails the gate.
func TestReservedEnvDynamicReadsDeclared(t *testing.T) {
	_, dynamic := envConstsRead(t, filepath.Join("..", ".."), os.Environ(), "./...")
	undeclared, stale := dynamicReadDrift(dynamic, dynamicEnvReads)
	for _, at := range undeclared {
		t.Errorf("dynamic env read at %s is not declared in dynamicEnvReads", at)
	}
	for _, at := range stale {
		t.Errorf("dynamicEnvReads entry %s matches no dynamic env read in the tree", at)
	}
	names := map[string]string{}
	for at, ns := range dynamicEnvReads {
		if len(ns) == 0 {
			t.Errorf("dynamicEnvReads entry %s names no variable", at)
		}
		for _, n := range ns {
			if strings.HasPrefix(n, "CASCADE_") {
				names[n] = at
			}
		}
	}
	for _, m := range uncoveredEnvNames(names) {
		t.Errorf("%s (dynamic read at %s) is not in reservedEnvVars", m, names[m])
	}
	dir, env := seedEnvModule(t, "package main\n\nimport \"os\"\n\nfunc main() {\n\tname := \"CASCADE_SEEDED_DYNAMIC\"\n\t_ = os.Getenv(name)\n}\n")
	_, seeded := envConstsRead(t, dir, env, "./...")
	if got, _ := dynamicReadDrift(seeded, dynamicEnvReads); len(got) != 1 || got[0] != "main.go:7" {
		t.Fatalf("undeclared = %v (found %v), want exactly [main.go:7]", got, seeded)
	}
}
