// Purpose: composes the process's one embedder Selection for retrieval
// (P1-CPA-11): the local BGE-M3 sidecar when [retrieval.embedding]
// local_socket is explicitly set and passes the counterpart guard, else a
// conductor lane over the in-process door, else not-configured.
//
// Inputs: the daemon's config (lane, model, dimensions, timeout), the
// ADMITTED effective config (local_socket, read only through
// runtime.RetrievalEmbeddingLocalSocket), the process paths and the
// conductor door.
//
// Outputs: *embedsel.Selection, one per config path per process.
//
// Constraints: no default socket path is ever auto-selected. A selected
// sidecar that fails is never re-routed to a lane: two embedding spaces in
// one index are undetectable. A lane embeds internal and public content
// only, at the tier the caller stamped from daemon-side state
// (conductor.ContextWithDoorSensitivity); an unstamped call is restricted
// and refused before the door. Lane egress transits the process firewall's
// engine (processFirewallFor(paths).Engine) under the conductor class.
//
// SPORT: cmd/cascade embedder composition (ADDED, P1-CPA-11).
package main

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"sync"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/retrieval/embedsel"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/embeddings"
	"github.com/acamarata/cascade/providers/embeddings/bgem3"
)

// laneDoor is the in-process conductor door a lane embedder dispatches
// through (*daemon.ConductorDoor in production).
type laneDoor interface {
	Embed(ctx context.Context, lane, model string, tier provider.SensitivityTier, inputs []string) ([][]float32, error)
}

// embedderDeps are composeEmbedder's process collaborators, injectable by
// tests: the door, the daemon uid the socket must be owned by, the GOOS and
// the egress engine factory.
type embedderDeps struct {
	door   laneDoor
	uid    int
	goos   string
	engine func(*egress.Registry) (*egress.Engine, error)
	lstat  func(string) (os.FileInfo, error)
}

// errNotConfigured is the not-configured selection's error text.
const errNotConfigured = "embedding: not configured: retrieval.embedding.local_socket is unset (no sidecar socket path; " +
	"none is auto-selected) and retrieval.embedding.lane is unset"

// composeEmbedder selects the embedder for cfg and eff. The socket comes
// only from runtime.RetrievalEmbeddingLocalSocket(eff), the admitted value.
func composeEmbedder(cfg *runtime.Config, eff runtime.EffectiveConfig, paths runtime.PathProvider, door *daemon.ConductorDoor) *embedsel.Selection {
	deps := embedderDeps{uid: os.Getuid(), goos: goruntime.GOOS, engine: firewallEngineFor(paths)}
	if door != nil {
		deps.door = door
	}
	return composeEmbedderWith(cfg, eff, deps)
}

// composeEmbedderWith is composeEmbedder over explicit deps.
func composeEmbedderWith(cfg *runtime.Config, eff runtime.EffectiveConfig, deps embedderDeps) *embedsel.Selection {
	if cfg == nil {
		return embedsel.NotConfigured(cascade.New(cascade.KindUnavailable, errNotConfigured))
	}
	s := cfg.RetrievalEmbedding()
	socket, err := runtime.RetrievalEmbeddingLocalSocket(eff)
	if err != nil {
		return embedsel.Failed(embedsel.SourceLocalSidecar, cascade.Wrap(cascade.KindInvalidInput, err, "embedding: local sidecar"))
	}
	if socket != "" {
		return sidecarSelection(socket, s, deps)
	}
	if s.Lane != "" {
		return laneSelection(s, deps)
	}
	return embedsel.NotConfigured(cascade.New(cascade.KindUnavailable, errNotConfigured))
}

// sidecarSelection guards the socket and builds the BGE-M3 client. Any
// failure is the sidecar's selection: it is never re-routed to a lane.
func sidecarSelection(socket string, s runtime.EmbeddingSettings, deps embedderDeps) *embedsel.Selection {
	if err := checkSidecarSocketWith(deps.goos, socket, deps.uid, deps.lstat); err != nil {
		return embedsel.Failed(embedsel.SourceLocalSidecar, err)
	}
	client, err := bgem3.New(bgem3.Config{
		Dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		Endpoint: socket,
		Model:    provider.EmbedModel{ID: s.LocalModel, Dimensions: s.LocalDimensions},
		Timeout:  s.Timeout,
	})
	if err != nil {
		return embedsel.Failed(embedsel.SourceLocalSidecar, err)
	}
	return embedsel.New(client, embedsel.SourceLocalSidecar, s.LocalModel, s.LocalDimensions, []provider.SensitivityTier{
		provider.SensitivityLocalOnly, provider.SensitivityRestricted, provider.SensitivityInternal, provider.SensitivityPublic,
	})
}

// checkSidecarSocket is the counterpart guard: path must lstat as a socket
// (not a symlink) owned by uid, in a directory neither group- nor
// world-writable. Every refusal is KindPolicyDenied.
func checkSidecarSocket(path string, uid int) error {
	return checkSidecarSocketOn(goruntime.GOOS, path, uid)
}

// checkSidecarSocketOn is checkSidecarSocket for goos.
func checkSidecarSocketOn(goos, path string, uid int) error {
	return checkSidecarSocketWith(goos, path, uid, os.Lstat)
}

// checkSidecarSocketWith injects metadata lookup for listener-free tests.
func checkSidecarSocketWith(goos, path string, uid int, lstat func(string) (os.FileInfo, error)) error {
	if lstat == nil {
		lstat = os.Lstat
	}
	deny := func(why string) error {
		return cascade.Newf(cascade.KindPolicyDenied, "embedding: local sidecar socket %q refused: %s", path, why)
	}
	if goos == "windows" {
		return deny("unix sockets are not supported on windows")
	}
	fi, err := lstat(path)
	if err != nil {
		return cascade.Wrapf(cascade.KindPolicyDenied, err, "embedding: local sidecar socket %q refused", path)
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return deny("it is a symlink")
	case fi.Mode()&os.ModeSocket == 0:
		return deny("it is not a socket")
	}
	if owner, ok := fileOwner(fi); !ok || owner != uid {
		return deny("it is not owned by the daemon user")
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return cascade.Wrapf(cascade.KindPolicyDenied, err, "embedding: local sidecar socket %q refused", path)
	}
	if parent.Mode().Perm()&0o022 != 0 {
		return deny("its directory is group- or world-writable")
	}
	return nil
}

// fileOwner reads the owning uid from fi's platform stat, ok false when
// the platform reports none.
func fileOwner(fi os.FileInfo) (int, bool) {
	v := reflect.ValueOf(fi.Sys())
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return 0, false
	}
	f := v.FieldByName("Uid")
	if !f.CanUint() {
		return 0, false
	}
	return int(f.Uint()), true
}

// laneSelection builds the ProviderEmbedder over the door. It admits
// internal and public content only.
func laneSelection(s runtime.EmbeddingSettings, deps embedderDeps) *embedsel.Selection {
	if deps.door == nil {
		return embedsel.Failed(embedsel.SourceConductorLane, cascade.New(cascade.KindUnavailable, "embedding: no conductor door"))
	}
	exec := doorEmbedExecutor{door: deps.door, lane: s.Lane}
	pe, err := embeddings.NewProviderEmbedder(exec, provider.EmbedModel{ID: s.Model, Dimensions: s.Dimensions},
		provider.SensitivityPublic, &laneInterceptor{engine: deps.engine})
	if err != nil {
		return embedsel.Failed(embedsel.SourceConductorLane, err)
	}
	return embedsel.New(pe, embedsel.SourceConductorLane, s.Model, s.Dimensions,
		[]provider.SensitivityTier{provider.SensitivityInternal, provider.SensitivityPublic})
}

// stampedLaneTier returns the tier the caller stamped on ctx, or an error
// when it is missing (restricted) or not one a lane may carry.
func stampedLaneTier(ctx context.Context) (provider.SensitivityTier, error) {
	tier, ok := conductor.DoorSensitivityFrom(ctx)
	if !ok || !tier.Valid() {
		tier = provider.SensitivityRestricted
	}
	if tier != provider.SensitivityInternal && tier != provider.SensitivityPublic {
		return tier, cascade.Newf(cascade.KindPolicyDenied,
			"embedding: a conductor lane embeds internal and public content only, refused at tier %s", tier)
	}
	return tier, nil
}

// doorEmbedExecutor is the lane embedder's EmbedExecutor: the door, at the
// stamped tier. A refused tier never reaches the door.
type doorEmbedExecutor struct {
	door laneDoor
	lane string
}

// Embed dispatches req on the lane at the caller's stamped tier.
func (d doorEmbedExecutor) Embed(ctx context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	tier, err := stampedLaneTier(ctx)
	if err != nil {
		return provider.ModelEmbedResponse{}, err
	}
	vectors, err := d.door.Embed(conductor.ContextWithDoorSensitivity(ctx, tier), d.lane, req.Model, tier, req.Inputs)
	if err != nil {
		return provider.ModelEmbedResponse{}, err
	}
	return provider.ModelEmbedResponse{Vectors: vectors}, nil
}

// daemonEmbedders memoizes the one Selection per config path per process.
var daemonEmbedders = struct {
	mu     sync.Mutex
	byPath map[string]*embedsel.Selection
}{byPath: map[string]*embedsel.Selection{}}

// daemonEmbedderFor returns the process's Selection for paths: composed on
// the first call from eff (the admitted EffectiveConfig the daemon holds
// after ApplyEffective) and the config's other embedding settings, then the
// same pointer on every call. It uses daemon.ProcessConductorDoor().
func daemonEmbedderFor(paths runtime.PathProvider, eff runtime.EffectiveConfig) *embedsel.Selection {
	if paths == nil {
		return embedsel.NotConfigured(cascade.New(cascade.KindUnavailable, errNotConfigured))
	}
	cfg, err := loadPolicyConfig(context.Background(), paths)
	if err != nil {
		cfg = nil
	}
	return daemonEmbedderWith(paths, cfg, eff)
}

// daemonEmbedderWith is daemonEmbedderFor over an already loaded cfg (the
// daemon's own at start). A nil cfg composes not-configured.
func daemonEmbedderWith(paths runtime.PathProvider, cfg *runtime.Config, eff runtime.EffectiveConfig) *embedsel.Selection {
	daemonEmbedders.mu.Lock()
	defer daemonEmbedders.mu.Unlock()
	if s, ok := daemonEmbedders.byPath[paths.ConfigPath()]; ok {
		return s
	}
	s := composeEmbedder(cfg, eff, paths, daemon.ProcessConductorDoor())
	daemonEmbedders.byPath[paths.ConfigPath()] = s
	return s
}

// processEmbedder is the Selection a consumer without the admitted config
// reads: the one the daemon composed at start, else one composed with no
// admitted local_socket (never a sidecar from an unadmitted value).
func processEmbedder(paths runtime.PathProvider) *embedsel.Selection {
	return daemonEmbedderFor(paths, runtime.EffectiveConfig{})
}
