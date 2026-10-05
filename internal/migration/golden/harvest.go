// Purpose: orchestrate one harvest: verify the pinned inputs, build every
// selected domain's fixtures in memory, guard them, then write.
// Inputs: the module root, the selected domains, the dry-run flag and the
// injected clock.
// Outputs: an exit code (0 ok, 2 refused input or redaction failure) and one
// summary line through the single internal/output Writer.
// Constraints: nothing is written unless every pinning check, every
// importer and every output guard passed for every selected domain; an
// importer's own error text is never printed (it can quote an input key),
// only its domain, input and Kind.
// SPORT: migration/golden/ADD (P1-E26-W10-S54-T3).
package main

import (
	"context"
	"fmt"
	"path/filepath"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Exit codes.
const (
	exitOK      = 0
	exitRefused = 2
)

// runConfig is one harvest invocation.
type runConfig struct {
	moduleRoot string
	domains    []v1.Domain
	dryRun     bool
	clock      runtime.Clock
}

// importRefusal is an importer failure. Error() names only the domain,
// input and Kind; the importer's error stays reachable through Unwrap for
// tests that check identity and message.
type importRefusal struct {
	domain v1.Domain
	input  string
	cause  error
}

func (e *importRefusal) Error() string {
	kind, _ := cascade.KindOf(e.cause)
	return fmt.Sprintf("golden harvest: the %s importer refused %s (%s)", e.domain, e.input, kind)
}

func (e *importRefusal) Unwrap() error { return e.cause }

// runImporter runs importer live against the scratch v1 home.
func runImporter(ctx context.Context, importer v1.Importer, domain v1.Domain, input, home string) (v1.DryRunResult, error) {
	if importer == nil {
		return v1.DryRunResult{}, cascade.Newf(cascade.KindInternal, "golden harvest: no %s importer", domain)
	}
	res, err := importer.Import(ctx, v1.Request{SourceRoot: home})
	if err != nil {
		return v1.DryRunResult{}, &importRefusal{domain: domain, input: input, cause: err}
	}
	return res, nil
}

// harvestRun is the whole harvest. It returns the process exit code.
func harvestRun(ctx context.Context, cfg runConfig, w *output.Writer) int {
	inputs, err := loadPinned(filepath.Join(cfg.moduleRoot, filepath.FromSlash(pinnedInputDir)))
	if err != nil {
		w.Fail(err)
		return exitRefused
	}
	fixtures, err := buildFixtures(ctx, cfg, inputs)
	if err == nil {
		err = guardFixtures(fixtures)
	}
	if err != nil {
		w.Fail(err)
		return exitRefused
	}
	if cfg.dryRun {
		w.Println(fmt.Sprintf("golden harvest: dry run, %d fixture(s) computed, 0 written", len(fixtures)))
		return exitOK
	}
	stats, err := applyFixtures(cfg.moduleRoot, fixtures, cfg.clock)
	if err != nil {
		w.Fail(err)
		return exitRefused
	}
	w.Println(fmt.Sprintf("golden harvest: %d fixture(s), %d written, %d unchanged, %d README(s) written",
		len(fixtures), stats.written, stats.unchanged, stats.readmes))
	return exitOK
}

// buildFixtures harvests every selected domain from its verified inputs.
func buildFixtures(ctx context.Context, cfg runConfig, inputs []inputFile) ([]fixture, error) {
	byDomain := map[v1.Domain][]inputFile{}
	for _, input := range inputs {
		byDomain[input.Domain] = append(byDomain[input.Domain], input)
	}
	var out []fixture
	for _, domain := range cfg.domains {
		files := byDomain[domain]
		var got []fixture
		var err error
		switch domain {
		case v1.DomainMemory:
			got, err = harvestMemory(ctx, cfg.clock, files)
		case v1.DomainVault:
			got, err = harvestVaultInputs(ctx, files)
		case v1.DomainAccounts:
			got, err = harvestAccountsInputs(ctx, cfg.clock, files)
		case v1.DomainConfig:
			got, err = harvestConfigInputs(ctx, files)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}
