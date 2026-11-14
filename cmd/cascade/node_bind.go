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
//	A designated controller is never bound: bind refuses any designation
//	except an untouched epoch-one bootstrap, which it supersedes after
//	writing the permanent bound_once tombstone.
//
// SPORT: cmd/cascade/node bind/ADDED, node enroll --controller-endpoint/ADDED (P1-CI-13).

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/nodes"
	cruntime "github.com/acamarata/cascade/internal/runtime"
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

// runNodeEnroll is the enroll verb: the tier-2 refusal comes first, then the
// required endpoint; the installation key is read after elevation and
// before any dial, so a machine that cannot pin a key enrolls nothing.
func runNodeEnroll(cmd *cobra.Command, deps nodeCLIDeps, a nodeAdmitArgs, endpoint string) error {
	if err := nodes.RefuseOnGOOS(deps.GOOS); err != nil {
		return err
	}
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
// elevation for node.bind, refuses a designated controller, and only then
// writes the tombstone, supersedes an untouched bootstrap and saves it.
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
	devices, jobRows, err := bindDesignationCheck(cmd.Context(), deps, dataDir)
	if err != nil {
		return err
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
	if err := writeBoundTombstone(dataDir); err != nil {
		return err
	}
	if err := nodes.SupersedeBootstrap(nodes.NewFileDesignationBackend(dataDir), devices, jobRows); err != nil {
		return err
	}
	if err := nodes.NewFileControllerBindingBackend(dataDir).Save(binding); err != nil {
		return err
	}
	return out.Result(nodeBoundView{ControllerKeyID: keyID, Endpoint: binding.Endpoint})
}

// bindDesignationCheck refuses a home that holds controller authority. No
// designation passes; an epoch-one bootstrap passes only with zero device
// records and zero jobs rows (the counts SupersedeBootstrap rechecks); every
// other designation, and every error reading the three, is KindConflict.
func bindDesignationCheck(ctx context.Context, deps nodeCLIDeps, dataDir string) (devices, jobRows int, err error) {
	d, found, err := nodes.NewFileDesignationBackend(dataDir).Load()
	if err != nil {
		return 0, 0, cascade.Wrap(cascade.KindConflict, err, "node bind: cannot read the controller designation")
	}
	if !found {
		return 0, 0, nil
	}
	if d.Reason != "bootstrap" || d.Epoch != 1 {
		return 0, 0, cascade.Newf(cascade.KindConflict,
			"node bind: this machine is the designated controller (%s, epoch %d); a controller is never bound", d.Reason, d.Epoch)
	}
	store, err := openRecordStore(deps)
	if err != nil {
		return 0, 0, cascade.Wrap(cascade.KindConflict, err, "node bind: cannot count device records")
	}
	records, err := store.List()
	if err != nil {
		return 0, 0, cascade.Wrap(cascade.KindConflict, err, "node bind: cannot count device records")
	}
	jobRows, err = countJobRows(ctx, dataDir)
	if err != nil {
		return 0, 0, err
	}
	if len(records) != 0 || jobRows != 0 {
		return 0, 0, cascade.Newf(cascade.KindConflict,
			"node bind: the bootstrap controller is in use (%d device records, %d jobs); a controller is never bound", len(records), jobRows)
	}
	return len(records), jobRows, nil
}

// countJobRows counts jobs_job rows in <dataDir>/cascade.db opened read-only
// (no file created or changed). An absent database counts 0; any other open
// or query error is KindConflict.
func countJobRows(ctx context.Context, dataDir string) (int, error) {
	path := filepath.Join(dataDir, "cascade.db")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, cascade.Wrap(cascade.KindConflict, err, "node bind: cannot inspect cascade.db")
	}
	mode, err := readOnlyMode(path)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindConflict, err, "node bind: cannot open cascade.db read-only")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+mode+"&_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, cascade.Wrap(cascade.KindConflict, err, "node bind: cannot open cascade.db")
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM jobs_job`).Scan(&n); err != nil {
		return 0, cascade.Wrap(cascade.KindConflict, err, "node bind: cannot count jobs in cascade.db")
	}
	return n, nil
}

// writeBoundTombstone writes <dataDir>/nodes/bound_once (0600, fsynced). It
// is never removed, so a later daemon start never bootstraps this home.
func writeBoundTombstone(dataDir string) error {
	path := filepath.Join(dataDir, "nodes", "bound_once")
	if err := cruntime.WriteBytesAtomic(path, []byte("node bind ran on this home\n")); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "node bind: write the bound_once tombstone")
	}
	return nil
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
