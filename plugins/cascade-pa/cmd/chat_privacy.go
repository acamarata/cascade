package cmd

// Purpose (this file): `cascade chat --private` / `--local-only` — the
//   07-CLI-COMMAND-TREE per-thread privacy modes, resolved to the §5.16
//   tier name the daemon records on the thread it creates.
//
// Its own file because chat.go is at Art.10.3's 300-line cap, and because
// this is one responsibility: two flags in, one tier name or one usage
// error out, with no I/O and nothing to mock.
//
// SPORT: plugins/cascade-pa:cmd:chat privacy flags (ADD) — P1-E20-W5-S44-T2.

import "github.com/acamarata/cascade/pkg/cascade"

// The two §5.16 tier names `cascade chat` can put on a new thread.
const (
	chatPrivacyLocalOnly  = "local-only"
	chatPrivacyRestricted = "restricted"
)

// errChatPrivacyFlagsConflict is --private and --local-only together.
//
// A usage error rather than a precedence rule: the two flags name
// different tiers, and silently picking one would give an operator who
// asked for both a thread under a mode they did not choose. Picking the
// STRICTER one would be defensible and is still wrong to do silently --
// the operator would never learn their command said two things.
var errChatPrivacyFlagsConflict = cascade.New(cascade.KindInvalidInput,
	"cascade chat: --private and --local-only name different privacy modes; pass at most one")

// resolveChatPrivacy turns the two flags into the tier name to send.
//
// Neither flag returns "", NOT "restricted": the fail-closed default is
// the DAEMON's to apply (an absent privacy row already means restricted),
// and sending an explicit tier for a caller who named none would make
// every thread look deliberately marked in the store. The one place that
// distinction matters is an audit asking which threads an operator chose
// a mode for.
func resolveChatPrivacy(opts chatOptions) (string, error) {
	switch {
	case opts.private && opts.localOnly:
		return "", errChatPrivacyFlagsConflict
	case opts.localOnly:
		return chatPrivacyLocalOnly, nil
	case opts.private:
		return chatPrivacyRestricted, nil
	default:
		return "", nil
	}
}
