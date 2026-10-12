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
// needed. A chunker failure refuses the whole ingest rather than silently
// dropping the failed file, so a caller never mistakes a partial index for
// a complete one. Admission is different: every tracked path goes through
// repo.AdmitBatch (the one ingest guard), and a path it refuses (dotfile,
// vendored, key material, over 1 MiB, secret-bearing, unreadable) is
// skipped and reported, never chunked.
//
// SPORT: internal.retrieval.IngestGitRepo/ADDED,
//
//	internal.retrieval.GitTrackedFiles/ADDED (P1-E25-W5-S52-T6).
package retrieval

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

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

// admitBatch is the guard call; tests wrap it to observe slices and to
// swap a file between the scan and the chunking.
var admitBatch = repo.AdmitBatch

// IngestGitRepo enumerates repoRoot's tracked files, admits them through
// repo.AdmitBatch in slices of at most repo.MaxAdmitBatch, chunks only the
// admitted bytes (Admission.Content, the bytes the secret detector
// scanned; the file is never read a second time) through the code-aware
// chunker, and returns the classified corpus.Corpus, every chunk produced
// and the refused admissions (Rel and Reason only; no content). A slice's
// Content is dropped before the next slice is admitted. trust is threaded
// through unchanged, never overridden to "trusted" by this function.
func IngestGitRepo(
	ctx context.Context, repoRoot string, scopeRef corpus.ScopeRef, trust corpus.TrustLevel,
) (corpus.Corpus, []Chunk, []repo.Admission, error) {
	if err := corpus.ValidateCorpusKind(corpus.CorpusIDCode); err != nil {
		return corpus.Corpus{}, nil, nil, err
	}
	c := corpus.Corpus{
		ID:         corpus.CorpusIDCode,
		ScopeRef:   scopeRef,
		Privacy:    corpus.PrivacyProject,
		Visibility: corpus.VisibilityPrivate,
		Trust:      trust,
	}
	if err := c.Validate(); err != nil {
		return corpus.Corpus{}, nil, nil, err
	}

	files, err := GitTrackedFiles(ctx, repoRoot)
	if err != nil {
		return corpus.Corpus{}, nil, nil, err
	}

	var chunks []Chunk
	var refused []repo.Admission
	for start := 0; start < len(files); start += repo.MaxAdmitBatch {
		end := min(start+repo.MaxAdmitBatch, len(files))
		cs, rs, err := admitAndChunk(ctx, repoRoot, files[start:end])
		if err != nil {
			return corpus.Corpus{}, nil, nil, err
		}
		chunks = append(chunks, cs...)
		refused = append(refused, rs...)
	}
	return c, chunks, refused, nil
}

// admitAndChunk admits one slice and chunks what was admitted. Each
// admission's Content is released as soon as it is chunked.
func admitAndChunk(ctx context.Context, repoRoot string, rels []string) ([]Chunk, []repo.Admission, error) {
	adm, err := admitBatch(ctx, repoRoot, rels)
	if err != nil {
		return nil, nil, err
	}
	chunker := &CodeChunker{}
	var chunks []Chunk
	var refused []repo.Admission
	for i := range adm {
		a := &adm[i]
		if !a.Admitted {
			refused = append(refused, repo.Admission{Rel: a.Rel, Reason: a.Reason, PatternClass: a.PatternClass})
			continue
		}
		cs, err := chunker.Chunk(a.Rel, a.Content)
		a.Content = nil
		if err != nil {
			return nil, nil, cascade.Wrapf(cascade.KindInvalidInput, err, "retrieval: chunking %q", a.Rel)
		}
		chunks = append(chunks, cs...)
	}
	return chunks, refused, nil
}
