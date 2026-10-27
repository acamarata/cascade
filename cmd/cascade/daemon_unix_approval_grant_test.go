//go:build !windows

// Purpose: approval.grant end to end on the daemon: an attested tokenless
//
//	grant redeems the daemon-held signed record once (over the registry and
//	from the real CLI command over a socket), and a crash between redeem and
//	confirm leaves an unknown-outcome effect that refuses every later grant.
//
// Inputs: a fresh HOME / CASCADE_HOME, an injected elevation custody, the
//
//	file vault forced by TestMain; the crash leg re-runs this test binary.
//
// SPORT: cmd/cascade approval grant tests (P1-SEC-23).

package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/providers/sqlite"
)

func TestDaemonApprovalGrantRoundTrip(t *testing.T) {
	t.Run("approved_state_injected", func(t *testing.T) {
		d := newApprovalDaemon(t, nil)
		ctx := context.Background()
		res := d.injectApprovedState(t)
		var out policy.ApprovalGrantResult
		if err := d.Call(ctx, "approval.grant", grantParams(res.RequestID), &out); err != nil || out.RequestID != res.RequestID {
			t.Fatalf("attested tokenless grant = %+v, %v", out, err)
		}
		err := d.Call(ctx, "approval.grant", grantParams(res.RequestID), nil)
		if err == nil || !strings.Contains(err.Error(), "redeem") {
			t.Fatalf("second grant = %v, want a refusal", err)
		}
	})
}

// TestProductionDecideNeverApprovesSoGrantRefuses: through the daemon's own
// verbs a queued entry is never approved (the only production Decide,
// approval.deny, denies), so a tokenless grant refuses before and after the
// deny and spends nothing. The approve step is a later ticket.
func TestProductionDecideNeverApprovesSoGrantRefuses(t *testing.T) {
	d := newApprovalDaemon(t, nil)
	ctx := context.Background()
	res, err := d.enqueueAsk(ctx)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := d.Call(ctx, "approval.grant", grantParams(res.RequestID), nil); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("grant of a queued entry = %v, want the not-approved refusal", err)
	}
	if err := d.Call(ctx, "approval.deny", policy.ApprovalDecisionParams{RequestID: res.RequestID}, nil); err != nil {
		t.Fatalf("approval.deny: %v", err)
	}
	if err := d.Call(ctx, "approval.grant", grantParams(res.RequestID), nil); err == nil {
		t.Fatal("grant of a denied entry succeeded")
	}
}

func TestApprovalGrantCLIWithoutTokenSignsAndRedeems(t *testing.T) {
	t.Run("approved_state_injected", testApprovalGrantCLIInjected)
}

func testApprovalGrantCLIInjected(t *testing.T) {
	d := newApprovalDaemon(t, nil)
	res := d.injectApprovedState(t)
	socket := serveRegistry(t, d.Registry)
	previous := approvalGrantCallDeps
	approvalGrantCallDeps = func(stderr io.Writer) elevatedCallDeps {
		return elevatedCallDeps{Custody: testCustody(t, d.KS, elevation.CustodyPlatform), Clock: runtime.NewSystemClock(),
			Getenv: func(string) string { return "" }, Stderr: stderr}
	}
	t.Cleanup(func() { approvalGrantCallDeps = previous })
	deps := hermeticApprovalDeps(t)
	deps.DialContext = redirectDial(client.UnixDialer, socket)
	out, err := execLeaf(t, newApprovalGrantCmd(deps), []string{res.RequestID, "--action", "fake action"})
	if err != nil || !strings.Contains(out, res.RequestID) {
		t.Fatalf("approval grant <id> (no --token) = %q, %v", out, err)
	}
	if _, err := execLeaf(t, newApprovalGrantCmd(deps), []string{res.RequestID, "--action", "fake action"}); err == nil {
		t.Fatal("a second CLI grant of the same request succeeded")
	}
}

// serveRegistry serves reg over a fresh unix socket and returns its path.
func serveRegistry(t *testing.T, reg *rpc.Registry) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "c") // unix socket paths are length-capped
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "rpc.sock")
	l, err := runtime.ListenOwnerSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(rpc.NewHandler(reg))
	_ = s.Listener.Close()
	s.Listener = l
	s.Config.ConnContext = rpc.ConnContext
	s.Start()
	t.Cleanup(s.Close)
	return path
}

// redirectDial dials path whatever socket the caller names. Generic so this
// unit test names no net type (the no-network unit-lane gate).
func redirectDial[C any](dial func(context.Context, string) (C, error), path string) func(context.Context, string) (C, error) {
	return func(ctx context.Context, _ string) (C, error) { return dial(ctx, path) }
}

// --- crash between redeem and confirm (P1-SEC-33 recipe) -------------------

const grantCrashEnv = "CASCADE_TEST_GRANT_CRASH_DIR"

// crashQueue records the consume, then dies before ConfirmEffect runs.
type crashQueue struct {
	policy.ApprovalQueue
	dir   string
	crash bool
}

func (q crashQueue) ConsumeToken(_ context.Context, req policy.ConsumeRequest) (policy.ConsumeResult, error) {
	f, err := os.OpenFile(filepath.Join(q.dir, "consumed.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.WriteString(req.RequestID + "\n")
		_ = f.Close()
	}
	if q.crash {
		os.Exit(3)
	}
	return policy.ConsumeResult{RequestID: req.RequestID, Capability: "fake.cap", Level: policy.L2}, nil
}

// heldFile serves the held record a parent wrote to disk.
type heldFile struct{ signed []byte }

func (h heldFile) HeldSignedRecord(string) ([]byte, error)   { return h.signed, nil }
func (heldFile) CheckRedeemable(policy.ConsumeRequest) error { return nil }

// crashRegistry is approval.grant over the store at dir/cascade.db, behind
// a real elevation middleware trusting key, with keys read from dir.
func crashRegistry(t *testing.T, dir string, crash bool) (*rpc.Registry, func(ctx context.Context, method string, params, out any) error, *audit.EffectLog) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := runtime.NewSystemClock()
	log := audit.New(store, clock, nil)
	effects, err := openGrantEffects(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	approvalPub, _ := os.ReadFile(filepath.Join(dir, "approval.pub")) //nolint:gosec // test temp dir
	signed, _ := os.ReadFile(filepath.Join(dir, "record.bin"))        //nolint:gosec // test temp dir
	elevSeed, _ := os.ReadFile(filepath.Join(dir, "elevation.seed"))  //nolint:gosec // test temp dir
	verifier, err := policy.NewApprovalVerifier(approvalPub, clock)
	if err != nil {
		t.Fatal(err)
	}
	priv := ed25519.NewKeyFromSeed(elevSeed)
	ks := signingKeystore{dir: dir, priv: priv, pub: priv.Public().(ed25519.PublicKey)}
	pubB64, _ := ks.PubKeyB64()
	fp, _ := elevation.Fingerprint(pubB64)
	issuer := rpc.NewWitnessIssuer()
	reg := rpc.NewRegistry()
	reg.Use(rpc.ElevationMiddleware(rpc.ElevationDeps{Ledger: rpc.NewNonceLedger(clock), Trust: rpc.MapTrustStore{fp: ks.pub},
		Clock: clock, Audit: rpc.LocalCeremonyAudit{}, Issuer: issuer, Custody: func() (string, bool) { return "platform", true }}))
	deps := policy.RPCDeps{Queue: crashQueue{dir: dir, crash: crash}, Clock: clock, Verifier: verifier,
		Attestor: policy.WitnessAttestor(issuer.Verifier()), Held: heldFile{signed: signed}, Effects: effects,
		AuditVerify: log, Recorder: log}
	reg.Register("approval.grant", rpc.HandlerFunc(policy.MethodHandlers(deps)["approval.grant"]))
	return reg, elevatedCaller(reg, ks, clock), effects
}

func TestGrantCrashBetweenRedeemAndConfirm(t *testing.T) {
	if dir := os.Getenv(grantCrashEnv); dir != "" {
		_, call, _ := crashRegistry(t, dir, true)
		id, _ := os.ReadFile(filepath.Join(dir, "request.id")) //nolint:gosec // test temp dir
		err := call(context.Background(), "approval.grant", grantParams(string(id)), nil)
		t.Fatalf("the child returned instead of crashing inside ConsumeToken: %v", err)
	}
	dir := t.TempDir()
	_, approvalPriv, _ := ed25519.GenerateKey(rand.Reader)
	clock := runtime.NewSystemClock()
	signer, _ := policy.NewApprovalSigner(staticKeySource{priv: approvalPriv}, clock)
	verifier, _ := policy.NewApprovalVerifier(approvalPriv.Public().(ed25519.PublicKey), clock)
	tok, err := signerMinter{signer: signer, verifier: verifier}.Mint(context.Background(), policy.ApprovalMintRequest{Level: policy.L2})
	if err != nil {
		t.Fatal(err)
	}
	elevSeed := make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(elevSeed)
	for name, v := range map[string][]byte{"approval.pub": approvalPriv.Public().(ed25519.PublicKey), "record.bin": tok.Signed, "elevation.seed": elevSeed, "request.id": []byte(tok.RequestID)} {
		if err := os.WriteFile(filepath.Join(dir, name), v, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGrantCrashBetweenRedeemAndConfirm$", "-test.count=1") //nolint:gosec // re-runs this test binary
	cmd.Env = append(os.Environ(), grantCrashEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("child = %v, want exit 3 inside ConsumeToken; output:\n%s", err, out)
	}
	_, call, effects := crashRegistry(t, dir, false) // recovery runs in openGrantEffects
	if phase, found, err := effects.EffectState(context.Background(), policy.GrantEffectKey(tok.RequestID)); err != nil || !found || phase != audit.EffectUnknown {
		t.Fatalf("effect after crash and recovery = %q %v %v, want unknown-outcome", phase, found, err)
	}
	err = call(context.Background(), "approval.grant", grantParams(tok.RequestID), nil)
	if err == nil || !strings.Contains(err.Error(), "redeem") {
		t.Fatalf("grant after the crash = %v, want a refusal", err)
	}
	if n := countLines(t, filepath.Join(dir, "consumed.log")); n != 1 {
		t.Fatalf("ConsumeToken ran %d times, want once", n)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	n := 0
	for s := bufio.NewScanner(f); s.Scan(); {
		n++
	}
	return n
}
