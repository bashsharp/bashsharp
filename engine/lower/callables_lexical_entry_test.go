//go:build full

package lower

import (
	"github.com/bashsharp/bashsharp/engine/syntax"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompiledLexicalEntryIsolation(t *testing.T) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(`var count int = 1
func add() { count += 1; println(count) }
add()
`), "cells.bpp")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Compile(file, Options{Package: "generated", Entry: "Execute"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result.Source), "var count int") {
		t.Fatal("entry state remains package global")
	}
	dir := t.TempDir()
	writeEntryModule(t, dir)
	generated := filepath.Join(dir, "generated", "program.go")
	host := filepath.Join(dir, "host", "cells_test.go")
	writeEntryFile(t, generated, string(result.Source))
	writeEntryFile(t, host, `package host_test
import("bytes";"sync";"testing";"entryartifact/generated";rt "github.com/bashsharp/bashsharp/engine/lower/shellrt")
func TestEntries(t *testing.T){var group sync.WaitGroup;for i:=0;i<24;i++ {group.Add(1);go func(){defer group.Done();var out,diagnostic bytes.Buffer;status,err:=generated.Execute(rt.WithStdio(nil,&out,&diagnostic));if status!=0||err!=nil||out.String()!="2\n"||diagnostic.Len()!=0 {t.Errorf("status=%d err=%v out=%q diagnostic=%q",status,err,out.String(),diagnostic.String())}}()};group.Wait()}
`)
	binary := filepath.Join(dir, "cells.test")
	runEntryCommand(t, dir, "go", "test", "-mod=mod", "-race", "-c", "-o", binary, "./host")
	for _, path := range []string{generated, host} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(binary, "-test.timeout=15s")
	command.Env = []string{"PATH=/no-tools", "GORACE=halt_on_error=1"}
	command.Dir = t.TempDir()
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("entry isolation: %v\n%s", err, out)
	}
}
