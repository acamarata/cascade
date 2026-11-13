// Purpose: bootstrap controller authority only for a never-bound installation.
// Inputs: designation and binding backends, data directory, identity, entropy and clock.
// Outputs: an epoch-one bootstrap designation or a fail-closed refusal.
// Constraints: no election or promotion; bound_once is permanent same-uid data.
// SPORT: nodes/controller-designation (ADD, P1-NODE-16).

package nodes

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrRoleConflict refuses implicit controller authority for a previously bound
// or indeterminate installation. No role can safely be inferred from this state.
var ErrRoleConflict = cascade.New(cascade.KindIntegrity, "nodes: controller role conflict")

var bootstrapWrites sync.Mutex

// BootstrapDesignation creates epoch-one authority only when the designation,
// binding file and permanent bound_once tombstone are all absent. Existing
// designations are returned unchanged, without consuming entropy or clock time.
func BootstrapDesignation(ctx context.Context, d DesignationBackend, b ControllerBindingBackend, dataDir string, self Identity, rnd io.Reader, clock runtime.Clock) (Designation, bool, error) {
	bootstrapWrites.Lock()
	defer bootstrapWrites.Unlock()
	if err := ctx.Err(); err != nil {
		return Designation{}, false, cascade.Wrap(cascade.KindCanceled, err, "nodes: bootstrap controller designation")
	}
	if d == nil || b == nil || strings.TrimSpace(dataDir) == "" {
		return Designation{}, false, ErrRoleConflict
	}
	stored, found, err := d.Load()
	if err != nil {
		return Designation{}, false, err
	}
	if found {
		if err := validateDesignation(stored); err != nil {
			return Designation{}, false, cascade.Wrap(cascade.KindIntegrity, err, "nodes: invalid stored controller designation")
		}
		return stored, false, nil
	}
	if err := bootstrapEligible(d, b, dataDir); err != nil {
		return Designation{}, false, err
	}
	writer := &bootstrapDesignationWriter{DesignationBackend: d}
	next, err := Designate(ctx, writer, b, self, rnd, clock)
	if err != nil {
		return Designation{}, false, err
	}
	if writer.wrote {
		next.Reason = "bootstrap"
	}
	return next, writer.wrote, nil
}

func bootstrapEligible(d DesignationBackend, b ControllerBindingBackend, dataDir string) error {
	// File-backed inputs must name the same directory as the tombstone checks.
	expected := NewFileDesignationBackend(dataDir).(fileDesignationBackend)
	if file, ok := d.(fileDesignationBackend); ok && file.path != expected.path {
		return ErrRoleConflict
	}
	if file, ok := b.(fileControllerBindingBackend); ok && filepath.Dir(file.path) != filepath.Dir(expected.path) {
		return ErrRoleConflict
	}
	binding, found, err := b.Load()
	if err != nil {
		return err
	}
	if found || strings.TrimSpace(binding.Endpoint) != "" {
		return ErrRoleConflict
	}
	for _, name := range []string{"controller_binding.json", "bound_once"} {
		// Lstat also refuses dangling symlinks and non-file tombstones.
		if _, err := os.Lstat(filepath.Join(dataDir, "nodes", name)); err == nil {
			return ErrRoleConflict
		} else if !os.IsNotExist(err) {
			return cascade.Wrap(cascade.KindIntegrity, err, "nodes: inspect bootstrap preconditions")
		}
	}
	return nil
}

// bootstrapDesignationWriter changes the initial reason before the sole write,
// retaining Designate's identity, entropy, clock and binding validation.
type bootstrapDesignationWriter struct {
	DesignationBackend
	wrote bool
}

func (b *bootstrapDesignationWriter) Write(d Designation) error {
	if d.Reason != "init" || d.Epoch != 1 {
		return ErrRoleConflict
	}
	d.Reason = "bootstrap"
	if err := b.DesignationBackend.Write(d); err != nil {
		return err
	}
	b.wrote = true
	return nil
}

// SupersedeBootstrap removes only untouched epoch-one bootstrap authority.
// The elevated binding caller supplies record and job counts and persists the
// bound_once tombstone first; this function never removes that tombstone.
func SupersedeBootstrap(d DesignationBackend, deviceRecords, jobRows int) error {
	bootstrapWrites.Lock()
	defer bootstrapWrites.Unlock()
	if d == nil {
		return ErrRoleConflict
	}
	stored, found, err := d.Load()
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if err := validateDesignation(stored); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "nodes: invalid stored controller designation")
	}
	if stored.Reason != "bootstrap" || stored.Epoch != 1 || deviceRecords != 0 || jobRows != 0 {
		return cascade.Newf(cascade.KindConflict, "nodes: designation %q for controller %q at epoch %d (%s) cannot be superseded",
			stored.InstallationID, stored.ControllerNodeID, stored.Epoch, stored.Reason)
	}
	return d.Remove()
}
