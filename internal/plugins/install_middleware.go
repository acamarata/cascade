// Package plugins (install_middleware.go): Purpose: publishes plugin.registered once a plugin.add or plugin.update
// commit has returned successfully.
// Inputs: the daemon's event bus and plugin store; each call's id.
// Outputs: one events.EventKindPluginRegistered event per successful commit,
// payload {id, version, runtime} read from the stored record.
// It also refuses a remote-runtime manifest on both verbs before the handler
// runs (DEF-P2-remote-plugin-runtime).
// Constraints: mounted inside the elevation middleware, so the params it
// reads are the attested `_args`, never the envelope. Publishes only after
// next returns a nil error and the stored record exists; a refusal, an
// error or a missing record publishes nothing; a failed publish after a
// commit is logged as a warning. The payload carries no grant, checksum or
// manifest byte.
// SPORT: internal/plugins install middleware.
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// PluginEventNamespace is the bus namespace plugin lifecycle events use.
const PluginEventNamespace = "plugins.lifecycle"

// pluginRegisteredSource is the Source of every plugin.registered event.
const pluginRegisteredSource = "plugin-install"

// pluginRegisteredPayload is the plugin.registered event body.
type pluginRegisteredPayload struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Runtime string `json:"runtime"`
}

// InstallMiddleware wraps plugin.add and plugin.update; every other method
// passes through untouched. It refuses a remote-runtime manifest before the
// handler runs. A nil bus or store publishes nothing: there is nothing to
// publish to or read from. The commit has already happened when a publish
// fails, so the call still succeeds and the failure is a warning on logger
// (the daemon's logger; the first non-nil one given, else slog.Default),
// never silent.
func InstallMiddleware(bus *events.Bus, store provider.Store, logger ...*slog.Logger) rpc.MiddlewareFunc {
	log := slog.Default()
	for _, l := range logger {
		if l != nil {
			log = l
			break
		}
	}
	return func(method string, next rpc.HandlerFunc) rpc.HandlerFunc {
		if method != "plugin.add" && method != UpdateRPCMethod {
			return next
		}
		return func(ctx context.Context, raw json.RawMessage) (any, error) {
			if err := refuseRemoteParams(method, raw); err != nil {
				return nil, err
			}
			res, err := next(ctx, raw)
			if err != nil || bus == nil || store == nil {
				return res, err
			}
			if perr := publishPluginRegistered(ctx, bus, store, raw); perr != nil {
				log.Warn("plugin.registered event not published; the commit stands",
					slog.String("method", method), slog.String("error", perr.Error()))
			}
			return res, nil
		}
	}
}

// refuseRemoteParams refuses an install whose manifest_bytes declare the
// deferred remote runtime, before the handler runs, so the elevated
// plugin.add path carries the same DEF-P2-remote-plugin-runtime refusal as
// AddPlugin. Params the handler cannot decode or parse pass through: the
// handler refuses them itself.
func refuseRemoteParams(method string, raw json.RawMessage) error {
	var p struct {
		ManifestBytes []byte `json:"manifest_bytes"`
	}
	if json.Unmarshal(raw, &p) != nil || len(p.ManifestBytes) == 0 {
		return nil
	}
	m, err := plugin.ParseManifest(bytes.NewReader(p.ManifestBytes))
	if err != nil {
		return nil
	}
	verb := "add"
	if method == UpdateRPCMethod {
		verb = "update"
	}
	return RefuseRemoteRuntime(verb, m)
}

// publishPluginRegistered reads the committed record named by raw's id and
// publishes it. Params with no id, or an id with no stored record, publish
// nothing and report nothing (there is no commit to announce); a store read
// or publish failure is returned.
func publishPluginRegistered(ctx context.Context, bus *events.Bus, store provider.Store, raw json.RawMessage) error {
	var p struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &p) != nil || p.ID == "" {
		return nil
	}
	rec, ok, err := LoadMetadata(ctx, store, p.ID)
	if err != nil || !ok {
		return err
	}
	payload, err := json.Marshal(pluginRegisteredPayload{ID: rec.Name, Version: rec.InstalledVersion, Runtime: string(rec.RuntimeMode)})
	if err != nil {
		return err
	}
	_, err = bus.Publish(context.WithoutCancel(ctx), PluginEventNamespace, events.EventKindPluginRegistered, pluginRegisteredSource, payload)
	return err
}
