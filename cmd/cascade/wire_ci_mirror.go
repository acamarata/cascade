//go:build !windows

package main

// Purpose: composes the CI mirror pusher for the daemon: the git runner, the
// jobs outbox queue, the host-scoped egress admission and the ci.mirror grant
// check. The wiring is inert while [ci].mirror_remote is empty.
// Inputs: the live config, the jobs database, the effect log, the policy store.
// Outputs: a *ci.MirrorPusher; one pending-push row per enqueue on the
// integration outbox site, keyed by the mirror effect key.
// Constraints: the push runs the argv ci.MirrorPushArgv builds, in a filtered
// environment, as the host's git user through its credential helper. No
// token is read, written or passed. Off unless remote set AND granted.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const mirrorSubsystem = "ci-mirror"

var _ = registerDaemonWiring(daemonRegistration{Name: mirrorSubsystem, Phase: phaseSupervised, Order: 25, Wire: wireCIMirror})

// execMirrorGit runs git in Dir with the filtered environment.
type execMirrorGit struct {
	Dir string
	Env []string
}

func (g execMirrorGit) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir, cmd.Env = g.Dir, g.Env
	out, err := cmd.Output()
	if err != nil {
		return out, cascade.Wrap(cascade.KindUnavailable, err, "ci mirror: git "+args[0]+" failed")
	}
	return out, nil
}

func (g execMirrorGit) RemoteTip(ctx context.Context, remote, branch string) (string, error) {
	out, err := g.run(ctx, "ls-remote", "--", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	if f := strings.Fields(string(out)); len(f) > 0 {
		return f[0], nil
	}
	return "", nil
}

func (g execMirrorGit) IsAncestor(ctx context.Context, ancestor, tip string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", ancestor, tip)
	cmd.Dir, cmd.Env = g.Dir, g.Env
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

func (g execMirrorGit) Push(ctx context.Context, remote, tip, branch string) error {
	argv := ci.MirrorPushArgv(remote, tip, branch)
	_, err := g.run(ctx, argv[1:]...)
	return err
}

// mirrorOutboxQueue records one integration-site outbox intent per push. The
// job id is the mirror effect key, so the doctor can tell these rows from
// job-advance and scheduler-cancel rows on the same site.
type mirrorOutboxQueue struct {
	db    *sql.DB
	clock runtime.Clock
}

func (q mirrorOutboxQueue) Enqueue(ctx context.Context, j ci.MirrorJob) error {
	sum := sha256.Sum256([]byte(j.Attestation))
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci mirror: begin outbox transaction")
	}
	defer func() { _ = tx.Rollback() }()
	in := jobs.OutboxIntent{JobID: ci.MirrorEffectKey(j), Site: jobs.OutboxSiteIntegration, PayloadHash: hex.EncodeToString(sum[:])}
	if _, err := jobs.RecordIntent(ctx, tx, q.clock.Now().UnixMilli(), in); err != nil {
		return err
	}
	return tx.Commit()
}

// mirrorAdmit refuses any host other than the one [ci].mirror_remote names;
// the class itself ships disabled, so this is the run-time scope.
func mirrorAdmit(remote string) func(context.Context) error {
	return func(context.Context) error {
		if _, err := runtime.MirrorRemoteHost(remote); err != nil {
			return cascade.New(cascade.KindPolicyDenied, "ci mirror: the remote has no host the egress scope can name")
		}
		return nil
	}
}

// mirrorGranted evaluates the ci.mirror capability through the policy engine.
func mirrorGranted(w *daemonWiring, remote string) func(context.Context) bool {
	return func(ctx context.Context) bool {
		engine, _, err := githubCIEngine(ctx, w.Store, w.Clock, w.Paths)
		if err != nil {
			return false
		}
		out, err := engine.Evaluate(ctx, policy.EvalRequest{Subject: githubCISubject, Capability: ci.MirrorCapability,
			CommandLess: true, Action: "git push " + remote, Summary: "mirror an accepted branch"})
		return err == nil && out.Verdict == policy.VerdictAllow
	}
}

func wireCIMirror(w *daemonWiring) error {
	if w.Deps.Fleet == nil || w.Deps.Reloader == nil {
		w.Manifest.Skipped(mirrorSubsystem, "no fleet runtime or config reloader")
		return nil
	}
	remote, err := w.Deps.Reloader.Current().CIMirrorRemote()
	if err != nil {
		return err
	}
	if remote == "" {
		w.Manifest.Skipped(mirrorSubsystem, "[ci].mirror_remote is empty")
		return nil
	}
	effects, err := openEffectLog(w.Store, w.Clock, w.Bus, w.Paths)
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	pusher := &ci.MirrorPusher{Remote: remote, Granted: mirrorGranted(w, remote), Admit: mirrorAdmit(remote),
		Git:   execMirrorGit{Dir: cwd, Env: ci.MirrorPushEnv(os.Environ())},
		Queue: mirrorOutboxQueue{db: w.Deps.Fleet.JobsDB(), clock: w.Clock}, Effects: effects, Pending: effects.PendingEffects, Now: w.Clock.Now}
	_ = pusher
	w.Manifest.Skipped(mirrorSubsystem, "armed; the accepted job event carries no branch name to enqueue")
	return nil
}
