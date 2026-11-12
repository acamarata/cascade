package antigravity

// Purpose: configure and validate the host-supplied spawn boundary.
// Inputs: harness configuration and a projected job specification.
// Outputs: a probe-decided driver or a typed pre-spawn refusal.
// Constraints: no inherited environment or unverified credential variables.
// SPORT: providers.agents.antigravity/ADD (P1-AGT-07e).

import (
	"slices"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Clock is the injected runtime time source.
type Clock interface{ Now() time.Time }

// Config supplies the executable, compliance posture and custody seams.
// Auth is fixture-only until an auth source has been recorded.
type Config struct {
	Binary       string
	Posture      provider.CompliancePosture
	Defaults     provider.DriverDefaults
	RuntimeHome  string
	Clock        Clock
	ContentMatch func([]byte) (string, bool)
}

// Spawn refusal sentinels are returned directly, preserving identity.
var (
	ErrEmptySpawnEnv      = cascade.New(cascade.KindInvalidInput, "antigravity: spawn environment is empty")
	ErrEmptyWorktreeRoot  = cascade.New(cascade.KindInvalidInput, "antigravity: worktree root is empty")
	ErrEnvNotAllowed      = cascade.New(cascade.KindPolicyDenied, "antigravity: spawn environment carries a variable outside the allowlist")
	ErrHomeMismatch       = cascade.New(cascade.KindPermissionDenied, "antigravity: HOME is not the spawn env home")
	ErrNoClock            = cascade.New(cascade.KindInvalidInput, "antigravity: Config.Clock is nil")
	ErrDuplicateJob       = cascade.New(cascade.KindConflict, "antigravity: job id already live")
	ErrStart              = cascade.New(cascade.KindUnavailable, "antigravity: print process failed to start")
	ErrNonZeroExit        = cascade.New(cascade.KindUnavailable, "antigravity: print process exited non-zero")
	ErrRunCancelled       = cascade.New(cascade.KindCanceled, "antigravity: job cancelled")
	errUnsupportedModelOp = cascade.New(cascade.KindUnsupported, "antigravity: the agent driver has no model-call surface")
)

func (d *Driver) preflight(spec provider.AgentJobSpec) error {
	if d.refusal != nil {
		return d.refusal
	}
	if !d.cfg.Posture.ProgrammaticEntitlement {
		return provider.ErrEntitlement
	}
	if len(spec.Env.Driver) == 0 {
		return ErrEmptySpawnEnv
	}
	if spec.WorktreeRoot == "" {
		return ErrEmptyWorktreeRoot
	}
	if spec.Filter == nil {
		return provider.ErrEgressSubstitutionFailed
	}
	if err := d.checkEnv(spec.Env); err != nil {
		return err
	}
	if d.cfg.Clock == nil {
		return ErrNoClock
	}
	if err := spec.Envelope.Check(spec.WorktreeRoot, d.cfg.Clock.Now()); err != nil {
		return err
	}
	if d.cfg.ContentMatch != nil {
		return provider.PreSpawnScan(spec.WorktreeRoot, spec.Scope, d.cfg.ContentMatch)
	}
	return nil
}

func (d *Driver) checkEnv(env provider.SpawnEnv) error {
	allow := provider.DriverEnvAllowlist()
	values := make(map[string]string)
	for _, kv := range env.Driver {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || !slices.Contains(allow, key) || strings.ContainsRune(value, 0) {
			return ErrEnvNotAllowed
		}
		if _, duplicate := values[key]; duplicate {
			return ErrEnvNotAllowed
		}
		values[key] = value
	}
	if env.Home == "" || values["HOME"] != env.Home {
		return ErrHomeMismatch
	}
	if d.cfg.RuntimeHome != "" && env.Home != d.cfg.RuntimeHome {
		return ErrHomeMismatch
	}
	return nil
}
