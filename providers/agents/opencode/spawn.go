package opencode

// Purpose: validate the custody envelope and launch only native run.
// Inputs: host-built spec and exact environment. Outputs: a process group.
// Constraints: no environment inheritance and no server launch flags.

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/acamarata/cascade/pkg/procgroup"
	"github.com/acamarata/cascade/pkg/provider"
)

// WorktreeRoot returns the leased worktree used for both cwd and --dir.
func WorktreeRoot(spec provider.AgentJobSpec) string { return spec.WorktreeRoot }

func (d *Driver) argv(s provider.AgentJobSpec) []string {
	a := []string{d.cfg.Binary, "run", "--format", "json", "--dir", WorktreeRoot(s), "--pure"}
	if s.Model != "" {
		a = append(a, "-m", s.Model)
	}
	e := s.Effort
	if e == "" {
		e = d.cfg.Defaults.Effort
	}
	if e != "" {
		a = append(a, "--variant", e)
	}
	return append(a, "--", s.Prompt)
}

func (d *Driver) preflight(s provider.AgentJobSpec) error {
	if !d.cfg.Posture.ProgrammaticEntitlement {
		return provider.ErrEntitlement
	}
	if runtime.GOOS == "windows" {
		return provider.ErrPlatform
	}
	if len(s.Env.Driver) == 0 {
		return ErrEmptySpawnEnv
	}
	if s.WorktreeRoot == "" {
		return ErrEmptyWorktreeRoot
	}
	if s.Filter == nil {
		return provider.ErrEgressSubstitutionFailed
	}
	if d.cfg.Clock == nil {
		return ErrNoClock
	}
	if err := s.Envelope.Check(s.WorktreeRoot, d.cfg.Clock.Now()); err != nil {
		return err
	}
	if _, err := provider.ResolveToolPolicy(s.ToolPolicy); err != nil {
		return err
	}
	if !validEffort(s.Effort) {
		return ErrInvalidConfig
	}
	if strings.HasPrefix(s.Model, "-") || strings.ContainsAny(s.Model, "\x00\r\n") {
		return ErrInvalidConfig
	}
	if err := d.checkEnv(s.Env); err != nil {
		return err
	}
	if d.cfg.ContentMatch != nil {
		return provider.PreSpawnScan(s.WorktreeRoot, s.Scope, d.cfg.ContentMatch)
	}
	return nil
}

func (d *Driver) checkEnv(e provider.SpawnEnv) error {
	allow := provider.DriverEnvAllowlist(VendorAuthVar, KeyVarCompatKey)
	vals := map[string]string{}
	for _, kv := range e.Driver {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !slices.Contains(allow, k) {
			return ErrEnvNotAllowed
		}
		if _, dup := vals[k]; dup {
			return ErrEnvNotAllowed
		}
		vals[k] = v
	}
	if e.Home == "" || vals["HOME"] != e.Home || (d.cfg.RuntimeHome != "" && e.Home != d.cfg.RuntimeHome) {
		return ErrHomeMismatch
	}
	if !filepath.IsAbs(vals[VendorAuthVar]) {
		return ErrHomeMismatch
	}
	if !profileContains(e.Home, vals[VendorAuthVar]) {
		return ErrHomeMismatch
	}
	if vals[KeyVarCompatKey] == "" {
		return ErrCredentialMissing
	}
	return nil
}

func profileContains(home, config string) bool {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return false
	}
	h, err := filepath.EvalSymlinks(home)
	if err != nil {
		return false
	}
	c, err := filepath.EvalSymlinks(config)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(h, c)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Spawn validates every guard before starting a child in a new process group.
func (d *Driver) Spawn(ctx context.Context, s provider.AgentJobSpec) (provider.SpawnResult, error) {
	if err := d.preflight(s); err != nil {
		return provider.SpawnResult{}, err
	}
	if ctx.Err() != nil {
		return provider.SpawnResult{}, ErrRunCancelled
	}
	if s.JobID == "" {
		s.JobID = newJobID()
	}
	j := &job{id: s.JobID, spec: s, tier: provider.JoinSensitivity(s.DataClass, s.Envelope.Sensitivity), done: make(chan struct{}), state: provider.AgentRunRunning, norm: provider.NewEventNormalizer()}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.jobs[j.id]; ok {
		return provider.SpawnResult{}, ErrDuplicateJob
	}
	if err := d.start(j); err != nil {
		return provider.SpawnResult{}, err
	}
	d.jobs[j.id] = j
	go d.run(j)
	return provider.SpawnResult{JobID: j.id, ProcessGroupID: j.pgid}, nil
}

func newJobID() provider.AgentJobID {
	var b [16]byte
	_, _ = cryptorand.Read(b[:])
	return provider.AgentJobID("opencode-" + hex.EncodeToString(b[:]))
}

func (d *Driver) start(j *job) error {
	if !filepath.IsAbs(d.cfg.Binary) {
		return ErrDriverNotFound
	}
	if _, err := os.Stat(d.cfg.Binary); err != nil {
		return ErrDriverNotFound
	}
	r, w, err := os.Pipe()
	if err != nil {
		return ErrStart
	}
	er, ew, err := os.Pipe()
	if err != nil {
		_ = r.Close()
		_ = w.Close()
		return ErrStart
	}
	defer func() { _ = w.Close() }()
	defer func() { _ = ew.Close() }()
	p, err := os.StartProcess(d.cfg.Binary, d.argv(j.spec), &os.ProcAttr{Dir: j.spec.WorktreeRoot, Env: slices.Clone(j.spec.Env.Driver), Files: []*os.File{nil, w, ew}, Sys: procgroup.Attr()})
	if err != nil {
		_ = r.Close()
		_ = er.Close()
		return ErrStart
	}
	j.process, j.stdout, j.stderr, j.pgid = p, r, er, p.Pid
	return nil
}
