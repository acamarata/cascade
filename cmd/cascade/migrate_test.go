// Purpose: `cascade migrate v1`'s CLI-layer tests — flag parsing, wiring
// at the shipping root, the ledger-open failure path, CASCADE_NO_INPUT
// gating, and the daemon-liveness note — driven through the real cobra
// tree with an injected migrateDeps so no test opens a real cascade.db or
// touches a real terminal.
// SPORT: cmd.cascade.migrate/CHANGED (P1-E26-W10-S53-T4).
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/migration"
	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recordingImporter is the shared fake migrationv1.Importer used across
// this file's scenarios.
type recordingImporter struct {
	request migrationv1.Request
	result  migrationv1.DryRunResult
	err     error
}

func (r *recordingImporter) Import(_ context.Context, request migrationv1.Request) (migrationv1.DryRunResult, error) {
	r.request = request
	return r.result, r.err
}

// fakeMigrateFactory builds an ImporterFactory over a fixed importer for
// every domain.
func fakeMigrateFactory(imp *recordingImporter, err error) migration.ImporterFactory {
	return func(context.Context, migrationv1.Domain) (migrationv1.Importer, func() error, error) {
		if err != nil {
			return nil, nil, err
		}
		return imp, func() error { return nil }, nil
	}
}

// noopOpenLedger returns a ledger the test never inspects, over an
// in-memory-equivalent throwaway file — used by scenarios whose focus is
// flag parsing or refusal before any importer would run.
func noopOpenLedger(t *testing.T) func(context.Context) (*migration.LedgerStore, func() error, error) {
	t.Helper()
	dir := t.TempDir()
	return func(ctx context.Context) (*migration.LedgerStore, func() error, error) {
		return productionOpenLedgerAt(ctx, dir)
	}
}

func TestRootMigrateV1Wiring(t *testing.T) {
	cmd, args, err := newRootCmd().Find([]string{"migrate", "v1"})
	if err != nil || cmd.CommandPath() != "cascade migrate v1" || len(args) != 0 {
		t.Fatalf("migrate v1 command is not mounted at the shipping root: cmd=%v args=%v err=%v", cmd, args, err)
	}
}

func TestMigrateV1CommandInvokesFactory(t *testing.T) {
	imp := &recordingImporter{result: migrationv1.DryRunResult{Domain: migrationv1.DomainConfig,
		Changes: []migrationv1.Change{{Operation: migrationv1.OperationCreate}}}}
	deps := migrateDeps{Factory: fakeMigrateFactory(imp, nil), OpenLedger: noopOpenLedger(t), Getenv: func(string) string { return "" }}
	cmd := newMigrateCmd(deps)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"v1", "--from", t.TempDir(), "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if imp.request.DryRun {
		t.Fatal("a non-dry-run invocation set DryRun=true")
	}
	if !strings.Contains(stdout.String(), "change(s)") {
		t.Fatalf("result did not use the output writer: %q", stdout.String())
	}
}

func TestMigrateV1CommandMissingFromRefuses(t *testing.T) {
	deps := migrateDeps{Factory: fakeMigrateFactory(&recordingImporter{}, nil), OpenLedger: noopOpenLedger(t), Getenv: func(string) string { return "" }}
	cmd := newMigrateCmd(deps)
	cmd.SetArgs([]string{"v1", "--yes"})
	err := cmd.Execute()
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("missing --from = %v", err)
	}
}

func TestMigrateV1CommandRejectsPositionalArgs(t *testing.T) {
	deps := migrateDeps{Factory: fakeMigrateFactory(&recordingImporter{}, nil), OpenLedger: noopOpenLedger(t)}
	cmd := newMigrateCmd(deps)
	cmd.SetArgs([]string{"v1", "memory", "--from", t.TempDir()})
	if err := cmd.Execute(); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("a stray positional argument was not refused: %v", err)
	}
}

func TestMigrateV1CommandNotConfiguredRefuses(t *testing.T) {
	cmd := newMigrateCmd(migrateDeps{})
	cmd.SetArgs([]string{"v1", "--from", t.TempDir()})
	if err := cmd.Execute(); !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("an unconfigured command did not refuse with KindInternal: %v", err)
	}
}

func TestMigrateV1CommandOpenLedgerErrorPropagates(t *testing.T) {
	want := cascade.New(cascade.KindUnavailable, "cascade.db is locked")
	deps := migrateDeps{
		Factory:    fakeMigrateFactory(&recordingImporter{}, nil),
		OpenLedger: func(context.Context) (*migration.LedgerStore, func() error, error) { return nil, nil, want },
	}
	cmd := newMigrateCmd(deps)
	cmd.SetArgs([]string{"v1", "--from", t.TempDir(), "--yes"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "cascade.db is locked") {
		t.Fatalf("OpenLedger's error was not propagated: %v", err)
	}
}

// TestMigrateV1CommandCASCADE_NO_INPUTWithoutYesRefuses proves the CLI
// wires Getenv("CASCADE_NO_INPUT") into the orchestrator's NoInput gate.
func TestMigrateV1CommandCASCADE_NO_INPUTWithoutYesRefuses(t *testing.T) {
	imp := &recordingImporter{result: migrationv1.DryRunResult{Domain: migrationv1.DomainConfig}}
	deps := migrateDeps{
		Factory: fakeMigrateFactory(imp, nil), OpenLedger: noopOpenLedger(t),
		Getenv: func(k string) string {
			if k == "CASCADE_NO_INPUT" {
				return "1"
			}
			return ""
		},
	}
	cmd := newMigrateCmd(deps)
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"v1", "--from", t.TempDir()})
	err := cmd.Execute()
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("CASCADE_NO_INPUT without --yes did not refuse referencing --yes: %v", err)
	}
}

// TestNoteDaemonLivenessWritesOnlyWhenLive proves the informational note
// (this file's CONTRACT NOTE: embedded-only execution) appears exactly
// when the probe reports a live daemon, and never when embedded.
func TestNoteDaemonLivenessWritesOnlyWhenLive(t *testing.T) {
	imp := &recordingImporter{result: migrationv1.DryRunResult{Domain: migrationv1.DomainConfig}}
	deps := migrateDeps{Factory: fakeMigrateFactory(imp, nil), OpenLedger: noopOpenLedger(t), Getenv: func(string) string { return "" }}

	cmd := newMigrateCmd(deps)
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: false})
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"v1", "--from", t.TempDir(), "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute with a live daemon state: %v", err)
	}
	if !strings.Contains(stderr.String(), "no daemon transport is available yet") {
		t.Fatalf("no daemon-liveness note was printed for a live daemon: %q", stderr.String())
	}

	stderr.Reset()
	cmd2 := newMigrateCmd(deps)
	cmd2.SetErr(&stderr)
	cmd2.SetContext(runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: true}))
	cmd2.SetArgs([]string{"v1", "--from", t.TempDir(), "--yes"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("Execute embedded: %v", err)
	}
	if strings.Contains(stderr.String(), "daemon dispatch") {
		t.Fatalf("an embedded run printed the live-daemon note: %q", stderr.String())
	}
}

func TestProductionMigrateFactoryRefusesUnknownDomain(t *testing.T) {
	_, _, err := productionMigrateFactory(context.Background(), "future")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("unknown domain = %v", err)
	}
}
