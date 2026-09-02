package runtime

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Purpose: the `cascade config` CLI-facing handlers — ListEffectiveHandler
//   (`cascade config list --effective`) and PathHandler (`cascade config
//   path`). Split out of config.go per R-14.117 (Art.10.3 file-cap
//   remedy) — behaviour-preserving, moved code only.
//
// CASCADE-ALLOW: P1-E03-W1-S04-T1 these handlers are the real, fully
// implemented capability behind `cascade config list --effective` and
// `cascade config path`; only the cobra command mounting is deferred,
// because the cobra root does not exist in this tree yet (D/S-06.T1,
// forward-stub per 06-FORGE-SPEC §5.19). Both handlers are unit-tested
// directly against a *Config/PathProvider, independent of any CLI layer.
// SPORT: runtime/config (ADD, placeholder per T-1 sport_updates).

// ListEffectiveHandler renders cfg's effective view for
// `cascade config list --effective`. jsonOut selects JSON over the
// human-readable table.
func ListEffectiveHandler(cfg *Config, jsonOut bool) (string, error) {
	entries := cfg.EffectiveEntries()
	if jsonOut {
		b, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s = %v (%s)\n", e.Key, e.Value, e.Source)
	}
	return b.String(), nil
}

// PathHandler renders paths' resolved locations for `cascade config path`.
func PathHandler(paths PathProvider, jsonOut bool) (string, error) {
	rows := map[string]string{
		"root":   paths.Root(),
		"config": paths.ConfigPath(),
		"socket": paths.SocketPath(),
		"data":   paths.DataDir(),
		"log":    paths.LogDir(),
	}
	if jsonOut {
		b, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %s\n", k, rows[k])
	}
	return b.String(), nil
}
