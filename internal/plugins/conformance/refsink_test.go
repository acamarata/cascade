// Package conformance (refsink_test.go): Purpose: refSink is the one
// real, shared, in-memory implementation of every ABI v1 host-fn delegate
// interface the wasm runtime's host_abi_*.go handlers call
// (wasm.Logger, wasm.EventBus, wasm.StreamSink, wasm.ToolRegistrar,
// plugin.Storage, wasm.SecretBroker, wasm.NetDoer).
//
// Inputs: the same typed requests wasm's host_abi_*.go handlers decode
// from a guest call.
//
// Outputs: the same typed responses those handlers marshal back, or one
// of the deterministic KindInvalidInput/KindNotFound refusals below.
//
// Constraints: fail-closed on every empty required field, matching this
// package's own error-path fixtures. builtinHarness (harness_test.go)
// calls refSink directly -- no serialisation, since a builtin plugin is
// compiled into the host binary and crosses no wire boundary; wasmHarness
// wraps the SAME refSink instance-shape as wasm.Deps, reached only
// through a real wazero guest call. Because both harnesses delegate to
// byte-for-byte the same logic, TestConformance_AllRuntimesAgree's
// builtin-vs-wasm comparison actually proves the wasm host_abi_*.go
// read/dispatch/write plumbing preserves what refSink decided, across a
// real linear-memory and wazero call boundary -- not a tautology.
//
// SPORT: internal.plugins.conformance/ADDED (P1-E15-W4-S32-T2).
package conformance

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/plugins/wasm"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// refSink is not usable at its zero value; use newRefSink.
type refSink struct {
	mu    sync.Mutex
	store map[string][]byte
}

func newRefSink() *refSink { return &refSink{store: map[string][]byte{}} }

// Log implements wasm.Logger. An empty message fails closed rather than
// silently accepting a blank log line.
func (s *refSink) Log(_ context.Context, _ string, message string) error {
	if message == "" {
		return cascade.New(cascade.KindInvalidInput, "conformance: host_log: message is empty")
	}
	return nil
}

// Emit implements wasm.EventBus.
func (s *refSink) Emit(_ context.Context, topic string, _ []byte) (string, error) {
	if topic == "" {
		return "", cascade.New(cascade.KindInvalidInput, "conformance: host_eventemit: topic is empty")
	}
	return "evt:" + topic, nil
}

// Write implements wasm.StreamSink.
func (s *refSink) Write(_ context.Context, channelID string, chunk []byte) (int, error) {
	if channelID == "" {
		return 0, cascade.New(cascade.KindInvalidInput, "conformance: host_stream: channelId is empty")
	}
	return len(chunk), nil
}

// Register implements wasm.ToolRegistrar.
func (s *refSink) Register(_ context.Context, toolName string, _ []byte) error {
	if toolName == "" {
		return cascade.New(cascade.KindInvalidInput, "conformance: host_toolregister: toolName is empty")
	}
	return nil
}

// Get implements plugin.Storage. A missing key is KindNotFound, matching
// pkg/plugin.Storage's own doc comment.
func (s *refSink) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.store[key]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "conformance: host_storage: key %q not found", key)
	}
	return v, nil
}

// Set implements plugin.Storage.
func (s *refSink) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[key] = value
	return nil
}

// Delete implements plugin.Storage.
func (s *refSink) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.store, key)
	return nil
}

// List implements plugin.Storage.
func (s *refSink) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.store {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// Migrate implements plugin.Storage. Not exercised by this ABI v1
// conformance suite (host_storage never issues a Migrate call); refused
// rather than silently returning a fabricated success.
func (s *refSink) Migrate(_ context.Context, _ []plugin.Migration) (plugin.MigrationReport, error) {
	return plugin.MigrationReport{}, cascade.New(cascade.KindUnsupported, "conformance: refSink does not implement Migrate")
}

// CreateRef implements wasm.SecretBroker. Never returns a literal secret
// value -- only an opaque reference token, matching the real
// SecretBroker contract.
func (s *refSink) CreateRef(_ context.Context, name string) (string, error) {
	return "ref:" + name, nil
}

// Do implements wasm.NetDoer. host_http's scope check runs upstream of
// this delegate (checkNetScope in wasm/host_abi_net.go); by the time Do
// is reached the call is already known to be in-scope, so it always
// succeeds.
func (s *refSink) Do(_ context.Context, req wasm.HTTPRequest) (wasm.HTTPResponse, error) {
	return wasm.HTTPResponse{Status: 200, Body: []byte("echo:" + req.URL)}, nil
}
