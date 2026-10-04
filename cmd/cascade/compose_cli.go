// Purpose: the CLI half of the composition root's registration contract.
//
//	Every root noun is one file, mount_<noun>.go, holding one
//	registerRootMount call; mountSubcommands (root_mounts.go) mounts the
//	registrations in (Order, Name) order and nothing else.
//
// Inputs:  rootMount values registered from package-level initialisers.
// Outputs: the ordered list mountSubcommands iterates.
// Constraints: root.go and root_mounts.go are never edited to add a noun. A
//
//	registration list that is a second source of truth, or a reflection over
//	one, is forbidden; this slice is the only list.
//
// SPORT: cmd/cascade composition root — CLI registrations (P1-CORE-01).
package main

import (
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

// rootMount is one root command registration. Order reproduces the mount
// order the hand-kept list had; Name is the tiebreak and must be unique.
type rootMount struct {
	Name  string
	Order int
	Mount func(root *cobra.Command)
}

// rootMounts holds every registration, in registration (file-name) order.
// mountSubcommands sorts a copy, so init order never matters.
var rootMounts []rootMount

// registerRootMount records m. It is used only as
// `var _ = registerRootMount(rootMount{...})` in a mount_<noun>.go file.
func registerRootMount(m rootMount) struct{} {
	rootMounts = append(rootMounts, m)
	return struct{}{}
}

// validateRootMounts reports the first duplicate Name or nil Mount in ms.
func validateRootMounts(ms []rootMount) error {
	seen := make(map[string]bool, len(ms))
	for _, m := range ms {
		if m.Name == "" || m.Mount == nil {
			return cascade.Newf(cascade.KindInternal, "root mount %q: empty name or nil Mount", m.Name)
		}
		if seen[m.Name] {
			return cascade.Newf(cascade.KindInternal, "duplicate root mount name %q", m.Name)
		}
		seen[m.Name] = true
	}
	return nil
}
