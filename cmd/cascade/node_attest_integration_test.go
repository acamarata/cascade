//go:build !windows

// Purpose: end-to-end coverage of contract:node-attest-sign from the CLI
//
//	composition root: a node enrolled through admitNode and bound with
//	`node bind` answers ci.attest.sign only for a request signed by the
//	controller installation key it pinned (R21b B-2), and node serve mounts
//	the leg only when an AttestSigner is composed. Untagged (R18 m5).
//
// SPORT: cmd/cascade/node serve ci.attest.sign (ADD tests, P1-CI-13).
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// passExecutor is a ci.Executor that reports success for every step; the
// run, its rows and the signing path around it are the real ones.
type passExecutor struct{}

func (passExecutor) Run(context.Context, ci.ExecRequest) ci.ExecResult { return ci.ExecResult{} }

// gitCheckout makes a one-commit repo under t.TempDir() and returns its root,
// HEAD commit and tree.
func gitCheckout(t *testing.T) (root, commit, tree string) {
	t.Helper()
	root = t.TempDir()
	git := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = root
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "main.go")
	git("commit", "-q", "-m", "initial")
	return root, git("rev-parse", "HEAD"), git("rev-parse", "HEAD^{tree}")
}

func TestAttestSignSucceedsAfterRealEnrollment(t *testing.T) {
	e := enrollRealNode(t)
	if _, err := bindOnNode(t, e, allowingGate(t, nil)); err != nil {
		t.Fatalf("bind: %v", err)
	}
	ctx := context.Background()
	pub, err := installationPublicKey(ctx, e.ctrl.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if e.binding.ControllerPubKeyB64 != base64.StdEncoding.EncodeToString(pub) {
		t.Fatal("controller_pubkey_b64 is not installationPublicKey (R21b B-2)")
	}
	clock := e.nodeSrv.Clock
	attestor, closeStore, err := ci.OpenNodeAttestor(ctx, e.node.self, e.node.keystore, e.node.dataDir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeStore() }()
	repo, commit, tree := gitCheckout(t)
	cfg := ci.RunnerConfig{RepoRoot: repo, Steps: []ci.RunnerStep{{Kind: ci.StepLint, Command: "lint"}, {Kind: ci.StepTest, Command: "test"}}}
	if _, err := attestor.Execute(ctx, ci.Deps{Clock: clock, Exec: passExecutor{}}, cfg, "exec-1", -21, 7, "repo"); err != nil {
		t.Fatalf("ci.Execute on the node store: %v", err)
	}
	reg := nodes.NewServeRegistry(nodes.ServeDeps{
		Records: e.node.recordStore, KnownHosts: e.node.knownHosts, Keystore: e.node.keystore, Self: e.node.self,
		Sequences: nodes.NewSequenceStore(), Clock: clock, Timeout: nodes.DefaultHeartbeatTimeout,
		AttestSigner: attestor.Sign, ControllerBinding: nodes.NewFileControllerBindingBackend(e.node.dataDir),
	})
	signer, err := productionEnvelopeSigner(ctx, e.ctrl.Paths)
	if err != nil {
		t.Fatal(err)
	}
	installSign := func(p []byte) ([]byte, error) { return signer.Sign(ctx, p) }
	req := attestRequest(clock.Now(), "exec-1")
	sa, err := ci.RequestNodeSignature(ctx, reg.Call, installSign, e.node.self.NodeID, req)
	if err != nil {
		t.Fatalf("installation-signed ci.attest.sign = %v", err)
	}
	records, err := openRecordStore(e.ctrl)
	if err != nil {
		t.Fatal(err)
	}
	if err := attest.VerifySignature(ctx, sa, commit, tree, attest.NewRecordTrustStore(records), clock.Now()); err != nil {
		t.Fatalf("controller rejects the node's record: %v", err)
	}
	assertSignRefusals(ctx, t, e, reg)
}

// assertSignRefusals checks ci.attest.sign refuses requests not signed by the pinned installation key.
func assertSignRefusals(ctx context.Context, t *testing.T, e enrolledNode, reg *nodes.ServeRegistry) {
	t.Helper()
	clock := e.nodeSrv.Clock
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	refusals := map[string]func([]byte) ([]byte, error){
		"another key": func(p []byte) ([]byte, error) { return ed25519.Sign(other, p), nil },
		"the controller NodeKeystore identity key": func(p []byte) ([]byte, error) {
			return e.ctrlCmp.keystore.Sign(ctx, e.ctrlCmp.self.NodeID, p)
		},
	}
	for label, sign := range refusals {
		_, err := ci.RequestNodeSignature(ctx, reg.Call, sign, e.node.self.NodeID, attestRequest(clock.Now(), "exec-1"))
		if !cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Fatalf("request signed by %s = %v, want PERMISSION_DENIED", label, err)
		}
	}
}

// attestRequest is an unsigned request for executionID.
func attestRequest(now time.Time, executionID string) nodes.AttestSignRequest {
	return nodes.AttestSignRequest{ExecutionID: executionID, Nonce: "nonce-1", ExpiresAt: now.Add(time.Hour).Truncate(time.Second),
		CheckpointID: "cp-1", RepoID: "repo-1", JobSpecHash: "jsh-1", PolicyVersion: "pv-1"}
}

func TestNodeServeMountsAttestSign(t *testing.T) {
	ctx := context.Background()
	deps := testNodeServeDeps(t)
	plain, err := composeNodeServe(ctx, deps)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := func(string) nodes.AttestSignRequest {
		req := attestRequest(deps.Clock.Now(), "exec-1")
		req.ControllerSig = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
		return req
	}
	var out json.RawMessage
	err = plain.registry.Call(ctx, nodes.AttestSignMethod, unsigned(plain.self.NodeID), &out)
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("ci.attest.sign without an AttestSigner = %v, want NOT_FOUND (unmounted)", err)
	}
	attested, closeAttest, err := composeNodeServeAttested(ctx, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAttest()
	err = attested.registry.Call(ctx, nodes.AttestSignMethod, unsigned(attested.self.NodeID), &out)
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("ci.attest.sign on an unbound node = %v, want PERMISSION_DENIED (mounted, no pin)", err)
	}
}
