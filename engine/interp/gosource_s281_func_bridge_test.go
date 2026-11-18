package interp_test

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceS281CollectionFunctionElementBridge(t *testing.T) {
	pkg := gosource.PackageSpec{
		Path: "test/p",
		Sources: []gosource.Source{{Name: "p.go", Data: []byte(`package p

import "fmt"

type Info struct{ Name string }

func Call(fn func(*Info)) {
	var info Info
	fn(&info)
	fmt.Println(info.Name)
}
`)}},
	}
	program, err := gosource.Load([]gosource.Source{{Name: "main.go", Data: []byte(`package main

import (
	"fmt"
	"test/p"
)

var funcs = map[string]func(*p.Info){
	"run": initInfo,
}

func initInfo(info *p.Info) {
	info.Name = "ok"
	fmt.Println("callback")
}

func main() {
	p.Call(funcs["run"])
}
`)}}, gosource.Options{
		RunMain:    true,
		ImportBase: "test",
		ImportPath: "test/main",
		Packages:   []gosource.PackageSpec{pkg},
	})
	if err != nil {
		t.Fatalf("gosource.Load: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if got := stdout.String(); got != "callback\nok\n" {
		t.Fatalf("stdout = %q, want callback through dependency bridge", got)
	}
	if strings.Contains(stderr.String(), "func(*") {
		t.Fatalf("function type leaked to stderr: %q", stderr.String())
	}
}
