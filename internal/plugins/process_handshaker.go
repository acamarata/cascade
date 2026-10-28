// Package plugins (process_handshaker.go): Purpose: the real
// ProcessHandshaker an update confirms a process-tier candidate with: launch
// it through the trusted launch path, let the handshake complete, close it.
// Inputs: the update's context (an attested plugin.update carries the
// digest its grant pinned), the plugin id and the candidate version.
// Outputs: nil when the candidate came up and answered cascade.hello;
// otherwise the launch's refusal, which UpdatePlugin turns into a rollback.
// Constraints: the executable must match a digest a witness pinned: the
// digest an attested plugin.update's grant just pinned (carried in ctx by
// update_rpc.go), otherwise the stored record's ExecDigest, so no update
// ever runs an executable no witness approved. The
// candidate starts with the base environment only (no token, no net scope):
// a handshake needs neither. The launch is recorded like any other.
// SPORT: internal/plugins process update handshaker (P1-PLG-03).
package plugins

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// NewProcessHandshaker returns the handshaker the plugin.update handler
// passes; it reads the stored record from the launch environment's store.
func NewProcessHandshaker() ProcessHandshaker { return processHandshaker{} }

// NewProcessHandshakerOver is NewProcessHandshaker for a caller that already
// holds the plugin store it is updating (the CLI): the stored record is read
// from that store, so the skip decision never depends on a second open.
func NewProcessHandshakerOver(store provider.Store) ProcessHandshaker {
	return processHandshaker{store: store}
}

type processHandshaker struct{ store provider.Store }

// Handshake launches name's executable, waits for the handshake, and closes
// it. A stored non-process record with no pinned executable has no child to
// confirm (the meaning of a nil handshaker before this one was wired), so
// nothing is launched.
func (ph processHandshaker) Handshake(ctx context.Context, name, _ string) error {
	var env LaunchEnv
	store := ph.store
	if _, pinned := grantedDigest(ctx, name); !pinned {
		if store == nil {
			var err error
			if env, err = ResolveLaunchEnv(ctx); err != nil {
				return err
			}
			store = env.Store
		}
		if rec, ok, err := LoadMetadata(ctx, store, name); err != nil {
			return err
		} else if ok && rec.RuntimeMode != plugin.RuntimeProcess {
			return nil
		}
	}
	if env.Store == nil {
		var err error
		if env, err = ResolveLaunchEnv(ctx); err != nil {
			return err
		}
	}
	want, err := handshakeDigest(ctx, env, name)
	if err != nil {
		return err
	}
	h, err := launchPinned(ctx, env, PluginMetadata{Name: name, RuntimeMode: plugin.RuntimeProcess}, want)
	if err != nil {
		return err
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeGrace)
	defer cancel()
	return h.Close(closeCtx)
}

// grantedDigestKey carries the digest an attested update's grant pinned.
type grantedDigestKey struct{}

// withGrantedDigest records the digest grantProcessTrust pinned for id.
func withGrantedDigest(ctx context.Context, id, digest string) context.Context {
	return context.WithValue(ctx, grantedDigestKey{}, id+"\x00"+digest)
}

// grantedDigest reads withGrantedDigest's digest for id.
func grantedDigest(ctx context.Context, id string) (string, bool) {
	v, _ := ctx.Value(grantedDigestKey{}).(string)
	digest, ok := strings.CutPrefix(v, id+"\x00")
	return digest, ok && digest != ""
}

// handshakeDigest is the digest the candidate must match: the one an
// attested update's grant pinned, else the stored trusted record's.
func handshakeDigest(ctx context.Context, env LaunchEnv, name string) (string, error) {
	if digest, ok := grantedDigest(ctx, name); ok {
		return digest, nil
	}
	rec, ok, err := LoadMetadata(ctx, env.Store, name)
	if err != nil {
		return "", err
	}
	if !ok || rec.TrustTier != TrustTrusted || rec.ExecDigest == "" {
		return "", cascade.Newf(cascade.KindPolicyDenied,
			"plugins: %s has no trusted executable to confirm; run `cascade plugin update %s` through the elevated path", name, name)
	}
	return rec.ExecDigest, nil
}
