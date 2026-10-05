package sync

// Purpose (this file): the [R115b] rows for the three sync guard sites: a
//   tier above SensitivityPublic reaching sync eligibility, the
//   pre-serialization filter and the metadata merge is refused on each
//   site's own range guard. Plus the P1-BF-R131 row: what one received
//   record with an unknown tier name does to its batch.
// Inputs: real records at the registered accounts domain.
// Outputs: assertions only.
// Constraints: every refusal row has a control row at internal, so no row
//   passes on empty input; removing any one site's guard turns its row red.
// SPORT: internal/sync tier range guards (CHANGE, P1-SEC-19).

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestTierOutOfRangeRefused is the sync half of the [R115b] table.
func TestTierOutOfRangeRefused(t *testing.T) {
	dc := accountsDomain(t)
	if dc.Class == ClassLocalOnly || !EligibleForTier(dc.Domain, dc.Subkind, nodes.TierController) {
		t.Fatalf("the accounts domain is not a syncable fixture: %+v", dc)
	}
	odd := arec("odd", egress.SensitivityTier(9), 1, "laptop")
	keep := arec("keep", egress.TierInternal, 1, "server")
	t.Run("sync_eligibility", func(t *testing.T) {
		if v := Eligible(odd, nodes.TierController); v.Eligible {
			t.Fatal("tier 9 was eligible for a controller peer")
		}
		if v := Eligible(keep, nodes.TierController); !v.Eligible {
			t.Fatalf("control: internal was not eligible: %+v", v)
		}
	})
	t.Run("sync_filter", func(t *testing.T) {
		if res := Admit(odd); res.Admitted {
			t.Fatal("tier 9 was admitted by the pre-serialization filter")
		}
		if res := Admit(keep); !res.Admitted {
			t.Fatalf("control: internal was refused: %+v", res)
		}
	})
	t.Run("sync_merge_metadata", func(t *testing.T) {
		journal := &ConflictJournal{}
		merged := MergeMetadataOnly(journal, dc,
			map[string]Record{"keep": keep}, map[string]Record{"odd": odd})
		if _, present := merged["odd"]; present {
			t.Fatal("tier 9 replicated through the metadata merge")
		}
		if _, present := merged["keep"]; !present {
			t.Fatal("control: the internal record was dropped")
		}
		if entries := journal.List(); len(entries) != 1 || entries[0].RecordID != "odd" {
			t.Fatalf("the refusal was not journaled once for the odd record: %+v", entries)
		}
	})
}

// TestAnUnknownTierNameRefusesTheWholeBatch records the P1-BF-R131 answer:
// one record whose tier name is not a tier fails ReceiveBatch's decode as
// KindIntegrity and NO record of the batch is returned, so the valid
// neighbour is refused with it. Fail-closed; the batch-granularity cost is
// recorded as debt rather than fixed here.
func TestAnUnknownTierNameRefusesTheWholeBatch(t *testing.T) {
	recs := []Record{
		arec("good", egress.TierInternal, 1, "server"),
		arec("odd", egress.TierPublic, 1, "server"),
	}
	payload, err := json.Marshal(recs)
	if err != nil {
		t.Fatal(err)
	}
	marked := strings.Replace(string(payload), `"Tier":"public"`, `"Tier":"bogus"`, 1)
	if marked == string(payload) {
		t.Fatalf("the fixture's tier field was not found in %s", payload)
	}
	var wire bytes.Buffer
	if err := TransferBytes(context.Background(), &wire, 5, []byte(marked), DefaultChunkSize, 0); err != nil {
		t.Fatalf("TransferBytes: %v", err)
	}
	got, _, err := mergeEngine().ReceiveBatch(context.Background(), &wire, 5, 0)
	if err == nil || !cascade.HasKind(err, cascade.KindIntegrity) || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("ReceiveBatch with an unknown tier = %v; want KindIntegrity naming the value", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReceiveBatch returned %d record(s) beside the refusal; the whole batch is refused", len(got))
	}
}
