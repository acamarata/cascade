// Purpose: reverifyTrust (P1-PLG-12): after a witnessed grant and a replaced
// sibling executable, trust is re-pinned only when the witness covers the
// call, the index verifies with the registry key, its entry checksum is the
// sha256 of the manifest bytes, and the manifest's signed exec_sha256 for
// this platform equals the sibling's digest. Every refusal is
// KindPolicyDenied (or ELEVATION_REQUIRED without a witness) and leaves the
// stored record byte-identical. The grant is a real witnessed plugin.add;
// the index is signed in-test with a test key injected as the verifier key.
// SPORT: internal/plugins reverify tests (P1-PLG-12).
package plugins

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// reverifyFixture is a granted trustdemo whose sibling was then replaced.
type reverifyFixture struct {
	trustFixture
	pub       ed25519.PublicKey
	priv      ed25519.PrivateKey
	newDigest string
	before    string
}

func newReverifyFixture(t *testing.T) reverifyFixture {
	t.Helper()
	f := grantedTrustDemo(t)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	replaced := fakeExecutable + "# rebuilt by a cascade upgrade\n"
	if err := os.WriteFile(f.exe, []byte(replaced), 0o755); err != nil {
		t.Fatal(err)
	}
	return reverifyFixture{trustFixture: f, pub: pub, priv: priv, newDigest: sha256Hex([]byte(replaced)),
		before: storedRecordHash(t, f.store, "trustdemo")}
}

// execManifest is trustManifest listing digest for this platform.
func execManifest(digest string) string {
	return trustManifest + "\n[exec_sha256]\n\"" + goruntime.GOOS + "/" + goruntime.GOARCH + "\" = \"" + digest + "\"\n"
}

// signedIndex signs an index whose trustdemo 1.0.0 entry carries checksum.
func signedIndex(t *testing.T, priv ed25519.PrivateKey, checksum string, sequence uint64) []byte {
	t.Helper()
	entries, err := json.Marshal([]plugin.RegistryIndexEntry{{ID: "trustdemo", LatestVersion: "1.0.0",
		Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum}}}})
	if err != nil {
		t.Fatal(err)
	}
	const published = "2026-11-01T00:00:00Z"
	payload, err := json.Marshal(struct {
		SchemaVersion string          `json:"schema_version"`
		Entries       json.RawMessage `json:"entries"`
		Sequence      uint64          `json:"sequence,omitempty"`
		PublishedAt   string          `json:"published_at,omitempty"`
	}{plugin.SchemaVersionCurrent, entries, sequence, published})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := json.Marshal(map[string]any{"schema_version": plugin.SchemaVersionCurrent, "entries": json.RawMessage(entries),
		"sequence": sequence, "published_at": published, "signature": base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// params packs a reverify call for manifest under index.
func reverifyParams(manifest string, index []byte) UpdateParams {
	return UpdateParams{ID: "trustdemo", Checksum: sha256Hex([]byte(manifest)), ManifestBytes: []byte(manifest), Reverify: true, IndexBytes: index}
}

// goodParams is the call that must re-pin.
func (f reverifyFixture) goodParams(t *testing.T) UpdateParams {
	m := execManifest(f.newDigest)
	return reverifyParams(m, signedIndex(t, f.priv, sha256Hex([]byte(m)), 1))
}

func (f reverifyFixture) verifier() IndexVerifier {
	return plugin.Ed25519Verifier{PublicKey: f.pub}.VerifyIndex
}

// attestedReverify dispatches p through the real elevation middleware and
// InstallMiddleware to the plugin.update handler.
func (f reverifyFixture) attestedReverify(t *testing.T, p UpdateParams) error {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = attestedCall(t, UpdateRPCMethod, raw, InstallMiddleware(nil, nil)(UpdateRPCMethod, WithReverify(UpdateRPCHandler(f.store, nil), f.store, f.verifier())))
	return err
}

func (f reverifyFixture) requireRefused(t *testing.T, err error, fragments ...string) {
	t.Helper()
	requireRefusal(t, err, cascade.KindPolicyDenied, fragments...)
	if after := storedRecordHash(t, f.store, "trustdemo"); after != f.before {
		t.Fatalf("a refused reverify changed the record: %s -> %s", f.before, after)
	}
}

// Positive control: the chain re-pins and changes nothing else.
func TestReverifyRepinsUnderSignedIndex(t *testing.T) {
	f := newReverifyFixture(t)
	old, _, _ := LoadMetadata(context.Background(), f.store, "trustdemo")
	if err := f.attestedReverify(t, f.goodParams(t)); err != nil {
		t.Fatalf("witnessed reverify: %v", err)
	}
	rec, _, _ := LoadMetadata(context.Background(), f.store, "trustdemo")
	if rec.ExecDigest != f.newDigest || rec.ExecDigest == old.ExecDigest || rec.TrustTier != TrustTrusted {
		t.Fatalf("record = %+v, want digest %s (was %s), trusted", rec, f.newDigest, old.ExecDigest)
	}
	if strings.Join(rec.TrustGrants, ",") != strings.Join(old.TrustGrants, ",") || strings.Join(rec.Grants, ",") != strings.Join(old.Grants, ",") {
		t.Fatalf("grants changed: %+v -> %+v", old, rec)
	}
}

func TestReverifyRefusesMismatchedDigest(t *testing.T) {
	f := newReverifyFixture(t)
	m := execManifest(sha256Hex([]byte("some other build")))
	err := f.attestedReverify(t, reverifyParams(m, signedIndex(t, f.priv, sha256Hex([]byte(m)), 1)))
	f.requireRefused(t, err, "signed executable digest does not match the installed executable")
}

func TestReverifyRefusesUnsignedIndex(t *testing.T) {
	f := newReverifyFixture(t)
	_, other, _ := ed25519.GenerateKey(nil)
	m := execManifest(f.newDigest)
	sum := sha256Hex([]byte(m))
	tampered := []byte(strings.Replace(string(signedIndex(t, f.priv, sum, 1)), `"sequence":1`, `"sequence":2`, 1))
	for name, index := range map[string][]byte{
		"another key":   signedIndex(t, other, sum, 1),
		"tampered body": tampered,
		"not json":      []byte("not an index"),
	} {
		t.Run(name, func(t *testing.T) {
			f.requireRefused(t, f.attestedReverify(t, reverifyParams(m, index)), "registry index signature did not verify")
		})
	}
}

func TestReverifyRefusesChecksumMismatch(t *testing.T) {
	f := newReverifyFixture(t)
	m := execManifest(f.newDigest)
	wrong := sha256Hex([]byte("a different manifest"))
	p := reverifyParams(m, signedIndex(t, f.priv, wrong, 1))
	f.requireRefused(t, f.attestedReverify(t, p), "index checksum is not the checksum the call pins")
	p.Checksum = wrong // index and call agree, but neither is the manifest's sha256
	f.requireRefused(t, f.attestedReverify(t, p), "not the sha256 of the manifest bytes")
}

func TestReverifyRefusesMissingPlatformDigest(t *testing.T) {
	f := newReverifyFixture(t)
	m := trustManifest + "\n[exec_sha256]\n\"plan9/mips\" = \"" + f.newDigest + "\"\n"
	err := f.attestedReverify(t, reverifyParams(m, signedIndex(t, f.priv, sha256Hex([]byte(m)), 1)))
	f.requireRefused(t, err, "no signed executable digest for "+goruntime.GOOS+"/"+goruntime.GOARCH)
	bare := reverifyParams(trustManifest, signedIndex(t, f.priv, sha256Hex([]byte(trustManifest)), 1))
	f.requireRefused(t, f.attestedReverify(t, bare), "no signed executable digest for")
}

func TestReverifyRequiresWitness(t *testing.T) {
	f := newReverifyFixture(t)
	p := f.goodParams(t)
	// Direct call: no witness at all.
	_, err := reverifyTrust(context.Background(), f.store, f.verifier(), p)
	f.requireRefused(t, err, "no verified elevation witness")
	raw, _ := json.Marshal(p)
	// Through the middleware stack with no elevation: the handler refuses.
	_, err = InstallMiddleware(nil, nil)(UpdateRPCMethod, WithReverify(UpdateRPCHandler(f.store, nil), f.store, f.verifier()))(context.Background(), raw)
	requireRefusal(t, err, cascade.KindElevationRequired)
	// A witness for another verb does not carry a reverify.
	handler := InstallMiddleware(nil, nil)(UpdateRPCMethod, func(ctx context.Context, _ json.RawMessage) (any, error) {
		return reverifyTrust(ctx, f.store, f.verifier(), p)
	})
	_, err = attestedCall(t, "plugin.add", raw, handler)
	f.requireRefused(t, err, "covers plugin.add, not plugin.update")
}

func TestReverifyRefusesNeverTrustedRecord(t *testing.T) {
	f := newTrustFixture(t, "trustdemo")
	rec := PluginMetadata{Name: "trustdemo", InstalledVersion: "1.0.0", Enabled: true, RuntimeMode: plugin.RuntimeProcess}
	if err := SaveMetadata(context.Background(), f.store, rec); err != nil { // no trust fields
		t.Fatal(err)
	}
	before := storedRecordHash(t, f.store, "trustdemo")
	rf := reverifyFixture{trustFixture: f, before: before}
	rf.pub, rf.priv, _ = ed25519.GenerateKey(nil)
	rf.newDigest = f.digest
	err := rf.attestedReverify(t, rf.goodParams(t))
	rf.requireRefused(t, err, "never trusted", "no prior executable digest")
}

func TestReverifyRefusesOtherVersionAndSequenceZero(t *testing.T) {
	f := newReverifyFixture(t)
	m := strings.Replace(execManifest(f.newDigest), `"1.0.0"`, `"1.0.1"`, 1)
	f.requireRefused(t, f.attestedReverify(t, reverifyParams(m, signedIndex(t, f.priv, sha256Hex([]byte(m)), 1))), "is not the manifest's version 1.0.1")
	good := execManifest(f.newDigest)
	f.requireRefused(t, f.attestedReverify(t, reverifyParams(good, signedIndex(t, f.priv, sha256Hex([]byte(good)), 0))), "no sequence")
}

func TestReverifyRefusesSymlinkedSibling(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlink creation needs a privilege on windows")
	}
	f := newReverifyFixture(t)
	target := f.exe + ".real"
	if err := os.Rename(f.exe, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, f.exe); err != nil {
		t.Fatal(err)
	}
	f.requireRefused(t, f.attestedReverify(t, f.goodParams(t)), "executable is not trustable")
}

// LaunchInstalled's refusal after a changed executable names the exact
// command reverify is run with, and the trust doc quotes the same command.
func TestLaunchRefusalNamesReverify(t *testing.T) {
	f := newReverifyFixture(t)
	rec, _, _ := LoadMetadata(context.Background(), f.store, "trustdemo")
	_, err := beginLaunch(context.Background(), LaunchEnv{}, rec, rec.ExecDigest)
	cmd := ReverifyCommand("trustdemo")
	if cmd != "cascade plugin update trustdemo --reverify" {
		t.Fatalf("ReverifyCommand = %q", cmd)
	}
	requireRefusal(t, err, cascade.KindPolicyDenied, "executable changed since it was trusted", "run `"+cmd+"`")
	doc, rerr := os.ReadFile("../../docs/security-posture/plugin-trust.md")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(doc), "`"+ReverifyCommand("<id>")+"`") {
		t.Fatalf("docs/security-posture/plugin-trust.md does not quote `%s`", ReverifyCommand("<id>"))
	}
}

// A reverify wired without a plugin store refuses typed instead of panicking.
func TestReverifyNilStoreRefusesWithoutPanic(t *testing.T) {
	f := newReverifyFixture(t)
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("a reverify with no plugin store panicked: %v", r)
			}
		}()
		raw, merr := json.Marshal(f.goodParams(t))
		if merr != nil {
			t.Fatal(merr)
		}
		_, err = attestedCall(t, UpdateRPCMethod, raw, InstallMiddleware(nil, nil)(UpdateRPCMethod, WithReverify(UpdateRPCHandler(nil, nil), nil, f.verifier())))
	}()
	requireRefusal(t, err, cascade.KindUnavailable, "no plugin store")
}
