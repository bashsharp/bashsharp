package interp_test

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0

import "testing"

// TestGoSourceS281NativeReassignRange pins the general fix for `range` over a
// dependency-returned slice that was reassigned (not declared) into an
// already-typed variable, the shape cmd/compile/internal/inline/inlheur's
// TestDumpCallSiteScoreDump hits:
//
//	var lines []string
//	if content, err := os.ReadFile(dumpfile); err != nil {
//	        ...
//	} else {
//	        lines = strings.Split(string(content), "\n")
//	}
//	for _, line := range lines { ... }
//
// goSourceNativeAssignedValue answered a reused target's reassignment with an
// "assignable" check alone and handed back a bare collection meta with no
// kind and no materialized elements, so bashPPRangeCollection claimed the
// range (the meta was non-nil) and then its kind switch matched nothing,
// silently iterating zero times — len(lines) still read correctly because it
// does not go through that meta at all. `lines := strings.Split(...)` (a
// fresh declaration, never reassigned) never took the reuse path and so
// never showed the bug.
func TestGoSourceS281NativeReassignRange(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s281-native-reassign-range", `package main
import (
	"fmt"
	"strings"
)
func read() ([]byte, error) {
	return []byte("a\nb\nc\n"), nil
}
func main() {
	var lines []string
	if data, err := read(); err != nil {
		fmt.Println("err", err)
		return
	} else {
		lines = strings.Split(string(data), "\n")
	}
	n := 0
	for range lines {
		n++
	}
	fmt.Println(len(lines), n)
}`)
	if err != nil {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
	if out != "4 4\n" {
		t.Fatalf("got %q want %q", out, "4 4\n")
	}
}

// TestGoSourceS281NativeReassignSwitchCount pins the exact counting shape
// TestDumpCallSiteScoreDump uses: a tagless switch over strings.Contains/
// strings.HasPrefix on each line of a reassigned strings.Split result. Before
// the fix this produced 0 for every bucket instead of counting the lines that
// actually matched.
func TestGoSourceS281NativeReassignSwitchCount(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s281-native-reassign-switch-count", `package main
import (
	"fmt"
	"strings"
)
func read() ([]byte, error) {
	return []byte("# comment\n\nfoo PROMOTED |x\nbar INDPROM |x\nbaz DEMOTED |x\nqux |x\n"), nil
}
func main() {
	var lines []string
	if data, err := read(); err != nil {
		fmt.Println("err", err)
		return
	} else {
		lines = strings.Split(string(data), "\n")
	}
	prom, indprom, dem, unch := 0, 0, 0, 0
	for _, line := range lines {
		switch {
		case strings.TrimSpace(line) == "":
		case !strings.Contains(line, "|"):
		case strings.HasPrefix(line, "#"):
		case strings.Contains(line, "PROMOTED"):
			prom++
		case strings.Contains(line, "INDPROM"):
			indprom++
		case strings.Contains(line, "DEMOTED"):
			dem++
		default:
			unch++
		}
	}
	fmt.Println(prom, indprom, dem, unch)
}`)
	if err != nil {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
	if out != "1 1 1 1\n" {
		t.Fatalf("got %q want %q", out, "1 1 1 1\n")
	}
}
