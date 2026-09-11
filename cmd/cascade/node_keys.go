// Purpose: `cascade node rotate-key <id>` and `cascade node revoke <id>`
//
//	⚠ (R-21.220 amendment), both elevated verbs mounted by S-36.T4.
//
// Inputs: cobra args/flags; nodeCLIDeps (Gate, Paths, Clock, SecretsDir).
// Outputs: the updated DeviceRecord (rotate-key) or removal confirmation
//
//	(revoke), rendered via internal/output; a typed error on refusal.
//
// Constraints: SELF-SERVICE ROTATION (CONTRADICTION — full quote in the
//
//	ticket journal). internal/nodes.RecordStore.Rotate takes a signature
//	already produced by the CURRENT identity key (rotate.go, S-36.T1);
//	that key lives in the ENROLLED NODE's own keystore, not the operator
//	machine's, and no ticket in this tree's files_scope adds a remote
//	"produce a rotation signature" RPC this CLI could call without
//	inventing an unspecified wire protocol outside S-36.T4's scope. This
//	command therefore rotates THIS machine's own local node identity
//	(EnsureLocalIdentity's self-keystore, S-36.T2), signing via
//	NodeKeystore.Sign — never a raw private key, matching every other
//	signer in this package — and calling the SAME RecordStore.Rotate the
//	contract names. A controller-driven remote rotation of a peer's key
//	is out of this ticket's honestly-completable scope; see the journal.
//	revoke has no such gap: RecordStore.Revoke takes only a node id.
//
// SPORT: cmd/cascade/node rotate-key/ADDED, revoke/ADDED
//
//	(P1-E17-W4-S36-T4).
package main

import (
	"crypto/rand"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/secrets"
)

// nodeKeyView renders a rotate-key/revoke result.
type nodeKeyView struct {
	NodeID      string   `json:"node_id"`
	PubKeyB64   string   `json:"pubkey_b64,omitempty"`
	RevokedKeys []string `json:"revoked_keys,omitempty"`
	Action      string   `json:"action"`
}

func (v nodeKeyView) String() string {
	return fmt.Sprintf("%s: %s (revoked_keys=%d)", v.NodeID, v.Action, len(v.RevokedKeys))
}

// newNodeRotateKeyCmd builds `cascade node rotate-key <id>` ⚠. See this
// file's package doc for why it rotates this machine's OWN local identity
// rather than a remote peer's.
func newNodeRotateKeyCmd(deps nodeCLIDeps) *cobra.Command {
	return &cobra.Command{
		Use:         "rotate-key NODE_ID",
		Short:       "Rotate this node's own signing key (elevated, local identity only)",
		Args:        usageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := nodes.RefuseOnGOOS(deps.GOOS); err != nil {
				return err
			}
			if err := requireElevation(cmd.Context(), deps.Gate, "node.rotate_key"); err != nil {
				return err
			}
			nodeID := args[0]
			dataDir := deps.Paths.DataDir()
			keystore, err := nodes.NewNodeKeystore(secrets.Config{Dir: deps.SecretsDir})
			if err != nil {
				return err
			}
			newIdentity, newPriv, err := nodes.GenerateIdentity(rand.Reader)
			if err != nil {
				return err
			}
			sig, err := keystore.Sign(cmd.Context(), nodeID, rotationSigningPayloadFor(nodeID, newIdentity.PubKeyB64()))
			if err != nil {
				return err
			}
			store := nodes.NewRecordStore(nodes.NewFileRecordBackend(dataDir), deps.Clock)
			rec, err := store.Rotate(nodeID, newIdentity.PubKeyB64(), sig)
			if err != nil {
				return err
			}
			if err := keystore.Store(cmd.Context(), nodeID, newPriv); err != nil {
				return err
			}
			return nodeOutputWriter(cmd).Result(nodeKeyView{NodeID: rec.NodeID, PubKeyB64: rec.PubKeyB64, RevokedKeys: rec.RevokedKeys, Action: "rotated"})
		},
	}
}

// rotationSigningPayloadFor re-derives rotate.go's unexported
// rotationSigningPayload shape so this file's NodeKeystore.Sign call
// signs the EXACT bytes RecordStore.Rotate verifies. Duplicated rather
// than exported: rotate.go is in this ticket's files_scope to change, but
// exporting an internal signing-payload helper across the package
// boundary for a single call site is a worse shape than this small,
// obviously-matching duplication (rotate_test.go's own
// TestRotate_SignatureMustMatchPayload pins the exact byte format both
// sides depend on).
func rotationSigningPayloadFor(nodeID, newPubKeyB64 string) []byte {
	buf := append([]byte(nil), []byte(nodeID)...)
	buf = append(buf, 0x1F)
	buf = append(buf, []byte(newPubKeyB64)...)
	return buf
}

// newNodeRevokeCmd builds `cascade node revoke <id>` ⚠ (R-21.220
// amendment): invalidates every future attestation/heartbeat from the
// node's current key via RecordStore.Revoke.
func newNodeRevokeCmd(deps nodeCLIDeps) *cobra.Command {
	return &cobra.Command{
		Use:         "revoke NODE_ID",
		Short:       "Revoke a node's current signing key (elevated)",
		Args:        usageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := nodes.RefuseOnGOOS(deps.GOOS); err != nil {
				return err
			}
			if err := requireElevation(cmd.Context(), deps.Gate, "node.revoke"); err != nil {
				return err
			}
			store, err := openRecordStore(deps)
			if err != nil {
				return err
			}
			rec, err := store.Revoke(args[0])
			if err != nil {
				return err
			}
			return nodeOutputWriter(cmd).Result(nodeKeyView{NodeID: rec.NodeID, RevokedKeys: rec.RevokedKeys, Action: "revoked"})
		},
	}
}
