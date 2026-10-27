//go:build !windows

// Purpose: approval.grant end to end on the daemon: a signed-token grant
//
//	redeems an approved entry once over the registry, and a crash between
//	the held-record decision and its confirm leaves an unknown-outcome
//	effect that refuses every later grant.
//
// Inputs: a fresh HOME / CASCADE_HOME, an injected elevation custody, the
//
//	file vault forced by TestMain; the crash leg re-runs this test binary.
//
// SPORT: cmd/cascade approval grant tests (P1-SEC-23, P1-SEC-41).

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
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
		params := d.signedGrantParams(t, res.RequestID)
		var out policy.ApprovalGrantResult
		if err := d.Call(ctx, "approval.grant", params, &out); err != nil || out.RequestID != res.RequestID {
			t.Fatalf("attested signed-token grant = %+v, %v", out, err)
		}
		err := d.Call(ctx, "approval.grant", params, nil)
		if err == nil || !strings.Contains(err.Error(), "redeem") {
			t.Fatalf("second grant = %v, want a refusal", err)
		}
	})
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

// --- crash between decision and confirm (P1-SEC-33 recipe) ---------------

const heldCrashEnv = "CASCADE_TEST_HELD_GRANT_CRASH_DIR"

// crashDecideQueue logs each Decide and ConsumeToken; with crash set it dies
// inside Decide, after the effect intent and before its confirm.
type crashDecideQueue struct {
	policy.ApprovalQueue
	dir   string
	crash bool
}

func (q crashDecideQueue) Decide(_ context.Context, reqs []policy.DecisionRequest) ([]policy.DecisionOutcome, error) {
	appendLine(q.dir, "decided.log", reqs[0].RequestID)
	if q.crash {
		os.Exit(3)
	}
	return []policy.DecisionOutcome{{RequestID: reqs[0].RequestID, State: policy.ApprovalApproved}}, nil
}

func (q crashDecideQueue) ConsumeToken(_ context.Context, req policy.ConsumeRequest) (policy.ConsumeResult, error) {
	appendLine(q.dir, "consumed.log", req.RequestID)
	return policy.ConsumeResult{}, nil
}

// heldRecordFile serves the held record a parent wrote to disk; pendingView
// reports that request pending.
type heldRecordFile struct{ signed []byte }

func (h heldRecordFile) HeldSignedRecord(string) ([]byte, error) { return h.signed, nil }

type pendingView struct{}

func (pendingView) EntryView(_ context.Context, id string) (policy.ApprovalEntryView, error) {
	return policy.ApprovalEntryView{RequestID: id, Summary: "fake summary", Level: policy.L2, State: policy.ApprovalPending}, nil
}

func appendLine(dir, name, line string) {
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // test temp dir
	if err == nil {
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
}

func logLines(t *testing.T, dir, name string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // test temp dir
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

// heldCrashCaller is the guarded approval.grant over dir/cascade.db behind
// the real elevation middleware, with the logging queue above.
func heldCrashCaller(t *testing.T, dir string, crash bool) (func(context.Context, string, any, any) error, *audit.EffectLog) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := runtime.NewSystemClock()
	log := audit.New(store, clock, nil)
	effects, err := openGrantEffects(ctx, log) // recovery runs here
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) []byte {
		b, _ := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // test temp dir
		return b
	}
	verifier, err := policy.NewApprovalVerifier(read("approval.pub"), clock)
	if err != nil {
		t.Fatal(err)
	}
	priv := ed25519.NewKeyFromSeed(read("elevation.seed"))
	ks := signingKeystore{dir: dir, priv: priv, pub: priv.Public().(ed25519.PublicKey)}
	pubB64, _ := ks.PubKeyB64()
	fp, _ := elevation.Fingerprint(pubB64)
	issuer := rpc.NewWitnessIssuer()
	reg := rpc.NewRegistry()
	reg.Use(rpc.ElevationMiddleware(rpc.ElevationDeps{Ledger: rpc.NewNonceLedger(clock), Trust: rpc.MapTrustStore{fp: ks.pub},
		Clock: clock, Audit: rpc.LocalCeremonyAudit{}, Issuer: issuer, Custody: func() (string, bool) { return "platform", true }}))
	deps := policy.RPCDeps{Queue: crashDecideQueue{dir: dir, crash: crash}, Clock: clock, Verifier: verifier,
		Attestor: policy.WitnessAttestor(issuer.Verifier()), Held: heldRecordFile{signed: read("record.bin")},
		Entries: pendingView{}, Effects: effects, AuditVerify: log, Recorder: log}
	reg.Register("approval.grant", rpc.HandlerFunc(policy.MethodHandlers(deps)["approval.grant"]))
	return elevatedCaller(reg, ks, clock), effects
}

func TestGrantCrashBetweenDecideAndConfirm(t *testing.T) {
	if dir := os.Getenv(heldCrashEnv); dir != "" {
		call, _ := heldCrashCaller(t, dir, true)
		err := call(context.Background(), "approval.grant", policy.ApprovalGrantParams{RequestID: string(readFile(t, dir, "request.id"))}, nil)
		t.Fatalf("the child returned instead of crashing inside Decide: %v", err)
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
	for name, v := range map[string][]byte{"approval.pub": approvalPriv.Public().(ed25519.PublicKey), "record.bin": tok.Signed,
		"elevation.seed": elevSeed, "request.id": []byte(tok.RequestID)} {
		if err := os.WriteFile(filepath.Join(dir, name), v, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGrantCrashBetweenDecideAndConfirm$", "-test.count=1") //nolint:gosec // re-runs this test binary
	cmd.Env = append(os.Environ(), heldCrashEnv+"="+dir)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 || logLines(t, dir, "decided.log") != 1 {
		t.Fatalf("child = %v, want exit 3 inside Decide; output:\n%s", err, out)
	}
	call, effects := heldCrashCaller(t, dir, false)
	if phase, found, err := effects.EffectState(context.Background(), policy.GrantEffectKey(tok.RequestID)); err != nil || !found || phase != audit.EffectUnknown {
		t.Fatalf("effect after crash and recovery = %q %v %v, want unknown-outcome", phase, found, err)
	}
	err = call(context.Background(), "approval.grant", policy.ApprovalGrantParams{RequestID: tok.RequestID}, nil)
	if err == nil || !strings.Contains(err.Error(), "already granted") {
		t.Fatalf("grant after the crash = %v, want the replay refusal", err)
	}
	if logLines(t, dir, "decided.log") != 1 || logLines(t, dir, "consumed.log") != 0 {
		t.Fatalf("Decide ran %d times and ConsumeToken %d; want once and never",
			logLines(t, dir, "decided.log"), logLines(t, dir, "consumed.log"))
	}
}

func readFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatal(err)
	}
	return b
}
