package notify

// Purpose: caller-resolved inbox RPC views over the single notification service.
// Inputs: verified callers and strict list/read/ack arguments.
// Outputs: envelopes, bounded escaped body pages and per-addressee acknowledgments.
// Constraints: only the owner CLI bypasses the stamped model ceiling.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/inboxenv"
)

var (
	_ = rpc.DeclareAccess("inbox.list", rpc.AccessOwnerView)
	_ = rpc.DeclareAccess("inbox.read", rpc.AccessOwnerView)
	_ = rpc.DeclareAccess("inbox.ack", rpc.AccessOwnerView)
)

// ReadPageBytes bounds the raw body bytes consumed by one read.
const ReadPageBytes = 12 * 1024

// InboxListParams narrows the resolved caller's inbox.
type InboxListParams struct {
	All    bool `json:"all"`
	Unread bool `json:"unread"`
}

// InboxListResult contains envelopes only, with counts before unread filtering.
type InboxListResult struct {
	Items    []inboxenv.Envelope `json:"items"`
	Total    int                 `json:"total"`
	Unread   int                 `json:"unread"`
	Withheld int                 `json:"withheld"`
}

// InboxReadParams selects a byte offset within an item visible to the caller.
type InboxReadParams struct {
	ID     string `json:"id"`
	Offset int    `json:"offset"`
}

// InboxReadResult carries one escaped body page and its continuation offset.
type InboxReadResult struct {
	Envelope   inboxenv.Envelope `json:"envelope"`
	Heading    string            `json:"heading"`
	Body       string            `json:"body"`
	Offset     int               `json:"offset"`
	NextOffset int               `json:"next_offset"`
	TotalBytes int               `json:"total_bytes"`
	Done       bool              `json:"done"`
}

// InboxAckParams names an item to acknowledge for this caller alone.
type InboxAckParams struct {
	ID string `json:"id"`
}

// InboxAckResult reports a recorded acknowledgment, never approval or completion.
type InboxAckResult struct {
	ID    string `json:"id"`
	Acked bool   `json:"acked"`
}

// RegisterInboxRPC binds the three caller-scoped methods to svc.
func RegisterInboxRPC(reg *rpc.Registry, svc *Service) error {
	if reg == nil || svc == nil || svc.Inbox == nil || svc.clock == nil {
		return cascade.New(cascade.KindInvalidInput, "notify: inbox RPC needs a registry and service")
	}
	reg.Register("inbox.list", svc.inboxList)
	reg.Register("inbox.read", svc.inboxRead)
	reg.Register("inbox.ack", svc.inboxAck)
	return nil
}

type inboxViewer struct {
	who     Addressee
	scope   string
	owner   bool
	ceiling bool
	key     string
}

func viewerFrom(ctx context.Context) (inboxViewer, error) {
	c, ok := principal.CallerFrom(ctx)
	if !ok {
		return inboxViewer{}, errNoAddressee
	}
	v := inboxViewer{ceiling: !c.OwnerCLI}
	if c.Principal == nil {
		v.who, v.owner, v.key = Addressee{OwnerCLI: true}, true, OwnerCLITarget
		return v, nil
	}
	a, err := AddresseeFrom(c)
	if err != nil {
		return v, err
	}
	v.who, v.scope, v.key = a, c.Principal.Scope, "principal:"+a.PrincipalID
	return v, nil
}

func (v inboxViewer) visible(n Notification, all bool) bool {
	if all && v.owner {
		return true
	}
	if n.Class == ClassAddressed {
		return v.who.owns(n)
	}
	if n.Class == ClassGlobalCritical {
		return true
	}
	if n.Class != ClassScoped || (n.Visibility != VisibilityScoped && n.Visibility != VisibilityShared) {
		return false
	}
	target := n.TargetScope
	if target == "" {
		target = n.OriginScope
	}
	return target != "" && (v.owner || target == v.scope)
}

func (v inboxViewer) admits(n Notification) bool {
	return !v.ceiling || inboxenv.ModelAdmits(n.DataClass)
}

func inboxEnvelope(n Notification) inboxenv.Envelope {
	return inboxenv.Envelope{ID: n.ID, Class: string(n.Class),
		OriginScope: n.OriginScope, DataClass: n.DataClass.String(), Trust: string(n.Trust),
		Summary: inboxenv.Summarize([]byte(n.Kind))}
}

func decodeInbox(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return cascade.New(cascade.KindInvalidInput, "notify: inbox params must be an object")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "notify: invalid inbox params")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return cascade.New(cascade.KindInvalidInput, "notify: inbox params must be one object")
	}
	return nil
}

func validInboxID(id string) error {
	if id == "" || len(id) > 256 || strings.ContainsAny(id, "\x00\r\n\t ") {
		return cascade.New(cascade.KindInvalidInput, "notify: invalid notification id")
	}
	return nil
}

func (s *Service) inboxList(ctx context.Context, raw json.RawMessage) (any, error) {
	v, err := viewerFrom(ctx)
	if err != nil {
		return nil, err
	}
	var p InboxListParams
	if err := decodeInbox(raw, &p); err != nil {
		return nil, err
	}
	if p.All && !v.owner {
		return nil, cascade.New(cascade.KindPermissionDenied, "notify: all inbox items require the owner")
	}
	out := InboxListResult{Items: []inboxenv.Envelope{}}
	s.Inbox.mu.RLock()
	defer s.Inbox.mu.RUnlock()
	for _, rec := range s.Inbox.records {
		if rec.expired || rec.n.expired(s.clock.Now()) || !v.visible(rec.n, p.All) {
			continue
		}
		if !v.admits(rec.n) {
			out.Withheld++
			continue
		}
		out.Total++
		if !rec.ackedBy[v.key] {
			out.Unread++
		}
		if p.Unread && rec.ackedBy[v.key] {
			continue
		}
		env := inboxEnvelope(rec.n)
		if _, err := inboxenv.Encode(env); err != nil {
			return nil, err
		}
		out.Items = append(out.Items, env)
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].ID < out.Items[j].ID })
	return out, nil
}

// lookupInbox runs under the inbox lock; withheld and foreign IDs are unknown.
func (s *Service) lookupInbox(v inboxViewer, id string) (*inboxRecord, error) {
	rec, ok := s.Inbox.records[id]
	if !ok || !v.visible(rec.n, false) || !v.admits(rec.n) {
		return nil, ErrNotificationUnknown
	}
	if rec.expired || rec.n.expired(s.clock.Now()) {
		return nil, ErrNotificationExpired
	}
	return rec, nil
}

func (s *Service) inboxRead(ctx context.Context, raw json.RawMessage) (any, error) {
	v, err := viewerFrom(ctx)
	if err != nil {
		return nil, err
	}
	var p InboxReadParams
	if err := decodeInbox(raw, &p); err != nil {
		return nil, err
	}
	if err := validInboxID(p.ID); err != nil {
		return nil, err
	}
	if p.Offset < 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "notify: negative inbox offset")
	}
	s.Inbox.mu.RLock()
	defer s.Inbox.mu.RUnlock()
	rec, err := s.lookupInbox(v, p.ID)
	if err != nil {
		return nil, err
	}
	return inboxPage(rec.n, p.Offset)
}

func inboxPage(n Notification, offset int) (InboxReadResult, error) {
	out := InboxReadResult{Envelope: inboxEnvelope(n), Heading: inboxenv.BlockHeading,
		Offset: offset, TotalBytes: len(n.Payload)}
	if offset > len(n.Payload) {
		return out, cascade.New(cascade.KindInvalidInput, "notify: inbox offset exceeds body")
	}
	end := offset + min(ReadPageBytes, len(n.Payload)-offset)
	for {
		for end < len(n.Payload) && end > offset && !utf8.RuneStart(n.Payload[end]) {
			end--
		}
		if end == offset && offset < len(n.Payload) {
			return out, inboxenv.ErrEnvelopeTooLarge
		}
		out.Body = inboxenv.EscapeControl(string(n.Payload[offset:end]))
		out.NextOffset, out.Done = end, end == len(n.Payload)
		encoded, err := json.Marshal(out)
		if err != nil {
			return out, cascade.Wrap(cascade.KindInternal, err, "notify: encode inbox page")
		}
		if len(encoded) <= inboxenv.MaxEncodedBytes {
			return out, nil
		}
		if end <= offset {
			return out, inboxenv.ErrEnvelopeTooLarge
		}
		end = offset + (end-offset)/2
	}
}

func (s *Service) inboxAck(ctx context.Context, raw json.RawMessage) (any, error) {
	v, err := viewerFrom(ctx)
	if err != nil {
		return nil, err
	}
	var p InboxAckParams
	if err := decodeInbox(raw, &p); err != nil {
		return nil, err
	}
	if err := validInboxID(p.ID); err != nil {
		return nil, err
	}
	s.Inbox.mu.Lock()
	defer s.Inbox.mu.Unlock()
	rec, err := s.lookupInbox(v, p.ID)
	if err != nil {
		return nil, err
	}
	if rec.ackedBy[v.key] {
		return nil, ErrNotificationAlreadyAcked
	}
	rec.ackedBy[v.key] = true
	return InboxAckResult{ID: p.ID, Acked: true}, nil
}
