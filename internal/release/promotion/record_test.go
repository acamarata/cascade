package promotion

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestValidateRefusesEachField(t *testing.T) {
	cases := map[string]func(r *Record){
		"schema_version":   func(r *Record) { r.SchemaVersion = 2 },
		"rc_tag":           func(r *Record) { r.RCTag = "v2.1.0" },
		"final_tag":        func(r *Record) { r.FinalTag = "v2.1.1" },
		"commit_sha":       func(r *Record) { r.CommitSHA = strings.ToUpper(r.CommitSHA) },
		"checksums_sha256": func(r *Record) { r.ChecksumsSHA256 = "x" },
		"archives keys":    func(r *Record) { delete(r.Archives, "darwin_arm64") },
		"archives[":        func(r *Record) { r.Archives["linux_amd64"] = "zz" },
		"assets lacks":     func(r *Record) { delete(r.Assets, imagesName) },
		"plain release":    func(r *Record) { r.Assets["../x"] = strings.Repeat("a", 64) },
		"assets[":          func(r *Record) { r.Assets["install.sh"] = "zz" },
		"images has":       func(r *Record) { r.Images = map[string]string{} },
		"images[cascade]":  func(r *Record) { r.Images["cascade"] = "sha256:zz" },
		"gate_report":      func(r *Record) { r.GateReport.SHA256 = "" },
		"promoted_at":      func(r *Record) { r.PromotedAt = "2026-10-12T09:30:00+02:00" },
	}
	for frag, mutate := range cases {
		t.Run(frag, func(t *testing.T) {
			r := validRecord(t)
			r.Archives, r.Assets, r.Images = maps.Clone(r.Archives), maps.Clone(r.Assets), maps.Clone(r.Images)
			mutate(&r)
			wantRefusal(t, Validate(r), cascade.KindInvalidInput, frag)
		})
	}
}

func TestParseRecordStrict(t *testing.T) {
	r := validRecord(t)
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseRecord(data)
	if err != nil || got.CommitSHA != r.CommitSHA || got.Images["cascade"] != r.Images["cascade"] {
		t.Fatalf("round trip: %v %+v", err, got)
	}
	for frag, bad := range map[string][]byte{
		"over the":       append(data, make([]byte, MaxRecordBytes)...),
		"not valid JSON": []byte(strings.Replace(string(data), `"rc_tag"`, `"rc_tag_x":"",  "rc_tag"`, 1)),
		"trailing":       append(append([]byte{}, data...), "{}"...),
		"schema_version": []byte(strings.Replace(string(data), `"schema_version":1`, `"schema_version":0`, 1)),
	} {
		_, err := ParseRecord(bad)
		wantRefusal(t, err, cascade.KindInvalidInput, frag)
	}
}

func TestWithImagesBindsSignedLine(t *testing.T) {
	r := validRecord(t)
	_, err := WithImages(r, []byte(strings.Replace(testImages(), "aaaa", "dddd", 1)))
	wantRefusal(t, err, cascade.KindIntegrity, imagesName, "signed checksums line")
	noLine := r
	noLine.Assets = maps.Clone(r.Assets)
	delete(noLine.Assets, imagesName)
	_, err = WithImages(noLine, []byte(testImages()))
	wantRefusal(t, err, cascade.KindIntegrity, "no signed")
}
