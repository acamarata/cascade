// Purpose: prove the two production collaborators ci_policy_wiring.go
// injects into newCIRouteResolver -- loadCIPolicyConfig and
// tokenConfiguredWith -- against a real config.toml and a real file vault
// pinned to a temp CASCADE_HOME, never the operator's home or keychain
// (P1-E25-W5-S51-T5 CI follow-up: the internal/plugins coverage ratchet).
// SPORT: internal/plugins:ci-policy-wiring (TESTED).

package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/plugins/github/cipolicy"
)

// pinCascadeHome points every path the wiring resolves at a fresh temp
// dir, and blanks the fallbacks so nothing reaches the real home.
func pinCascadeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CASCADE_HOME", home)
	t.Setenv("CASCADE_CONFIG", "")
	return home
}

// unsetEveryHome makes NewDefaultPathProvider fail: no CASCADE_HOME and
// no home directory to derive one from, on every GOOS.
func unsetEveryHome(t *testing.T) {
	t.Helper()
	t.Setenv("CASCADE_HOME", "")
	t.Setenv("CASCADE_CONFIG", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
}

func TestLoadCIPolicyConfig_ReadsThePrivatePatterns(t *testing.T) {
	home := pinCascadeHome(t)
	toml := "[ci.policy.repos]\nprivate = [\"acamarata/secret-repo\", \"acamarata/*\"]\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadCIPolicyConfig(context.Background())
	if err != nil {
		t.Fatalf("loadCIPolicyConfig: %v", err)
	}
	want := []string{"acamarata/secret-repo", "acamarata/*"}
	if len(got) != len(want) {
		t.Fatalf("patterns = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("patterns[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoadCIPolicyConfig_MalformedPatternIsAnError(t *testing.T) {
	home := pinCascadeHome(t)
	toml := "[ci.policy.repos]\nprivate = [\"not-a-pattern\"]\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCIPolicyConfig(context.Background()); err == nil {
		t.Fatal("expected the malformed pattern to surface as an error, so the resolver reports the safe route")
	}
}

func TestLoadCIPolicyConfig_NoHomeIsAnError(t *testing.T) {
	unsetEveryHome(t)
	if _, err := loadCIPolicyConfig(context.Background()); err == nil {
		t.Fatal("expected an error when no home directory can be resolved")
	}
}

// fileVaultIn forces the encrypted file vault under the wiring's own
// DataDir with a test-only service label, so the probe travels the real
// custody path without a keychain in reach.
func fileVaultIn() custodySelector {
	return func(cfg secrets.Config) (secrets.Custody, error) {
		cfg.ForceFileVault = true
		cfg.Service = "cascade-test-ci-policy"
		return secrets.SelectCustody(cfg)
	}
}

func TestTokenConfiguredWith_ReportsTheVaultsAnswer(t *testing.T) {
	pinCascadeHome(t)
	ctx := context.Background()
	if tokenConfiguredWith(ctx, fileVaultIn()) {
		t.Fatal("an empty vault reported a configured token")
	}
	custody, err := fileVaultIn()(secrets.Config{Service: "ignored", Dir: filepath.Join(os.Getenv("CASCADE_HOME"), "data")})
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	if err := custody.Set(ctx, cipolicy.TokenVaultKey, []byte("synthetic-token-for-this-test")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !tokenConfiguredWith(ctx, fileVaultIn()) {
		t.Fatal("a vault holding the token name reported no token")
	}
	if tokenConfiguredWith(ctx, func(secrets.Config) (secrets.Custody, error) {
		return nil, secrets.ErrNoCustodyAvailable()
	}) {
		t.Fatal("a custody failure was reported as a configured token")
	}
}

// listFailsCustody is a Custody whose name index cannot be read: the one
// failure the file vault cannot stage on demand.
type listFailsCustody struct{ secrets.Custody }

func (listFailsCustody) List(context.Context) ([]string, error) {
	return nil, secrets.ErrNoCustodyAvailable()
}

func TestTokenProbeWith_AnUnreadableIndexIsNotAToken(t *testing.T) {
	pinCascadeHome(t)
	probe := tokenProbeWith(func(secrets.Config) (secrets.Custody, error) {
		return listFailsCustody{}, nil
	})
	if probe(context.Background()) {
		t.Fatal("a vault whose index cannot be listed reported a configured token")
	}
}

func TestTokenConfiguredWith_NoHomeIsNotAToken(t *testing.T) {
	unsetEveryHome(t)
	if tokenConfiguredWith(context.Background(), fileVaultIn()) {
		t.Fatal("an unresolvable home reported a configured token")
	}
}
