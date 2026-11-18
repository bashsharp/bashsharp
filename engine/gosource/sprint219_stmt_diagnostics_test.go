package gosource_test

import (
	"reflect"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestS219StmtDiagnostics(t *testing.T) {
	src := `package p
func f() {
	go 1; var goIndependent missing
	go (f())
	defer 1; var deferIndependent missing
	defer (f())
}
`
	want := []string{
		"stmt0.go:4:5: expression in go must not be parenthesized",
		"stmt0.go:6:8: expression in defer must not be parenthesized",
		"stmt0.go:3:6: expression in go must be function call",
		"stmt0.go:5:9: expression in defer must be function call",
		"stmt0.go:3:26: undefined: missing",
		"stmt0.go:5:32: undefined: missing",
		"stmt0.go:3:12: declared and not used: goIndependent",
		"stmt0.go:5:15: declared and not used: deferIndependent",
	}
	got := sprint165Diagnostics(t, "stmt0.go", []byte(src), checkerTestPolicy)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("checker-test policy\ngot: %q\nwant: %q", got, want)
	}

	// gc stderr is position-sorted, but retains the same independent errors.
	want = []string{
		"stmt0.go:3:6: expression in go must be function call",
		"stmt0.go:3:12: declared and not used: goIndependent",
		"stmt0.go:3:26: undefined: missing",
		"stmt0.go:4:5: expression in go must not be parenthesized",
		"stmt0.go:5:9: expression in defer must be function call",
		"stmt0.go:5:15: declared and not used: deferIndependent",
		"stmt0.go:5:32: undefined: missing",
		"stmt0.go:6:8: expression in defer must not be parenthesized",
	}
	got = sprint165Diagnostics(t, "stmt0.go", []byte(src), gosource.Options{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gc stderr\ngot: %q\nwant: %q", got, want)
	}
}
