package elevation

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type countingCustodyKey struct {
	fakeKeystore
	signs int
}

func (k *countingCustodyKey) Sign(p []byte) ([]byte, error) { k.signs++; return k.fakeKeystore.Sign(p) }

func custodyFixture(t *testing.T, tier CustodyTier) (Selector, *countingCustodyKey) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(name, dir)
	}
	k := &countingCustodyKey{fakeKeystore: fakeKeystore{available: true}}
	if err := k.GenerateKey(); err != nil {
		t.Fatal(err)
	}
	return Selector{DataDir: dir, Sources: []CustodySource{{Tier: tier, Name: "test", Open: func(string) (ElevationKeystore, bool) { return k, true }}}}, k
}

func plantCustodyKey(t *testing.T, dir string, k *countingCustodyKey) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "elevation.key"), []byte(base64.StdEncoding.EncodeToString(k.priv)), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSelectPrefersPlatformOverStaleFileKey(t *testing.T) {
	sel, k := custodyFixture(t, CustodyFile)
	plantCustodyKey(t, sel.DataDir, k)
	sel.Sources = append(sel.Sources, CustodySource{Tier: CustodyPlatform, Name: "platform", Open: func(string) (ElevationKeystore, bool) { return k, true }})
	if got := sel.Select().Tier(); got != CustodyPlatform {
		t.Fatalf("tier=%s", got)
	}
}

func TestSelectRanksBySourceNotSelfReport(t *testing.T) {
	sel, k := custodyFixture(t, CustodyFile)
	if k.Tier() != TierOSKeychain {
		t.Fatal("fixture must self-report keychain")
	}
	c := sel.Select()
	if c.Tier() != CustodyFile {
		t.Fatalf("tier=%s", c.Tier())
	}
	_, err := c.Signer()
	if !DevkeysBuild() {
		assertCustodyRefusal(t, err, CustodyFile)
	}
}

func assertCustodyRefusal(t *testing.T, err error, tier CustodyTier) {
	t.Helper()
	var typed *CustodyTierError
	if !errors.As(err, &typed) || typed.Tier != tier || !strings.Contains(err.Error(), string(tier)) || !strings.Contains(err.Error(), "ADR-0005") {
		t.Fatalf("refusal=%v", err)
	}
	if got, ok := CustodyTierOf(err); !ok || got != tier {
		t.Fatalf("tier=%s,ok=%v", got, ok)
	}
}

func TestSignerRefusesNonElevationTiers(t *testing.T) {
	for _, tier := range []CustodyTier{CustodyFile, CustodyNone} {
		sel, k := custodyFixture(t, tier)
		signer, err := sel.Select().Signer()
		if tier == CustodyFile && DevkeysBuild() {
			continue
		}
		assertCustodyRefusal(t, err, tier)
		if signer != nil || k.signs != 0 {
			t.Fatal("refusal returned a signer or signed")
		}
	}
}

func TestCustodyBuildPolicy(t *testing.T) {
	if DevkeysBuild() != devkeysBuild {
		t.Fatal("build constant disagrees")
	}
	if CustodyFile.SatisfiesElevation() != devkeysBuild {
		t.Fatal("file policy disagrees")
	}
}

func TestLinuxHasNoPlatformSource(t *testing.T) {
	for _, pam := range []bool{false, true} {
		_, k := custodyFixture(t, CustodyFile)
		sources := sourcesForOS("linux", func() ElevationKeystore {
			if pam {
				return k
			}
			return nil
		})
		for _, s := range sources {
			if s.Tier == CustodyPlatform {
				t.Fatal("linux registered platform")
			}
		}
		c := (Selector{DataDir: t.TempDir(), Sources: sources}).Select()
		if pam {
			if c.Tier() != CustodyFile {
				t.Fatalf("PAM tier=%s", c.Tier())
			}
			if !DevkeysBuild() {
				_, err := c.Signer()
				assertCustodyRefusal(t, err, CustodyFile)
			}
		}
	}
}
func TestCustodySourceConstructors(t *testing.T) {
	sel, k := custodyFixture(t, CustodyPlatform)
	c := sel.Select()
	if c.Storage() != TierOSKeychain || c.Source() != "test" {
		t.Fatalf("metadata=%+v", c)
	}
	if (Custody{}).Tier() != CustodyNone {
		t.Fatal("zero custody")
	}
	if _, ok := CustodyTierOf(errors.New("unrelated")); ok {
		t.Fatal("unrelated error classified")
	}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		sources := sourcesForOS(goos, func() ElevationKeystore { return k })
		for _, s := range sources {
			_, _ = s.Open(sel.DataDir)
		}
	}
	if len(DefaultSources()) == 0 {
		t.Skip("platform has no sources")
	}
	none, _ := custodyFixture(t, CustodyNone)
	if _, err := none.Enroll(); err == nil {
		t.Fatal("none enrolled")
	}
}
func TestStaleFilePublicKeyRefusesCorruption(t *testing.T) {
	sel, _ := custodyFixture(t, CustodyFile)
	for _, raw := range []string{"invalid!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if err := os.WriteFile(filepath.Join(sel.DataDir, "elevation.key"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := filePublicKey(sel.DataDir); err == nil {
			t.Fatal("corrupt file accepted")
		}
	}
}
