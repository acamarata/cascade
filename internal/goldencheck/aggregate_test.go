package goldencheck

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// events renders a synthetic go test -json stream: one run+terminal pair per
// (test, action), plus the package-level terminal action.
func events(pkgAction string, tests ...[2]string) []byte {
	var b strings.Builder
	b.WriteString("{\"Action\":\"start\",\"Package\":\"p\"}\n")
	for _, tc := range tests {
		fmt.Fprintf(&b, "{\"Action\":\"run\",\"Package\":\"p\",\"Test\":%q}\n", tc[0])
		fmt.Fprintf(&b, "{\"Action\":\"output\",\"Package\":\"p\",\"Test\":%q,\"Output\":\"x\"}\n", tc[0])
		if tc[1] != "" {
			fmt.Fprintf(&b, "{\"Action\":%q,\"Package\":\"p\",\"Test\":%q}\n", tc[1], tc[0])
		}
	}
	fmt.Fprintf(&b, "{\"Action\":%q,\"Package\":\"p\"}\n", pkgAction)
	return []byte(b.String())
}

func row(root, regex string) Row { return Row{Root: root, Package: "./p", RunRegex: regex} }

func TestAggregateZeroTestsFails(t *testing.T) {
	rows := []Row{row("r1", "^TestA$"), row("r2", "^TestB$")}
	noTests := []byte("{\"Action\":\"start\",\"Package\":\"p\"}\n{\"Action\":\"output\",\"Package\":\"p\",\"Output\":\"testing: warning: no tests to run\\n\"}\n{\"Action\":\"pass\",\"Package\":\"p\"}\n")
	rep := Aggregate(rows, map[string][]byte{
		"r1": noTests,
		"r2": events("pass", [2]string{"TestB", "pass"}),
	})
	if rep.OK || rep.Rows[0].OK || !rep.Rows[1].OK {
		t.Fatalf("report = %+v", rep)
	}
	if got := rep.Rows[0]; got.Root != "r1" || !strings.Contains(got.Reason, "zero tests") {
		t.Fatalf("row 1 must fail naming zero tests: %+v", got)
	}
	if !strings.Contains(rep.Summary(), "FAIL r1") {
		t.Fatalf("summary must name the row: %s", rep.Summary())
	}
}

func TestAggregateReportsAllFailures(t *testing.T) {
	rows := []Row{row("a", "^TestA$"), row("b", "^TestB$"), row("c", "^TestC$")}
	rep := Aggregate(rows, map[string][]byte{
		"a": events("fail", [2]string{"TestA", "fail"}),
		"b": events("pass", [2]string{"TestB", "pass"}),
		"c": nil,
	})
	if rep.OK {
		t.Fatal("two failing rows must fail the report")
	}
	var failed []string
	for _, r := range rep.Rows {
		if !r.OK {
			failed = append(failed, r.Root)
		}
	}
	if strings.Join(failed, ",") != "a,c" {
		t.Fatalf("failed rows = %v", failed)
	}
	data, err := json.Marshal(rep)
	if err != nil || !strings.Contains(string(data), `"failed_tests":["TestA"]`) {
		t.Fatalf("json = %s, %v", data, err)
	}
}

func TestAggregateRowVerdicts(t *testing.T) {
	cases := []struct {
		name  string
		regex string
		raw   []byte
		want  string // "" = pass
	}{
		{"pass", "^TestA$", events("pass", [2]string{"TestA", "pass"}), ""},
		{"pass with subtest", "^TestA$", events("pass", [2]string{"TestA", "pass"}, [2]string{"TestA/sub", "pass"}), ""},
		{"alternation all ran", "^(TestA|TestB)$", events("pass", [2]string{"TestA", "pass"}, [2]string{"TestB", "pass"}), ""},
		{"alternation one dropped", "^(TestA|TestB)$", events("pass", [2]string{"TestA", "pass"}), "TestB"},
		{"all skipped", "^TestA$", events("pass", [2]string{"TestA", "skip"}), "no test passed"},
		{"skip plus pass", "^(TestA|TestB)$", events("pass", [2]string{"TestA", "skip"}, [2]string{"TestB", "pass"}), ""},
		{"unfinished", "^TestA$", events("pass", [2]string{"TestA", ""}), "did not finish"},
		{"panic mid-test", "^TestA$", events("fail", [2]string{"TestA", ""}), "package failed"},
		{"package fail only", "^TestA$", events("fail", [2]string{"TestA", "pass"}), "package failed"},
		{"malformed", "^TestA$", []byte("ok  \tp\t0.1s\n"), "malformed"},
		{"empty", "^TestA$", []byte("\n  \n"), "no go test -json output"},
		{"crlf", "^TestA$", []byte(strings.ReplaceAll(string(events("pass", [2]string{"TestA", "pass"})), "\n", "\r\n")), ""},
		{"free-form regex not name-checked", "TestA.*", events("pass", [2]string{"TestAx", "pass"}), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Aggregate([]Row{row("r", tc.regex)}, map[string][]byte{"r": tc.raw}).Rows[0]
			if (tc.want == "") != res.OK || !strings.Contains(res.Reason, tc.want) {
				t.Fatalf("OK=%v reason=%q, want %q", res.OK, res.Reason, tc.want)
			}
		})
	}
	if Aggregate(nil, nil).OK {
		t.Fatal("a report with no rows is not OK")
	}
	if got := (Report{OK: true}).Summary(); !strings.Contains(got, "golden-parity: OK") {
		t.Fatalf("summary = %q", got)
	}
}
