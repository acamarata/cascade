// Purpose: the registry of typed envelope extensions and their narrowers.
//
// Inputs: init-time RegisterExtension calls from the packages that own an
//
//	extension kind (for example a delegation budget).
//
// Outputs: the narrower Narrow applies to a parent/child extension pair.
// Constraints: registration is init-time only; a duplicate, empty or nil
//
//	registration panics, so two owners can never claim one kind. An
//	unregistered kind always refuses (fail closed).
//
// SPORT: pkg/envelope (ADD) — P1-SEC-20 (contract:scope-envelope).

package envelope

import (
	"encoding/json"
	"sync"
)

// ExtensionNarrower reports, with a non-nil error, that the child's
// extension data grants more than the parent's.
type ExtensionNarrower func(parent, child json.RawMessage) error

// extRegistry maps an extension kind to its narrower.
var extRegistry = struct {
	sync.RWMutex
	m map[string]ExtensionNarrower
}{m: map[string]ExtensionNarrower{}}

// RegisterExtension registers the narrower for kind. Call it from an init
// function; an empty kind, a nil narrower or a second registration of one
// kind panics.
func RegisterExtension(kind string, n ExtensionNarrower) {
	if kind == "" || n == nil {
		panic("envelope: RegisterExtension needs a kind and a narrower")
	}
	extRegistry.Lock()
	defer extRegistry.Unlock()
	if _, dup := extRegistry.m[kind]; dup {
		panic("envelope: extension kind " + kind + " registered twice")
	}
	extRegistry.m[kind] = n
}

// lookupExtension returns kind's narrower, or nil when unregistered.
func lookupExtension(kind string) ExtensionNarrower {
	extRegistry.RLock()
	defer extRegistry.RUnlock()
	return extRegistry.m[kind]
}
