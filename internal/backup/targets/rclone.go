// Purpose: RcloneTarget (05 §Epic S S-41.T3, §D-15) — the rclone backup
// Target: exec-only against the real rclone binary (no rclone Go-library
// linkage), covering the NAS/B2/WebDAV/Drive universe behind one tool.
// Put/Get/List/Delete map onto `rclone rcat`/`cat`/`lsjson --recursive`/
// `deletefile`. The version probe RcloneVersionProbe backs the
// `cascade doctor` check in doctor.go.
//
// Inputs: a remote spec (an rclone remote name + path, or a bare local
// path — whatever `rclone lsjson <spec>` accepts) and an RcloneRunner
// (production: execRcloneRunner over os/exec; test: a recording fake, so
// the default unit lane never spawns the real binary — Art.7.2's "unit
// tests make no network calls" extends here to "no process spawn
// either", matching the real-counterpart split every driver in this
// ticket follows).
// Outputs: real files under the configured remote, or a typed refusal.
// Constraints: an absent rclone binary is a typed KindUnsupported
// refusal at first use, never a silent skip.
//
// SPORT: internal.backup.targets.rclone/ADDED (P1-E19-W4-S41-T3).

package targets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

// rcloneBaseArgs are prepended to every invocation: an empty --config so
// no operator's real rclone.conf is silently consulted (the remote spec
// itself carries everything this driver needs, or names a bare local
// path), and bounded retries so a genuinely unreachable remote fails in
// seconds rather than rclone's own multi-minute backoff.
var rcloneBaseArgs = []string{"--config", "/dev/null", "--retries", "1", "--low-level-retries", "1"}

// RcloneRunner executes one rclone invocation. Production uses
// execRcloneRunner; tests inject a recording fake.
type RcloneRunner interface {
	Run(ctx context.Context, stdin io.Reader, args ...string) (stdout, stderr []byte, err error)
}

// execRcloneRunner is the production RcloneRunner, over os/exec.
type execRcloneRunner struct{}

// ErrRcloneBinaryAbsent is the typed, actionable refusal every method
// returns when the rclone binary is not on PATH — never a silent skip.
var ErrRcloneBinaryAbsent = errors.New("targets: rclone: binary not found on PATH; install rclone to use this target")

func (execRcloneRunner) Run(ctx context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "rclone", append(append([]string{}, rcloneBaseArgs...), args...)...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, nil, ErrRcloneBinaryAbsent
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

// RcloneTarget is the rclone backup Target driver. The zero value is not
// usable; construct with NewRcloneTarget.
type RcloneTarget struct {
	remote string
	runner RcloneRunner
	engine *egress.Engine
	cap    egress.Capability
}

// NewRcloneTarget returns a RcloneTarget writing under remote (an rclone
// remote spec or bare local path). runner nil uses the real binary.
func NewRcloneTarget(remote string, runner RcloneRunner, engine *egress.Engine) (*RcloneTarget, error) {
	if strings.TrimSpace(remote) == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "targets: rclone target requires a non-empty remote")
	}
	token, err := acquireBackupTargetCapability(engine)
	if err != nil {
		return nil, err
	}
	if runner == nil {
		runner = execRcloneRunner{}
	}
	return &RcloneTarget{remote: strings.TrimRight(remote, "/"), runner: runner, engine: engine, cap: token}, nil
}

// path joins t.remote and key with a single "/", rclone's own path
// separator regardless of host OS.
func (t *RcloneTarget) path(key string) string { return t.remote + "/" + key }

// classifyRcloneErr maps a run's (err, stderr) pair to the taxonomy.
// rclone's exit codes are coarse (nonzero = "some error"), so
// classification reads stderr's text — the only structured signal this
// exec-only integration has.
func classifyRcloneErr(err error, stderr []byte) cascade.Kind {
	if errors.Is(err, ErrRcloneBinaryAbsent) {
		return cascade.KindUnsupported
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return cascade.KindTimeout
	}
	if errors.Is(err, context.Canceled) {
		return cascade.KindCanceled
	}
	msg := strings.ToLower(string(stderr))
	switch {
	case strings.Contains(msg, "not found"):
		return cascade.KindNotFound
	case strings.Contains(msg, "permission denied") || strings.Contains(msg, "access denied"):
		return cascade.KindPermissionDenied
	default:
		return cascade.KindUnavailable
	}
}

// Put implements Target via `rclone rcat`.
func (t *RcloneTarget) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "targets: rclone: reading content")
	}
	out, err := interceptOutbound(ctx, t.engine, t.cap, data)
	if err != nil {
		return err
	}
	_, stderr, err := t.runner.Run(ctx, bytes.NewReader(out), "rcat", t.path(key))
	if err != nil {
		return cascade.Wrapf(classifyRcloneErr(err, stderr), err, "targets: rclone: put %q", key)
	}
	return nil
}

// Get implements Target via `rclone cat`.
func (t *RcloneTarget) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxErr(err)
	}
	stdout, stderr, err := t.runner.Run(ctx, nil, "cat", t.path(key))
	if err != nil {
		return nil, cascade.Wrapf(classifyRcloneErr(err, stderr), err, "targets: rclone: get %q", key)
	}
	return io.NopCloser(bytes.NewReader(stdout)), nil
}

// List implements Target via `rclone lsjson --recursive`, filtered
// client-side by prefix (matching FSTarget/S3Target's exact contract:
// prefix is a string match on the key, not necessarily a directory).
func (t *RcloneTarget) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxErr(err)
	}
	stdout, stderr, err := t.runner.Run(ctx, nil, "lsjson", "--recursive", t.remote)
	if err != nil {
		if classifyRcloneErr(err, stderr) == cascade.KindNotFound {
			return nil, nil
		}
		return nil, cascade.Wrapf(classifyRcloneErr(err, stderr), err, "targets: rclone: listing %q", prefix)
	}
	all, err := DecodeRcloneListOutput(stdout)
	if err != nil {
		return nil, err
	}
	return filterAndSortByPrefix(all, prefix), nil
}

// Delete implements Target via `rclone deletefile`. Deleting an absent
// key is not an error.
func (t *RcloneTarget) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	_, stderr, err := t.runner.Run(ctx, nil, "deletefile", t.path(key))
	if err != nil {
		if classifyRcloneErr(err, stderr) == cascade.KindNotFound {
			return nil
		}
		return cascade.Wrapf(classifyRcloneErr(err, stderr), err, "targets: rclone: delete %q", key)
	}
	return nil
}
