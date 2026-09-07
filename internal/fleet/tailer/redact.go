package tailer

import (
	"path/filepath"
	"strings"
	"time"
)

// Purpose: the R-21.152 FIRST BOUNDARY redaction. The tailer reads the
//   harness's own transcript, so it is the first and only place in the
//   system that can guarantee a raw transcript line, a prompt, or the
//   model's free text never propagates further. Every rawEvent produced
//   by parser.go passes through redact before Next() returns it.
// Inputs: a rawEvent (parser.go's internal decode) and the worktree root
//   any file-path field is scoped against.
// Outputs: a Record carrying ONLY the closed allowlist: tool name,
//   duration, exit code, worktree-relative file paths, and a result
//   hash. There is no field on Record for raw content, so a caller
//   cannot leak what it was never handed.
// Constraints: fail-closed with no permissive default (R-21.152) — a
//   file path outside worktreeRoot, or any field not on this allowlist,
//   is dropped, never passed through "just in case". An empty
//   worktreeRoot drops every path, since containment cannot be verified
//   against an unknown root.
//
//   Known incompleteness, stated plainly: no fixture committed for this
//   ticket (internal/context/testdata/transcripts/{cc,codex}-sample-
//   redacted.jsonl) contains a tool-call line — every real line observed
//   is a prompt/message/session-metadata event with no tool name,
//   duration, exit code, path, or hash field. parser.go therefore never
//   populates those rawEvent fields from a real line today; this
//   function's allowlist and drop logic is complete and unit-tested
//   against constructed rawEvent values (redact_test.go), ready to be
//   wired to a real per-harness tool-call JSON shape once a fixture
//   demonstrating one exists. Inventing that shape now, with no committed
//   evidence for it, would be exactly the self-authored-dialect risk
//   Art.2 forbids.
// SPORT: fleet/tailer (ADD, per T-2 sport_updates).

// Record is the reduced, redacted unit Next() returns. Per R-21.152 it
// carries ONLY this closed allowlist — there is structurally no field
// for a raw transcript line, a prompt, or the model's free text.
type Record struct {
	// Tool is the tool name a transcript tool-call line named, or "" when
	// the source line carried none.
	Tool string
	// Duration is the tool call's reported duration, or 0 when the source
	// line carried none.
	Duration time.Duration
	// ExitCode is the tool call's reported exit status. Only meaningful
	// when the source line actually carried an exit code; callers that
	// need to distinguish "0" from "absent" should not rely on this
	// package alone to do so, since Record intentionally carries no
	// separate presence flag (it is not on the allowlist).
	ExitCode int
	// Paths holds worktree-relative file paths the source line named.
	// A path outside the worktree root is dropped rather than included,
	// fail-closed.
	Paths []string
	// ResultHash is the tool call's reported result hash, or "" when the
	// source line carried none.
	ResultHash string
}

// redact reduces ev to the allowlisted Record, dropping anything not on
// the allowlist and any path outside worktreeRoot.
func redact(ev rawEvent, worktreeRoot string) Record {
	rec := Record{
		Tool:       ev.tool,
		ResultHash: ev.resultHash,
	}
	if ev.durationMS > 0 {
		rec.Duration = time.Duration(ev.durationMS) * time.Millisecond
	}
	if ev.hasExit {
		rec.ExitCode = ev.exitCode
	}
	for _, p := range ev.paths {
		if rel, ok := worktreeRelative(worktreeRoot, p); ok {
			rec.Paths = append(rec.Paths, rel)
		}
	}
	return rec
}

// worktreeRelative reports whether p lies within root, returning its
// root-relative form when it does. An empty root or an empty p is never
// considered contained (fail-closed: containment cannot be verified
// against an unknown root), and a resolved path that climbs above root
// via ".." is rejected even if the string form looked contained.
func worktreeRelative(root, p string) (string, bool) {
	if root == "" || p == "" {
		return "", false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	absPath, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}
