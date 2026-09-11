// Package retrieval (this file) is the code-corpus type: a git repository
// root as an ingest source, enumerated through the real git CLI so only
// tracked files are read (never a .gitignore'd artifact), chunked by the
// F/S-10.T1 code-aware chunker already in this package (code.go), and
// handed back as a classified corpus.Corpus plus its Chunks for the
// existing F/S-10.T2/T3 ingest pipeline to write.
//
// Purpose: register a first-class "code" corpus kind alongside the
// existing scope/trust/privacy/visibility dimensions internal/retrieval/
// corpus already defines, without adding any graph feature (call graphs,
// import graphs, clustering) — those are explicitly deferred to P2
// (DEF-P2-graphrag) and never appear here.
//
// This ticket's files_scope names internal/retrieval/corpus/code.go as
// the intended location. That location is unreachable without an import
// cycle: internal/retrieval/corpus is a leaf package internal/retrieval's
// own fusion sub-package already imports (retrieval -> fusion -> corpus),
// so corpus importing retrieval (for CodeChunker) closes a cycle the
// compiler refuses. This file lives in package retrieval instead, the
// layer already positioned above corpus (internal/retrieval/embed does
// exactly this same corpus+retrieval combination). corpus/registry.go
// still carries the kind registration (CorpusIDCode, ValidateCorpusKind),
// which has no such dependency and stays at the ticket's named path.
//
// Inputs: a repository root path and the caller's chosen corpus.ScopeRef
// and corpus.TrustLevel (never hardcoded to "trusted" here: an
// untrusted-tagged source must propagate through this seam exactly as
// given, per F/S-10.T4's trust dimension).
//
// Outputs: a validated corpus.Corpus{ID: corpus.CorpusIDCode} plus the
// ordered []Chunk every tracked file produced. This file writes nothing
// to any store: internal/retrieval/embed's Pipeline.Run and this
// package's Indexer.Write are the write paths, fed by this function's
// return value.
//
// Constraints: git ls-files runs through exec, the R-14.80 decision (git
// is already an Art.2 external contract exercised in tests; no new
// module dependency). No platform-conditional code path: git's own
// output uses NUL-separated relative paths on every OS the test
// environment runs on, including Windows, so no separator normalizer is
// needed. A file-read or chunker failure refuses the whole ingest rather
// than silently dropping the failed file, so a caller never mistakes a
// partial index for a complete one.
//
// SPORT: internal.retrieval.IngestGitRepo/ADDED,
//
//	internal.retrieval.GitTrackedFiles/ADDED (P1-E25-W5-S52-T6).
package retrieval

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// maxCodeFileBytes bounds a single tracked file's read. A file above this
// is refused rather than truncated, so a truncated chunk is never mistaken
// for a complete one.
const maxCodeFileBytes = 2 << 20 // 2 MiB

// maxCodeTrackedFiles bounds how many tracked files one ingest call
// accepts, so a caller pointed at an unexpectedly enormous repository
// gets a typed refusal instead of an unbounded read loop.
const maxCodeTrackedFiles = 20000

// GitTrackedFiles enumerates repoRoot's git-tracked files using the real
// git CLI (`git -C repoRoot ls-files -z`), so untracked and .gitignore'd
// files are never ingested. Paths are returned relative to repoRoot, in
// git's own index order.
func GitTrackedFiles(ctx context.Context, repoRoot string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "ls-files", "-z")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err,
			"retrieval: git ls-files in %q: %s", repoRoot, strings.TrimSpace(stderr.String()))
	}
	trimmed := strings.TrimSuffix(stdout.String(), "\x00")
	if trimmed == "" {
		return nil, nil
	}
	raw := strings.Split(trimmed, "\x00")
	if len(raw) > maxCodeTrackedFiles {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"retrieval: %d tracked files exceeds the %d-file bound", len(raw), maxCodeTrackedFiles)
	}
	return raw, nil
}

// IngestGitRepo enumerates repoRoot's tracked files, chunks each through
// the code-aware chunker, and returns the classified corpus.Corpus plus
// every chunk produced. trust is threaded through unchanged, never
// overridden to "trusted" by this function.
func IngestGitRepo(
	ctx context.Context, repoRoot string, scopeRef corpus.ScopeRef, trust corpus.TrustLevel,
) (corpus.Corpus, []Chunk, error) {
	if err := corpus.ValidateCorpusKind(corpus.CorpusIDCode); err != nil {
		return corpus.Corpus{}, nil, err
	}
	c := corpus.Corpus{
		ID:         corpus.CorpusIDCode,
		ScopeRef:   scopeRef,
		Privacy:    corpus.PrivacyProject,
		Visibility: corpus.VisibilityPrivate,
		Trust:      trust,
	}
	if err := c.Validate(); err != nil {
		return corpus.Corpus{}, nil, err
	}

	files, err := GitTrackedFiles(ctx, repoRoot)
	if err != nil {
		return corpus.Corpus{}, nil, err
	}

	chunker := &CodeChunker{}
	var chunks []Chunk
	for _, rel := range files {
		if rel == "" {
			continue
		}
		data, err := readTrackedFile(filepath.Join(repoRoot, rel))
		if err != nil {
			return corpus.Corpus{}, nil, cascade.Wrapf(cascade.KindUnavailable, err,
				"retrieval: reading tracked file %q", rel)
		}
		cs, err := chunker.Chunk(rel, data)
		if err != nil {
			return corpus.Corpus{}, nil, cascade.Wrapf(cascade.KindInvalidInput, err,
				"retrieval: chunking %q", rel)
		}
		chunks = append(chunks, cs...)
	}
	return c, chunks, nil
}

// readTrackedFile reads path bounded by maxCodeFileBytes, refusing rather
// than truncating a file over the bound.
func readTrackedFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxCodeFileBytes {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"retrieval: %q is %d bytes, over the %d-byte bound", path, info.Size(), maxCodeFileBytes)
	}
	return os.ReadFile(path)
}
