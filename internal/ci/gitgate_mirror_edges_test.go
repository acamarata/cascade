package ci

// Purpose: error and edge branches of the mirror pusher and its backlog
// check: job validation, effect-key hashing, crash-recovery lookups, egress
// admission, non-fast-forward refusal and effect-log write failures.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/doctor"
)

var errSeam = errors.New("seam failure")

// errMirrorGit fails the chosen call and otherwise behaves like the fake.
type errMirrorGit struct {
	fakeMirrorGit
	tipErr, ancErr error
}

func (g *errMirrorGit) RemoteTip(ctx context.Context, r, b string) (string, error) {
	if g.tipErr != nil {
		return "", g.tipErr
	}
	return g.fakeMirrorGit.RemoteTip(ctx, r, b)
}

func (g *errMirrorGit) IsAncestor(ctx context.Context, a, t string) (bool, error) {
	if g.ancErr != nil {
		return false, g.ancErr
	}
	return g.fakeMirrorGit.IsAncestor(ctx, a, t)
}

// errEffects fails the chosen effect-log write.
type errEffects struct {
	*fakeEffects
	beginErr, confirmErr, failErr error
}

func (e *errEffects) BeginEffect(ctx context.Context, r audit.EffectRequest) (audit.EffectHandle, error) {
	if e.beginErr != nil {
		return audit.EffectHandle{}, e.beginErr
	}
	return e.fakeEffects.BeginEffect(ctx, r)
}

func (e *errEffects) ConfirmEffect(ctx context.Context, h audit.EffectHandle, o string) (audit.Record, error) {
	if e.confirmErr != nil {
		return audit.Record{}, e.confirmErr
	}
	return e.fakeEffects.ConfirmEffect(ctx, h, o)
}

func (e *errEffects) FailEffect(ctx context.Context, h audit.EffectHandle, c string) (audit.Record, error) {
	if e.failErr != nil {
		return audit.Record{}, e.failErr
	}
	return e.fakeEffects.FailEffect(ctx, h, c)
}

func TestMirrorEnqueueRejectsBadJobs(t *testing.T) {
	r := newMirrorRig(mirrorRemote, true)
	cases := map[string][3]string{
		"empty branch":     {"", mirrorTip, "a"},
		"option branch":    {"-f", mirrorTip, "a"},
		"full ref":         {"refs/tags/v1", mirrorTip, "a"},
		"dotdot":           {"a..b", mirrorTip, "a"},
		"colon":            {"a:b", mirrorTip, "a"},
		"short tip":        {"main", "abc123", "a"},
		"no attestation":   {"main", mirrorTip, ""},
		"whitespace":       {"a b", mirrorTip, "a"},
		"glob in branch":   {"a*", mirrorTip, "a"},
		"backslash branch": {"a\\b", mirrorTip, "a"},
	}
	for name, c := range cases {
		err := r.p.Enqueue(context.Background(), c[0], c[1], c[2])
		if err == nil {
			t.Errorf("%s: Enqueue accepted %v", name, c)
		}
	}
	if len(r.queue.jobs) != 0 {
		t.Fatalf("queued %d jobs from invalid input", len(r.queue.jobs))
	}
}

func TestMirrorRunSkipsOtherRemoteAndRejectsInvalid(t *testing.T) {
	r := newMirrorRig(mirrorRemote, true)
	other := r.job()
	other.Remote = "ssh://git@elsewhere.example.org/y.git"
	if err := r.p.Run(context.Background(), other); err != nil || len(r.effects.reqs) != 0 {
		t.Fatalf("job for another remote: err %v, effects %d; want a silent skip", err, len(r.effects.reqs))
	}
	bad := r.job()
	bad.Attestation = ""
	if err := r.p.Run(context.Background(), bad); err == nil || len(r.git.pushes) != 0 {
		t.Fatalf("invalid job: err %v, pushes %d; want an error and no push", err, len(r.git.pushes))
	}
}

func TestMirrorEffectKeyCleansAndHashesLongKeys(t *testing.T) {
	short := MirrorEffectKey(MirrorJob{Remote: "ssh://h/x.git", Branch: "feat/x", Tip: mirrorTip})
	if !strings.HasPrefix(short, MirrorEffectPrefix+"ssh___h_x.git:feat_x:") {
		t.Fatalf("short key %q not cleaned", short)
	}
	long := MirrorEffectKey(MirrorJob{Remote: strings.Repeat("r", 100), Branch: strings.Repeat("b", 100), Tip: mirrorTip})
	if len(long) > 128 || !strings.HasSuffix(long, ":"+mirrorTip) {
		t.Fatalf("long key %q (%d bytes) must fit 128 and end in the tip", long, len(long))
	}
	other := MirrorEffectKey(MirrorJob{Remote: strings.Repeat("r", 100), Branch: strings.Repeat("c", 100), Tip: mirrorTip})
	if other == long {
		t.Fatal("different branches must hash to different keys")
	}
}

func TestMirrorBeginErrorPaths(t *testing.T) {
	ctx := context.Background()
	r := newMirrorRig(mirrorRemote, true)
	r.p.Effects = &errEffects{fakeEffects: r.effects, beginErr: errSeam}
	if err := r.p.Run(ctx, r.job()); !errors.Is(err, errSeam) {
		t.Fatalf("begin failure returned %v, want it surfaced", err)
	}
	// Key already ended and no Pending lookup: nothing may run, no error.
	r = newMirrorRig(mirrorRemote, true)
	r.effects.phase[MirrorEffectKey(r.job())] = "confirmed"
	r.p.Pending = nil
	if err := r.p.Run(ctx, r.job()); !errors.Is(err, audit.ErrEffectExists) || len(r.git.pushes) != 0 {
		t.Fatalf("existing key without Pending: err %v, pushes %d", err, len(r.git.pushes))
	}
	// Pending lookup fails.
	r = newMirrorRig(mirrorRemote, true)
	r.effects.phase[MirrorEffectKey(r.job())] = "intent"
	r.p.Pending = func(context.Context) ([]audit.EffectHandle, error) { return nil, errSeam }
	if err := r.p.Run(ctx, r.job()); !errors.Is(err, errSeam) {
		t.Fatalf("pending failure returned %v, want it surfaced", err)
	}
	// Open intents exist but none carries this key: skip quietly.
	r = newMirrorRig(mirrorRemote, true)
	r.effects.phase[MirrorEffectKey(r.job())] = "intent"
	r.p.Pending = func(context.Context) ([]audit.EffectHandle, error) {
		return []audit.EffectHandle{{Key: "ci.mirror:other"}}, nil
	}
	if err := r.p.Run(ctx, r.job()); err != nil || len(r.git.pushes) != 0 {
		t.Fatalf("foreign pending intent: err %v, pushes %d; want a quiet skip", err, len(r.git.pushes))
	}
}

func TestMirrorPerformRefusals(t *testing.T) {
	ctx := context.Background()
	key := MirrorEffectKey(newMirrorRig(mirrorRemote, true).job())
	check := func(name string, r *mirrorRig) {
		t.Helper()
		if err := r.p.Run(ctx, r.job()); err != nil {
			t.Fatalf("%s: Run = %v; a refused push must not fail Run", name, err)
		}
		if r.effects.phase[key] != "failed" || len(r.attention) != 1 || len(r.git.pushes) != 0 {
			t.Fatalf("%s: phase %q, attention %d, pushes %d; want failed, one item, no push", name, r.effects.phase[key], len(r.attention), len(r.git.pushes))
		}
		if len(r.records) != 1 || r.records[0].OK || r.records[0].Error == "" {
			t.Fatalf("%s: records %+v, want one failure", name, r.records)
		}
	}
	noAdmit := newMirrorRig(mirrorRemote, true)
	noAdmit.p.Admit = nil
	check("no admit", noAdmit)

	denied := newMirrorRig(mirrorRemote, true)
	denied.p.Admit = func(context.Context) error { return errSeam }
	check("admit denied", denied)

	probeFail := newMirrorRig(mirrorRemote, true)
	probeFail.p.Git = &errMirrorGit{tipErr: errSeam}
	check("probe fails", probeFail)

	diverged := newMirrorRig(mirrorRemote, true)
	diverged.git.remoteTip, diverged.git.ancestor = mirrorOldTip, false
	check("diverged remote", diverged)
	if !strings.Contains(diverged.records[0].Error, "non-fast-forward") {
		t.Fatalf("diverged error %q should name the refusal", diverged.records[0].Error)
	}

	ancFail := newMirrorRig(mirrorRemote, true)
	ancFail.git.remoteTip = mirrorOldTip
	ancFail.p.Git = &errMirrorGit{fakeMirrorGit: *ancFail.git, ancErr: errSeam}
	check("ancestor check fails", ancFail)
}

func TestMirrorEffectLogWriteFailuresSurface(t *testing.T) {
	ctx := context.Background()
	confirm := newMirrorRig(mirrorRemote, true)
	confirm.p.Effects = &errEffects{fakeEffects: confirm.effects, confirmErr: errSeam}
	if err := confirm.p.Run(ctx, confirm.job()); !errors.Is(err, errSeam) || len(confirm.records) != 0 {
		t.Fatalf("confirm failure: err %v, records %d; want the error and no success record", err, len(confirm.records))
	}
	fail := newMirrorRig(mirrorRemote, true)
	fail.git.pushErr = errSeam
	fail.p.Effects = &errEffects{fakeEffects: fail.effects, failErr: errSeam}
	if err := fail.p.Run(ctx, fail.job()); !errors.Is(err, errSeam) || len(fail.attention) != 0 {
		t.Fatalf("fail-record failure: err %v, attention %d; want the error and no attention item", err, len(fail.attention))
	}
}

func TestMirrorRecordStampsTimeAndToleratesMissingSeams(t *testing.T) {
	r := newMirrorRig(mirrorRemote, true)
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	r.p.Now = func() time.Time { return at }
	if err := r.p.Run(context.Background(), r.job()); err != nil {
		t.Fatal(err)
	}
	if len(r.records) != 1 || !r.records[0].OK || !r.records[0].At.Equal(at) {
		t.Fatalf("records %+v, want one success stamped %v", r.records, at)
	}
	bare := newMirrorRig(mirrorRemote, true)
	bare.p.Record, bare.p.Attention = nil, nil
	bare.git.pushErr = errSeam
	if err := bare.p.Run(context.Background(), bare.job()); err != nil {
		t.Fatalf("Run without Record/Attention seams = %v, want nil", err)
	}
}

func TestMirrorBacklogCheckWithoutSourceAndMetadata(t *testing.T) {
	c := MirrorBacklogCheck{}
	res, err := c.Run(context.Background())
	if err != nil || res.Status != doctor.StatusOK {
		t.Fatalf("untracked backlog = %+v, %v; want OK", res, err)
	}
	if c.Name() != "ci_mirror_backlog" || c.Describe() == "" {
		t.Fatalf("name %q describe %q", c.Name(), c.Describe())
	}
	_ = c.Metadata()
	if _, err := c.Fix(context.Background()); !errors.Is(err, doctor.ErrCheckNotFixable) {
		t.Fatalf("Fix = %v, want ErrCheckNotFixable", err)
	}
}
