// Purpose: `cascade docs gen`, the daemonless generator for the CLI reference
//
//	pages (./docs/cli) and the config-key reference (./docs/reference).
//
// Inputs:  the production root built on an empty CASCADE_HOME, the working
//
//	directory's ./docs tree.
//
// Outputs: generator-owned markdown pages; nothing outside ./docs/cli and
//
//	./docs/reference/config-keys.md is written.
//
// Constraints: never reads the operator's installed plugins or config; a
//
//	foreign .md file in ./docs/cli is a conflict, never overwritten.
//
// SPORT: cmd/cascade docs generator (P1-DOC-04c).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/runtime/initconfig"
	"github.com/acamarata/cascade/internal/tools/docgen"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	docsCLIDir     = "docs/cli"
	docsRefDir     = "docs/reference"
	docsConfigPage = "config-keys.md"
	docsGuideDir   = "docs/cli-reference"
)

// docsGenResult is the --json payload of `docs gen`.
type docsGenResult struct {
	Files []string `json:"files"`
}

// mountDocsCmd attaches the `docs` noun with its `gen` verb.
func mountDocsCmd(root *cobra.Command) {
	docs := &cobra.Command{
		Use:   "docs",
		Short: "Generate the reference documentation",
	}
	docs.AddCommand(&cobra.Command{
		Use:   "gen",
		Short: "Write the CLI and config-key reference pages under ./docs",
		Args:  usageArgs(cobra.NoArgs),
		RunE:  runDocsGen,
	})
	root.AddCommand(docs)
	suppressEmbeddedWarning(root, "docs")
}

func runDocsGen(cmd *cobra.Command, _ []string) error {
	asJSON := globalFlags.JSON // newRootCmd below rebinds globalFlags
	files, err := genDocs(".")
	if err != nil {
		return err
	}
	if asJSON {
		line, err := output.NewOKEnvelope(docsGenResult{Files: files}).MarshalLine()
		if err != nil {
			return cascade.Wrap(cascade.KindInternal, err, "encode docs gen result")
		}
		_, err = cmd.OutOrStdout().Write(line)
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "wrote %d files under %s and %s\n", len(files), docsCLIDir, docsRefDir)
	return err
}

// genDocs renders the production root and writes the owned trees below base.
// It returns the slash-separated paths of the pages written.
func genDocs(base string) ([]string, error) {
	docs := filepath.Join(base, "docs")
	if st, err := os.Stat(docs); err != nil || !st.IsDir() {
		return nil, cascade.Newf(cascade.KindNotFound, "docs: %s not found; run from the repository root", docs)
	}
	cliDir := filepath.Join(base, filepath.FromSlash(docsCLIDir))
	if err := refuseForeign(cliDir); err != nil {
		return nil, err
	}
	root, err := docsRoot()
	if err != nil {
		return nil, err
	}
	pages := withGuides(base, docgen.RenderCommands(root))
	// An up-to-date tree is left alone: WriteTree syncs every page.
	if drift, err := docgen.DiffTree(cliDir, pages); err != nil || len(drift) > 0 {
		if err := docgen.WriteTree(cliDir, pages); err != nil {
			return nil, err
		}
	}
	cfg := docgen.RenderConfigKeys(docsConfigPage, configKeyRows())
	if err := docgen.WriteTree(filepath.Join(base, filepath.FromSlash(docsRefDir)), []docgen.Page{cfg}); err != nil {
		return nil, err
	}
	files := make([]string, 0, len(pages)+1)
	for _, p := range pages {
		files = append(files, docsCLIDir+"/"+p.Path)
	}
	return append(files, docsRefDir+"/"+cfg.Path), nil
}

// docsRoot builds the tree to render; tests replace it with a small tree.
var docsRoot = emptyHomeRoot

// emptyHomeRoot builds the production root with CASCADE_HOME pointing at an
// empty directory, so no installed plugin or operator config is read.
func emptyHomeRoot() (*cobra.Command, error) {
	home, err := os.MkdirTemp("", "cascade-docs-home-")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "docs: create empty home")
	}
	defer func() { _ = os.RemoveAll(home) }() // best effort: an empty temp dir
	prev, had := os.LookupEnv("CASCADE_HOME")
	if err := os.Setenv("CASCADE_HOME", home); err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "docs: set CASCADE_HOME")
	}
	defer func() {
		if had {
			_ = os.Setenv("CASCADE_HOME", prev)
		} else {
			_ = os.Unsetenv("CASCADE_HOME")
		}
	}()
	saved := globalFlags
	defer func() { globalFlags = saved }()
	return newRootCmd(), nil
}

// refuseForeign reports KindConflict for any .md file in dir that is not
// generator-owned: the directory is the generator's, a stray page is not.
func refuseForeign(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return cascade.Wrapf(cascade.KindUnavailable, err, "docs: read %s", dir)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "docs: read %s", e.Name())
		}
		if !strings.HasPrefix(string(b), docgen.OwnedMarker) {
			return cascade.Newf(cascade.KindConflict, "docs: %s/%s exists and is not generator-owned", docsCLIDir, e.Name())
		}
	}
	return nil
}

// withGuides appends a Guide line to each root noun page whose hand-written
// guide docs/cli-reference/<noun>.md exists under base.
func withGuides(base string, pages []docgen.Page) []docgen.Page {
	out := make([]docgen.Page, 0, len(pages))
	for _, p := range pages {
		noun, ok := strings.CutPrefix(strings.TrimSuffix(p.Path, ".md"), "cascade_")
		if ok && !strings.Contains(noun, "_") {
			guide := filepath.Join(base, filepath.FromSlash(docsGuideDir), noun+".md")
			if _, err := os.Stat(guide); err == nil {
				p.Body = append(append([]byte(nil), p.Body...),
					[]byte(fmt.Sprintf("Guide: [%s](../cli-reference/%s.md)\n", noun, noun))...)
			}
		}
		out = append(out, p)
	}
	return out
}

// configKeyRows lists every key of the two key registries: the dotted paths
// `cascade config set` accepts (runtime.KnownConfigKeys) and the
// cascade-init.toml schema keys (initconfig.KnownKeys). The registries are
// the source of truth, so the page lists exactly what the code accepts.
func configKeyRows() []docgen.ConfigKey {
	cfg := runtime.KnownConfigKeys()
	initKeys := initconfig.KnownKeys()
	rows := make([]docgen.ConfigKey, 0, len(cfg)+len(initKeys))
	for _, k := range cfg {
		rows = append(rows, docgen.ConfigKey{Key: k, Doc: "config.toml key (`cascade config set`)"})
	}
	for _, k := range initKeys {
		rows = append(rows, docgen.ConfigKey{Key: k, Doc: "cascade-init.toml key"})
	}
	return rows
}
