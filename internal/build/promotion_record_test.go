// Purpose: schema gate for contract:promotion-record. The committed fixture
// must validate under internal/release/promotion.ParseRecord; when
// CASCADE_PROMOTION_RECORD is set, the record it names must exist and
// validate too (a set-but-missing path fails). No network.
package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/release/promotion"
	"github.com/acamarata/cascade/pkg/cascade"
)

const promotionRecordFixture = "internal/build/testdata/promotion-record.json"

// checkPromotionRecordFile reads and validates the record at path.
func checkPromotionRecordFile(path string) (promotion.Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return promotion.Record{}, cascade.Newf(cascade.KindNotFound, "promotion record %s: %v", path, err)
	}
	return promotion.ParseRecord(data)
}

func TestPromotionRecordSchema(t *testing.T) {
	fixture := filepath.Join(sweepModuleRoot(t), filepath.FromSlash(promotionRecordFixture))
	rec, err := checkPromotionRecordFile(fixture)
	if err != nil {
		t.Fatalf("the committed fixture does not validate: %v", err)
	}
	if len(rec.Archives) != 5 || len(rec.Images) != 3 || rec.GateReport.SHA256 == "" || rec.Assets["cascade_images.txt"] == "" {
		t.Fatalf("fixture lacks archives, images, assets or gate_report: %+v", rec)
	}
	text := relRead(t, promotionRecordFixture)
	seeds := map[string][2]string{
		"gate_report":                 {`"gate_report"`, `"gate_report_x"`},
		"schema_version":              {`"schema_version": 1`, `"schema_version": 2`},
		"archives keys":               {`"windows_amd64"`, `"windows_arm64"`},
		"images[cascade-linux-arm64]": {`"cascade-linux-arm64": "sha256:`, `"cascade-linux-riscv64": "sha256:`},
	}
	for want, seed := range seeds {
		if strings.Count(text, seed[0]) != 1 {
			t.Fatalf("seed %q does not match exactly once", seed[0])
		}
		if _, err := promotion.ParseRecord([]byte(strings.Replace(text, seed[0], seed[1], 1))); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("seeded record violation %q stayed green: %v", want, err)
		}
	}
	if _, err := checkPromotionRecordFile(filepath.Join(t.TempDir(), "absent.json")); err == nil || !strings.Contains(err.Error(), "absent.json") {
		t.Fatalf("a missing record path must fail: %v", err)
	}
	if path, set := os.LookupEnv("CASCADE_PROMOTION_RECORD"); set {
		if _, err := checkPromotionRecordFile(path); err != nil {
			t.Fatalf("CASCADE_PROMOTION_RECORD: %v", err)
		}
	}
}
