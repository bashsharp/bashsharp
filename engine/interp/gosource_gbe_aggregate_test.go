//go:build full

package interp_test

// Sprint: #118; Story: #66; Story-ID: 276647cf7a01

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceGbEAggregatesInterpreted(t *testing.T) {
	tests := []struct {
		name   string
		digest string
		args   []string
		want   string
		match  *regexp.Regexp
	}{
		{
			name: "command-line-subcommands", digest: "31712b78e12412b679bc86e038fa3356e74494c70a0455e5a6ee1137471cc6d8",
			args: []string{"foo", "-enable", "-name=joe", "a1", "a2"},
			want: "subcommand 'foo'\n  enable: true\n  name: joe\n  tail: [a1 a2]\n",
		},
		{
			name: "directories", digest: "3d749420f9cacda2ab10eda7d96ec6afda60be05cdec596c2ee7000fe255c7be",
			want: "Listing subdir/parent\n  child true\n  file2 false\n  file3 false\nListing subdir/parent/child\n  file4 false\nVisiting subdir\n  subdir true\n  subdir/file1 false\n  subdir/parent true\n  subdir/parent/child true\n  subdir/parent/child/file4 false\n  subdir/parent/file2 false\n  subdir/parent/file3 false\n",
		},
		{
			name: "errors", digest: "ecd2400f24ccf59e03edef06ea3dd51990a5be1c3580a789ced64123fcaa1998",
			want: "f worked: 10\nf failed: can't work with 42\nTea is ready!\nTea is ready!\nWe should buy new tea!\nTea is ready!\nNow it is dark.\n",
		},
		{
			name: "maps", digest: "b5f6887155dfba0a2d017049b6c3d73a4eb2c347d17b38503b5c83a3f31e1138",
			want: "map: map[k1:7 k2:13]\nv1: 7\nv3: 0\nlen: 2\nmap: map[k1:7]\nmap: map[]\nprs: false\nmap: map[bar:2 foo:1]\nn == n2\n",
		},
		{
			name: "pointers", digest: "6eb2e7177ce71a30a22246e4b63389a9d146414b7ea6d3f64ec8d61328417cfa",
			match: regexp.MustCompile(`^initial: 1\nzeroval: 1\nzeroptr: 0\npointer: 0x[0-9a-f]+\nvalue at \*p: 42\nvalue at \*p: 0\n$`),
		},
		{
			name: "strings-and-runes", digest: "c368cb6e9c99f57b3a35a9f22730811a653d076c6eac36f3956ff65ac20d1c01",
			want: "Len: 18\ne0 b8 aa e0 b8 a7 e0 b8 b1 e0 b8 aa e0 b8 94 e0 b8 b5 \nRune count: 6\nU+0E2A 'ส' starts at 0\nU+0E27 'ว' starts at 3\nU+0E31 'ั' starts at 6\nU+0E2A 'ส' starts at 9\nU+0E14 'ด' starts at 12\nU+0E35 'ี' starts at 15\n\nUsing DecodeRuneInString\nU+0E2A 'ส' starts at 0\nfound so sua\nU+0E27 'ว' starts at 3\nU+0E31 'ั' starts at 6\nU+0E2A 'ส' starts at 9\nfound so sua\nU+0E14 'ด' starts at 12\nU+0E35 'ี' starts at 15\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "lower", "testdata", "gosource-gbe", test.name+".go.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(source)); got != test.digest {
				t.Fatalf("unchanged source digest = %s", got)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, test.name+".go")
			program, err := gosource.Parse(bytes.NewReader(source), path, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			options := []interp.RunnerOption{interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &stdout, &stderr)}
			if len(test.args) > 0 {
				options = append(options, interp.Params(append([]string{"--"}, test.args...)...))
			}
			runner, err := interp.New(options...)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := runner.Run(ctx, program.File); err != nil {
				t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			outputOK := stdout.String() == test.want
			if test.match != nil {
				outputOK = test.match.MatchString(stdout.String())
			}
			if !outputOK || stderr.Len() != 0 {
				t.Fatalf("streams: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestGoSourceGbECommandLineSubcommandsBar(t *testing.T) {
	// Exercise a second FlagSet whose returned *int points into dependency-owned
	// storage. The corpus case above covers *bool and *string.
	source, err := os.ReadFile(filepath.Join("..", "lower", "testdata", "gosource-gbe", "command-line-subcommands.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(bytes.NewReader(source), "command-line-subcommands.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.Params("--", "bar", "-level", "8", "tail"), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err != nil {
		t.Fatalf("Run: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if want := "subcommand 'bar'\n  level: 8\n  tail: [tail]\n"; stdout.String() != want || strings.TrimSpace(stderr.String()) != "" {
		t.Fatalf("streams: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestGoSourceAggregateSemanticsThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"new_value_composite": `package main
import "fmt"
type item struct{ N int }
func main(){p:=new(item{N:7});q:=p;q.N=8;fmt.Println(p.N,q.N,p==q)}`,
		"range_composite_value": `package main
import "fmt"
type item struct{ N int }
func main(){sum:=0;for _,v:=range []item{{N:2},{N:3}}{sum+=v.N};fmt.Println(sum)}`,
		"generic_map_equality": `package main
import("fmt";"maps")
func main(){var z map[string]int;a:=map[string]int{"a":1,"b":2};b:=map[string]int{"b":2,"a":1};fmt.Println(maps.Equal(a,b),maps.Equal(z,map[string]int{}));b["a"]=3;fmt.Println(maps.Equal(a,b))}`,
		"computed_string_index_slice_once": `package main
import "fmt"
func text()string{fmt.Println("text");return "hé"}
func main(){fmt.Printf("%x\n",text()[1]);fmt.Println(text()[1:])}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
