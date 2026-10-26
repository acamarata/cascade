package cmd

// Purpose (this file): a `cascade chat` session's destination thread, and
//   the ONE resume path both the --resume-topic/--resume-query flags and
//   the in-session /resume command call (resumeIntoSession).
// Inputs: the session's operator-named thread (--thread, or "") and the
//   tier name its privacy flags resolve to ("" when none was given).
// Outputs: the session thread id, created on first need through
//   Client.CreateThread; a ResumeResult for the session thread.
// Constraints: a session thread is created ONLY by the daemon
//   (chat.create_thread), with the flag tier, or "restricted" when no flag
//   was given, so its marker is always stored before chat.resume reads it
//   as the target tier. An operator-named thread is never switched: resume
//   is admitted into it and the next append goes to it. A refused resume
//   leaves a created thread with zero turns and appends nothing.
// SPORT: plugins/cascade-pa:cmd:resume (ADD) -- P1-E22-W5-S47-T2.

import (
	"context"
	"sync"
)

// chatSession is one `cascade chat` session's destination thread state,
// shared between the submit closure and the resume goroutine in TUI mode.
type chatSession struct {
	mu      sync.Mutex
	thread  string // "" until named by --thread or created
	tier    string // resolveChatPrivacy's result; "" when no flag was given
	created bool   // true once ensure created the thread
}

// newChatSession returns a session over an operator-named thread (or "")
// and the tier name its flags resolved to.
func newChatSession(thread, tier string) *chatSession {
	return &chatSession{thread: thread, tier: tier}
}

// ensure returns the session thread, creating it through client with the
// flag tier (restricted when no flag was given) when the session has none.
func (s *chatSession) ensure(ctx context.Context, client Client) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.thread != "" {
		return s.thread, nil
	}
	tier := s.tier
	if tier == "" {
		tier = chatPrivacyRestricted
	}
	id, err := client.CreateThread(ctx, tier)
	if err != nil {
		return "", err
	}
	s.thread, s.created = id, true
	return id, nil
}

// snapshot returns the current thread and whether this session created it.
func (s *chatSession) snapshot() (thread string, created bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.thread, s.created
}

// appendRequest builds the next turn's request on this session. The flag
// tier rides along only while the session has no thread yet (the daemon
// then mints the thread and marks it in the same transaction); once a
// thread exists its marker is already stored and no privacy_mode is sent.
func (s *chatSession) appendRequest(prompt, leading string) OneShotRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	privacy := ""
	if s.thread == "" {
		privacy = s.tier
	}
	return OneShotRequest{Thread: s.thread, Prompt: prompt, Privacy: privacy, LeadingContext: leading}
}

// resumeIntoSession is the ONE resume call: it makes sure the session has
// a thread, then asks the daemon to compose req's source INTO it. The flag
// path (applyChatResume) and the /resume command (pumpResume) both call it.
func resumeIntoSession(ctx context.Context, client Client, s *chatSession, req ResumeRequest) (ResumeResult, error) {
	thread, err := s.ensure(ctx, client)
	if err != nil {
		return ResumeResult{}, err
	}
	req.Thread = thread
	return client.Resume(ctx, req)
}

// chatSessionTierKey carries the session's flag tier into TUI mode, so the
// TUI's /resume and first turn honour --private/--local-only.
type chatSessionTierKey struct{}

// withChatSessionTier returns ctx carrying tier; "" is a no-op.
func withChatSessionTier(ctx context.Context, tier string) context.Context {
	if tier == "" {
		return ctx
	}
	return context.WithValue(ctx, chatSessionTierKey{}, tier)
}

// chatSessionTierFrom reads back what withChatSessionTier stored.
func chatSessionTierFrom(ctx context.Context) string {
	s, _ := ctx.Value(chatSessionTierKey{}).(string)
	return s
}
