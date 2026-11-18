package lower_test

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// Sprint 162 S162.4 wave 2: a composite literal, an anonymous struct type
// or a call argument list that the input spread over several lines is
// emitted on those lines, not collapsed onto the statement's line. gc
// reports an escape or inlining note at the element's own line, and
// upstream errorcheck keys every expectation on file:line: collapsing
// `[]func(){\n\ts.Inc,\n}` moved "s.Inc does not escape" up a line and the
// expectation went missing (Barrier B compiled -m rows). The reproducer
// under testdata/sprint162/layout is outside the corpus: slice, keyed and
// nested literals, a literal with a blank line inside, a multi-line struct
// type in a literal, calls with arguments on their own lines and a spread
// argument — and, as negatives, the one-line spellings of each, which must
// stay on one line.
func TestGoSourceMultilineLayout(t *testing.T) {
	name := "multiline.go"
	data, err := os.ReadFile(filepath.Join("testdata", "sprint162", "layout", name))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(bytes.NewReader(data), name, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: name})
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := format.Source(result.Source)
	if err != nil {
		t.Fatalf("generated Go is not gofmt-parseable: %v\n%s", err, result.Source)
	}
	if !bytes.Equal(formatted, result.Source) {
		t.Errorf("generated != gofmt(generated)\n--- generated\n%s\n--- gofmt\n%s", result.Source, formatted)
	}
	// D1: the input, line structure included.
	if in, out := fidelityNormalize(t, data), fidelityNormalize(t, result.Source); in != out {
		t.Errorf("generated Go is not the input\n--- want\n%s\n--- got\n%s", in, out)
	}
	// Every emitted line is the input's line it claims to be: a //line
	// directive names the line of the next physical line, and the lines
	// after it count on from there until the next directive.
	input := strings.Split(string(data), "\n")
	directive := regexp.MustCompile(`^//line ` + regexp.QuoteMeta(name) + `:(\d+):\d+$`)
	claimed, offset, checked := 0, 0, 0
	for _, line := range strings.Split(string(result.Source), "\n") {
		trimmed := strings.TrimSpace(line)
		if m := directive.FindStringSubmatch(line); m != nil {
			claimed, _ = strconv.Atoi(m[1])
			offset = 0
			continue
		}
		if claimed == 0 || strings.HasPrefix(trimmed, "// lower:") || trimmed == "//" {
			continue
		}
		at := claimed + offset
		offset++
		if trimmed == "" || trimmed == "}" || strings.HasPrefix(trimmed, "package ") {
			continue
		}
		if at > len(input) || strings.TrimSpace(input[at-1]) != trimmed {
			want := ""
			if at <= len(input) {
				want = strings.TrimSpace(input[at-1])
			}
			t.Errorf("generated line %q claims input line %d, which is %q", trimmed, at, want)
		}
		checked++
	}
	if checked < 40 {
		t.Errorf("only %d emitted lines checked against the input\n%s", checked, result.Source)
	}
}
