package config

// Purpose: P1-SEC-30 acceptance [4] and [5] through the real `config set`
// and `config edit` verbs with the process detector installed exactly as
// the cascade binary installs it (ProcessSecretLiteralScanner).
// Constraints: every fixture is generated at run time from a fixed seed;
// none is a real credential, and no assertion prints one.

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

func init() { runtime.SetSecretLiteralScanner(ProcessSecretLiteralScanner) }

// fakeKey is a deterministic opaque run of n characters (n >= 2): a letter
// and a digit first, so it always reads as an opaque token.
func fakeKey(seed int64, n int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	r := rand.New(rand.NewSource(seed))
	b := []byte("Q7")
	for len(b) < n {
		b = append(b, alphabet[r.Intn(len(alphabet))])
	}
	return string(b)
}

// fakeJWT has the three-part header shape of a JWT and nothing else.
func fakeJWT() string { return "eyJ" + fakeKey(2, 20) + "." + fakeKey(3, 30) + "." + fakeKey(4, 30) }

// secretHome writes content (if any) as the config and returns home, path.
func secretHome(t *testing.T, content string) (home, path string) {
	t.Helper()
	home = t.TempDir()
	path = filepath.Join(home, "config.toml")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home, path
}

// runEdit runs `config edit` with the fake editor appending appendLine.
func runEdit(t *testing.T, home, appendLine string) (string, error) {
	t.Helper()
	root, out, errOut := newTestRootWithEnv(t, home, map[string]string{"EDITOR": fakeEditorScript(t, appendLine)})
	root.SetArgs([]string{"config", "edit"})
	err := root.ExecuteContext(context.Background())
	return out.String() + errOut.String(), err
}

// wantRefused asserts an invalid-input refusal that left path's bytes as
// they were and never echoed secret.
func wantRefused(t *testing.T, label, path string, before []byte, secret, out string, err error, reason string) {
	t.Helper()
	if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
		t.Errorf("%s: err = %v (kind %q), want an invalid-input refusal", label, err, kind)
		return
	}
	if reason != "" && !strings.Contains(err.Error(), reason) {
		t.Errorf("%s: refused for another reason: %v (want %q)", label, err, reason)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("%s: config.toml changed on a refused write", label)
	}
	if strings.Contains(err.Error()+out, secret) {
		t.Errorf("%s: the refusal echoed the secret value", label)
	}
}

func TestConfigSetRefusesWhitespaceAndShortTokens(t *testing.T) {
	for name, v := range map[string]string{
		"ghp_ plus 36":           "ghp" + "_" + fakeKey(1, 36),
		"Bearer jwt":             "Bearer " + fakeJWT(),
		"whitespace password":    "password: " + fakeKey(5, 20) + " rotated weekly",
		"whitespace split token": fakeKey(6, 20) + " " + fakeKey(7, 20),
	} {
		home, path := secretHome(t, "schema_version = 1\n")
		out, err := runVerb(t, home, "set", "registry.url", `"`+v+`"`)
		wantRefused(t, name, path, []byte("schema_version = 1\n"), v, out, err, "")
	}
}

func TestConfigEditReportsEveryHit(t *testing.T) {
	home, path := secretHome(t, "")
	a, b, c := fakeKey(8, 40), "Bearer "+fakeJWT(), "host:"+fakeKey(9, 40)
	out, err := runEdit(t, home, "[scratch]\na = \""+a+"\"\nb = \""+b+"\"\nc = \""+c+"\"\nplain = \"info\"")
	wantRefused(t, "edit", path, nil, a, out, err, "at 3 field(s)")
	for _, field := range []string{"scratch.a", "scratch.b", "scratch.c"} {
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("the refusal does not name %s: %v", field, err)
		}
	}
	if err != nil && (strings.Contains(err.Error(), b) || strings.Contains(err.Error(), c)) {
		t.Error("the refusal echoed a secret value")
	}
}

func TestConfigSetRefusesSplitAndEmbeddedKeys(t *testing.T) {
	h1, h2 := fakeKey(10, 20), "Zr"+fakeKey(11, 20)[2:]
	cases := []struct {
		name, prior, setKey, setValue, edit, reason string
	}{
		{"halves in two string leaves", "[secrets]\nkeychain_backend = \"" + h2 + "\"\n", "secrets.clipboard_ttl", `"` + h1 + `"`,
			"[scratch]\na = \"" + h1 + "\"\nb = \"" + h2 + "\"", "joined with"},
		{"halves in two array elements", "", "retrieval.sources", `["` + h1 + `", "` + h2 + `"]`,
			"[scratch]\nlist = [\"" + h1 + "\", \"" + h2 + "\"]", "adjacent array elements"},
		{"punctuation-embedded key", "", "registry.url", `"host:` + fakeKey(12, 40) + `"`, "[scratch]\nv = \"host:" + fakeKey(12, 40) + "\"", ""},
		{"userinfo without a scheme", "", "registry.url", `"deploy:` + fakeKey(13, 12) + `@db.internal"`,
			"[scratch]\nv = \"deploy:" + fakeKey(13, 12) + "@db.internal\"", "userinfo"},
		{"prefix inside a segment", "", "registry.url", `"abcghp_` + fakeKey(14, 24) + `"`, "[scratch]\nv = \"abcghp_" + fakeKey(14, 24) + "\"", ""},
		{"fullwidth-confusable prefix", "", "registry.url", "\"ｇｈｐ_" + fakeKey(15, 24) + "\"",
			"[scratch]\nv = \"ｇｈｐ_" + fakeKey(15, 24) + "\"", ""},
	}
	for _, tc := range cases {
		home, path := secretHome(t, tc.prior)
		out, err := runVerb(t, home, "set", tc.setKey, tc.setValue)
		wantRefused(t, tc.name+" (set)", path, []byte(tc.prior), h1+h2, out, err, tc.reason)
		home, path = secretHome(t, tc.prior)
		out, err = runEdit(t, home, tc.edit)
		wantRefused(t, tc.name+" (edit)", path, []byte(tc.prior), h1+h2, out, err, tc.reason)
	}
}

func TestPathKeyValuesScannedPerSegment(t *testing.T) {
	key := fakeKey(16, 40)
	for name, v := range map[string]string{
		"slash plus opaque run": "/" + key,
		"token= segment":        "/srv/token=" + key,
		"dot-dot segments":      "/srv/app/../" + key,
	} {
		for _, k := range []string{"registry.cache_dir", "registry.pubkey_path"} {
			home, path := secretHome(t, "")
			out, err := runVerb(t, home, "set", k, `"`+v+`"`)
			wantRefused(t, name+" at "+k, path, nil, key, out, err, "")
		}
	}
	home, path := secretHome(t, "")
	if out, err := runVerb(t, home, "set", "registry.cache_dir", `"/home/dev/src/cascade"`); err != nil {
		t.Fatalf("a bare absolute project directory was refused: %v\n%s", err, out)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "/home/dev/src/cascade") {
		t.Fatalf("the accepted directory was not written: %q", data)
	}
}

// TestShippedDefaultsPassTheScanner is the escalate_if guard: no shipped
// default value is refused by the installed detector.
func TestShippedDefaultsPassTheScanner(t *testing.T) {
	_, path := secretHome(t, "")
	cfg, err := runtime.Load(context.Background(), runtime.LoadOptions{Path: path, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	tree := map[string]interface{}{}
	for _, e := range cfg.EffectiveEntries() {
		tree[e.Key] = e.Value
	}
	if len(tree) == 0 {
		t.Fatal("no default entries to scan")
	}
	if err := runtime.ScanTreeForSecrets(tree); err != nil {
		t.Fatalf("a shipped default is refused: %v", err)
	}
}

// TestConfigRefusesSecretKeyNames: edit and set refuse a secret used as a
// key name and never print it.
func TestConfigRefusesSecretKeyNames(t *testing.T) {
	token := "ghp" + "_" + fakeKey(17, 36)
	home, path := secretHome(t, "")
	out, err := runEdit(t, home, "[x]\n\""+token+"\" = true")
	wantRefused(t, "edit key name", path, nil, token, out, err, "<key ")
	out, err = runVerb(t, home, "set", token+".y", "1")
	if err == nil || strings.Contains(err.Error()+out, token) {
		t.Fatalf("set with a secret key name: err = %v; want a refusal that never prints it", err)
	}
}

// TestProcessScannerRefusesWhenDetectorFails pins the fail-closed adapter: a
// detector that cannot be resolved reports a hit, so every value refuses.
func TestProcessScannerRefusesWhenDetectorFails(t *testing.T) {
	saved := processDetector
	processDetector = func() (*secrets.Detector, error) { return nil, cascade.New(cascade.KindInvalidInput, "bad [secrets]") }
	t.Cleanup(func() { processDetector = saved })
	if got := ProcessSecretLiteralScanner("info"); len(got) == 0 {
		t.Fatal("a failed detector reported a clean value")
	}
	home, path := secretHome(t, "")
	out, err := runVerb(t, home, "set", "logging.level", `"debug"`)
	wantRefused(t, "set with a failed detector", path, nil, "\x00", out, err, "detector_unavailable")
}
