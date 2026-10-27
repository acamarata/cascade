//go:build !windows

// Purpose: the daemon's approval key wiring: the signing minter hands the
//
//	queue the ids the signed record carries; an unreadable key leaves the
//	queue minting nothing, approval.grant refusing and status.get showing
//	"approval-keys" failed; the doctor's presence check writes nothing.
//
// Inputs: a fresh HOME / CASCADE_HOME per test, the file vault forced by
//
//	TestMain's custody guard, an injected elevation custody.
//
// SPORT: cmd/cascade approval-keys tests (P1-SEC-23).

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// approvalDaemon is a daemon registry with the policy wiring and the
// elevation middleware both mounted, over a fresh data directory.
type approvalDaemon struct {
	elevatedDaemon
	Pol *policyWiring
}

// newApprovalDaemon wires policy (approval key included) and builds the
// daemon registry with its handlers, the way composeDaemon orders them.
// seed, when non-nil, runs against the data directory before wiring.
func newApprovalDaemon(t *testing.T, seed func(dataDir string)) approvalDaemon {
	t.Helper()
	ks := newSigningKeystore(t)
	pub := useElevationCustody(t, ks)
	paths := auditTestPaths(t)
	enrollKey(t, paths.DataDir(), pub)
	if seed != nil {
		seed(paths.DataDir())
	}
	clock := runtime.NewSystemClock()
	store := storetest.NewMemStore()
	pol, err := wirePolicy(context.Background(), store, clock, nil, paths)
	if err != nil {
		t.Fatalf("wirePolicy: %v", err)
	}
	reg, _, _, _, err := buildDaemonRegistry(events.New(store, clock), clock, nil, daemon.Settings{}, paths, nil, store,
		withPolicyHandlers(pol), withDaemonRuntime(nil, nil, pol, nil, daemonDeps{}))
	if err != nil {
		t.Fatalf("buildDaemonRegistry: %v", err)
	}
	d := elevatedDaemon{Registry: reg, Store: store, KS: ks, DataDir: paths.DataDir(), Call: elevatedCaller(reg, ks, clock)}
	return approvalDaemon{elevatedDaemon: d, Pol: pol}
}

// enqueueAsk queues one L2 approval against a boot capability.
func (d approvalDaemon) enqueueAsk(ctx context.Context) (policy.EnqueueResult, error) {
	return d.Pol.Queue.Enqueue(ctx, policy.EnqueueRequest{
		Subject:    policy.Subject{Kind: policy.SubjectAgent, ID: "fake-agent"},
		Capability: bootCapabilities()[0].Name, Level: policy.L2,
		Action: "fake action", Summary: "fake action summary",
	})
}

// injectApprovedState queues one approval and records the approval by
// calling the queue's Decide directly. No production verb approves an entry
// yet (the only production Decide, approval.deny, denies; the owner's
// approve step is a later ticket), so the tests that redeem inject the
// approved state here and say so in their subtest name.
func (d approvalDaemon) injectApprovedState(t *testing.T) policy.EnqueueResult {
	t.Helper()
	ctx := context.Background()
	res, err := d.enqueueAsk(ctx)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	out, err := d.Pol.Queue.Decide(ctx, []policy.DecisionRequest{{
		RequestID: res.RequestID, Approved: true, PresentedSummary: res.Summary, PresentedLevel: res.Level,
	}})
	if err != nil || len(out) != 1 || out[0].Err != nil {
		t.Fatalf("Decide: %v %+v", err, out)
	}
	return res
}

// grantParams is the tokenless grant for the enqueueAsk action.
func grantParams(id string) policy.ApprovalGrantParams {
	return policy.ApprovalGrantParams{RequestID: id, Action: "fake action"}
}

// signedGrantParams is the signed-token grant for the enqueueAsk action,
// carrying the entry's held record as the caller's token.
func (d approvalDaemon) signedGrantParams(t *testing.T, id string) policy.ApprovalGrantParams {
	t.Helper()
	raw, err := d.Pol.rpcDeps.Held.HeldSignedRecord(id)
	if err != nil {
		t.Fatalf("held record of %s: %v", id, err)
	}
	p := grantParams(id)
	p.SignedToken = base64.StdEncoding.EncodeToString(raw)
	return p
}

// seedVaultValue writes a reserved entry straight into the daemon's file
// vault custody (Broker.Set refuses the name).
func seedVaultValue(t *testing.T, dataDir, name string, value []byte) {
	t.Helper()
	c, err := secrets.SelectCustody(secrets.Config{Service: vaultService, Dir: dataDir, ForceFileVault: true, Runner: refusingRunner})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Set(context.Background(), name, value); err != nil {
		t.Fatal(err)
	}
}

func TestSignerMinterTokenIDsMatchSignedRecord(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	clock := runtime.NewSystemClock()
	signer, _ := policy.NewApprovalSigner(staticKeySource{priv: priv}, clock)
	verifier, _ := policy.NewApprovalVerifier(priv.Public().(ed25519.PublicKey), clock)
	m := signerMinter{signer: signer, verifier: verifier}
	req := policy.ApprovalMintRequest{Verb: "fake.verb", ActionHash: "fake-action-hash", ParamsHash: "fake-params-hash", Level: policy.L3}
	tok, err := m.Mint(context.Background(), req)
	if err != nil || len(tok.Signed) == 0 {
		t.Fatalf("Mint = %+v, %v; want a signed token", tok, err)
	}
	rec, err := verifier.Verify(tok.Signed)
	if err != nil {
		t.Fatalf("the minted record does not verify: %v", err)
	}
	if rec.RequestID.String() != tok.RequestID || rec.Nonce.String() != tok.Nonce || !rec.Expiry.Equal(tok.Expires) ||
		!rec.Issued.Equal(tok.Issued) || rec.Verb != req.Verb || rec.ParamsDigest != req.ParamsHash || rec.Risk != policy.L3 {
		t.Fatalf("token %+v does not match its signed record %+v", tok, rec)
	}
	if raw, _ := json.Marshal(tok); strings.Contains(string(raw), "signed") {
		t.Fatalf("the token serialises its signed record: %s", raw)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherVerifier, _ := policy.NewApprovalVerifier(other.Public().(ed25519.PublicKey), clock)
	if _, err := otherVerifier.Verify(tok.Signed); err == nil {
		t.Fatal("another key's verifier accepted the record")
	}
}

// refusingRunner stands in for the platform keychain tool: it refuses, so
// no test reaches the operator's credential store.
func refusingRunner(context.Context, string, ...string) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnavailable, "fake keychain: unavailable in tests")
}

// staticKeySource hands a copy of a fixed test key.
type staticKeySource struct{ priv ed25519.PrivateKey }

func (k staticKeySource) ApprovalKey(context.Context) (string, ed25519.PrivateKey, error) {
	return "fake-key-id", append(ed25519.PrivateKey(nil), k.priv...), nil
}

func TestApprovalKeyUnreadableKeepsGrantRefusing(t *testing.T) {
	d := newApprovalDaemon(t, func(dataDir string) {
		seedVaultValue(t, dataDir, secrets.ApprovalKeyName, []byte("fake-malformed-approval-key"))
	})
	ctx := context.Background()
	if _, err := d.enqueueAsk(ctx); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Enqueue with an unreadable key = %v, want the refusing minter (no unsigned fallback)", err)
	}
	var status daemon.StatusResponse
	if err := d.Call(ctx, "status.get", nil, &status); err != nil {
		t.Fatalf("status.get: %v", err)
	}
	found := false
	for _, s := range status.Subsystems {
		if s.Name == approvalKeysSubsystem {
			found = s.State == daemon.SubsystemError && strings.Contains(s.Detail, "ed25519")
		}
	}
	if !found {
		t.Fatalf("status.get subsystems = %+v, want approval-keys failed with the reason", status.Subsystems)
	}
	err := d.Call(ctx, "approval.grant", grantParams("fake-request"), nil)
	if err == nil || !strings.Contains(err.Error(), "verifier") {
		t.Fatalf("approval.grant with no key = %v, want the no-verifier refusal", err)
	}
}

func TestApprovalKeyStatusReadOnly(t *testing.T) {
	paths := auditTestPaths(t)
	previous := approvalKeyStatusCustody
	approvalKeyStatusCustody = func(cfg secrets.Config) (secrets.Custody, error) {
		cfg.ForceFileVault, cfg.Runner = true, refusingRunner
		return secrets.SelectCustodyReadOnly(cfg)
	}
	t.Cleanup(func() { approvalKeyStatusCustody = previous })
	if present, reason := approvalKeyStatus(context.Background(), paths); present || reason == "" {
		t.Fatalf("status before provisioning = %v %q, want absent with a reason", present, reason)
	}
	if m, _ := filepath.Glob(filepath.Join(paths.DataDir(), "vault.*")); len(m) != 0 {
		t.Fatalf("the doctor check created vault files: %v", m)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	mismatched := append(append([]byte(nil), priv[:ed25519.SeedSize]...), make([]byte, ed25519.PublicKeySize)...)
	seedVaultValue(t, paths.DataDir(), secrets.ApprovalKeyName, mismatched)
	if present, reason := approvalKeyStatus(context.Background(), paths); present || !strings.Contains(reason, "consistent") {
		t.Fatalf("status with mismatched halves = %v %q, want absent (the daemon refuses it)", present, reason)
	}
	seedVaultValue(t, paths.DataDir(), secrets.ApprovalKeyName, priv)
	if present, reason := approvalKeyStatus(context.Background(), paths); !present {
		t.Fatalf("status after seeding = absent: %s", reason)
	}
}

func TestApprovalKeyLockPathScopedToCustody(t *testing.T) {
	home := t.TempDir()
	previous := userLockRoot
	userLockRoot = home
	t.Cleanup(func() { userLockRoot = previous })
	t.Setenv("HOME", filepath.Join(home, "home-one"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache-one"))
	a, b := filepath.Join(home, "data-a"), filepath.Join(home, "data-b")
	fa, err1 := approvalKeyLockPath(a, fileVaultBackend)
	fb, err2 := approvalKeyLockPath(b, fileVaultBackend)
	if err1 != nil || err2 != nil || filepath.Dir(fa) != a || filepath.Dir(fb) != b {
		t.Fatalf("file-vault locks = %q, %q (%v %v); want one per data directory", fa, fb, err1, err2)
	}
	pa, err1 := approvalKeyLockPath(a, "fake-platform-keystore")
	// A second process of the same user with a different environment.
	t.Setenv("HOME", filepath.Join(home, "home-two"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache-two"))
	pb, err2 := approvalKeyLockPath(b, "fake-platform-keystore")
	other, err3 := approvalKeyLockPath(a, "another-fake-keystore")
	if err1 != nil || err2 != nil || err3 != nil || pa != pb || pa == other ||
		strings.HasPrefix(pa, a) || !strings.HasPrefix(pa, home) {
		t.Fatalf("platform locks = %q, %q, other %q; want one per user and keystore, outside any data directory", pa, pb, other)
	}
}

func TestUserLockDirRefusesForeignOrLooseDirectory(t *testing.T) {
	root := t.TempDir()
	loose := filepath.Join(root, fmt.Sprintf("cascade-%d", os.Getuid()))
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := userLockDir(root, os.Getuid()); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("a 0755 lock directory = %v, want a refusal", err)
	}
	if _, err := userLockDir(root, os.Getuid()+1); err == nil {
		t.Fatal("a lock directory owned by another uid was accepted")
	}
}
