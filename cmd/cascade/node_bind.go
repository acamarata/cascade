// Purpose: the controller-key binding handoff (contract:controller-key-binding):
//
//	`cascade node enroll --controller-endpoint <url>` prints the binding a
//	node must pin, and the elevated `cascade node bind --binding-file <path>`
//	pins it on the node.
//
// Inputs: the enrolled DeviceRecord and the controller installation public
//
//	key (installationPublicKey, P1-SEC-40) on the controller; a binding file
//	on the node.
//
// Outputs: the binding object (internal/output); on the node,
//
//	<dataDir>/nodes/controller_binding.json, write-once.
//
// Constraints: the printed key is the installation key, never the
//
//	controller's per-node identity key (R21b B-2). Enrollment never writes a
//	binding; only the elevated bind does, after naming the key it pins.
//
// SPORT: cmd/cascade/node bind/ADDED, node enroll --controller-endpoint/ADDED (P1-CI-13).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// nodeEnrollView is enroll's output: the enrolled row and the binding the
// node pins with `cascade node bind`.
type nodeEnrollView struct {
	Node    nodeRowView             `json:"node"`
	Binding nodes.ControllerBinding `json:"binding"`
}

func (v nodeEnrollView) String() string {
	raw, _ := json.MarshalIndent(v.Binding, "", "  ")
	return v.Node.String() + "\nbinding (save it on the node and run `cascade node bind --binding-file <file>`):\n" + string(raw)
}

// runNodeEnroll is the enroll verb: the endpoint is required and checked
// before anything else; the installation key is read after elevation and
// before any dial, so a machine that cannot pin a key enrolls nothing.
func runNodeEnroll(cmd *cobra.Command, deps nodeCLIDeps, a nodeAdmitArgs, endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return cascade.New(cascade.KindInvalidInput, "node enroll: --controller-endpoint is required (the node's binding records it)")
	}
	var keyB64 string
	prepare := func(ctx context.Context) error {
		var err error
		keyB64, err = controllerPubKeyB64(ctx, deps.Paths)
		return err
	}
	rec, err := admitFromCLI(cmd, deps, a, prepare)
	if err != nil {
		return err
	}
	binding := nodes.ControllerBinding{Endpoint: endpoint, EnrollmentID: nodes.DeriveEnrollmentID(rec), ControllerPubKeyB64: keyB64}
	return nodeOutputWriter(cmd).Result(nodeEnrollView{Node: newNodeRowView(rec, deps.Clock), Binding: binding})
}

// nodeBoundView is bind's output.
type nodeBoundView struct {
	ControllerKeyID string `json:"controller_key_id"`
	Endpoint        string `json:"endpoint"`
}

func (v nodeBoundView) String() string {
	return fmt.Sprintf("pinned controller key %s for %s", v.ControllerKeyID, v.Endpoint)
}

// newNodeBindCmd builds `cascade node bind --binding-file <path>` (elevated).
func newNodeBindCmd(deps nodeCLIDeps) *cobra.Command {
	var bindingFile string
	cmd := &cobra.Command{
		Use:         "bind",
		Short:       "Pin the controller key this node trusts (elevated, write-once)",
		Args:        usageArgs(cobra.NoArgs),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runNodeBind(cmd, deps, bindingFile)
		},
	}
	cmd.Flags().StringVar(&bindingFile, "binding-file", "", "required: the binding object `cascade node enroll` printed")
	return cmd
}

// runNodeBind validates the binding, names the key it pins, takes the signed
// elevation for node.bind and only then saves it.
func runNodeBind(cmd *cobra.Command, deps nodeCLIDeps, bindingFile string) error {
	if err := nodes.RefuseOnGOOS(deps.GOOS); err != nil {
		return err
	}
	if bindingFile == "" {
		return cascade.New(cascade.KindInvalidInput, "node bind: --binding-file is required")
	}
	dataDir := deps.Paths.DataDir()
	if dataDir == "" {
		return cascade.New(cascade.KindUnavailable, "node bind: could not resolve the data directory")
	}
	keyID, err := peekBindingKeyID(bindingFile)
	if err != nil {
		return err
	}
	out := nodeOutputWriter(cmd)
	out.Progress("node bind: pinning controller key %s requires elevation", keyID)
	if err := requireElevation(cmd.Context(), deps.Gate, "node.bind"); err != nil {
		return withReason(err, "node bind: pinning controller key "+keyID)
	}
	binding, err := readBindingFile(bindingFile)
	if err != nil {
		return err
	}
	pub, err := nodes.ParsePublicKey(binding.ControllerPubKeyB64)
	if err != nil {
		return err
	}
	if nodes.KeyIDFor(pub) != keyID {
		return cascade.New(cascade.KindConflict, "node bind: the binding file changed after the key was named for elevation")
	}
	if err := nodes.NewFileControllerBindingBackend(dataDir).Save(binding); err != nil {
		return err
	}
	return out.Result(nodeBoundView{ControllerKeyID: keyID, Endpoint: binding.Endpoint})
}

// peekBindingKeyID reads only the controller key field of the binding file
// and returns its key id, so elevation can name the key before the file is
// decoded or validated in full.
func peekBindingKeyID(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, read only
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "node bind: read the binding file")
	}
	var keyOnly struct {
		Key string `json:"controller_pubkey_b64"`
	}
	if err := json.Unmarshal(raw, &keyOnly); err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "node bind: the binding file is not a binding object")
	}
	pub, err := nodes.ParsePublicKey(keyOnly.Key)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "node bind: the binding file key")
	}
	return nodes.KeyIDFor(pub), nil
}

// readBindingFile strictly decodes the binding object and validates it.
func readBindingFile(path string) (nodes.ControllerBinding, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, read only
	if err != nil {
		return nodes.ControllerBinding{}, cascade.Wrap(cascade.KindInvalidInput, err, "node bind: read the binding file")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var binding nodes.ControllerBinding
	if err := dec.Decode(&binding); err != nil {
		return nodes.ControllerBinding{}, cascade.Wrap(cascade.KindInvalidInput, err, "node bind: the binding file is not a binding object")
	}
	if err := nodes.ValidateControllerBinding(binding); err != nil {
		return nodes.ControllerBinding{}, err
	}
	return binding, nil
}

// withReason prefixes err's message with reason and keeps its Kind.
func withReason(err error, reason string) error {
	kind, ok := cascade.KindOf(err)
	if !ok {
		kind = cascade.KindElevationRequired
	}
	return cascade.Wrap(kind, err, reason)
}
