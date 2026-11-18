package lower_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// Sprint 162 S162.4 wave 2: a spec of a grouped `var (` / `type (`
// declaration is emitted as its own declaration, and its //line directive
// must cite the spec's own line, not the group keyword's — gc reports
// diagnostics and -m notes ("x does not escape") on the spec's line, and
// upstream errorcheck keys every expectation on file:line. The reproducer
// under testdata/sprint162/declgroup is outside the corpus: a var group
// with a blank line inside, a type group, and the negatives — an ungrouped
// var and a const group, whose lines were already right and must not move.
func TestGoSourceGroupedDeclarationLines(t *testing.T) {
	dir := filepath.Join("testdata", "sprint162", "declgroup")
	data, err := os.ReadFile(filepath.Join(dir, "grouped.go"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(bytes.NewReader(data), "grouped.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: "grouped.go"})
	if err != nil {
		t.Fatal(err)
	}
	// The group itself is not yet re-emitted as a group (the converter
	// splits it, and a const group's iota is spelled out), so the D1
	// identity is not asserted here — only the lines are.
	// Where each declared name sits in the input.
	inputLine := map[string]int{}
	for i, line := range strings.Split(string(data), "\n") {
		if m := regexp.MustCompile(`^\t?(one|two|three|four|Celsius|Fahrenheit|A|B)\b`).FindStringSubmatch(line); m != nil {
			inputLine[m[1]] = i + 1
		} else if strings.HasPrefix(line, "var single") {
			inputLine["single"] = i + 1
		} else if strings.HasPrefix(line, "const (") {
			inputLine["const ("] = i + 1
		}
	}
	// Where the generated Go says each one is: the //line directive above a
	// top-level declaration governs it; inside the const group the lines
	// count on from the group's directive.
	directive := regexp.MustCompile(`^//line grouped\.go:(\d+):\d+$`)
	lines := strings.Split(string(result.Source), "\n")
	got := map[string]int{}
	current, offset := 0, 0
	for _, line := range lines {
		if m := directive.FindStringSubmatch(line); m != nil {
			current, _ = strconv.Atoi(m[1])
			offset = 0
			continue
		}
		switch {
		case strings.HasPrefix(line, "var "), strings.HasPrefix(line, "type "):
			got[strings.Fields(line)[1]] = current + offset
		case strings.HasPrefix(line, "const ("):
			got["const ("] = current + offset
		case strings.HasPrefix(line, "\tA "), strings.HasPrefix(line, "\tB "):
			got[strings.Fields(line)[0]] = current + offset
		}
		offset++
	}
	for name, want := range inputLine {
		if got[name] != want {
			t.Errorf("%s: input line %d, generated Go cites line %d\n%s", name, want, got[name], result.Source)
		}
	}
	if len(got) != len(inputLine) {
		t.Errorf("declarations seen: %v, want %v", got, inputLine)
	}
}
