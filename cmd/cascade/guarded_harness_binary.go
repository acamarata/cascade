// Purpose: registers [fleet].harness_binary as a guarded config family. The
// list names the harness binaries the fleet may launch, so any change to it
// is a loosening that must be accepted before it takes effect.
// Inputs: the config tree. Outputs: a RegisterGuardedFamily call at init.
// Constraints: an absent key is the shipped default; anything but a list of
// absolute path strings is an Extract error, never a default.
// SPORT: cmd/cascade guarded harness binary.

package main

import (
	"encoding/json"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

var harnessBinarySpec = table(map[string]runtime.FieldSpec{"harness_binary": ruleAny}, nil)

func init() {
	runtime.RegisterGuardedFamily(harnessBinarySpec.Bind(runtime.GuardedFamily{
		Section: "fleet", Keys: []string{"fleet.harness_binary"}, Extract: extractHarnessBinaryFamily,
	}))
}

// errHarnessBinaryShape is the one refusal for a malformed list.
var errHarnessBinaryShape = cascade.New(cascade.KindInvalidInput, "fleet.harness_binary must be a list of absolute paths")

// extractHarnessBinaryFamily returns {"harness_binary": <list>}, or nil when
// the key is absent or the list is empty.
func extractHarnessBinaryFamily(extra map[string]interface{}) (json.RawMessage, error) {
	fleet, err := subTable(extra, "fleet")
	if err != nil {
		return nil, err
	}
	raw, ok := fleet["harness_binary"]
	if !ok || raw == nil {
		return nil, nil
	}
	var list []string
	switch v := raw.(type) {
	case []string:
		list = v
	case []interface{}:
		for _, x := range v {
			s, isStr := x.(string)
			if !isStr {
				return nil, errHarnessBinaryShape
			}
			list = append(list, s)
		}
	default:
		return nil, errHarnessBinaryShape
	}
	for _, p := range list {
		if !filepath.IsAbs(p) {
			return nil, errHarnessBinaryShape
		}
	}
	if len(list) == 0 {
		return nil, nil
	}
	return json.Marshal(map[string]interface{}{"harness_binary": list})
}
