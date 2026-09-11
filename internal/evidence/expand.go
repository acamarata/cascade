package evidence

// Purpose: Expand is the engine behind conductor.expand(claim_id,
// page_token) (internal/rpc/conductor_expand.go): the R-21.41 packet
// {claim, evidence[], content[]}, paged deterministically in 16,384-byte
// windows.
// Inputs: a claim id and an opaque page token.
// Outputs: a Packet, or a typed error -- ErrClaimNotFound,
// ErrClaimWithoutEvidence (both from Store.GetClaim), or invalid-input on
// a malformed/unknown token.
// Constraints: items are emitted in ascending evidence-id order; each page
// holds one PageBound window of one evidence row; the same claim id and
// page token always yield the same bytes (every page is re-fetched and
// re-verified, never cached across calls); an empty token starts at the
// first evidence row.
//
// SPORT: evidence/claim-record (ADD), R-21.41/R-21.78.

import (
	"context"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// PageBound is the R-21.41 per-item page bound in bytes.
const PageBound = 16384

// Packet is the R-21.41 expand packet: the claim, its full
// evidence list, the current page's content, and the opaque token to
// resume from (empty when the packet is complete).
type Packet struct {
	Claim         Claim         `json:"claim"`
	Evidence      []Evidence    `json:"evidence"`
	Content       []ContentItem `json:"content"`
	NextPageToken string        `json:"next_page_token"`
}

// Expand returns claimID's packet, resuming from pageToken. An unknown
// claim id is ErrClaimNotFound; a claim with no evidence row is
// ErrClaimWithoutEvidence; an invalidated claim expands normally. A
// malformed or unknown token is a typed invalid-input error.
func (f *Fetcher) Expand(ctx context.Context, claimID, pageToken string) (Packet, error) {
	claim, err := f.store.GetClaim(ctx, claimID)
	if err != nil {
		return Packet{}, err
	}
	evs, err := f.store.EvidenceForClaim(ctx, claimID)
	if err != nil {
		return Packet{}, err
	}
	idx, offset, err := resolvePageToken(evs, pageToken)
	if err != nil {
		return Packet{}, err
	}
	if idx >= len(evs) {
		return Packet{Claim: claim, Evidence: evs}, nil
	}
	item, err := f.FetchContent(ctx, evs[idx])
	if err != nil {
		return Packet{}, err
	}
	if offset > len(item.Bytes) {
		return Packet{}, cascade.Newf(cascade.KindInvalidInput, "evidence: page token %q offset out of range", pageToken)
	}
	end := offset + PageBound
	moreInRow := end < len(item.Bytes)
	if !moreInRow {
		end = len(item.Bytes)
	}
	page := ContentItem{EvidenceID: item.EvidenceID, Bytes: item.Bytes[offset:end], Truncated: moreInRow, Trust: item.Trust}

	next := ""
	switch {
	case moreInRow:
		next = evs[idx].ID + ":" + strconv.Itoa(end)
	case idx+1 < len(evs):
		next = evs[idx+1].ID + ":0"
	}
	return Packet{Claim: claim, Evidence: evs, Content: []ContentItem{page}, NextPageToken: next}, nil
}

// resolvePageToken decodes token against evs (ordered ascending by
// evidence id). An empty token starts at evs[0], offset 0. A malformed
// token, or one naming an evidence id not in evs, is a typed invalid-input
// error.
func resolvePageToken(evs []Evidence, token string) (idx, offset int, err error) {
	if token == "" {
		return 0, 0, nil
	}
	parts := strings.SplitN(token, ":", 2)
	if len(parts) != 2 {
		return 0, 0, cascade.Newf(cascade.KindInvalidInput, "evidence: malformed page token %q", token)
	}
	offsetVal, convErr := strconv.Atoi(parts[1])
	if convErr != nil || offsetVal < 0 {
		return 0, 0, cascade.Newf(cascade.KindInvalidInput, "evidence: malformed page token %q", token)
	}
	for i, e := range evs {
		if e.ID == parts[0] {
			return i, offsetVal, nil
		}
	}
	return 0, 0, cascade.Newf(cascade.KindInvalidInput, "evidence: page token %q names an unknown evidence id", token)
}
