package conversation

// Purpose (this file): the owner-CLI privacy gate for chat.list_threads,
//   chat.get_thread, chat.search, chat.resume and chat.reply_open/chat.reply.
//   Local-only threads are served only when servesLocalOnly reports true;
//   every other caller sees them dropped or refused. Composed after the
//   P1-SEC-14 principal scope filter in scopedListThreads/scopedGetThread.
// SPORT: internal.conversation.adapter (ADDED -- p1-cpa-04).

import (
	"context"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// servesLocalOnly reports whether ctx carries the owner-CLI marker with no
// principal attached. True means the request arrived on the daemon RPC
// socket carrying HeaderOwnerCLI (P1-SEC-18 as routed 2026-09-23); the MCP
// socket peer and the in-process stdio registry are served the owner view
// with OwnerCLI == false (P1-SEC-38 as routed), so they never satisfy this.
func servesLocalOnly(ctx context.Context) bool {
	c, ok := principal.CallerFrom(ctx)
	if !ok {
		return false
	}
	return c.OwnerCLI && c.Principal == nil
}

// filterListableThreads drops local-only threads from a list result unless
// servesLocalOnly is true. A ThreadPrivacy read failure returns
// KindUnavailable wrapping the store error and no partial list; order of
// admitted threads is preserved.
func (a *Adapter) filterListableThreads(ctx context.Context, threads []Thread) ([]Thread, error) {
	out := make([]Thread, 0, len(threads))
	for _, t := range threads {
		tier, err := a.store.ThreadPrivacy(ctx, t.ID)
		if err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "conversation: chat.list_threads: privacy read failed")
		}
		if tier == provider.SensitivityLocalOnly && !servesLocalOnly(ctx) {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// listScopedThreads applies principal scope before reading privacy tiers.
func (a *Adapter) listScopedThreads(ctx context.Context, threads []Thread) ([]Thread, error) {
	admits, err := CallerThreadView(ctx, a.store, a.clock, "")
	if err != nil {
		return nil, err
	}
	out := make([]Thread, 0, len(threads))
	for _, thread := range threads {
		if admits(thread.ID) {
			out = append(out, thread)
		}
	}
	return out, nil
}
