package conversation

// Purpose (this file): the typed sentinel errors Resumer
//   (resume.go, resume_topic.go, resume_query.go) returns on every refusal
//   path. Split out of resume.go under Art.10.3's 300-line cap, matching
//   errors.go's own precedent for this package's append path.
// Inputs: none -- static cascade.Error values only.
// Outputs: none.
// Constraints: PRIVACY (errors.go's own rule, repeated here because this
//   file adds new sentinels): every message below is a static string
//   literal or interpolates only a caller-supplied ID (a thread id, a
//   turn id) -- never Segment.Content, a recall.what snippet, or an
//   egress substitution error's own text, any of which could carry
//   conversation user data or a fragment of a secret the substitution
//   pass just refused to let through.
// SPORT: internal.conversation.resume/ADDED (P1-E22-W5-S47-T2).

import (
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ErrResumeInvalidRequest reports that a ResumeRequest named neither a
// TopicID nor a Query, or named both -- exactly one source is valid.
var ErrResumeInvalidRequest = cascade.New(cascade.KindInvalidInput,
	"conversation: resume: request must name exactly one of topic_id or query")

// ErrResumeMissingSessionThreadID reports that chat.resume was called with
// an absent or empty session_thread_id. The destination is always named
// explicitly; handleResume (adapter_resume.go) refuses before any store
// read. RESIDUAL (recorded): the daemon has no session binding, so a
// same-UID local caller on the 0600 socket names its own destination; it
// can already read every thread through chat.get_thread.
var ErrResumeMissingSessionThreadID = cascade.New(cascade.KindInvalidInput,
	"conversation: resume: session_thread_id is required")

// errResumeSessionThreadNotFound reports a session_thread_id that names no
// thread. A new session creates its thread with chat.create_thread first,
// so the target tier is always a stored marker, never a default.
func errResumeSessionThreadNotFound(threadID string) error {
	return cascade.Newf(cascade.KindNotFound,
		"conversation: resume: session thread %q does not exist; create it with chat.create_thread first", threadID)
}

// errResumeTopicNotFound reports that the resumed thread id does not name
// an existing thread. Static message plus the caller-supplied id only.
func errResumeTopicNotFound(topicID string) error {
	return cascade.Newf(cascade.KindNotFound, "conversation: resume: topic %q not found", topicID)
}

// ErrResumePrivacyBlocked is the sentinel every resume privacy refusal
// wraps: KindPolicyDenied. *cascade.Error.Is compares Kind only, so tests
// assert the refusal's message as well as errors.Is.
var ErrResumePrivacyBlocked = cascade.New(cascade.KindPolicyDenied,
	"conversation: resume: privacy blocked")

// errResumePrivacyRefused reports that the source thread's tier is more
// restrictive than the destination's (provider.SensitivityTier.
// MoreRestrictiveThan, order local-only > restricted > internal > public).
// Actionable: it names the thread and both tiers.
func errResumePrivacyRefused(threadID string, sourceTier, intoTier provider.SensitivityTier) error {
	return cascade.Wrapf(cascade.KindPolicyDenied, ErrResumePrivacyBlocked,
		"conversation: resume: thread %q is %s, which is more restrictive than the destination session's %s; "+
			"resume it into a session created at least as strict as %s, or resume a different thread",
		threadID, sourceTier.String(), intoTier.String(), sourceTier.String())
}

// ErrResumeConversationLegUnavailable reports that recall.what answered
// with its conversation domain unavailable (Errors["conversation"]). The
// query path resolves only conversation threads, so it refuses rather than
// compose the other domains' rows. Static message only.
var ErrResumeConversationLegUnavailable = cascade.New(cascade.KindUnavailable,
	"conversation: resume: recall.what's conversation domain is unavailable, so a query cannot be resolved to a thread yet")

// errResumeQueryUnresolvable reports that the top conversation row's Path
// is empty or names no stored thread: refused, never read as a default
// tier.
func errResumeQueryUnresolvable(path string) error {
	return cascade.Newf(cascade.KindNotFound,
		"conversation: resume: a conversation result of the query names thread %q, which does not exist", path)
}

// ErrResumeQuerierUnavailable reports that a ResumeRequest.Query was given
// but no ResumeQuerier was wired at construction (NewResumer's
// querier argument was nil) -- the query path cannot run at all, not a
// per-request failure.
var ErrResumeQuerierUnavailable = cascade.New(cascade.KindUnavailable,
	"conversation: resume: no query seam configured; only topic_id resume is available")

// ErrResumeQueryNoResults reports that recall.what returned no row in the
// conversation domain -- nothing to resume, and never an unscoped
// fallback to other domains.
var ErrResumeQueryNoResults = cascade.New(cascade.KindNotFound,
	"conversation: resume: query matched no resumable content")

// ErrResumeSubstitutionUnavailable reports that no EgressSubstitutor was
// wired at construction. Unlike sse.go's Substitutor (an optional local
// echo, nil is a documented passthrough there), Resume's substitution
// pass is the one place H/S-16.T1 stands between assembled conversation
// content and the RPC response a client reads back -- so a missing
// substitutor fails the whole call closed rather than silently skipping
// the boundary the ticket's PRIVACY constraint requires.
var ErrResumeSubstitutionUnavailable = cascade.New(cascade.KindUnavailable,
	"conversation: resume: no egress substitution boundary configured")

// ErrResumeSubstitutionFailed reports that the H/S-16.T1 substitution
// pass over the composed context block failed. Fails closed: the
// composed text is discarded, never returned unsubstituted. Static
// message only -- see this file's PRIVACY doc comment; the
// substitutor's own error text is never interpolated, since it ran over
// conversation content and could echo a fragment back.
var ErrResumeSubstitutionFailed = cascade.New(cascade.KindUnavailable,
	"conversation: resume: egress substitution failed for the composed context block")

// errResumeComposeFailed wraps a Composer.Compose failure. err is a
// cascade.Error from composer_errors.go carrying only Kind/Label/a fixed
// reason (composer_types.go's own PRIVACY rule) -- safe to wrap, never a
// raw slot content string.
func errResumeComposeFailed(err error) error {
	return cascade.Wrap(cascade.KindUnavailable, err, "conversation: resume: compose context")
}

// errResumeNilStore/errResumeNilComposer report that
// NewResumer was called with a required dependency missing.
// Neither store nor composer has a useful degraded mode (unlike querier/
// subst, which fail closed per-call instead) so construction itself
// refuses.
func errResumeNilStore() error {
	return cascade.New(cascade.KindInvalidInput, "conversation: resume: no store configured")
}

func errResumeNilComposer() error {
	return cascade.New(cascade.KindInvalidInput, "conversation: resume: no composer configured")
}
