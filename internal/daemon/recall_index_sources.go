package daemon

// Purpose: the lifecycle.SourceProvider RegisterRecallIndexHandler wires
// into its lifecycle.Manager — the ingest half of the retrieval.sources[]
// config key. Until now internal/runtime.(*Config).RetrievalSources had
// zero production callers tree-wide (DEFECT-retrieval-sources-not-wired-
// to-ingest.md): `cascade recall index rebuild` walked a SourceProvider
// nothing populated, so CorporaIndexed was always 0 against a real,
// registered, git-initialized project.
//
// Inputs: the daemon's runtime.PathProvider (config.toml's path). Sources
// re-reads the config fresh on every call — the same choice
// gitTreeHashExec/gitDiffExec already make for git state in this same
// file's sibling — so a live `cascade config set retrieval.sources` takes
// effect on the next rebuild without a daemon restart.
//
// Outputs: one lifecycle.Source per configured source root, its files
// read from disk and its corpus.Corpus classified corpus.CorpusIDCode /
// corpus.PrivacyProject / corpus.VisibilityPrivate / corpus.TrustTrusted
// under memory.DefaultScopeRef ("local", internal/memory/rpc.go) — the one
// named scope this tree already uses when no per-project scope resolver
// exists yet (that file's own doc comment: "rather than inventing a
// per-call scope the store cannot check, every record written here lands
// in one named scope"). This mirrors
// internal/migration/v1/indexrebuild.go's rootSourceProvider, the one
// existing composition of this exact shape (a directory walk feeding
// lifecycle.SourceProvider), rather than a second, divergent one.
//
// Constraints: a source root that does not exist yet is a real, convergent
// empty source, not an error, matching walkChunkableFiles's own contract.
// A file whose extension retrieval.ChunkerFor does not recognize is
// skipped before it is even read, so lifecycle.Manager.Rebuild's own
// chunkFiles (plan.go) never needs to special-case a byte this provider
// handed it. Two or more configured sources get distinct corpus ids
// (suffixed by their retrieval.sources[] index) so their manifests never
// collide in lifecycle's own per-corpus bookkeeping (store.go: "one
// [manifest] per corpus").
//
// SPORT: internal/daemon (CHANGED, retrieval.sources ingest wiring).
import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// configSourceProvider implements lifecycle.SourceProvider over the
// retrieval.sources[] config key at configPath.
type configSourceProvider struct {
	configPath string
}

// Sources implements lifecycle.SourceProvider. A config file this
// operator has never written (readAndUpgradeTree's documented
// os.IsNotExist fallback) resolves to an empty, defaulted Config whose
// RetrievalSources is nil, so a virgin HOME with no config.toml is a
// real, convergent empty source, not an error.
func (p configSourceProvider) Sources(ctx context.Context) ([]lifecycle.Source, error) {
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: p.configPath})
	if err != nil {
		return nil, err
	}
	roots := cfg.RetrievalSources()
	if len(roots) == 0 {
		return nil, nil
	}
	sources := make([]lifecycle.Source, 0, len(roots))
	for i, root := range roots {
		files, err := walkChunkableSourceFiles(root)
		if err != nil {
			return nil, err
		}
		sources = append(sources, lifecycle.Source{
			Corpus: corpus.Corpus{
				ID:         retrievalSourceCorpusID(i),
				ScopeRef:   corpus.ScopeRef(memory.DefaultScopeRef),
				Privacy:    corpus.PrivacyProject,
				Visibility: corpus.VisibilityPrivate,
				Trust:      corpus.TrustTrusted,
			},
			Files: files,
		})
	}
	return sources, nil
}

// retrievalSourceCorpusID names one configured retrieval.sources[] entry's
// corpus. corpus.CorpusIDCode is reused as the shared prefix: every
// configured source is source/text content on disk, the same kind
// internal/retrieval/gitcorpus.go's IngestGitRepo classifies a repository
// root under. Index 0 keeps the bare "code" id (the common single-source
// case, and what a --corpus filter naturally reaches for); later indices
// disambiguate, since lifecycle's per-corpus manifest bookkeeping
// (store.go) would silently merge two sources that shared one id.
func retrievalSourceCorpusID(i int) string {
	if i == 0 {
		return corpus.CorpusIDCode
	}
	return corpus.CorpusIDCode + ":" + strconv.Itoa(i)
}

// walkChunkableSourceFiles reads every file under root that
// retrieval.ChunkerFor recognizes, mirroring
// internal/migration/v1/indexrebuild.go's walkChunkableFiles (the one
// existing composition of this exact shape) rather than a second,
// divergent walk. ".git" is skipped entirely: its loose-object and pack
// files carry no extension retrieval.ChunkerFor accepts, so walking into
// it only spends time no chunk will ever come from.
func walkChunkableSourceFiles(root string) ([]lifecycle.SourceFile, error) {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, cascade.Wrapf(cascade.KindUnavailable, err,
			"recall index: stat retrieval source %s", root)
	}
	var out []lifecycle.SourceFile
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if _, chunkerErr := retrieval.ChunkerFor(path); chunkerErr != nil {
			return nil // unrecognized extension: not an error, just not this ticket's corpus
		}
		content, readErr := os.ReadFile(path) //nolint:gosec // path comes from the operator's own configured source root
		if readErr != nil {
			return readErr
		}
		out = append(out, lifecycle.SourceFile{Path: path, Content: content})
		return nil
	})
	if walkErr != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, walkErr,
			"recall index: walk retrieval source %s", root)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
