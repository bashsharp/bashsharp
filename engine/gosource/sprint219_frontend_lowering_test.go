package gosource_test

import (
	"bytes"
	"context"
	"go/build"
	"os"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS219ComplexSelectReceiveTargets(t *testing.T) {
	run := func(t *testing.T, source, want string) {
		t.Helper()
		program, err := gosource.Load([]gosource.Source{{Name: "select.go", Data: []byte(source)}}, gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &output, &output))
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Run(context.Background(), program.File); err != nil {
			t.Fatalf("Run: %v; output=%q", err, output.String())
		}
		if got := output.String(); got != want {
			t.Fatalf("stdout = %q, want %q", got, want)
		}
	}
	t.Run("winner-evaluates-targets-left-to-right", func(t *testing.T) {
		run(t, `package main
var step int
func next() int { println("target", step); step++; return step - 1 }
func main() {
	c := make(chan int, 1); c <- 7
	values := [1]int{}; oks := [2]bool{}
	select { case values[next()], oks[next()] = <-c: }
	println(values[0], oks[1], step)
}
`, "target 0\ntarget 1\n7 true 2\n")
	})
	t.Run("unselected-arm-has-no-target-effects", func(t *testing.T) {
		run(t, `package main
var effects int
func target() int { effects++; return 0 }
func main() {
	ready := make(chan int, 1); ready <- 9
	blocked := make(chan int)
	values := [1]int{}; oks := [1]bool{}
	select {
	case values[target()], oks[target()] = <-blocked:
	case values[0], oks[0] = <-ready:
	}
	println(values[0], oks[0], effects)
}
`, "9 true 0\n")
	})
	t.Run("plain-targets-remain-direct", func(t *testing.T) {
		run(t, `package main
func main() {
	c := make(chan int, 1); c <- 8
	var value int; var ok bool
	select { case value, ok = <-c: }
	println(value, ok)
}
`, "8 true\n")
	})
}

func TestS219DefaultImporterSharesMappedTypeUniverse(t *testing.T) {
	pkg, err := build.Default.Import("go/types", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var sources []gosource.Source
	for _, name := range pkg.GoFiles {
		data, err := os.ReadFile(filepath.Join(pkg.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, gosource.Source{Name: name, Data: data})
	}
	program := gosource.Source{Name: "consumer.go", Data: []byte(`package consumer
import (
	"go/importer"
	. "go/types"
)
var _ = Config{Importer: importer.Default()}
`)}
	if _, err := gosource.Load([]gosource.Source{program}, gosource.Options{
		ImportPath:         "example.com/consumer",
		Packages:           []gosource.PackageSpec{{Path: "go/types", Sources: sources}},
		PreserveNativeInit: true,
	}); err != nil {
		t.Fatal(err)
	}
}
