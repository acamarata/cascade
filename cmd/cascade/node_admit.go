// Purpose: `cascade node enroll <user@host>` ⚠ (S-36.T4's admission
//
//	verb), plus the CLI-side reachability/host-key-pinning dial that
//	precedes it.
//
// Inputs: cobra args/flags (--trust-tier, --host-key-fingerprint) and the
//
//	NODE-supplied half of the enrollment handshake payload, read from
//	stdin (or --payload-file) as JSON {node_id, node_pubkey_b64,
//	node_signature_b64}.
//
// Outputs: a persisted DeviceRecord, rendered via internal/output; a
//
//	typed error on any refusal, written before any ssh dial for the
//	--trust-tier check and before enrollment commits for every other
//	check.
//
// Constraints: REMOTE PAYLOAD SOURCE (CONTRADICTION — full quote in the
//
//	ticket journal). The contract's HOW section describes enrollment as
//	reaching "from the enrolled node's device record... over ssh," which
//	implies the controller can pull the node's identity+signature
//	directly off the wire. internal/nodes' Session interface (tunnel.go,
//	S-36.T3) exposes only ListenUnix (a REMOTE listen the node's OUTBOUND
//	traffic forwards through, the heartbeat direction) — it has no method
//	to dial INTO a remote unix socket over the session, and tunnel.go is
//	outside this ticket's files_scope to extend. This command therefore
//	uses the real ssh Dialer/HostKeyVerifier ONLY to prove reachability
//	and pin/verify the host key (R-21.220's actual requirement), and
//	takes the node's own payload half out-of-band (stdin/--payload-file)
//	rather than fetching it — a real, supported enrollment shape (the
//	node prints its own payload via a local command and the operator
//	pipes it in), not a stub.
//
// SPORT: cmd/cascade/node enroll/ADDED (P1-E17-W4-S36-T4).
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/nodes"
	cruntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// nodeAdmitPayload is the node-supplied half of the enrollment handshake,
// read from stdin/--payload-file. Field names match EnrollPayload's own
// JSON tags exactly, so a node's own future payload-producing command can
// emit EnrollPayload's JSON directly and this decoder still round-trips
// it (extra host-side fields are ignored, not rejected, by design).
type nodeAdmitPayload struct {
	NodeID           string `json:"node_id"`
	NodePubKeyB64    string `json:"node_pubkey_b64"`
	NodeSignatureB64 string `json:"node_signature_b64"`
}

func decodeNodeAdmitPayload(raw []byte) (nodeAdmitPayload, error) {
	var p nodeAdmitPayload
	if len(raw) == 0 {
		return p, cascade.New(cascade.KindInvalidInput, "node enroll: no node payload on stdin (pipe the output of the node's own identity command, or use --payload-file)")
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, cascade.Wrap(cascade.KindInvalidInput, err, "node enroll: node payload is not valid JSON")
	}
	if p.NodeID == "" || p.NodePubKeyB64 == "" || p.NodeSignatureB64 == "" {
		return p, cascade.New(cascade.KindInvalidInput, "node enroll: node payload missing node_id, node_pubkey_b64 or node_signature_b64")
	}
	return p, nil
}

// splitUserHost parses "user@host[:port]" (the enroll positional arg's
// documented shape), defaulting the port to 22.
func splitUserHost(arg string) (user, addr string, err error) {
	at := strings.IndexByte(arg, '@')
	if at <= 0 || at == len(arg)-1 {
		return "", "", cascade.Newf(cascade.KindInvalidInput, "node enroll: %q is not user@host", arg)
	}
	user, host := arg[:at], arg[at+1:]
	if _, _, splitErr := net.SplitHostPort(host); splitErr != nil {
		host = net.JoinHostPort(host, strconv.Itoa(22))
	}
	return user, host, nil
}

// newNodeAdmitCmd builds `cascade node enroll <user@host>` ⚠.
func newNodeAdmitCmd(deps nodeCLIDeps) *cobra.Command {
	var trustTier, hostKeyOverride, payloadFile string
	cmd := &cobra.Command{
		Use:         "enroll USER@HOST",
		Short:       "Admit a node into the fleet over ssh (elevated)",
		Args:        usageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNodeAdmit(cmd, deps, args[0], trustTier, hostKeyOverride, payloadFile)
		},
	}
	cmd.Flags().StringVar(&trustTier, "trust-tier", "", "required: worker-trusted or controller")
	cmd.Flags().StringVar(&hostKeyOverride, "host-key-fingerprint", "", "out-of-band sha256 ssh host-key fingerprint override")
	cmd.Flags().StringVar(&payloadFile, "payload-file", "", "read the node's enrollment payload from this file instead of stdin")
	return cmd
}

// nodeAdmitComposition is runNodeAdmit's built collaborators, split out
// so runNodeAdmit itself stays under the 50-line cap (mirrors
// nodeServeComposition's identical precedent in node_serve.go).
type nodeAdmitComposition struct {
	dataDir    string
	keystore   *nodes.NodeKeystore
	self       nodes.Identity
	knownHosts *nodes.KnownHosts
}

func composeNodeAdmit(ctx context.Context, deps nodeCLIDeps) (nodeAdmitComposition, error) {
	dataDir := deps.Paths.DataDir()
	if dataDir == "" {
		return nodeAdmitComposition{}, cascade.New(cascade.KindUnavailable, "node enroll: could not resolve the data directory")
	}
	keystore, err := nodes.NewNodeKeystore(secrets.Config{Dir: deps.SecretsDir})
	if err != nil {
		return nodeAdmitComposition{}, err
	}
	self, err := nodes.EnsureLocalIdentity(ctx, nodes.NewFileSelfIdentityBackend(dataDir), keystore, rand.Reader)
	if err != nil {
		return nodeAdmitComposition{}, err
	}
	knownHosts := nodes.NewKnownHosts(nodes.NewFileKnownHostsBackend(dataDir))
	return nodeAdmitComposition{dataDir: dataDir, keystore: keystore, self: self, knownHosts: knownHosts}, nil
}

func runNodeAdmit(cmd *cobra.Command, deps nodeCLIDeps, hostArg, trustTier, hostKeyOverride, payloadFile string) error {
	if err := nodes.RefuseOnGOOS(deps.GOOS); err != nil {
		return err
	}
	// R-21.220: --trust-tier is REQUIRED and never defaulted. Checked
	// before elevation, before any ssh dial, and before any payload is
	// read: a missing tier writes no device record and touches nothing.
	if trustTier == "" {
		return cascade.New(cascade.KindInvalidInput, "node enroll: --trust-tier {worker-trusted|controller} is required (never defaulted)")
	}
	if err := deps.Gate.Authorize(cmd.Context(), "node.enroll"); err != nil {
		return err
	}
	user, addr, err := splitUserHost(hostArg)
	if err != nil {
		return err
	}
	comp, err := composeNodeAdmit(cmd.Context(), deps)
	if err != nil {
		return err
	}
	target := nodes.Target{NodeID: "enroll-probe", User: user, Addr: addr}
	observedFP, err := dialForHostKey(cmd.Context(), deps, comp.keystore, comp.self, comp.knownHosts, target, hostKeyOverride)
	if err != nil {
		return err
	}
	nodePayload, err := loadNodeAdmitPayload(cmd, payloadFile)
	if err != nil {
		return err
	}
	rec, err := admitNode(cmd.Context(), deps, comp, nodePayload, trustTier, user+"@"+addr, observedFP, hostKeyOverride)
	if err != nil {
		return err
	}
	return nodeOutputWriter(cmd).Result(newNodeRowView(rec, deps.Clock))
}

// admitNode builds the EnrollDeps/EnrollPayload and calls the real
// EnrollNode, split out of runNodeAdmit to keep it under the 50-line cap.
func admitNode(ctx context.Context, deps nodeCLIDeps, comp nodeAdmitComposition, nodePayload nodeAdmitPayload, trustTier, hostAddr, observedFP, hostKeyOverride string) (nodes.DeviceRecord, error) {
	edeps := nodes.EnrollDeps{
		Records:             nodes.NewRecordStore(nodes.NewFileRecordBackend(comp.dataDir), deps.Clock),
		KnownHosts:          comp.knownHosts,
		Keystore:            comp.keystore,
		ControllerNodeID:    comp.self.NodeID,
		ControllerPubKeyB64: comp.self.PubKeyB64(),
	}
	payload := nodes.EnrollPayload{
		NodeID: nodePayload.NodeID, NodePubKeyB64: nodePayload.NodePubKeyB64,
		TrustTier: trustTier, Host: hostAddr,
		HostKeyFingerprint: observedFP, HostKeyOverride: hostKeyOverride,
		NodeSignatureB64: nodePayload.NodeSignatureB64,
	}
	precondition := cruntime.ElevationPrecondition(deps.Gate.preconditions)
	return nodes.EnrollNode(ctx, edeps, precondition, payload)
}

// loadNodeAdmitPayload reads and decodes the node-supplied payload half,
// split out of runNodeAdmit to keep it under the 50-line function cap.
func loadNodeAdmitPayload(cmd *cobra.Command, payloadFile string) (nodeAdmitPayload, error) {
	raw, err := readNodeAdmitPayload(cmd, payloadFile)
	if err != nil {
		return nodeAdmitPayload{}, err
	}
	return decodeNodeAdmitPayload(raw)
}

// dialForHostKey performs the real ssh dial (or the injected test Dialer)
// solely to prove reachability and capture/pin the host key fingerprint;
// the session is closed immediately after (see package doc CONTRADICTIONS
// on why no RPC traverses it).
func dialForHostKey(ctx context.Context, deps nodeCLIDeps, keystore *nodes.NodeKeystore, self nodes.Identity, knownHosts *nodes.KnownHosts, target nodes.Target, override string) (string, error) {
	dialer := deps.Dialer
	if dialer == nil {
		dialer = nodes.NewSSHDialer(keystore, self.NodeID, self.PubKey, 15*time.Second)
	}
	var observed string
	verify := func(fp string) error {
		observed = fp
		return knownHosts.Verify(target.User+"@"+target.Addr, fp, override)
	}
	session, err := dialer.Dial(ctx, target, verify)
	if err != nil {
		return "", err
	}
	_ = session.Close()
	return observed, nil
}

func readNodeAdmitPayload(cmd *cobra.Command, payloadFile string) ([]byte, error) {
	if payloadFile != "" {
		return os.ReadFile(payloadFile) //nolint:gosec // operator-supplied path, not attacker-controlled
	}
	return io.ReadAll(io.LimitReader(cmd.InOrStdin(), 64*1024))
}
