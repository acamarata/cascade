// Purpose: `cascade node upgrade [--all] NODE_ID` ⚠ (S-36.T5's own verb,
//   mounted on the S-36.T4 `node` noun per that ticket's own contract
//   text: "upgrade arrives with S-36.T5"). Split into its own file for
//   the 300-line cap, matching node_admit.go/node_keys.go's identical
//   precedent.
// Inputs: cobra args/flags (--all, --artifact, --signature, --pubkey);
//   the artifact bytes, its detached minisign signature, and the
//   release-train minisign public key are all read from local files —
//   never generated or guessed by this command.
// Outputs: one internal/nodes.UpgradeOutcome per targeted node, rendered
//   via internal/output; a typed error if the rollout could not even be
//   resolved (e.g. an unknown node id with --all not set).
// Constraints: elevated (deps.Gate.Authorize("node.upgrade") —
//   internal/rpc's canonical elevationTable already lists node.upgrade,
//   always:true, so this uses the SAME table-driven gate node remove
//   uses, unlike rotate-key/revoke's direct requireElevation). Windows
//   tier-2: refuses via the same nodes.RefuseOnGOOS every other verb
//   uses. CASCADE_NO_INPUT=1 hard-errors via elevationGate.Authorize's
//   own branch. Never MCP: Annotations["local"]="true" matches every
//   other elevated verb's exclusion marker in this package.
// SPORT: cmd/cascade/node upgrade/ADDED (P1-E17-W4-S36-T5).

package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/buildinfo"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// nodeUpgradeView renders one node's rollout outcome for `--json` and
// table output alike.
type nodeUpgradeView struct {
	Outcomes []nodes.UpgradeOutcome `json:"outcomes"`
}

func (v nodeUpgradeView) String() string {
	s := ""
	for _, o := range v.Outcomes {
		switch {
		case o.Err != "":
			s += fmt.Sprintf("%s: FAILED (%s)\n", o.NodeID, o.Err)
		case o.Installed:
			s += fmt.Sprintf("%s: upgraded %s -> %s\n", o.NodeID, o.PreviousVersion, o.NewVersion)
		default:
			s += fmt.Sprintf("%s: skipped (%s)\n", o.NodeID, upgradeSkipReason(o))
		}
	}
	return s
}

func upgradeSkipReason(o nodes.UpgradeOutcome) string {
	if o.Reason != "" {
		return o.Reason
	}
	return "already at " + o.NewVersion
}

// newNodeUpgradeCmd builds `cascade node upgrade [--all] [NODE_ID]` ⚠.
func newNodeUpgradeCmd(deps nodeCLIDeps) *cobra.Command {
	var all bool
	var artifactPath, signaturePath, pubkeyPath string
	cmd := &cobra.Command{
		Use:         "upgrade [NODE_ID]",
		Short:       "Upgrade one or every enrolled node to a signed release artifact (elevated)",
		Args:        usageArgs(cobra.MaximumNArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeID := ""
			if len(args) == 1 {
				nodeID = args[0]
			}
			return runNodeUpgrade(cmd, deps, nodeUpgradeArgs{
				nodeID: nodeID, all: all,
				artifactPath: artifactPath, signaturePath: signaturePath, pubkeyPath: pubkeyPath,
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "upgrade every enrolled, non-drained node")
	cmd.Flags().StringVar(&artifactPath, "artifact", "", "required: path to the release binary to ship")
	cmd.Flags().StringVar(&signaturePath, "signature", "", "required: path to the artifact's detached minisign .minisig file")
	cmd.Flags().StringVar(&pubkeyPath, "pubkey", "", "path to the minisign public key file (default: $CASCADE_MINISIGN_PUBKEY)")
	return cmd
}

// nodeUpgradeArgs bundles runNodeUpgrade's parsed flags so the function
// itself stays under the 50-line cap.
type nodeUpgradeArgs struct {
	nodeID, artifactPath, signaturePath, pubkeyPath string
	all                                             bool
}

func runNodeUpgrade(cmd *cobra.Command, deps nodeCLIDeps, a nodeUpgradeArgs) error {
	if err := nodes.RefuseOnGOOS(deps.GOOS); err != nil {
		return err
	}
	if !a.all && a.nodeID == "" {
		return cascade.New(cascade.KindInvalidInput, "node upgrade: pass a NODE_ID or --all")
	}
	if err := deps.Gate.Authorize(cmd.Context(), "node.upgrade"); err != nil {
		return err
	}
	artifact, verifyDeps, err := composeNodeUpgrade(deps, a)
	if err != nil {
		return err
	}
	store, err := openRecordStore(deps)
	if err != nil {
		return err
	}
	outcomes, err := nodes.RunUpgrade(cmd.Context(), nodes.UpgradeRequest{NodeID: a.nodeID, All: a.all},
		nodes.UpgradeDeps{Store: store, Provision: verifyDeps, Artifact: artifact})
	if err != nil {
		return err
	}
	return nodeOutputWriter(cmd).Result(nodeUpgradeView{Outcomes: outcomes})
}

// composeNodeUpgrade reads the artifact/signature/pubkey files and builds
// the controller's own ssh signing identity, mirroring
// composeNodeAdmit's construction pattern exactly.
func composeNodeUpgrade(deps nodeCLIDeps, a nodeUpgradeArgs) (nodes.Artifact, nodes.ProvisionDeps, error) {
	if a.artifactPath == "" || a.signaturePath == "" {
		return nodes.Artifact{}, nodes.ProvisionDeps{}, cascade.New(cascade.KindInvalidInput, "node upgrade: --artifact and --signature are required")
	}
	data, err := os.ReadFile(a.artifactPath)
	if err != nil {
		return nodes.Artifact{}, nodes.ProvisionDeps{}, cascade.Wrap(cascade.KindInvalidInput, err, "node upgrade: read --artifact")
	}
	sig, err := os.ReadFile(a.signaturePath)
	if err != nil {
		return nodes.Artifact{}, nodes.ProvisionDeps{}, cascade.Wrap(cascade.KindInvalidInput, err, "node upgrade: read --signature")
	}
	pubkeyPath := a.pubkeyPath
	if pubkeyPath == "" {
		pubkeyPath = deps.Getenv("CASCADE_MINISIGN_PUBKEY")
	}
	if pubkeyPath == "" {
		return nodes.Artifact{}, nodes.ProvisionDeps{}, cascade.New(cascade.KindInvalidInput,
			"node upgrade: no minisign public key: pass --pubkey or set CASCADE_MINISIGN_PUBKEY")
	}
	pubBytes, err := os.ReadFile(pubkeyPath)
	if err != nil {
		return nodes.Artifact{}, nodes.ProvisionDeps{}, cascade.Wrap(cascade.KindInvalidInput, err, "node upgrade: read --pubkey")
	}
	pub, err := nodes.ParseMinisignPublicKey(pubBytes)
	if err != nil {
		return nodes.Artifact{}, nodes.ProvisionDeps{}, err
	}
	dialer, verifyFor, err := composeUpgradeDialer(context.Background(), deps)
	if err != nil {
		return nodes.Artifact{}, nodes.ProvisionDeps{}, err
	}
	artifact := nodes.Artifact{Data: data, Signature: sig}
	return artifact, nodes.ProvisionDeps{Dialer: dialer, VerifyFor: verifyFor, PublicKey: pub, ControllerVersion: buildinfo.Version}, nil
}

// composeUpgradeDialer builds the controller's own ssh signing identity
// and a per-target known_hosts verifier factory, mirroring
// composeNodeAdmit's construction. The verifier is built PER-TARGET (see
// ProvisionDeps.VerifyFor's doc) because KnownHosts.Verify checks a
// specific pinned host, and a --all rollout dials many different hosts.
func composeUpgradeDialer(ctx context.Context, deps nodeCLIDeps) (nodes.ExecDialer, func(nodes.Target) nodes.HostKeyVerifier, error) {
	dataDir := deps.Paths.DataDir()
	if dataDir == "" {
		return nil, nil, cascade.New(cascade.KindUnavailable, "node upgrade: could not resolve the data directory")
	}
	keystore, err := nodes.NewNodeKeystore(secrets.Config{Dir: deps.SecretsDir, ForceFileVault: deps.SecretsDir != ""})
	if err != nil {
		return nil, nil, err
	}
	self, err := nodes.EnsureLocalIdentity(ctx, nodes.NewFileSelfIdentityBackend(dataDir), keystore, rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	pub, err := nodes.ParsePublicKey(self.PubKeyB64())
	if err != nil {
		return nil, nil, err
	}
	signer := nodes.NewKeystoreExecSigner(ctx, keystore, self.NodeID, pub)
	knownHosts := nodes.NewKnownHosts(nodes.NewFileKnownHostsBackend(dataDir))
	verifyFor := func(target nodes.Target) nodes.HostKeyVerifier {
		host := target.User + "@" + target.Addr
		return func(fingerprint string) error { return knownHosts.Verify(host, fingerprint, "") }
	}
	return nodes.NewSSHExecDialer(signer, 0), verifyFor, nil
}
