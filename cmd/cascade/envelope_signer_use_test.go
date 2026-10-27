//go:build !windows

// Purpose: what the installation key is for: envelopes it signs verify only
//
//	under its public key, the reserved name refuses every vault set, the key
//	lives in the TestMain custody (never host custody), and the signer never
//	formats key bytes. Every test uses the MA-2 helper in
//	envelope_signer_test.go; [4] alone keeps the TestMain selector.
//
// SPORT: cmd/cascade installation identity tests (P1-SEC-40).

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	scopeenvelope "github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestEnvelopeSignedByInstallationKeyVerifies(t *testing.T) {
	isolateInstallationKey(t)
	paths := firewallTestPaths(t)
	ctx := context.Background()
	signer, err := productionEnvelopeSigner(ctx, paths)
	if err != nil || signer == nil {
		t.Fatalf("productionEnvelopeSigner = (%v, %v)", signer, err)
	}
	pub := mustPublicKey(t, paths)
	signed, err := scopeenvelope.Issue(ctx, signer, testEnvelope())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := scopeenvelope.Verify(signed, pub, time.Now()); err != nil {
		t.Fatalf("an envelope the production signer issued does not verify under installationPublicKey: %v", err)
	}
	// A second call is the same key, not a fresh one.
	again, err := productionEnvelopeSigner(ctx, paths)
	if err != nil || !bytes.Equal(again.PublicKey(), pub) {
		t.Fatalf("a second productionEnvelopeSigner differs: %v", err)
	}

	_, nodePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	byNode, err := scopeenvelope.Issue(ctx, nodeKeySigner{priv: nodePriv}, testEnvelope())
	if err != nil {
		t.Fatalf("Issue with the node key: %v", err)
	}
	if _, err := scopeenvelope.Verify(byNode, pub, time.Now()); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("a node-signed envelope verified under the installation key: %v", err)
	}
	// The controller's issuer with a node signature does not verify either.
	msg, err := scopeenvelope.Canonical(signed.Env)
	if err != nil {
		t.Fatal(err)
	}
	forged := scopeenvelope.Signed{Env: signed.Env, Sig: ed25519.Sign(nodePriv, msg)}
	if _, err := scopeenvelope.Verify(forged, pub, time.Now()); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("a node signature under the controller issuer verified: %v", err)
	}
}

func TestVaultSetRefusesInstallationKeyName(t *testing.T) {
	env := isolateInstallationKey(t)
	fw, err := processFirewallFor(firewallTestPaths(t))
	if err != nil {
		t.Fatalf("processFirewallFor: %v", err)
	}
	for _, mode := range []secrets.SetMode{secrets.SetUpdate, secrets.SetRename, secrets.SetRefuse} {
		_, err := fw.Broker().Set(context.Background(), secrets.InstallationKeyName, []byte("fake-not-a-key"), mode)
		if !cascade.HasKind(err, cascade.KindPermissionDenied) || !inChain(err, secrets.ErrReservedName) {
			t.Fatalf("Set(%s, mode %v) = %v, want the reserved-name refusal", secrets.InstallationKeyName, mode, err)
		}
	}
	if n := env.entries(t); n != 0 || env.faults.createCount() != 0 {
		t.Fatalf("after the refusals the vault holds %d entries (%d writes)", n, env.faults.createCount())
	}
}

func TestInstallationKeyUsesTestMainCustody(t *testing.T) {
	if reflect.ValueOf(selectProcessCustody).Pointer() == reflect.ValueOf(secrets.SelectCustody).Pointer() {
		t.Fatal("selectProcessCustody is secrets.SelectCustody itself; TestMain must replace it")
	}
	resetProcessFirewall(t) // the selector stays the TestMain one
	resetInstallationSignerMemo(t)
	// The per-run root: TestMain's throwaway HOME, never an inherited
	// CASCADE_HOME that could name a real installation.
	t.Setenv("CASCADE_HOME", "")
	paths := lazyPaths{}
	if home := os.Getenv("HOME"); paths.DataDir() == "" || !strings.HasPrefix(paths.DataDir(), home+string(os.PathSeparator)) {
		t.Fatalf("the data directory %q is not under the per-run HOME %q", paths.DataDir(), home)
	}
	ctx := context.Background()
	intercepted := interceptedHostSelections.Load()
	attempts, _ := hostCustodyAttempts()
	pub := mustPublicKey(t, paths)

	fw, err := processFirewallFor(paths)
	if err != nil {
		t.Fatalf("processFirewallFor: %v", err)
	}
	reader, err := secrets.NewInstallationKeyReader(fw.Broker())
	if err != nil {
		t.Fatalf("NewInstallationKeyReader: %v", err)
	}
	_, priv, err := reader.InstallationKey(ctx)
	if err != nil {
		t.Fatalf("reading the entry back from the TestMain vault: %v", err)
	}
	defer clear(priv)
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), pub) {
		t.Fatal("installationPublicKey is not the public half of the TestMain vault entry")
	}
	signer, err := productionEnvelopeSigner(ctx, paths)
	if err != nil {
		t.Fatalf("productionEnvelopeSigner: %v", err)
	}
	signed, err := scopeenvelope.Issue(ctx, signer, testEnvelope())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := scopeenvelope.Verify(signed, pub, time.Now()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// Both branches assert: without the QA switch the selection must have
	// passed through TestMain's guard; with it, the custody must be the file vault.
	if forced := hostCustodyConfig(paths.DataDir()).ForceFileVault; !forced && interceptedHostSelections.Load() == intercepted {
		t.Fatal("the provisioning selection did not pass through TestMain's guard")
	} else if forced && fw.Broker().Backend() != guardedCustodyName {
		t.Fatalf("the QA switch is set but the custody is %q", fw.Broker().Backend())
	}
	if n, seen := hostCustodyAttempts(); n != attempts {
		t.Fatalf("provisioning reached host custody: %v", seen)
	}
}

func TestInstallationSignerNeverFormatsKey(t *testing.T) {
	isolateInstallationKey(t)
	paths := firewallTestPaths(t)
	signer, err := productionEnvelopeSigner(context.Background(), paths)
	if err != nil {
		t.Fatalf("productionEnvelopeSigner: %v", err)
	}
	fw, err := processFirewallFor(paths)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := secrets.NewInstallationKeyReader(fw.Broker())
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := reader.InstallationKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer clear(priv)
	value := *signer.(*installationSigner)
	out := fmt.Sprintf("%v %+v %#v %s %x %X %q %d %v %+v %#v %x", signer, signer, signer, signer, signer, signer, signer, signer, value, value, value, value)
	seed := priv.Seed()
	for _, leak := range []string{hex.EncodeToString(seed), strings.ToUpper(hex.EncodeToString(seed)), base64.StdEncoding.EncodeToString(seed), string(seed)} {
		if strings.Contains(out, leak) {
			t.Fatalf("formatting the signer printed the private key: %s", out)
		}
	}
}

// TestInstallationKeyCreatedOnceAtomicCreate is [0]'s create-once leg over
// the unwrapped helper vault, so provisioning takes the file vault's atomic
// create-if-absent exactly as production does (faultCustody hides it).
func TestInstallationKeyCreatedOnceAtomicCreate(t *testing.T) {
	resetProcessFirewall(t)
	resetInstallationSignerMemo(t)
	env := &installKeyEnv{vaultDir: t.TempDir(), faults: &keyFaults{}}
	selectProcessCustody = testProcessCustody(env.vaultDir)
	paths := firewallTestPaths(t)
	pub := mustPublicKey(t, paths)
	written := env.backing(t)
	clearSignerMemo()
	if again := mustPublicKey(t, paths); !bytes.Equal(again, pub) || env.entries(t) != 1 || !reflect.DeepEqual(env.backing(t), written) {
		t.Fatalf("second provisioning over the atomic create: same key %v, entries %d", bytes.Equal(again, pub), env.entries(t))
	}
}

// TestInstallationSignerSignRefuses covers Sign's guards: a canceled context
// and a signer with no key refuse, never sign.
func TestInstallationSignerSignRefuses(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x22}, ed25519.SeedSize))
	s := &installationSigner{pub: priv.Public().(ed25519.PublicKey), priv: priv}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sig, err := s.Sign(ctx, []byte("fake message")); sig != nil || !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Sign on a canceled context = (%x, %v), want KindCanceled", sig, err)
	}
	var none *installationSigner
	for _, empty := range []*installationSigner{none, {}} {
		if sig, err := empty.Sign(context.Background(), []byte("fake message")); sig != nil || !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("Sign with no key = (%x, %v), want KindUnavailable", sig, err)
		}
	}
	if none.PublicKey() != nil {
		t.Fatal("a nil signer returned a public key")
	}
}

// testEnvelope is a minimal valid root envelope issued now.
func testEnvelope() scopeenvelope.Envelope {
	now := time.Now().UTC()
	return scopeenvelope.Envelope{
		Version: scopeenvelope.Version, ID: "env-fake-1", Principal: "principal-fake",
		Roots: []string{"/srv/fake"}, Tools: []string{"read"}, Commands: []string{"go"}, Egress: []string{"model"},
		Grants: []scopeenvelope.GrantRef{}, Budget: scopeenvelope.Budget{WallClock: time.Hour, Tokens: 10, Procs: 1},
		Fence:       scopeenvelope.Fence{DispatchID: "d-fake", Attempt: 1, LeaseEpoch: 1},
		Sensitivity: provider.SensitivityRestricted, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
}

// nodeKeySigner signs with a node identity key generated in the test.
type nodeKeySigner struct{ priv ed25519.PrivateKey }

func (n nodeKeySigner) PublicKey() ed25519.PublicKey { return n.priv.Public().(ed25519.PublicKey) }
func (n nodeKeySigner) Sign(_ context.Context, msg []byte) ([]byte, error) {
	return ed25519.Sign(n.priv, msg), nil
}
