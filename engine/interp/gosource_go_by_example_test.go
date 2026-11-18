//go:build full

package interp_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestGoSourceGoByExampleNativeReferenceSlicePrograms pins the exact upstream
// Go by Example rows that exercise retained native pointers, structural
// out-parameters, regexp byte-slice reads and generic slice helpers.
func TestGoSourceGoByExampleNativeReferenceSlicePrograms(t *testing.T) {
	cases := []struct {
		name       string
		sha256     string
		args       []string
		normalizer string
	}{
		{"command-line-flags", "3038d81a4af1f57c4b82bc5bfe66253eba0fd2a872afeb602a8af2c12eb1da25", []string{"foo", "bar", "baz"}, "none"},
		{"json", "2fc7c85f636a5f22d5480a7bc36262db6891d56890ea257470a8789afdb460a7", nil, "map_order"},
		{"logging", "a7c2043beb550a5a070019455a28de8bbaf92ab90455643a620837fe93aea446", nil, "wallclock"},
		{"regular-expressions", "061619299ede25698866c615f0a215d0188c38b8cb6f6f056a0996bffe239088", nil, "none"},
		{"sorting", "6c73638d79cf799d8de01597c9ffc53d2720e328cdfce6fb2da1301b35934805", nil, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourcePath := filepath.Join("testdata", "gosource-go-by-example", tc.name, tc.name+".go.txt")
			source, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(source)); got != tc.sha256 {
				t.Fatalf("exact fixture digest changed: %s", got)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, tc.name+".go")
			if err := os.WriteFile(path, source, 0600); err != nil {
				t.Fatal(err)
			}
			want := normalizeGoByExampleOutcome(t, runNativeOracle(t, dir, path, tc.args, ""), tc.normalizer)
			got := normalizeGoByExampleOutcome(t, runGoSourceRunner(t, dir, path, string(source), tc.args, ""), tc.normalizer)
			if got != want {
				t.Fatalf("Runner %+v; native Go %+v", got, want)
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != string(source) {
				t.Fatal("original source changed")
			}
		})
	}
}

func normalizeGoByExampleOutcome(t *testing.T, outcome goSourceOutcome, normalizer string) goSourceOutcome {
	t.Helper()
	switch normalizer {
	case "none":
	case "map_order":
		outcome.stdout = normalizeGoByExampleMapOrder(t, outcome.stdout)
	case "wallclock":
		outcome.stdout = normalizeGoByExampleWallclock(outcome.stdout)
		outcome.stderr = normalizeGoByExampleWallclock(outcome.stderr)
	default:
		t.Fatalf("unknown normalizer %q", normalizer)
	}
	return outcome
}

func normalizeGoByExampleMapOrder(t *testing.T, output string) string {
	t.Helper()
	lines := strings.SplitAfter(output, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) != 15 {
		return output
	}
	for _, index := range []int{5, 13} {
		switch lines[index] {
		case "{\"apple\":5,\"lettuce\":7}\n", "{\"lettuce\":7,\"apple\":5}\n":
			lines[index] = "{\"apple\":5,\"lettuce\":7}\n"
		default:
			t.Fatalf("json map members at line %d: %q", index+1, lines[index])
		}
	}
	return strings.Join(lines, "")
}

var (
	goByExampleLogTimeRE = regexp.MustCompile(`\b\d{4}/\d\d/\d\d \d\d:\d\d:\d\d(?:\.\d+)?`)
	goByExampleJSONTime  = regexp.MustCompile(`"time":"[^"]+"`)
)

func normalizeGoByExampleWallclock(output string) string {
	output = goByExampleLogTimeRE.ReplaceAllString(output, "<time>")
	return goByExampleJSONTime.ReplaceAllString(output, `"time":"<time>"`)
}

func TestGoSourceNativePointerWritebackGeneral(t *testing.T) {
	for name, source := range map[string]string{
		"scan_defined_scalar": `package main
import "fmt"
type Value int
func main(){v:=Value(1);_,err:=fmt.Sscan("9",&v);fmt.Println(v,err)}`,
		"scan_struct_fields": `package main
import "fmt"
type Pair struct{A int; B string}
func main(){p:=Pair{};_,err:=fmt.Sscan("7 ok",&p.A,&p.B);fmt.Println(p,err)}`,
	} {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}
