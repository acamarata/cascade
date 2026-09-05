package hooks

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Interceptor is the outbound firewall seam this package dispatches
// through. It is an interface so the dispatcher never depends on how the
// firewall is built, and so a test can drive the real engine or a
// deliberately failing one through the same path.
type Interceptor interface {
	// Intercept returns bytes safe to hand across a process boundary, or
	// an error and nothing to write.
	Intercept(ctx context.Context, token egress.Capability, tier egress.SensitivityTier, content []byte) ([]byte, error)
}

// hookEgressTier is the classification the dispatcher declares for a
// hook's action parameters. Internal: the params come from the operator's
// own configuration file and cross to a plugin this daemon started, not
// to a third party.
const hookEgressTier = egress.TierInternal

// interceptParams runs a hook's action parameters through the firewall
// before they cross the plugin or note seam.
//
// This is an OUTBOUND crossing even though it is shaped like a request:
// PluginDispatcher and NoteWriter are opaque injected interfaces, the
// plugin behind them runs outside this process, and this package has no
// visibility into what it does with the bytes. Substituting credential
// material out of the params here is the last point at which that is
// still possible.
//
// It fails closed. If the firewall errors, the params are not handed on
// and the caller refuses the action: a hook that does not fire is a
// missed automation, and a hook that fires with a live credential in its
// parameters is a disclosed credential.
func interceptParams(ctx context.Context, firewall Interceptor, token egress.Capability, params map[string]string) (map[string]string, error) {
	if firewall == nil {
		return nil, cascade.New(cascade.KindUnavailable,
			"hooks: no egress firewall configured; action parameters are not sent")
	}
	encoded, err := encodeParams(params)
	if err != nil {
		return nil, err
	}
	safe, err := firewall.Intercept(ctx, token, hookEgressTier, encoded)
	if err != nil {
		return nil, err
	}
	return decodeParams(safe)
}

// encodeParams renders params as canonical JSON. Keys are sorted by
// encoding/json's own map ordering, which is lexical, so the same params
// always produce the same bytes and the same substitution.
func encodeParams(params map[string]string) ([]byte, error) {
	if params == nil {
		params = map[string]string{}
	}
	out, err := json.Marshal(params)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err,
			"hooks: action parameters could not be encoded for the egress firewall")
	}
	return out, nil
}

// decodeParams reads the substituted params back. Unparseable output is
// an error, not a fallback to the originals: content the firewall
// returned that this package cannot read is content it cannot vouch for.
func decodeParams(raw []byte) (map[string]string, error) {
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err,
			"hooks: substituted action parameters are not readable; the action is not dispatched")
	}
	if out == nil {
		out = map[string]string{}
	}
	return out, nil
}

// paramKeys returns params' keys in sorted order, for diagnostics that
// must not depend on map iteration order.
func paramKeys(params map[string]string) []string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
