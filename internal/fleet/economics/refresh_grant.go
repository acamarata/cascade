// Purpose: RefreshGrantOnce, the one custody-gated refresh grant path. Once
// a refresh is due (lead max(1 h, 2 x usage_poll)) and the custody gate
// allows it, it exchanges a refresh token (copy fallback, each distinct token
// once) and writes the rotated pair to every daemon copy; a tick that does
// not grant syncs the freshest pair into every copy instead.
// Constraints: no vendor, no net, no token bytes in errors or events;
// auth-required only on provider evidence; a copy read error is never absence.

package economics

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// AccountRef names one subscription account and its harness config dir.
type AccountRef struct{ ID, ConfigDir string }

// Credential is one decoded token pair; it never prints token bytes.
type Credential struct {
	Access, Refresh []byte
	Expiry          time.Time
}

// String prints the expiry only.
func (c Credential) String() string {
	return fmt.Sprintf("economics.Credential{expiry:%s}", c.Expiry.UTC())
}

// GoString prints the expiry only.
func (c Credential) GoString() string { return c.String() }

// CredentialCodec maps a harness entry (vendor shape) to and from a Credential.
type CredentialCodec interface {
	Decode(entry []byte) (Credential, error)
	Encode(prior []byte, c Credential) ([]byte, error)
}

// AuthRefreshEvent is the auth.refresh event a successful grant publishes.
type AuthRefreshEvent struct {
	Kind, Account string
	OK, Rotated   bool
}

// GrantMemo remembers the expiry each fleet-only dir was CLI-refreshed for
// (one harness-CLI refresh per expiry). The zero value is ready to use.
type GrantMemo struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// claim reports whether (id, expiry) was not yet refreshed, and marks it.
func (m *GrantMemo) claim(id string, expiry time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, ok := m.seen[id]
	if m.seen == nil {
		m.seen = map[string]time.Time{}
	}
	m.seen[id] = expiry
	return !ok || !prev.Equal(expiry)
}

// GrantDeps are RefreshGrantOnce's collaborators, all required but
// IdleBeforeGrant and Refreshers (absolute harness binary candidates).
// Exchange answers secrets.ErrInvalidGrant for a dead token; AuthRequired is
// the provider-lane writer. DefaultDir, the harness default config dir, names
// the copy set and is the PRIMARY account (never a harness-CLI refresh).
type GrantDeps struct {
	Creds           secrets.HarnessCredentials
	Codec           CredentialCodec
	Exchange        func(ctx context.Context, refresh []byte) (Credential, error)
	Census          func(ctx context.Context, acct AccountRef) (live bool, idleFor time.Duration, err error)
	AuthRequired    func(ctx context.Context, acct AccountRef) error
	Publish         func(ctx context.Context, ev AuthRefreshEvent) error
	Clock           runtime.Clock
	UsagePoll       func() time.Duration
	IdleBeforeGrant time.Duration
	DefaultDir      string
	Refreshers      []string
	RunRefresher    func(ctx context.Context, binary, configDir string) error
	Memo            *GrantMemo
	copies          func(configDir string) ([]secrets.CopyRef, error)
}

// GrantResult reports one tick. Reason: not_due, custody_gate, granted,
// cli_refreshed, no_refresh_token, invalid_grant, refresher_binary_absent.
type GrantResult struct {
	Due, Deferred, Synced, Granted, Rotated, AuthRequired bool
	Posts                                                 int
	Reason                                                string
}

func (d GrantDeps) validate(acct AccountRef) error {
	if d.Creds == nil || d.Codec == nil || d.Exchange == nil || d.Census == nil || d.AuthRequired == nil ||
		d.Publish == nil || d.Clock == nil || d.UsagePoll == nil || d.RunRefresher == nil || d.Memo == nil ||
		!filepath.IsAbs(d.DefaultDir) || acct.ID == "" || !filepath.IsAbs(acct.ConfigDir) {
		return cascade.New(cascade.KindInvalidInput, "economics: refresh grant dependencies or account are incomplete")
	}
	return nil
}

// tick is one read of the account's copies and their decoded pairs.
type tick struct {
	copies []secrets.CopyEntry
	creds  []Credential
}

// readTick reads exactly the daemon copies (secrets.DaemonCopies, in its
// order); a read, copy-set or parse failure is an error, never absence.
func readTick(ctx context.Context, d GrantDeps, acct AccountRef) (tick, error) {
	copySet := d.copies
	if copySet == nil {
		copySet = func(dir string) ([]secrets.CopyRef, error) {
			return secrets.DaemonCopies(dir, filepath.EvalSymlinks, d.DefaultDir)
		}
	}
	refs, err := copySet(acct.ConfigDir)
	if err != nil {
		return tick{}, err
	}
	copies, err := d.Creds.ReadCopies(ctx, acct.ConfigDir)
	if err != nil {
		return tick{}, err
	}
	if !slices.EqualFunc(refs, copies, func(r secrets.CopyRef, c secrets.CopyEntry) bool { return r.Label == c.Label }) {
		return tick{}, cascade.New(cascade.KindIntegrity, "economics: the copies read are not the daemon copy set")
	}
	t := tick{copies: copies, creds: make([]Credential, len(copies))}
	for i, c := range copies {
		if c.Absent {
			continue
		}
		if t.creds[i], err = d.Codec.Decode(c.Entry.Bytes()); err != nil {
			return tick{}, cascade.Wrapf(cascade.KindIntegrity, err, "economics: copy %q did not decode", c.Label)
		}
	}
	return t, nil
}

// freshest is the copy with a full pair and the latest expiry (ties in
// copy order), or -1.
func (t tick) freshest() int {
	best := -1
	for i, c := range t.creds {
		if len(c.Access) > 0 && len(c.Refresh) > 0 && (best < 0 || c.Expiry.After(t.creds[best].Expiry)) {
			best = i
		}
	}
	return best
}

// expiry is the freshest pair's expiry, else the latest expiry seen.
func (t tick) expiry() (latest time.Time) {
	for i, c := range t.creds {
		if f := t.freshest(); (f < 0 && c.Expiry.After(latest)) || i == f {
			latest = c.Expiry
		}
	}
	return latest
}

// refreshOrder is the client copy's token first, then the others by latest
// expiry (ties in copy order), each distinct token once.
func (t tick) refreshOrder() (out []int) {
	order := make([]int, len(t.creds))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		if ca, cb := t.copies[order[a]].Label == "client", t.copies[order[b]].Label == "client"; ca != cb {
			return ca
		}
		return t.creds[order[a]].Expiry.After(t.creds[order[b]].Expiry)
	})
	for _, i := range order {
		if len(t.creds[i].Refresh) > 0 && !slices.ContainsFunc(out, func(j int) bool { return bytes.Equal(t.creds[i].Refresh, t.creds[j].Refresh) }) {
			out = append(out, i)
		}
	}
	return out
}

// sync writes the freshest pair into every copy when any copy differs.
func (t tick) sync(ctx context.Context, d GrantDeps, acct AccountRef) (bool, error) {
	f := t.freshest()
	for i := 0; f >= 0 && i < len(t.copies); i++ {
		if t.copies[i].Absent || !bytes.Equal(t.copies[i].Entry.Bytes(), t.copies[f].Entry.Bytes()) {
			return true, d.Creds.WriteDurable(ctx, acct.ConfigDir, t.copies[f].Entry)
		}
	}
	return false, nil
}

// RefreshGrantOnce runs one grant tick for acct; it is the one grant path.
func RefreshGrantOnce(ctx context.Context, deps GrantDeps, acct AccountRef) (GrantResult, error) {
	var res GrantResult
	if err := deps.validate(acct); err != nil {
		return res, err
	}
	t, err := readTick(ctx, deps, acct)
	if err != nil {
		return res, err
	}
	now, poll, exp := deps.Clock.Now(), deps.UsagePoll(), t.expiry()
	res.Due = exp.IsZero() || !now.Before(exp.Add(-max(time.Hour, 2*poll)))
	if res.Due && (exp.IsZero() || now.Before(exp.Add(2*poll))) {
		live, idle, err := deps.Census(ctx, acct)
		if err != nil {
			return res, err
		}
		res.Deferred = live || idle < deps.IdleBeforeGrant
	}
	if !res.Due || res.Deferred {
		res.Reason = map[bool]string{false: "not_due", true: "custody_gate"}[res.Deferred]
		res.Synced, err = t.sync(ctx, deps, acct)
		return res, err
	}
	return grant(ctx, deps, acct, t, res)
}

// grant tries each distinct refresh token once; a failure that is not
// provider evidence is returned with no lane-state write.
func grant(ctx context.Context, d GrantDeps, acct AccountRef, t tick, res GrantResult) (GrantResult, error) {
	reason := "no_refresh_token"
	for _, i := range t.refreshOrder() {
		res.Posts++
		got, err := d.Exchange(ctx, t.creds[i].Refresh)
		if errors.Is(err, secrets.ErrInvalidGrant) {
			reason = "invalid_grant"
			continue
		} else if err != nil {
			return res, cascade.Wrapf(cascade.KindUnavailable, err, "economics: refresh grant for account %q", acct.ID)
		}
		if len(got.Access) == 0 || len(got.Refresh) == 0 {
			return res, cascade.New(cascade.KindIntegrity, "economics: the token exchange returned an incomplete pair")
		}
		data, err := d.Codec.Encode(t.copies[i].Entry.Bytes(), got)
		entry, eerr := secrets.NewEntry(data)
		if err = cmp.Or(err, eerr); err == nil {
			err = d.Creds.WriteDurable(ctx, acct.ConfigDir, entry)
		}
		if err != nil {
			return res, err
		}
		res.Granted, res.Rotated, res.Reason = true, !bytes.Equal(got.Refresh, t.creds[i].Refresh), "granted"
		return res, d.Publish(ctx, AuthRefreshEvent{Kind: "auth.refresh", Account: acct.ID, OK: true, Rotated: res.Rotated})
	}
	return evidence(ctx, d, acct, t.expiry(), res, reason)
}

// evidence handles a provider-dead account: a fleet-only dir first gets one
// harness-CLI refresh per expiry; PRIMARY never does; then auth-required.
func evidence(ctx context.Context, d GrantDeps, acct AccountRef, exp time.Time, res GrantResult, reason string) (GrantResult, error) {
	if filepath.Clean(d.DefaultDir) != filepath.Clean(acct.ConfigDir) {
		if bin := d.refresher(); bin == "" {
			reason = "refresher_binary_absent"
		} else if d.Memo.claim(acct.ID, exp) {
			if err := d.RunRefresher(ctx, bin, acct.ConfigDir); err != nil {
				return res, cascade.Wrapf(cascade.KindUnavailable, err, "economics: harness refresh for account %q", acct.ID)
			}
			t, err := readTick(ctx, d, acct)
			if err != nil {
				return res, err
			}
			if f := t.freshest(); f >= 0 && t.creds[f].Expiry.After(exp) {
				if res.Synced, err = t.sync(ctx, d, acct); err != nil {
					return res, err
				}
				res.Granted, res.Rotated, res.Reason = true, true, "cli_refreshed"
				return res, d.Publish(ctx, AuthRefreshEvent{Kind: "auth.refresh", Account: acct.ID, OK: true, Rotated: true})
			}
		}
	}
	res.AuthRequired, res.Reason = true, reason
	return res, d.AuthRequired(ctx, acct)
}

// refresher is the first candidate that is an absolute executable file.
func (d GrantDeps) refresher() string {
	for _, p := range d.Refreshers {
		if fi, err := os.Stat(p); err == nil && filepath.IsAbs(p) && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p
		}
	}
	return ""
}
