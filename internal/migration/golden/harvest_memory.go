// Purpose: harvest the memory domain: stage every pinned memory input into
// one scratch v1 home, run the real memory importer into a scratch v2
// FileStore, read each written v2 file back, and redact it per input file.
// Inputs: the verified memory inputs and the injected clock.
// Outputs: one fixture per input file holding the v2 frontmatter map, the
// body and that file's importer Change, all through the Redactor.
// Constraints: the v1 memory importer reads a whole directory, so all memory
// inputs share one scratch home; a tombstone or an unreadable v2 file is a
// refusal rather than a guess.
// SPORT: migration/golden/ADD (P1-E26-W10-S54-T3).
package main

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/memory"
	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// harvestMemory returns the memory fixtures for files.
func harvestMemory(ctx context.Context, clock runtime.Clock, files []inputFile) ([]fixture, error) {
	if len(files) == 0 {
		return nil, nil
	}
	scratch, err := newScratch()
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	home := filepath.Join(scratch, "home")
	for _, file := range files {
		if err := stage(home, ".cascade/memory/"+path.Base(file.Rel), file.Data); err != nil {
			return nil, err
		}
	}
	storeDir := filepath.Join(scratch, "v2-memory")
	importer := importerFor(v1.DomainMemory, importerDeps{store: memory.NewFileStore(storeDir, clock)})
	res, err := runImporter(ctx, importer, v1.DomainMemory, "memory", home)
	if err != nil {
		return nil, err
	}
	out := make([]fixture, 0, len(files))
	for _, file := range files {
		fx, err := memoryFixture(storeDir, file, res)
		if err != nil {
			return nil, err
		}
		out = append(out, fx)
	}
	return out, nil
}

// memoryFixture builds the record for one memory input from its Change and
// its read-back v2 file.
func memoryFixture(storeDir string, file inputFile, res v1.DryRunResult) (fixture, error) {
	source := ".cascade/memory/" + path.Base(file.Rel)
	var mine []v1.Change
	for _, change := range res.Changes {
		if change.Source == source {
			mine = append(mine, change)
		}
	}
	if len(mine) != 1 || mine[0].Operation != v1.OperationCreate {
		return fixture{}, cascade.Newf(cascade.KindIntegrity,
			"golden harvest: memory %s did not produce exactly one created v2 file", file.Rel)
	}
	target := mine[0].Target
	if !safeMemoryTarget(target) {
		return fixture{}, cascade.Newf(cascade.KindIntegrity, "golden harvest: memory %s has an unsafe target", file.Rel)
	}
	data, err := os.ReadFile(filepath.Join(storeDir, filepath.FromSlash(target)+".md")) //nolint:gosec // validated kind/name inside scratch
	if err != nil {
		return fixture{}, cascade.Wrapf(cascade.KindIntegrity, err, "golden harvest: read back memory %s", file.Rel)
	}
	frontmatter, body, err := parseV2Memory(data)
	if err != nil {
		return fixture{}, err
	}
	r := newRedactor(v1.DomainMemory, file.Rel)
	record := r.dryRun(v1.DryRunResult{Changes: mine})
	record["frontmatter"] = r.memoryFrontmatter(frontmatter)
	record["body"] = body
	return newFixture(v1.DomainMemory, "memory", file, record)
}

// parseV2Memory splits a v2 memory file into its frontmatter map (values
// JSON-unquoted where quoted) and its body.
func parseV2Memory(data []byte) (map[string]string, string, error) {
	text := string(data)
	malformed := cascade.New(cascade.KindIntegrity, "golden harvest: v2 memory file is malformed")
	if !strings.HasPrefix(text, "---\n") {
		return nil, "", malformed
	}
	header, body, ok := strings.Cut(text[len("---\n"):], "\n---\n")
	if !ok {
		return nil, "", malformed
	}
	fields := map[string]string{}
	for _, line := range strings.Split(header, "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok || key == "" {
			return nil, "", malformed
		}
		if strings.HasPrefix(value, `"`) {
			var unquoted string
			if err := json.Unmarshal([]byte(value), &unquoted); err != nil {
				return nil, "", malformed
			}
			value = unquoted
		}
		fields[key] = value
	}
	return fields, body, nil
}

// safeMemoryTarget reports whether target is "<known kind>/<valid name>", so
// the read-back path cannot leave the scratch store.
func safeMemoryTarget(target string) bool {
	kind, name, ok := strings.Cut(target, "/")
	if !ok {
		return false
	}
	if _, err := memory.ParseKind(kind); err != nil {
		return false
	}
	return memory.ValidateName(name) == nil
}
