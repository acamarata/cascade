// Purpose: the daemon's [daemon] and [nodes] config resolution, split out
//   of daemon.go under Art.10.3's 300-line cap. Settings is the shape the
//   composition root reads; this file is how it is filled.
// SPORT: internal/daemon (CHANGED, P1-E17-W4-S37-T2 settings split).

package daemon

import (
	"github.com/acamarata/cascade/internal/runtime"
	syncpkg "github.com/acamarata/cascade/internal/sync"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ResolveSettings reads the [daemon] and [nodes] sections out of cfg.Extra
// (neither is a typed Config field — only runtime/elevation/logging are, as
// of C/S-04.T1; everything else round-trips through Extra, per
// config_write.go's knownConfigKeys doc) and resolves Settings against
// paths' derived socket default. A malformed shutdown_grace value (present
// but neither a Go duration string nor a plain number of seconds) is a
// typed KindInvalidInput error — bad config is never silently ignored.
func ResolveSettings(cfg *runtime.Config, paths runtime.PathProvider) (Settings, error) {
	s := Settings{SocketPath: paths.SocketPath()}
	if cfg == nil || cfg.Extra == nil {
		return s, nil
	}
	nodesSection, err := resolveNodesSection(cfg)
	if err != nil {
		return Settings{}, err
	}
	s.Nodes = nodesSection
	// The [sync] overrides. A malformed or WIDENING entry is a typed
	// error, never a silently-dropped key: a class an operator set and
	// the daemon ignored would be a policy they believed was in force.
	syncCfg, err := syncpkg.ParseSection(cfg.Extra, nil)
	if err != nil {
		return Settings{}, err
	}
	s.Sync = syncCfg
	section, ok := cfg.Extra["daemon"].(map[string]interface{})
	if !ok {
		return s, nil
	}
	if sock, ok := section["socket"].(string); ok && sock != "" {
		s.SocketPath = sock
	}
	raw, present := section["shutdown_grace"]
	if !present {
		return s, nil
	}
	grace, err := parseGraceValue(raw)
	if err != nil {
		return Settings{}, cascade.Wrapf(cascade.KindInvalidInput, err,
			"daemon.shutdown_grace: %v", raw)
	}
	s.ShutdownGrace = grace
	s.GraceSet = true
	return s, nil
}
