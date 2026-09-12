package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/pkg/cascade"
)

type recordingImporter struct {
	request migrationv1.Request
	result  migrationv1.DryRunResult
	err     error
}

func (r *recordingImporter) Import(_ context.Context, request migrationv1.Request) (migrationv1.DryRunResult, error) {
	r.request = request
	return r.result, r.err
}

func TestRootMigrateV1Wiring(t *testing.T) {
	cmd, args, err := newRootCmd().Find([]string{"migrate", "v1", "memory"})
	if err != nil || cmd.CommandPath() != "cascade migrate v1" || len(args) != 1 {
		t.Fatalf("migrate v1 command is not mounted at the shipping root: cmd=%v args=%v err=%v", cmd, args, err)
	}
}

func TestMigrateV1CommandInvokesImporter(t *testing.T) {
	importer := &recordingImporter{result: migrationv1.DryRunResult{Domain: migrationv1.DomainMemory,
		Changes: []migrationv1.Change{{Operation: migrationv1.OperationCreate}}}}
	closed := false
	factory := func(_ context.Context, domain migrationv1.Domain) (migrationv1.Importer, func() error, error) {
		if domain != migrationv1.DomainMemory {
			t.Fatalf("factory domain = %q", domain)
		}
		return importer, func() error { closed = true; return nil }, nil
	}
	cmd := newMigrateCmd(factory)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"v1", "memory", "--from", "/v1-home", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if importer.request.SourceRoot != "/v1-home" || !importer.request.DryRun || !closed {
		t.Fatalf("request/close wiring failed: request=%+v closed=%v", importer.request, closed)
	}
	if !strings.Contains(stdout.String(), "memory: 1 change(s)") {
		t.Fatalf("result did not use output writer: %q", stdout.String())
	}
}

func TestMigrateV1CommandRefusals(t *testing.T) {
	cmd := newMigrateCmd(nil)
	cmd.SetArgs([]string{"v1", "memory"})
	err := cmd.Execute()
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("missing --from = %v", err)
	}

	cmd = newMigrateCmd(nil)
	cmd.SetArgs([]string{"v1", "memory", "--from", "/v1"})
	err = cmd.Execute()
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("nil factory = %v", err)
	}
}

func TestMigrateV1CommandReturnsImportBeforeCloseError(t *testing.T) {
	want := cascade.New(cascade.KindIntegrity, "import refused")
	importer := &recordingImporter{err: want}
	factory := func(context.Context, migrationv1.Domain) (migrationv1.Importer, func() error, error) {
		return importer, func() error { return errors.New("close failed") }, nil
	}
	cmd := newMigrateCmd(factory)
	cmd.SetArgs([]string{"v1", "memory", "--from", "/v1"})
	err := cmd.Execute()
	if !errors.Is(err, want) {
		t.Fatalf("import error was masked: %v", err)
	}
}

func TestProductionMigrateFactoryRefusesUnknownDomain(t *testing.T) {
	_, _, err := productionMigrateFactory(context.Background(), "future")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("unknown domain = %v", err)
	}
}
