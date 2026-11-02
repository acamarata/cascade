//go:build !windows

// Purpose: coverage for the controller-key binding handoff
//
//	(contract:controller-key-binding): enroll prints the binding, the
//	elevated `node bind` saves it, an unelevated bind writes nothing.
//
// SPORT: cmd/cascade/node bind (ADD tests, P1-CI-13).
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// jsonCmd is fakeCmdWithContext with --json on and stdout captured.
func jsonCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	cmd := fakeCmdWithContext(t)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	return cmd, &out
}

// enrolledNode is a node identity enrolled through the real admitNode path.
type enrolledNode struct {
	ctrl    nodeCLIDeps
	ctrlCmp nodeAdmitComposition
	binding nodes.ControllerBinding
	node    nodeServeComposition
	nodeSrv nodeServeDeps
}

// enrollRealNode enrolls the node's own identity (composed over its own data
// dir) on a controller through runNodeEnroll and returns the printed binding.
func enrollRealNode(t *testing.T) enrolledNode {
	t.Helper()
	isolateInstallationKey(t)
	ctx := context.Background()
	ctrl := testNodeCLIDeps(t, nil)
	ctrl.Gate = allowingGate(t, nil)
	ctrl.Dialer = fakeVerifyOnlyDialer{fingerprint: "fp-node"}
	ctrlCmp, err := composeNodeAdmit(ctx, ctrl)
	if err != nil {
		t.Fatal(err)
	}
	nodeSrv := testNodeServeDeps(t)
	node, err := composeNodeServe(ctx, nodeSrv)
	if err != nil {
		t.Fatal(err)
	}
	tr := nodes.Transcript{NodeID: node.self.NodeID, NodePubKeyB64: node.self.PubKeyB64(),
		ControllerPubKeyB64: ctrlCmp.self.PubKeyB64(), HostKeyFingerprint: "fp-node"}
	sig, err := node.keystore.Sign(ctx, node.self.NodeID, tr.SigningPayload())
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"node_id": node.self.NodeID,
		"node_pubkey_b64": node.self.PubKeyB64(), "node_signature_b64": base64.StdEncoding.EncodeToString(sig)})
	payloadFile := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(payloadFile, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, out := jsonCmd(t)
	a := nodeAdmitArgs{hostArg: "worker@host1", trustTier: "worker-trusted", hostKeyOverride: "fp-node", payloadFile: payloadFile}
	if err := runNodeEnroll(cmd, ctrl, a, "https://controller.example:7443"); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	var env struct {
		Data nodeEnrollView `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("decode enroll output %q: %v", out.String(), err)
	}
	return enrolledNode{ctrl: ctrl, ctrlCmp: ctrlCmp, binding: env.Data.Binding, node: node, nodeSrv: nodeSrv}
}

// writeBindingFile writes b as the file `node bind` reads.
func writeBindingFile(t *testing.T, b nodes.ControllerBinding) string {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "binding.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// bindOnNode runs `node bind` against the node's own data dir.
func bindOnNode(t *testing.T, e enrolledNode, gate *elevationGate) (string, error) {
	t.Helper()
	deps := testNodeCLIDeps(t, nil)
	deps.Paths = e.nodeSrv.Paths
	deps.Gate = gate
	cmd, out := jsonCmd(t)
	err := runNodeBind(cmd, deps, writeBindingFile(t, e.binding))
	return out.String(), err
}

func TestNodeEnrollPrintsBinding(t *testing.T) {
	e := enrollRealNode(t)
	b := e.binding
	if b.Endpoint != "https://controller.example:7443" || b.EnrollmentID == "" || b.ControllerPubKeyB64 == "" {
		t.Fatalf("enroll printed %+v, want endpoint, enrollment_id and controller_pubkey_b64", b)
	}
	pub, err := installationPublicKey(context.Background(), e.ctrl.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if b.ControllerPubKeyB64 != base64.StdEncoding.EncodeToString(pub) {
		t.Fatal("the printed key is not the installation public key")
	}
	if b.ControllerPubKeyB64 == e.ctrlCmp.self.PubKeyB64() {
		t.Fatal("the printed key is the controller's per-node identity key (R21b B-2)")
	}
	if _, err := os.Lstat(filepath.Join(e.ctrl.Paths.DataDir(), "nodes", "controller_binding.json")); !os.IsNotExist(err) {
		t.Fatalf("enroll wrote a binding: %v", err)
	}
}

func TestNodeEnrollRequiresControllerEndpoint(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	err := runNodeEnroll(fakeCmdWithContext(t), deps, nodeAdmitArgs{hostArg: "w@h", trustTier: "worker-trusted"}, " ")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("enroll without an endpoint = %v, want INVALID_INPUT", err)
	}
}

func TestNodeBindWritesControllerBinding(t *testing.T) {
	e := enrollRealNode(t)
	if _, err := bindOnNode(t, e, allowingGate(t, nil)); err != nil {
		t.Fatalf("bind: %v", err)
	}
	got, ok, err := nodes.NewFileControllerBindingBackend(e.nodeSrv.Paths.DataDir()).Load()
	if err != nil || !ok || got != e.binding {
		t.Fatalf("saved binding %+v ok=%v err=%v, want %+v", got, ok, err, e.binding)
	}
}

func TestNodeBindRefusedWithoutElevation(t *testing.T) {
	e := enrollRealNode(t)
	getenv := func(k string) string { return map[string]string{"CASCADE_NO_INPUT": "1"}[k] }
	gate := newElevationGate(nil, nil, nil, e.ctrl.Clock, getenv)
	_, err := bindOnNode(t, e, gate)
	if !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("unelevated bind = %v, want ELEVATION_REQUIRED", err)
	}
	if _, ok, lerr := nodes.NewFileControllerBindingBackend(e.nodeSrv.Paths.DataDir()).Load(); ok || lerr != nil {
		t.Fatalf("an unelevated bind wrote a binding (ok=%v err=%v)", ok, lerr)
	}
}

func TestNodeBindPrintsControllerKeyID(t *testing.T) {
	e := enrollRealNode(t)
	pub, err := nodes.ParsePublicKey(e.binding.ControllerPubKeyB64)
	if err != nil {
		t.Fatal(err)
	}
	keyID := nodes.KeyIDFor(pub)
	out, err := bindOnNode(t, e, allowingGate(t, nil))
	if err != nil || !strings.Contains(out, keyID) {
		t.Fatalf("bind output %q err=%v, want key id %s", out, err, keyID)
	}
	getenv := func(k string) string { return map[string]string{"CASCADE_NO_INPUT": "1"}[k] }
	e2 := enrollRealNode(t)
	_, err = bindOnNode(t, e2, newElevationGate(nil, nil, nil, e.ctrl.Clock, getenv))
	pub2, _ := nodes.ParsePublicKey(e2.binding.ControllerPubKeyB64)
	if err == nil || !strings.Contains(err.Error(), nodes.KeyIDFor(pub2)) {
		t.Fatalf("elevation refusal %v does not name the controller key id", err)
	}
}

func TestNodeBindRefusesMissingFile(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	if err := runNodeBind(fakeCmdWithContext(t), deps, ""); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("bind without --binding-file = %v", err)
	}
	deps.GOOS = "windows"
	if err := runNodeBind(fakeCmdWithContext(t), deps, "x"); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("bind on windows = %v", err)
	}
}
