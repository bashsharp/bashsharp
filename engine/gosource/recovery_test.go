package gosource_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceDiagnosticRecovery(t *testing.T) {
	// Each fixture fails gc's parser, so the load's diagnostics carry exactly
	// gc's parser diagnostics — wording, positions and multiplicity, in
	// source order. After a "syntax error" gc runs no type checker and the
	// list is complete; after the parser's other diagnostics (expr3's missing
	// 3-index slice bounds) gc type-checks and its stderr adds the checker's
	// rows, interleaved by position.
	for _, tc := range []struct {
		name, sha string
		checks    bool
		want      []string
	}{
		{"expr3", "09291a9472f94a3001a74d01f46aa25fbb8a0490973c803c3dbbd13f0087aad9", true, []string{
			"expr3.go:22:46: middle index required in 3-index slice",
			"expr3.go:22:83: final index required in 3-index slice",
			"expr3.go:23:47: middle index required in 3-index slice",
			"expr3.go:23:84: final index required in 3-index slice",
			"expr3.go:24:47: middle index required in 3-index slice",
			"expr3.go:90:46: middle index required in 3-index slice",
			"expr3.go:90:84: final index required in 3-index slice",
		}},
		{"stmt0", "29472d473c7ed9ad26b3d762837e8c1757b85473637a8ab28f12510a8ad249a0", false, []string{
			"stmt0.go:234:5: expression in go must not be parenthesized",
			"stmt0.go:244:8: expression in defer must not be parenthesized",
			"stmt0.go:254:2: break is not in a loop, switch, or select",
			"stmt0.go:256:3: break is not in a loop, switch, or select",
			"stmt0.go:259:3: break is not in a loop, switch, or select",
			"stmt0.go:314:2: continue is not in a loop",
			"stmt0.go:316:3: continue is not in a loop",
			"stmt0.go:320:3: continue is not in a loop",
			"stmt0.go:325:3: continue is not in a loop",
			"stmt0.go:331:3: continue is not in a loop",
			"stmt0.go:337:3: continue is not in a loop",
			"stmt0.go:526:2: fallthrough statement out of place",
			"stmt0.go:531:3: fallthrough statement out of place",
			"stmt0.go:541:3: cannot fallthrough final case in switch",
			"stmt0.go:547:3: cannot fallthrough in type switch",
			"stmt0.go:554:4: fallthrough statement out of place",
			"stmt0.go:578:15: cannot fallthrough final case in switch",
			"stmt0.go:586:4: fallthrough statement out of place",
			"stmt0.go:591:3: fallthrough statement out of place",
			"stmt0.go:594:3: cannot fallthrough final case in switch",
			"stmt0.go:600:4: fallthrough statement out of place",
			"stmt0.go:803:53: syntax error: cannot declare in post statement of for loop",
			"stmt0.go:969:2: label L1 already defined at stmt0.go:968:2",
			"stmt0.go:973:3: label L0 already defined at stmt0.go:967:2",
		}},
		{"issue43190", "def735fe9882adcc1af85ce2d59c3a97e0a3444cc054575719f9cb9cef32c02a", false, []string{
			"issue43190.go:10:8: syntax error: missing import path",
			"issue43190.go:13:1: syntax error: missing import path",
			"issue43190.go:14:9: syntax error: missing import path",
			"issue43190.go:15:8: syntax error: import path must be a string",
			"issue43190.go:17:1: syntax error: imports must appear before other declarations",
			"issue43190.go:21:10: syntax error: missing import path",
			"issue43190.go:25:1: syntax error: missing import path",
			"issue43190.go:30:1: syntax error: imports must appear before other declarations",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join("testdata", "recovery", tc.name+".go.txt")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != tc.sha {
				t.Fatalf("original source changed: %s", got)
			}
			source := gosource.Source{Name: tc.name + ".go", Data: data}
			for _, runMain := range []bool{false, true} {
				p, err := gosource.Load([]gosource.Source{source}, gosource.Options{RunMain: runMain})
				if p != nil || err == nil {
					t.Fatalf("malformed AST exposed: program=%v error=%v", p, err)
				}
				list, ok := err.(gosource.ErrorList)
				if !ok {
					t.Fatalf("diagnostics lost types: %T", err)
				}
				var got, parser []string
				wanted := map[string]bool{}
				for _, w := range tc.want {
					wanted[w] = true
				}
				for _, e := range list {
					got = append(got, e.Error())
					if wanted[e.Error()] {
						parser = append(parser, e.Error())
					}
				}
				if !tc.checks && !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("diagnostics differ from gc's syntax errors\ngot: %q\nwant: %q", got, tc.want)
				}
				if tc.checks {
					if !reflect.DeepEqual(parser, tc.want) {
						t.Fatalf("gc parser diagnostics differ\ngot: %q\nwant: %q", parser, tc.want)
					}
					if len(got) == len(parser) {
						t.Fatalf("gc type-checks after these parser diagnostics; no checker diagnostic followed: %q", got)
					}
					for _, d := range got {
						if strings.Contains(d, "syntax error") {
							t.Fatalf("a syntax error would have stopped gc: %q", d)
						}
					}
				}
				if fmt.Sprintf("%x", sha256.Sum256(data)) != tc.sha {
					t.Fatal("load changed source bytes")
				}
				t.Logf("RunMain=%v: %d gc syntax diagnostics; nil program", runMain, len(got))
			}
		})
	}
}

func TestGoSourceDiagnosticsRecoveryAcrossFiles(t *testing.T) {
	sources := []gosource.Source{
		{Name: "d.go", Data: []byte("package other\nvar D = ignoredWithWrongPackage\n")},
		{Name: "c.go", Data: []byte("package p\nvar C = missingC\n")},
		{Name: "a.go", Data: []byte("package p\nimport ;\nvar A = missingA\n")},
		{Name: "b.go", Data: []byte("package p\nvar B int = \"wrong\"\n")},
	}
	// a.go fails gc's parser, so its syntax error is the load's complete
	// diagnostic set: the go/types diagnostics the siblings would produce
	// (undefined names, wrong package clause, bad assignment) never run —
	// gc stops before the type checker on any syntax error.
	want := []string{"a.go:2:8: syntax error: missing import path"}
	p, err := gosource.Load(sources, gosource.Options{RunMain: true})
	if p != nil || err == nil {
		t.Fatalf("malformed package exposed: %v %v", p, err)
	}
	var got []string
	for _, e := range err.(gosource.ErrorList) {
		got = append(got, e.Error())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics beyond the syntax verdict\ngot%q\nwant%q", got, want)
	}
	// Fatal package-clause recovery has an empty AST: still fail cleanly, never
	// call conversion, and permit a later independent valid load to run normally.
	for _, bad := range []string{"", "! not Go", "package", "package p\nfunc f( {"} {
		p, err := gosource.Parse(strings.NewReader(bad), "bad.go", gosource.Options{RunMain: true})
		if p != nil || err == nil {
			t.Fatalf("bad source accepted: %q %v %v", bad, p, err)
		}
	}
	valid := []gosource.Source{{Name: "b.go", Data: []byte("package main\nfunc answer()int{return 42}\n")}, {Name: "a.go", Data: []byte("package main\nfunc main(){println(answer())}\n")}}
	p, err = gosource.Load(valid, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &output, &output))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Run(context.Background(), p.File); err != nil || output.String() != "42\n" {
		t.Fatalf("valid load after errors: %v %q", err, output.String())
	}
}
