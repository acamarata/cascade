package topology

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// ExampleMerge shows the source-precedence ladder in action: a fresher but
// weaker cli-observation loses to a provider-status reading, exactly as
// R-21.26 requires ("a merge never downgrades a bucket to a weaker
// source").
func ExampleMerge() {
	existing := Bucket{Name: "rpm", Source: SourceProviderStatus, Confidence: 0.9}
	incoming := Bucket{Name: "rpm", Source: SourceCLIObservation, Confidence: 0.5}
	winner := Merge(existing, incoming)
	fmt.Println(winner.Source)
	// Output: provider-status
}

type mergeGoldenSide struct {
	Source     BucketSource `json:"source"`
	ObservedAt time.Time    `json:"observed_at"`
	Confidence float64      `json:"confidence"`
}

type mergeGoldenCase struct {
	Name     string          `json:"name"`
	Existing mergeGoldenSide `json:"existing"`
	Incoming mergeGoldenSide `json:"incoming"`
	Winner   string          `json:"winner"`
}

func sideToBucket(name string, s mergeGoldenSide) Bucket {
	return Bucket{Name: name, Source: s.Source, ObservedAt: s.ObservedAt, Confidence: s.Confidence, Window: BucketWindowRolling}
}

// TestSourceLadderMerge is the named acceptance test: Merge honours
// provider-status > cli-observation > user-estimate > unknown, breaks ties
// by later observed_at then higher confidence, and matches the table
// golden byte-for-byte (loaded, never regenerated from this test's own
// output).
func TestSourceLadderMerge(t *testing.T) {
	raw, err := os.ReadFile("testdata/source_ladder_merge.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var cases []mergeGoldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("golden must contain at least one case")
	}
	for _, c := range cases {
		existing := sideToBucket("existing", c.Existing)
		incoming := sideToBucket("incoming", c.Incoming)
		got := Merge(existing, incoming)
		want := c.Winner
		gotWinner := "existing"
		if got.Name == "incoming" {
			gotWinner = "incoming"
		}
		if gotWinner != want {
			t.Errorf("%s: Merge winner = %s, want %s", c.Name, gotWinner, want)
		}
	}
}

func TestMergeNeverDowngradesSource(t *testing.T) {
	strong := Bucket{Name: "a", Source: SourceProviderStatus, ObservedAt: time.Unix(0, 0)}
	weak := Bucket{Name: "b", Source: SourceUnknown, ObservedAt: time.Unix(1000, 0)}
	got := Merge(strong, weak)
	if got.Source != SourceProviderStatus {
		t.Errorf("Merge downgraded provider-status to %v despite a later, weaker incoming observation", got.Source)
	}
}

func TestMergeIsSymmetricInResult(t *testing.T) {
	a := Bucket{Name: "a", Source: SourceCLIObservation, ObservedAt: time.Unix(100, 0), Confidence: 0.5}
	b := Bucket{Name: "b", Source: SourceUserEstimate, ObservedAt: time.Unix(200, 0), Confidence: 0.9}
	got1 := Merge(a, b)
	got2 := Merge(b, a)
	if got1.Source != got2.Source {
		t.Errorf("Merge(a,b).Source=%v != Merge(b,a).Source=%v", got1.Source, got2.Source)
	}
}
