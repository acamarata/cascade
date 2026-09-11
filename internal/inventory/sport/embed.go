// Purpose: embed the tracked registry.json artifact, mirroring
//
//	internal/inventory/embed.go's LoadGenerated precedent — an installed
//	binary with no source tree can still answer `cascade doctor sport`
//	from the same bytes a website fetching registry.json off GitHub would
//	read.
//
// Constraints: only encoding/json and embed; never a live source-tree
//
//	dependency (git, os.ReadDir over providers/, ...).
//
// SPORT: internal.inventory.sport.LoadRegistry/ADDED.

package sport

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed registry.json
var registryJSON []byte

// LoadRegistry parses the embedded registry.json artifact.
func LoadRegistry() (Registry, error) {
	var reg Registry
	if err := json.Unmarshal(registryJSON, &reg); err != nil {
		return Registry{}, fmt.Errorf("sport: parse embedded registry.json: %w", err)
	}
	return reg, nil
}
