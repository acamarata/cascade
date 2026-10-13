// Purpose: the attested permit list for restricted content. A restricted
//   request may use a controller-local lane, and an external lane ONLY when
//   the operator named that lane in [conductor] restricted_external_lanes.
// Inputs: the raw [conductor] table (runtime.Config.Extra["conductor"]).
// Outputs: an immutable RestrictedPermits value and the RouterOption that
//   hands it to a DefaultRouter.
// Constraints: the default is EMPTY (every external lane refused for
//   restricted content). The list is read once, at conductor construction; a
//   changed list applies at the next start. Adding a lane is a loosening that
//   config.accept_loosening attests (P1-SEC-04/-36 model that key); this file
//   only parses. A wildcard, a non-string entry or a non-list value refuses
//   KindInvalidInput rather than being read leniently.
// SPORT: conductor.restricted-permits/ADD (P1-SEC-02).

package conductor

import (
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// restrictedPermitsField is the key inside the [conductor] table.
const restrictedPermitsField = "restricted_external_lanes"

// RestrictedPermits is the set of external lane names restricted content may
// use. The zero value permits nothing.
type RestrictedPermits struct {
	lanes map[string]bool
}

// Permits reports whether lane is named in the list.
func (p RestrictedPermits) Permits(lane string) bool {
	return p.lanes[lane]
}

// ParseRestrictedPermits reads restricted_external_lanes from the [conductor]
// table. An absent table or key is the empty list. Every entry must be a
// non-empty exact lane name: "*" (or any entry containing one), a non-string
// entry, a blank or whitespace-padded entry, and a value that is not a list
// all refuse KindInvalidInput naming the key.
func ParseRestrictedPermits(conductorSection map[string]interface{}) (RestrictedPermits, error) {
	raw, present := conductorSection[restrictedPermitsField]
	if !present || raw == nil {
		return RestrictedPermits{}, nil
	}
	var items []interface{}
	switch v := raw.(type) {
	case []interface{}:
		items = v
	case []string:
		for _, s := range v {
			items = append(items, s)
		}
	default:
		return RestrictedPermits{}, permitsInvalid("must be a list of lane names, got %T", raw)
	}
	lanes := make(map[string]bool, len(items))
	for i, it := range items {
		name, ok := it.(string)
		if !ok {
			return RestrictedPermits{}, permitsInvalid("entry %d must be a string lane name, got %T", i, it)
		}
		if strings.Contains(name, "*") {
			return RestrictedPermits{}, permitsInvalid("entry %d (%q) is a wildcard; name each lane explicitly", i, name)
		}
		if name == "" || name != strings.TrimSpace(name) {
			return RestrictedPermits{}, permitsInvalid("entry %d (%q) is not an exact lane name", i, name)
		}
		lanes[name] = true
	}
	return RestrictedPermits{lanes: lanes}, nil
}

// permitsInvalid builds the KindInvalidInput refusal naming the key.
func permitsInvalid(format string, args ...any) error {
	return cascade.Newf(cascade.KindInvalidInput, "config: "+RestrictedPermitsKey+" "+format, args...)
}

// WithRestrictedPermits is the RouterOption that hands the parsed permit
// list to a router. A router built without it permits nothing.
func WithRestrictedPermits(p RestrictedPermits) RouterOption {
	return func(r *DefaultRouter) { r.restrictedPermits = p }
}
