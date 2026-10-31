//go:build !windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/host"
	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/pkg/cascade"
)

type hostServer struct {
	*host.HostBoundaryEnforcer
	id, recorder string
	env          plugins.LaunchEnv
	engine       *egress.Engine
	transport    http.RoundTripper
	broker       hostReferenceBroker
	audit        *audit.Log
	logger       *slog.Logger
	seq          atomic.Uint64
}

// CheckSecretRef adapts the boundary's opaque handle to the transport seam.
func (s *hostServer) CheckSecretRef(ctx context.Context, key string) (any, error) {
	return s.HostBoundaryEnforcer.CheckSecretRef(ctx, key)
}

// AuditHostNotification persists method and sorted keys, never values.
func (s *hostServer) AuditHostNotification(ctx context.Context, method string, keys []string) {
	explain, _ := json.Marshal(map[string]any{"reason": "host call without reply", "keys": keys})
	_, _ = s.audit.Append(ctx, audit.Event{Kind: audit.KindPolicyDecide, Actor: "plugin:" + s.id, Action: method, Verdict: "deny", Explain: explain})
}

// ServeHTTP substitutes all request fields before hashing or sending them.
func (s *hostServer) ServeHTTP(ctx context.Context, in process.HostHTTPRequest) (process.HostHTTPResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.CheckHTTP(ctx, in.URL); err != nil {
		return process.HostHTTPResponse{}, err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return process.HostHTTPResponse{}, cascade.ErrInvalidInput
	}
	safe, err := s.engine.InterceptClass(ctx, egress.EgressClassPluginHostHTTP, egress.TierInternal, raw)
	if err != nil {
		return process.HostHTTPResponse{}, err
	}
	if json.Unmarshal(safe, &in) != nil {
		return process.HostHTTPResponse{}, cascade.ErrIntegrity
	}
	if err := s.CheckHTTP(ctx, in.URL); err != nil {
		return process.HostHTTPResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, in.Method, in.URL, strings.NewReader(in.Body))
	if err != nil || req.URL.Scheme != "https" && req.URL.Scheme != "http" {
		return process.HostHTTPResponse{}, cascade.ErrInvalidInput
	}
	for key, value := range in.Headers {
		req.Header.Set(key, value)
	}
	sum := sha256.Sum256(safe)
	explain, _ := json.Marshal(map[string]string{"recorder": s.recorder, "class": string(egress.EgressClassPluginHostHTTP)})
	h, err := s.env.Effects.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindExternalEffect, Actor: "plugin:" + s.id,
		Action: "plugin.host_http", Key: "plugin.call:" + s.id + ":" + s.recorder + ":" + strconv.FormatUint(s.seq.Add(1), 10), ParamsHash: hex.EncodeToString(sum[:]), Explain: explain})
	if err != nil {
		if h.IntentSeq != 0 {
			_, _ = s.env.Effects.FailEffect(context.WithoutCancel(ctx), h, "intent failed; HTTP not sent")
		}
		return process.HostHTTPResponse{}, err
	}
	return s.sendHTTP(ctx, req, h)
}

func (s *hostServer) sendHTTP(ctx context.Context, req *http.Request, h audit.EffectHandle) (process.HostHTTPResponse, error) {
	resp, err := s.transport.RoundTrip(req)
	terminal := context.WithoutCancel(ctx)
	if err != nil {
		if unknown, ok := s.env.Effects.(interface {
			MarkUnknownOutcome(context.Context, audit.EffectHandle) (audit.Record, error)
		}); ok {
			_, _ = unknown.MarkUnknownOutcome(terminal, h)
		}
		return process.HostHTTPResponse{}, cascade.ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }() // the response body is read-only; a close failure changes nothing
	_, confirmErr := s.env.Effects.ConfirmEffect(terminal, h, "ok")
	if confirmErr != nil {
		return process.HostHTTPResponse{}, cascade.ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil || len(body) > 1024*1024 {
		return process.HostHTTPResponse{}, cascade.ErrUnavailable
	}
	result := process.HostHTTPResponse{Status: resp.StatusCode, Body: string(body), Headers: make(map[string]string)}
	for key := range resp.Header {
		result.Headers[key] = resp.Header.Get(key)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return process.HostHTTPResponse{}, cascade.ErrIntegrity
	}
	safe, err := s.engine.InterceptClass(ctx, egress.EgressClassPluginHostHTTP, egress.TierInternal, raw)
	if err != nil {
		return process.HostHTTPResponse{}, err
	}
	if json.Unmarshal(safe, &result) != nil {
		return process.HostHTTPResponse{}, cascade.ErrIntegrity
	}
	return result, nil
}

// ServeSecretRef returns an identifier scoped to this plugin, never a value.
func (s *hostServer) ServeSecretRef(ctx context.Context, key string) (string, error) {
	if _, err := s.broker.Reference(ctx, key); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(s.id + ":" + key))
	return "plugin:" + s.id + ":ref:" + hex.EncodeToString(sum[:]), nil
}

// ServeStorage confines every operation to one literal namespace.
func (s *hostServer) ServeStorage(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Domain string          `json:"domain"`
		Key    string          `json:"key"`
		Value  json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil, cascade.ErrInvalidInput
	}
	if p.Domain != "plugin:"+s.id || strings.ContainsAny(p.Key, "/\\") || strings.Contains(p.Key, "..") {
		return nil, cascade.ErrCapabilityDenied
	}
	if err := s.CheckStorage(ctx, p.Domain); err != nil {
		return nil, err
	}
	switch op {
	case "get":
		return s.env.Store.Get(ctx, p.Domain, p.Key)
	case "put", "set":
		return nil, s.env.Store.Put(ctx, p.Domain, p.Key, p.Value)
	case "delete":
		return nil, s.env.Store.Delete(ctx, p.Domain, p.Key)
	default:
		return nil, cascade.ErrUnsupported
	}
}

// ServeLog redacts content before writing a plugin-tagged log entry.
func (s *hostServer) ServeLog(ctx context.Context, _, msg string) error {
	safe, err := s.engine.InterceptClass(ctx, egress.EgressClassPluginHostHTTP, egress.TierInternal, []byte(msg))
	if err != nil {
		return err
	}
	s.logger.InfoContext(ctx, string(safe))
	return nil
}

// ServeEvent always publishes inside the launching plugin's namespace.
func (s *hostServer) ServeEvent(ctx context.Context, kind string, payload json.RawMessage) error {
	if s.env.Bus == nil {
		return cascade.ErrUnavailable
	}
	_, err := s.env.Bus.Publish(ctx, "plugin."+s.id, events.EventKind(kind), "plugin:"+s.id, payload)
	return err
}
