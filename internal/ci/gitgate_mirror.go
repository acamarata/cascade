package ci

// Purpose: mirror an accepted branch to the one remote [ci].mirror_remote
// names, fast-forward only, as the host's git user through its credential
// helper. Enqueue runs after acceptance and only queues; Run is the worker.
// Inputs: an accepted branch, its tip commit and the attestation id that
// authorized it; the injected git, queue, effect-log and attention seams.
// Outputs: intent and confirm effect records naming the attestation, one
// attention item per failed push, and a MirrorPush for git-gate status.
// Constraints: off unless the remote is set AND ci.mirror is granted (then
// no git process starts). Never --mirror, --force, a tag ref or anything but
// the accepted tip. A failed push never reverses acceptance and never makes
// Run return an error. No token is read, written or passed on.
// SPORT: internal.ci.MirrorPusher/ADDED, internal.ci.MirrorCapability/ADDED.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MirrorCapability is the policy capability that must be granted before any
// accepted branch leaves the machine.
const MirrorCapability = "ci.mirror"

// MirrorEffectPrefix starts every mirror effect key. The outbox integration
// site is shared with job advance and scheduler cancel, so a backlog count
// must filter on this prefix.
const MirrorEffectPrefix = "ci.mirror:"

// MirrorPushArgv is the whole argument list of the one push the worker runs:
// the tip to the branch ref on the remote. No --mirror, --force, --tags or
// other option is ever added; the remote and branch are validated first.
func MirrorPushArgv(remote, tip, branch string) []string {
	return []string{"git", "push", "--porcelain", "--", remote, tip + ":refs/heads/" + branch}
}

// mirrorEnvKeep are the only variables the push child inherits: enough for
// git to find the user's credential helper and agent, nothing token-shaped.
var mirrorEnvKeep = map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
	"USERPROFILE": true, "SSH_AUTH_SOCK": true, "XDG_CONFIG_HOME": true, "LANG": true}

// MirrorPushEnv filters parent down to mirrorEnvKeep and turns prompting off,
// so no secret in the caller's environment reaches the child.
func MirrorPushEnv(parent []string) []string {
	out := make([]string, 0, len(mirrorEnvKeep)+1)
	for _, kv := range parent {
		if k, _, ok := strings.Cut(kv, "="); ok && mirrorEnvKeep[k] {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_TERMINAL_PROMPT=0")
}

// MirrorJob is one queued push.
type MirrorJob struct {
	Remote, Branch, Tip, Attestation string
}

// MirrorGit is the git surface the worker needs.
type MirrorGit interface {
	// RemoteTip returns the remote branch tip, "" when the ref is absent.
	RemoteTip(ctx context.Context, remote, branch string) (string, error)
	// IsAncestor reports whether ancestor is reachable from tip.
	IsAncestor(ctx context.Context, ancestor, tip string) (bool, error)
	// Push runs `git push <remote> <tip>:refs/heads/<branch>` and nothing else.
	Push(ctx context.Context, remote, tip, branch string) error
}

// MirrorQueue holds a job until the worker runs it (the jobs outbox).
type MirrorQueue interface {
	Enqueue(ctx context.Context, job MirrorJob) error
}

// MirrorPusher queues and performs mirror pushes.
type MirrorPusher struct {
	Remote string
	// Granted reports whether ci.mirror is granted; nil means not granted.
	Granted func(ctx context.Context) bool
	// Admit acquires the egress capability for the push; nil refuses.
	Admit     func(ctx context.Context) error
	Git       MirrorGit
	Queue     MirrorQueue
	Effects   audit.EffectWriter
	Pending   func(ctx context.Context) ([]audit.EffectHandle, error)
	Attention func(ctx context.Context, sourceRef string)
	Record    func(MirrorPush)
	Now       func() time.Time
}

func (m *MirrorPusher) enabled(ctx context.Context) bool {
	return strings.TrimSpace(m.Remote) != "" && m.Granted != nil && m.Granted(ctx)
}

// Enqueue queues a push of tip as branch after acceptance. With the remote
// unset or the capability absent it does nothing and reports no failure.
func (m *MirrorPusher) Enqueue(ctx context.Context, acceptedBranch, tipSHA, attestationID string) error {
	if !m.enabled(ctx) {
		return nil
	}
	job := MirrorJob{Remote: m.Remote, Branch: acceptedBranch, Tip: tipSHA, Attestation: attestationID}
	if err := validateMirrorJob(job); err != nil {
		return err
	}
	return m.Queue.Enqueue(ctx, job)
}

func validateMirrorJob(j MirrorJob) error {
	b := j.Branch
	if b == "" || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "refs/") || strings.Contains(b, "..") ||
		strings.ContainsAny(b, " \t\n:^~?*[\\") {
		return cascade.New(cascade.KindInvalidInput, "ci mirror: not a plain branch name")
	}
	if !branchObjectID(j.Tip) {
		return cascade.New(cascade.KindInvalidInput, "ci mirror: tip is not a full commit id")
	}
	if j.Attestation == "" {
		return cascade.New(cascade.KindInvalidInput, "ci mirror: an attestation id is required")
	}
	return nil
}

// MirrorEffectKey is "ci.mirror:<remote>:<branch>:<tip>" with characters the
// effect log refuses replaced; a key over the 128 byte cap hashes its middle.
func MirrorEffectKey(j MirrorJob) string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
				return r
			}
			return '_'
		}, s)
	}
	key := MirrorEffectPrefix + clean(j.Remote) + ":" + clean(j.Branch) + ":" + j.Tip
	if len(key) > 128 {
		sum := sha256.Sum256([]byte(j.Remote + "\x00" + j.Branch))
		key = MirrorEffectPrefix + hex.EncodeToString(sum[:12]) + ":" + j.Tip
	}
	return key
}

// Run performs one queued push. A push that fails is recorded, raises one
// attention item and returns nil: acceptance already happened and stays.
// An error is returned only when the effect log itself cannot be written.
func (m *MirrorPusher) Run(ctx context.Context, job MirrorJob) error {
	if !m.enabled(ctx) || job.Remote != m.Remote {
		return nil
	}
	if err := validateMirrorJob(job); err != nil {
		return err
	}
	h, ok, err := m.begin(ctx, job)
	if err != nil || !ok {
		return err
	}
	outcome, perr := m.perform(ctx, job)
	if perr != nil {
		return m.failed(ctx, h, perr)
	}
	if _, err := m.Effects.ConfirmEffect(ctx, h, outcome); err != nil {
		return err
	}
	m.record(true, "")
	return nil
}

// begin writes the intent, or finds the open one a crash left behind. ok is
// false when the key already ended, so nothing may run again.
func (m *MirrorPusher) begin(ctx context.Context, job MirrorJob) (audit.EffectHandle, bool, error) {
	explain, err := json.Marshal(map[string]string{"attestation": job.Attestation, "remote": job.Remote,
		"branch": job.Branch, "tip": job.Tip})
	if err != nil {
		return audit.EffectHandle{}, false, err
	}
	sum := sha256.Sum256(explain)
	key := MirrorEffectKey(job)
	h, err := m.Effects.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindExternalEffect, Actor: "ci.mirror",
		Action: "mirror-push", Key: key, ParamsHash: hex.EncodeToString(sum[:]), Explain: explain})
	if err == nil {
		return h, true, nil
	}
	if !errors.Is(err, audit.ErrEffectExists) || m.Pending == nil {
		return audit.EffectHandle{}, false, err
	}
	open, perr := m.Pending(ctx)
	if perr != nil {
		return audit.EffectHandle{}, false, perr
	}
	for _, p := range open {
		if p.Key == key {
			return p, true, nil
		}
	}
	return audit.EffectHandle{}, false, nil
}

// perform probes the remote ref first: a tip already there means a crashed
// earlier run pushed it, so it is confirmed without a second push. A remote
// ref that is not an ancestor of the tip is refused before any push.
func (m *MirrorPusher) perform(ctx context.Context, job MirrorJob) (string, error) {
	if m.Admit == nil {
		return "", cascade.New(cascade.KindPolicyDenied, "ci mirror: no egress capability")
	}
	if err := m.Admit(ctx); err != nil {
		return "", err
	}
	remoteTip, err := m.Git.RemoteTip(ctx, job.Remote, job.Branch)
	if err != nil {
		return "", err
	}
	if remoteTip == job.Tip {
		return "already-present", nil
	}
	if remoteTip != "" {
		ff, err := m.Git.IsAncestor(ctx, remoteTip, job.Tip)
		if err != nil || !ff {
			return "", cascade.New(cascade.KindConflict, "ci mirror: the remote branch is not an ancestor of the tip; refusing a non-fast-forward push")
		}
	}
	if err := m.Git.Push(ctx, job.Remote, job.Tip, job.Branch); err != nil {
		return "", err
	}
	return "pushed", nil
}

func (m *MirrorPusher) failed(ctx context.Context, h audit.EffectHandle, cause error) error {
	if _, err := m.Effects.FailEffect(ctx, h, cause.Error()); err != nil {
		return err
	}
	m.record(false, cause.Error())
	if m.Attention != nil {
		m.Attention(ctx, h.Key)
	}
	return nil
}

func (m *MirrorPusher) record(ok bool, msg string) {
	if m.Record == nil {
		return
	}
	var at time.Time
	if m.Now != nil {
		at = m.Now()
	}
	m.Record(MirrorPush{OK: ok, At: at, Error: msg})
}
