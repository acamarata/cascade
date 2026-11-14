package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestFailedOutputKeepsOnlyFailedTestsTail(t *testing.T) {
	var b strings.Builder
	for i := 0; i < failTailLines+5; i++ {
		fmt.Fprintf(&b, `{"Action":"output","Test":"TestBad","Output":"line %d\n"}`+"\n", i)
	}
	b.WriteString(`{"Action":"fail","Test":"TestBad"}` + "\n")
	b.WriteString(`{"Action":"output","Test":"TestGood","Output":"fine\n"}` + "\n")
	b.WriteString(`{"Action":"pass","Test":"TestGood"}` + "\n")
	got := failedOutput(map[string][]byte{"./x": []byte(b.String())})
	if _, ok := got["TestGood"]; ok || len(got) != 1 {
		t.Fatalf("failed tests = %v, want only TestBad", got)
	}
	lines := got["TestBad"]
	if len(lines) != failTailLines || lines[0] != "line 5" || lines[len(lines)-1] != fmt.Sprintf("line %d", failTailLines+4) {
		t.Fatalf("tail = %d lines from %q to %q", len(lines), lines[0], lines[len(lines)-1])
	}
}
