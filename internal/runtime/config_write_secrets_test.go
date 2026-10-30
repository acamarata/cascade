package runtime

// Purpose: the SecretLiteralScanner seam of ScanTreeForSecrets (P1-SEC-30):
// no scanner refuses, HEAD refusals survive, every hit is reported, and the
// scanner sees pieces, folded forms and joined halves. Fixtures are built at
// run time; none is a real credential.

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// cleanScanner detects nothing, so the package's other tests see exactly
// the HEAD heuristic they were written against.
func cleanScanner(string) []string { return nil }

func init() { SetSecretLiteralScanner(cleanScanner) }

// withScanner installs s for one test and restores the clean scanner.
func withScanner(t *testing.T, s SecretLiteralScanner) {
	t.Helper()
	SetSecretLiteralScanner(s)
	t.Cleanup(func() { SetSecretLiteralScanner(cleanScanner) })
}

// markerScanner flags any value containing the marker run, the way the
// process detector flags a token, so the tests control what is "secret".
func markerScanner(marker string) SecretLiteralScanner {
	return func(v string) []string {
		if strings.Contains(v, marker) {
			return []string{"api_key"}
		}
		return nil
	}
}

func TestConfigSetRefusesWithoutScanner(t *testing.T) {
	withScanner(t, nil)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("# keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (&ConfigWriter{Path: path}).Set("logging.level", `"debug"`)
	if kind, _ := cascade.KindOf(err); kind != cascade.KindUnavailable {
		t.Fatalf("Set with no scanner: err = %v (kind %q), want KindUnavailable", err, kind)
	}
	if got, _ := os.ReadFile(path); string(got) != "# keep\n" {
		t.Fatalf("file changed to %q; a refused write must leave it alone", got)
	}
	err = ScanTreeForSecrets(map[string]interface{}{"logging": map[string]interface{}{"level": "info"}})
	if kind, _ := cascade.KindOf(err); kind != cascade.KindUnavailable {
		t.Fatalf("ScanTreeForSecrets with no scanner on a clean tree: %v, want KindUnavailable", err)
	}
}

// headRefusals is the positive table of the f688c0b LooksLikeSecret tests,
// assembled from parts exactly as those tests did.
func headRefusals() []string {
	return []string{
		"sk" + "-abcdefghijklmnopqrstuvwxyz0123456789",
		"ghp" + "_abcdefghijklmnopqrstuvwxyz0123456789",
		"xoxb" + "-1234567890-" + "abcdefghijklmnop",
		"ya29" + ".a0AfH6SMBexampletoken1234567890",
		"-----BEGIN " + "PRIVATE KEY-----",
		strings.Repeat("aB3", 20),
		"sk" + "-abcdefghijklmnopqrstuvwxyz with trailing words",
	}
}

func TestConfigSetKeepsHeadRefusals(t *testing.T) {
	withScanner(t, cleanScanner)
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, v := range headRefusals() {
		_, err := (&ConfigWriter{Path: path}).Set("logging.level", `"`+v+`"`)
		var hit *SecretLiteralError
		if !errors.As(err, &hit) {
			t.Errorf("Set refused nothing for a HEAD positive (len %d): %v", len(v), err)
		}
		if err := ScanTreeForSecrets(map[string]interface{}{"x": map[string]interface{}{"y": v}}); err == nil {
			t.Errorf("ScanTreeForSecrets admitted a HEAD positive (len %d)", len(v))
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a refused Set created the file: %v", err)
	}
	for _, v := range []string{"", "info", "json", "debug", "a short string", "true", "80"} {
		if err := ScanTreeForSecrets(map[string]interface{}{"x": v}); err != nil {
			t.Errorf("HEAD negative %q refused: %v", v, err)
		}
	}
}

func TestScanTreeForSecretsReportsEveryHitSorted(t *testing.T) {
	marker := "Q7" + strings.Repeat("zx9", 6)
	withScanner(t, markerScanner(marker))
	tree := map[string]interface{}{
		"b": map[string]interface{}{"k": "pre " + marker},
		"a": map[string]interface{}{"k": []interface{}{"ok", "host:" + marker}},
		"c": map[string]interface{}{"k": "/srv/token=" + marker, "plain": "info"},
	}
	var all *SecretLiteralsError
	if err := ScanTreeForSecrets(tree); !errors.As(err, &all) {
		t.Fatalf("err = %v, want *SecretLiteralsError", err)
	}
	var fields []string
	for _, h := range all.Hits {
		fields = append(fields, h.Field)
	}
	if strings.Join(fields, ",") != "a.k,b.k,c.k" {
		t.Fatalf("fields = %v, want every hit sorted [a.k b.k c.k]", fields)
	}
	if strings.Contains(all.Error(), marker) {
		t.Fatal("the error text carries the scanned value")
	}
	var one *SecretLiteralError
	if !errors.As(error(all), &one) || one.Field != "a.k" {
		t.Fatalf("errors.As to *SecretLiteralError = %v, want the first hit", one)
	}
}

func TestScanTreeForSecretsScansFoldedPiecesAndJoins(t *testing.T) {
	half1, half2 := "Hq4"+strings.Repeat("w8", 5), "Zr6"+strings.Repeat("k2", 5)
	withScanner(t, markerScanner(half1+half2))
	full := "ｇｈｐ_" + strings.Repeat("Ab1", 12) // fullwidth "ghp_"
	cases := map[string]map[string]interface{}{
		"fullwidth prefix":  {"t": map[string]interface{}{"v": full}},
		"userinfo, no URL":  {"t": map[string]interface{}{"v": "deploy:" + "pw" + "Zx81@db.internal"}},
		"two string leaves": {"t": map[string]interface{}{"a": half1, "b": half2}},
		"two array items":   {"t": map[string]interface{}{"a": []interface{}{half1, half2}}},
		"prefix split off":  {"t": map[string]interface{}{"a": []interface{}{"gh", "p_" + "Ab1Cd2Ef3Gh4Ij5Kl6Mn"}}},
	}
	for name, tree := range cases {
		if err := ScanTreeForSecrets(tree); err == nil {
			t.Errorf("%s: admitted", name)
		}
	}
	apart := map[string]interface{}{"t": map[string]interface{}{"a": half1, "m": int64(3)}, "u": map[string]interface{}{"b": half2}}
	if err := ScanTreeForSecrets(apart); err != nil {
		t.Errorf("halves in different tables were joined: %v", err)
	}
}

// TestScanTreeForSecretsScreensKeyNames: a secret used as a key name is
// refused, and no error names it; the field carries the key's position.
func TestScanTreeForSecretsScreensKeyNames(t *testing.T) {
	withScanner(t, cleanScanner)
	token := "ghp" + "_" + strings.Repeat("Ab1", 12)
	for name, tree := range map[string]map[string]interface{}{
		"key with a plain value":  {"x": map[string]interface{}{"ok": "info", token: true}},
		"key with a secret value": {"x": map[string]interface{}{token: token}},
		"table named by a secret": {token: map[string]interface{}{"y": int64(1)}},
	} {
		var all *SecretLiteralsError
		err := ScanTreeForSecrets(tree)
		if !errors.As(err, &all) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
			continue
		}
		if strings.Contains(err.Error(), "Ab1Ab1") {
			t.Errorf("%s: the error names the secret key", name)
		}
		if !strings.Contains(err.Error(), "<key ") {
			t.Errorf("%s: the error does not give the key position: %v", name, err)
		}
	}
}

// sortedKnownKeys returns a sorted copy of knownConfigKeys.
func sortedKnownKeys() []string {
	out := append([]string(nil), knownConfigKeys...)
	sort.Strings(out)
	return out
}
