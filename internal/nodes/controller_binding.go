// Purpose: the node's local record of its controller (ControllerBinding):
//
//	the heartbeat endpoint, the controller-issued enrollment id and the
//	pinned controller installation public key every ci.attest.sign request
//	must be signed by. Moved out of serve.go unchanged, then extended with
//	the pinned key and a write-once Save (contract:controller-key-binding).
//
// Inputs: the node's data directory; a binding object printed by the
//
//	controller's `cascade node enroll --controller-endpoint` and saved by
//	the elevated `cascade node bind`.
//
// Outputs: <dataDir>/nodes/controller_binding.json, 0600 under a 0700
//
//	parent.
//
// Constraints: the pinned key is a write-once trust anchor. Save never
//
//	replaces an existing binding that differs from the one offered; it
//	only creates the file or confirms an identical one. A binding loaded
//	without a key keeps the key empty, and every consumer of the pin
//	refuses on empty (VerifyControllerRequest).
//
// SPORT: internal/nodes ControllerBinding/CHG, ControllerBindingStore/ADDED (P1-CI-13).

package nodes

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ControllerBinding is the local record of this node's enrollment with a
// controller: the endpoint the heartbeat scheduler sends to, the
// controller-issued enrollment id every frame embeds (heartbeat_sign.go's
// DeriveEnrollmentID, computed and returned BY the controller during
// enrollment; this node persists it verbatim rather than re-deriving it,
// since only the controller holds the DeviceRecord DeriveEnrollmentID is
// computed from), and the controller's installation public key (standard
// base64 of 32 bytes) that this node pins.
type ControllerBinding struct {
	Endpoint            string `json:"endpoint"`
	EnrollmentID        string `json:"enrollment_id"`
	ControllerPubKeyB64 string `json:"controller_pubkey_b64"`
}

// ControllerBindingBackend reads ControllerBinding. An absent file (no
// enrollment has run yet on this node) is a valid "nothing to heartbeat to
// yet" state, never an error.
type ControllerBindingBackend interface {
	// Load returns the binding and true, or a zero value and false if
	// this node has not yet enrolled with a controller.
	Load() (ControllerBinding, bool, error)
}

// ControllerBindingStore is a ControllerBindingBackend that can also pin a
// binding. Save is write-once: it creates the binding atomically at 0600
// under a 0700 parent; an identical existing binding is a no-op and a
// different one is KindConflict. An empty Endpoint or EnrollmentID, or a
// key that does not decode to 32 bytes, is KindInvalidInput.
type ControllerBindingStore interface {
	ControllerBindingBackend
	Save(ControllerBinding) error
}

// NewFileControllerBindingBackend returns the file-backed store at
// <dataDir>/nodes/controller_binding.json, mirroring
// NewFileSelfIdentityBackend's identical precedent.
func NewFileControllerBindingBackend(dataDir string) ControllerBindingStore {
	return fileControllerBindingBackend{path: filepath.Join(dataDir, "nodes", "controller_binding.json")}
}

type fileControllerBindingBackend struct{ path string }

func (b fileControllerBindingBackend) Load() (ControllerBinding, bool, error) {
	data, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return ControllerBinding{}, false, nil
		}
		return ControllerBinding{}, false, cascade.Wrap(cascade.KindUnavailable, err, "nodes: read controller binding")
	}
	var binding ControllerBinding
	if err := json.Unmarshal(data, &binding); err != nil {
		return ControllerBinding{}, false, cascade.Wrap(cascade.KindIntegrity, err, "nodes: controller binding file is not valid JSON")
	}
	// Preserve incomplete records so role resolution cannot mistake them for
	// absence and grant controller authority alongside a designation.
	return binding, true, nil
}

// Save pins binding. See ControllerBindingStore for the contract.
func (b fileControllerBindingBackend) Save(binding ControllerBinding) error {
	if err := ValidateControllerBinding(binding); err != nil {
		return err
	}
	data, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "nodes: encode controller binding")
	}
	dir := filepath.Dir(b.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: create the nodes directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: restrict the nodes directory")
	}
	// Write the full binding to a private temp file first, then publish it
	// with a hard link: link(2) fails with EEXIST atomically, and the name
	// only ever points at complete content, so a concurrent Save never reads
	// a half-written binding.
	f, err := os.CreateTemp(dir, ".controller-binding-*.tmp")
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: create controller binding")
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := writeSynced(f, data); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: write controller binding")
	}
	if err := os.Link(tmp, b.path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return b.confirmExisting(binding)
		}
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: publish controller binding")
	}
	return nil
}

// writeSynced writes data, fsyncs and closes f.
func writeSynced(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// confirmExisting compares the binding already on disk with the one offered:
// the same key is a no-op, a different key is the write-once conflict, and an
// unreadable key is KindInvalidInput.
func (b fileControllerBindingBackend) confirmExisting(binding ControllerBinding) error {
	existing, ok, err := b.Load()
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(existing.Endpoint) == "" {
		return cascade.New(cascade.KindInvalidInput, "nodes: the existing controller binding is empty or malformed")
	}
	if _, err := ParsePublicKey(existing.ControllerPubKeyB64); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "nodes: the existing controller binding key is malformed")
	}
	if existing.ControllerPubKeyB64 != binding.ControllerPubKeyB64 {
		return cascade.New(cascade.KindConflict,
			"nodes: this node already pins a different controller key; the binding is write-once (revoke and re-provision the node to change it)")
	}
	if existing != binding {
		return cascade.New(cascade.KindConflict, "nodes: this node already holds a different controller binding; the binding is write-once")
	}
	return nil
}

// ValidateControllerBinding refuses a binding with an empty endpoint or
// enrollment id, or a controller key that is not standard base64 of a
// 32-byte Ed25519 public key. KindInvalidInput.
func ValidateControllerBinding(binding ControllerBinding) error {
	if strings.TrimSpace(binding.Endpoint) == "" || strings.TrimSpace(binding.EnrollmentID) == "" {
		return cascade.New(cascade.KindInvalidInput, "nodes: controller binding needs an endpoint and an enrollment id")
	}
	if _, err := ParsePublicKey(binding.ControllerPubKeyB64); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "nodes: controller binding key")
	}
	return nil
}
