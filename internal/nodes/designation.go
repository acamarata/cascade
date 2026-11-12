// Purpose: persist explicit controller authority (contract:controller-designation).
// Inputs: local identity, injected entropy and clock, and stored designation.
// Outputs: an atomic 0600 controller_designation.json under a 0700 parent.
// Constraints: same-uid data, not a security boundary; no implicit promotion.
// SPORT: nodes/controller-designation (ADD, P1-NODE-16).

package nodes

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Designation records the controller and its installation's fencing epoch.
type Designation struct {
	InstallationID   string    `json:"installation_id"`
	ControllerNodeID string    `json:"controller_node_id"`
	Epoch            uint64    `json:"epoch"`
	DesignatedAt     time.Time `json:"designated_at"`
	Reason           string    `json:"reason"`
}

// DesignationBackend persists explicit controller authority.
type DesignationBackend interface {
	Load() (Designation, bool, error)
	Write(Designation) error
	// Remove is reserved for SupersedeBootstrap.
	Remove() error
}

// DesignationSource exposes the designation in a binding's data directory.
type DesignationSource interface {
	DesignationBackend() DesignationBackend
}

type fileDesignationBackend struct{ path string }

// designationWrites serializes local read/check/write operations across backends.
var designationWrites sync.Mutex

var _ DesignationSource = fileControllerBindingBackend{}
var _ DesignationSource = fileDesignationBackend{}

// NewFileDesignationBackend opens <dataDir>/nodes/controller_designation.json.
func NewFileDesignationBackend(dataDir string) DesignationBackend {
	return fileDesignationBackend{filepath.Join(dataDir, "nodes", "controller_designation.json")}
}

// DesignationBackend returns this file backend's designation source.
func (b fileDesignationBackend) DesignationBackend() DesignationBackend { return b }

// DesignationBackend returns the designation beside the controller binding.
func (b fileControllerBindingBackend) DesignationBackend() DesignationBackend {
	return fileDesignationBackend{filepath.Join(filepath.Dir(b.path), "controller_designation.json")}
}

func (b fileDesignationBackend) Load() (Designation, bool, error) {
	data, err := os.ReadFile(b.path)
	if os.IsNotExist(err) {
		return Designation{}, false, nil
	}
	if err != nil {
		return Designation{}, false, cascade.Wrap(cascade.KindUnavailable, err, "nodes: read controller designation")
	}
	var d Designation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return Designation{}, false, cascade.Wrap(cascade.KindIntegrity, err, "nodes: invalid controller designation JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Designation{}, false, cascade.New(cascade.KindIntegrity, "nodes: trailing controller designation JSON")
	}
	if err := validateDesignation(d); err != nil {
		return Designation{}, false, cascade.Wrap(cascade.KindIntegrity, err, "nodes: invalid stored controller designation")
	}
	return d, true, nil
}

func validateDesignation(d Designation) error {
	if strings.TrimSpace(d.InstallationID) == "" || strings.TrimSpace(d.ControllerNodeID) == "" ||
		d.Epoch == 0 || d.DesignatedAt.IsZero() || strings.TrimSpace(d.Reason) == "" {
		return cascade.New(cascade.KindInvalidInput, "nodes: controller designation requires every field and epoch >= 1")
	}
	if d.Reason != "init" && d.Reason != "handover" && d.Reason != "bootstrap" {
		return cascade.New(cascade.KindInvalidInput, "nodes: invalid controller designation reason")
	}
	return nil
}

func (b fileDesignationBackend) Write(d Designation) error {
	designationWrites.Lock()
	defer designationWrites.Unlock()
	if err := validateDesignation(d); err != nil {
		return err
	}
	stored, ok, err := b.Load()
	if err != nil {
		return err
	}
	if ok && (d.InstallationID != stored.InstallationID || d.Epoch <= stored.Epoch) {
		return cascade.New(cascade.KindConflict, "nodes: controller designation installation or epoch conflicts")
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "nodes: encode controller designation")
	}
	dir := filepath.Dir(b.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: create designation directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: restrict designation directory")
	}
	return runtime.WriteFileAtomic(b.path, data, 0o600)
}

func (b fileDesignationBackend) Remove() error {
	designationWrites.Lock()
	defer designationWrites.Unlock()
	if err := os.Remove(b.path); err != nil && !os.IsNotExist(err) {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: remove controller designation")
	}
	return nil
}

// Designate initializes authority once; repeated initialization by self is a no-op.
func Designate(ctx context.Context, d DesignationBackend, b ControllerBindingBackend, self Identity, rnd io.Reader, clock runtime.Clock) (Designation, error) {
	if err := ctx.Err(); err != nil {
		return Designation{}, cascade.Wrap(cascade.KindCanceled, err, "nodes: designate controller")
	}
	if d == nil || b == nil {
		return Designation{}, cascade.New(cascade.KindIntegrity, "nodes: designation and binding backends are required")
	}
	binding, _, err := b.Load()
	if err != nil {
		return Designation{}, cascade.Wrap(cascade.KindIntegrity, err, "nodes: load binding before designation")
	}
	if strings.TrimSpace(binding.Endpoint) != "" {
		return Designation{}, cascade.New(cascade.KindConflict, "nodes: enrolled as a node")
	}
	stored, ok, err := d.Load()
	if err != nil {
		return Designation{}, err
	}
	if ok {
		if err := validateDesignation(stored); err != nil {
			return Designation{}, cascade.Wrap(cascade.KindIntegrity, err, "nodes: invalid stored controller designation")
		}
		if stored.ControllerNodeID != self.NodeID {
			return Designation{}, cascade.New(cascade.KindConflict, "nodes: another node is designated controller")
		}
		return stored, nil
	}
	return initialDesignation(d, self, rnd, clock)
}

func initialDesignation(d DesignationBackend, self Identity, rnd io.Reader, clock runtime.Clock) (Designation, error) {
	if strings.TrimSpace(self.NodeID) == "" || rnd == nil || clock == nil {
		return Designation{}, cascade.New(cascade.KindInvalidInput, "nodes: designation needs identity, entropy and clock")
	}
	var entropy [16]byte
	if _, err := io.ReadFull(rnd, entropy[:]); err != nil {
		return Designation{}, cascade.Wrap(cascade.KindUnavailable, err, "nodes: installation id entropy")
	}
	id := "inst_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(entropy[:]))
	next := Designation{InstallationID: id, ControllerNodeID: self.NodeID, Epoch: 1, DesignatedAt: clock.Now(), Reason: "init"}
	if err := d.Write(next); err != nil {
		return Designation{}, err
	}
	return next, nil
}

// Handover accepts only the next epoch for an existing installation.
func Handover(d DesignationBackend, next Designation) error {
	if d == nil {
		return cascade.New(cascade.KindIntegrity, "nodes: no designation backend")
	}
	stored, ok, err := d.Load()
	if err != nil {
		return err
	}
	if !ok {
		return cascade.New(cascade.KindConflict, "nodes: handover requires an existing designation")
	}
	if err := validateDesignation(stored); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "nodes: invalid stored controller designation")
	}
	if next.InstallationID != stored.InstallationID || stored.Epoch == ^uint64(0) || next.Epoch != stored.Epoch+1 {
		return cascade.New(cascade.KindConflict, "nodes: handover requires the same installation and stored epoch + 1")
	}
	if next.Reason != "handover" {
		return cascade.New(cascade.KindInvalidInput, "nodes: handover reason must be handover")
	}
	if err := validateDesignation(next); err != nil {
		return err
	}
	return d.Write(next)
}
