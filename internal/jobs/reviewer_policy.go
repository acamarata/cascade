package jobs

// Purpose: the reviewer-side policy for read-only job kinds (P1-PBD-01a):
//
//	a BLIND review context built by allowlist, a check that a read-only
//	node holds no mutable lease scope, and a verifier that the worktree
//	tree a reviewer saw is the tree it left behind.
//
// Inputs: job/node ids and candidate review files (BuildCleanReviewContext),
//
//	a template kind plus its DagNode (EnforceReadOnlyLease), and the tree
//	ids recorded before and after a review (VerifyReadOnly).
//
// Outputs: a ReviewContext, or a typed *cascade.Error sentinel.
// Constraints: the context is built field by field from an allowlist; it
//
//	is never a fuller context trimmed afterwards. ReviewContext has no
//	field for session, transcript, author, lane, family, prior verdict or
//	outcome hint. Instruction files (.claude/**, AGENTS.md, CLAUDE.md and
//	the like) never reach a reviewer. jobs is L2 and imports no review
//	package. Every refusal fails closed.
// SPORT: jobs/reviewer-policy/ADD (P1-PBD-01a).

import (
	"path"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ErrReviewContextIDs is returned when BuildCleanReviewContext is given an
// empty job or node id: a context that names no job cannot be audited.
var ErrReviewContextIDs = cascade.New(cascade.KindInvalidInput,
	"jobs: review context needs a job id and a node id")

// ErrReviewerMutatedWorktree is returned by VerifyReadOnly when the tree
// after a review differs from the tree before it, or either side is
// unknown. An unknown side is treated as a mutation: it cannot be proven
// otherwise.
var ErrReviewerMutatedWorktree = cascade.New(cascade.KindIntegrity,
	"jobs: reviewer mutated the worktree")

// ErrMutableReview is returned by EnforceReadOnlyLease when a read-only
// kind (review, adversarial, qa, ci) carries a mutable lease scope.
var ErrMutableReview = cascade.New(cascade.KindPolicyDenied,
	"jobs: read-only review node holds a mutable lease scope")

// ReviewFile is one file a reviewer may read: its repo-relative path, its
// content and the provenance of that content. Untrusted content is data
// for the reviewer, never an instruction to it.
type ReviewFile struct {
	Path       string
	Content    string
	Provenance provider.Provenance
}

// ReviewContext is the blind input of one review dispatch. Its field set
// is the whole allowlist: the job and node under review, the base tree the
// diff applies to, the diff itself and the files the reviewer may read.
// Author, lane, family, session, transcript, prior verdict and expected
// outcome are not fields and are unreachable from it.
type ReviewContext struct {
	JobID    string
	NodeID   string
	BaseTree string
	Diff     string
	Files    []ReviewFile
}

// BuildCleanReviewContext builds a ReviewContext from exactly the allowed
// inputs. Empty ids are refused with ErrReviewContextIDs. A file whose
// path is empty, absolute, escapes the root or names an instruction file
// is dropped. Each kept file's provenance is parsed closed, so an unset or
// unknown value becomes ProvenanceUntrustedSource.
func BuildCleanReviewContext(jobID, nodeID, baseTree, diff string, files []ReviewFile) (ReviewContext, error) {
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(nodeID) == "" {
		return ReviewContext{}, ErrReviewContextIDs
	}
	kept := make([]ReviewFile, 0, len(files))
	for _, f := range files {
		clean, ok := cleanReviewPath(f.Path)
		if !ok || isInstructionPath(clean) {
			continue
		}
		prov, err := provider.ParseProvenance(string(f.Provenance))
		if err != nil {
			prov = provider.ProvenanceUntrustedSource
		}
		kept = append(kept, ReviewFile{Path: clean, Content: f.Content, Provenance: prov})
	}
	return ReviewContext{
		JobID:    jobID,
		NodeID:   nodeID,
		BaseTree: baseTree,
		Diff:     diff,
		Files:    kept,
	}, nil
}

// cleanReviewPath normalises p to a slash-separated repo-relative path. It
// reports false for an empty, absolute or root-escaping path.
func cleanReviewPath(p string) (string, bool) {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if p == "" || strings.HasPrefix(p, "/") || (len(p) > 1 && p[1] == ':') {
		return "", false
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// instructionDirs are directory names whose whole subtree is agent
// instruction material. Matching is case-insensitive.
var instructionDirs = map[string]bool{
	".claude": true, ".cursor": true, ".codex": true, ".gemini": true,
	".windsurf": true, ".aider": true, ".continue": true,
}

// instructionFiles are base names that carry agent instructions wherever
// they sit in the tree. Matching is case-insensitive.
var instructionFiles = map[string]bool{
	"agents.md": true, "claude.md": true, "gemini.md": true,
	"claude.local.md": true, ".cursorrules": true, ".windsurfrules": true,
	"copilot-instructions.md": true, ".clinerules": true,
}

// isInstructionPath reports whether clean names an instruction file or
// lies under an instruction directory.
func isInstructionPath(clean string) bool {
	segs := strings.Split(strings.ToLower(clean), "/")
	for _, s := range segs[:len(segs)-1] {
		if instructionDirs[s] {
			return true
		}
	}
	last := segs[len(segs)-1]
	return instructionFiles[last] || instructionDirs[last]
}

// readOnlyKinds are the template kinds whose nodes must hold no mutable
// lease scope (template_kinds.go sets their MutableScope empty).
var readOnlyKinds = map[string]bool{
	TemplateKindReview:      true,
	TemplateKindAdversarial: true,
	TemplateKindQA:          true,
	TemplateKindCI:          true,
}

// EnforceReadOnlyLease returns ErrMutableReview when kind is a read-only
// kind and node declares any mutable scope entry (a blank entry included:
// it fails closed), and nil otherwise.
func EnforceReadOnlyLease(kind string, node DagNode) error {
	if readOnlyKinds[kind] && len(node.MutableScope) > 0 {
		return ErrMutableReview
	}
	return nil
}

// VerifyReadOnly compares the tree id recorded before a review with the
// one recorded after it. A mismatch, or an empty side, returns
// ErrReviewerMutatedWorktree; equal non-empty ids return nil.
func VerifyReadOnly(beforeTree, afterTree string) error {
	b, a := strings.TrimSpace(beforeTree), strings.TrimSpace(afterTree)
	if b == "" || a == "" || b != a {
		return ErrReviewerMutatedWorktree
	}
	return nil
}
