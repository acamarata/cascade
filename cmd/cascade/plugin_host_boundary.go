//go:build !windows

package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/host"
	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

var hostBoundaryInstalled bool

func init() {
	plugins.SetHostBoundary(newHostBoundaryFactory(lazyPaths{}, http.DefaultTransport))
	hostBoundaryInstalled = true
}

// grantsFor accepts only witnessed network grants supplied by the launcher.
func grantsFor(id string, grants []string) host.Grants {
	g := host.Grants{StorageDomains: StorageDomains(id)}
	for _, grant := range grants {
		if strings.HasPrefix(grant, "net.http:") {
			scope := strings.TrimPrefix(grant, "net.http:")
			if scope != "" && strings.TrimSpace(scope) == scope {
				g.NetScopes = append(g.NetScopes, scope)
			}
		}
	}
	return g
}

// StorageDomains is the sole storage domain owned by this plugin.
func StorageDomains(id string) map[string]bool { return map[string]bool{"plugin:" + id: true} }

// newHostBoundaryFactory builds all collaborators before exposing a server.
func newHostBoundaryFactory(paths runtime.PathProvider, transport http.RoundTripper) plugins.HostBoundaryFactory {
	return func(ctx context.Context, env plugins.LaunchEnv, id string, grants []string) (process.HostCapabilityChecker, error) {
		if env.Effects == nil || transport == nil {
			return nil, cascade.ErrUnavailable
		}
		cfg, err := loadPolicyConfig(ctx, paths)
		if err != nil {
			return nil, err
		}
		parts, err := newPolicyEngine(ctx, policyEngineInputs{Store: env.Store, Clock: env.Clock, Config: cfg, Paths: paths,
			Capabilities: []policy.Capability{
				{Name: "host.event_emit", Desc: "emit plugin event", DefaultPolicy: policy.ClassWorkspaceMutation},
				{Name: "host.stream_emit", Desc: "emit plugin stream", DefaultPolicy: policy.ClassWorkspaceMutation},
			}})
		if err != nil {
			return nil, err
		}
		fw, err := processFirewallFor(paths)
		if err != nil {
			return nil, err
		}
		engine, err := egress.ProcessEngine(egress.DefaultRegistry())
		if err != nil {
			return nil, err
		}
		broker := hostReferenceBroker{id: id, broker: fw.Broker()}
		enforcer, err := host.NewHostBoundaryEnforcer(id, grantsFor(id, grants), hostPolicy{parts.Engine}, broker, hostAudit{parts.Audit})
		if err != nil {
			return nil, err
		}
		token, err := newHostRecorder(paths)
		if err != nil {
			return nil, err
		}
		return &hostServer{HostBoundaryEnforcer: enforcer, id: id, env: env, engine: engine,
			transport: transport, broker: broker, audit: parts.Audit, recorder: token, logger: slog.Default().With("plugin", id)}, nil
	}
}

type hostPolicy struct{ engine *policy.Engine }

func (p hostPolicy) Evaluate(ctx context.Context, id, capability string) (host.PolicyVerdict, string, error) {
	out, err := p.engine.Evaluate(ctx, policy.EvalRequest{Subject: policy.Subject{Kind: policy.SubjectPlugin, ID: id}, Capability: capability, CommandLess: true})
	if err == nil && out.Verdict == policy.VerdictAllow {
		return host.PolicyVerdictAllow, out.Reason, nil
	}
	return host.PolicyVerdictDeny, out.Reason, err
}

type hostAudit struct{ log *audit.Log }

func (a hostAudit) LogHostCall(ctx context.Context, event host.AuditEvent) {
	explain, _ := json.Marshal(map[string]any{"allowed": event.Allowed, "reason": event.Reason})
	_, _ = a.log.Append(ctx, audit.Event{Kind: audit.KindPolicyDecide, Actor: "plugin:" + event.PluginID, Action: event.CallType, Explain: explain})
}

type hostReferenceBroker struct {
	id     string
	broker *secrets.Broker
}

func (b hostReferenceBroker) Reference(ctx context.Context, key string) (host.SecretHandle, error) {
	if !strings.HasPrefix(key, "plugin."+b.id+".") || strings.ContainsAny(key, "/\\") || strings.Contains(key, "..") {
		return host.SecretHandle{}, cascade.ErrCapabilityDenied
	}
	exists, err := b.broker.Exists(ctx, key)
	if err != nil {
		return host.SecretHandle{}, err
	}
	if !exists {
		return host.SecretHandle{}, cascade.ErrNotFound
	}
	return host.SecretHandle{}, nil
}

// hostRecorderFiles holds recorder locks for the process lifetime, as the
// trusted launcher does. Its owner files let RecoverPluginEffects recover HTTP.
var hostRecorderFiles struct {
	sync.Mutex
	files []*os.File
}

func newHostRecorder(paths runtime.PathProvider) (string, error) {
	var nonce [16]byte
	if _, err := cryptorand.Read(nonce[:]); err != nil {
		return "", cascade.ErrUnavailable
	}
	token := hex.EncodeToString(nonce[:])
	dir := pluginEffectsDir(paths)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", cascade.ErrUnavailable
	}
	tmp := filepath.Join(dir, "."+token+".lock.tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return "", cascade.ErrUnavailable
	}
	if ok, err := tryFlock(f); !ok || err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", cascade.ErrUnavailable
	}
	if err := os.Rename(tmp, filepath.Join(dir, token+".lock")); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", cascade.ErrUnavailable
	}
	if err := runtime.WriteFileAtomic(filepath.Join(dir, token+".launch"), []byte(token), 0o600); err != nil {
		_ = f.Close()
		return "", err
	}
	hostRecorderFiles.Lock()
	hostRecorderFiles.files = append(hostRecorderFiles.files, f)
	hostRecorderFiles.Unlock()
	return token, nil
}
