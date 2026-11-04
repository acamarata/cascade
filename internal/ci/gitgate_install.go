package ci

// Purpose: the gated repo id half of contract:receive-gate. The elevated
// install binds the canonical gate dir in the repo registry, then writes the
// registry id to the gated repo's git config key cascade.gate.repoId; the
// pre-receive verb reads that key back as AttestWant.RepoID.
//
// Inputs: a gated repository path (bare, shallow or full) and a registry id.
// Outputs: the id read from git config, or a typed refusal.
// Constraints: only git config is read, never history, so shallow and bare
// repos resolve the same way. A repo without the key refuses every push.
// The binding is written before the config key, and a binding error leaves
// nothing written (R18d MA-1, R18e MA-1b).

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// GateRepoIDConfigKey is the git config key that names a gated repo's
// registry id.
const GateRepoIDConfigKey = "cascade.gate.repoId"

// ErrGateRepoIDMissing refuses a push into a gated repo whose git config has
// no cascade.gate.repoId.
var ErrGateRepoIDMissing = cascade.New(cascade.KindPolicyDenied, "ci: the gated repo has no cascade.gate.repoId; re-run node git-gate install")

// GateBinder records the registry binding of a canonical gate dir to a repo
// id. jobs.RepoRegistry satisfies it.
type GateBinder interface {
	BindGate(ctx context.Context, gateDir, id string) error
}

// GateRepoIDFromConfig returns the registry id stored under
// cascade.gate.repoId in repoPath's git config. An absent or empty key
// returns ErrGateRepoIDMissing.
func GateRepoIDFromConfig(ctx context.Context, repoPath string) (string, error) {
	out, err := baselineGit(ctx, repoPath, "config", "--get", GateRepoIDConfigKey)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", ErrGateRepoIDMissing
	}
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "ci: read cascade.gate.repoId")
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", ErrGateRepoIDMissing
	}
	return id, nil
}

// BindGateRepoID binds repoPath's gate dir to repoID through binder (the
// controller's registry; nil on a node), then writes cascade.gate.repoId.
// A binding error, including jobs.ErrRepoNotRegistered for an id the
// registry does not know, leaves the git config untouched.
func BindGateRepoID(ctx context.Context, repoPath, repoID string, binder GateBinder) error {
	if repoPath == "" || repoID == "" {
		return cascade.New(cascade.KindInvalidInput, "ci: git-gate install requires a repo path and a registry id")
	}
	if binder != nil {
		if err := binder.BindGate(ctx, repoPath, repoID); err != nil {
			return err
		}
	}
	if _, err := baselineGit(ctx, repoPath, "config", GateRepoIDConfigKey, repoID); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: write cascade.gate.repoId")
	}
	return nil
}

// ResolveGateWant fills want.RepoID from the gated repo's git config. On the
// controller role (gates non-nil) the config value must equal the registry
// binding for want.GateDir; a rewritten config id refuses.
func ResolveGateWant(ctx context.Context, repoPath string, want AttestWant, gates GateBinding) (AttestWant, error) {
	id, err := GateRepoIDFromConfig(ctx, repoPath)
	if err != nil {
		return AttestWant{}, err
	}
	want.RepoID = id
	if gates != nil {
		bound, err := gates.GateRepoID(ctx, want.GateDir)
		if err != nil {
			return AttestWant{}, err
		}
		if bound != id {
			return AttestWant{}, cascade.New(cascade.KindPolicyDenied, "ci: the gate's repo id disagrees with its registry binding")
		}
	}
	return want, nil
}

var _ GateBinder = (*jobs.RepoRegistry)(nil)
