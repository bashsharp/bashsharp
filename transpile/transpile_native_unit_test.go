package transpile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranspileNativeModuleUnits(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	sources := map[string]string{
		"a/a.go": `package a
import "fmt"
type T struct{X int ` + "`go:\"track\"`" + `}
func (t T) Read()int{return t.X}
func init(){fmt.Println("init a")}
func Value()int{return 2}
`,
		"b/b.go": `package b
import("fmt";"unit.example/a")
func init(){fmt.Println("init b",a.Value())}
func Value()int{return a.Value()+3}
`,
		"main.go": `package main
import("fmt";"reflect";"strings";"unit.example/a";"unit.example/b")
var fieldTrackInfo string
func init(){fmt.Println("init main")}
func main(){fmt.Println(b.Value(),reflect.TypeFor[a.T]().PkgPath(),a.T{X:7}.Read());if !strings.Contains(fieldTrackInfo,"unit.example/a.T.X"){panic("lost field-track identity: "+fieldTrackInfo)}}
`,
	}
	for file, body := range sources {
		writeFile(t, filepath.Join(src, file), body)
	}
	for _, dir := range []string{src, out} {
		writeFile(t, filepath.Join(dir, "go.mod"), "module unit.example\n\ngo 1.27\n")
	}
	run := func(dir string) string {
		t.Helper()
		cmd := exec.Command("go", "run", "-p=2", "-ldflags=-k=main.fieldTrackInfo", ".")
		cmd.Env = append(os.Environ(), "GOEXPERIMENT=fieldtrack")
		cmd.Dir = dir
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run %s: %v\n%s", dir, err, b)
		}
		return string(b)
	}
	want := run(src)
	var packages []string
	for _, unit := range []struct{ file, path, pkg string }{{"a/a.go", "unit.example/a", "a"}, {"b/b.go", "unit.example/b", "b"}, {"main.go", "unit.example", "main"}} {
		target := filepath.Join(out, unit.file)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		args := []string{"--bashpp", "--source=go", "--go-native-unit", "--go-import-path", unit.path}
		args = append(args, packages...)
		args = append(args, "--go-file", filepath.Join(src, unit.file), "-o", target)
		if code, err := captureTranspileStderr(t, args); code != 0 {
			t.Fatalf("unit %s exit%d: %s", unit.path, code, err)
		}
		data, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "package "+unit.pkg) || strings.Contains(string(data), "__gosource_pkg_") {
			t.Fatalf("unit identity changed: %s", data)
		}
		if unit.pkg != "a" && !strings.Contains(string(data), `"unit.example/a"`) {
			t.Fatalf("import path lost: %s", data)
		}
		readSourceMap(t, strings.TrimSuffix(target, ".go")+".go.map")
		packages = append(packages, "--go-package", unit.path+"="+filepath.Join(src, unit.file))
	}
	if got := run(out); got != want {
		t.Fatalf("compiled=%q native=%q", got, want)
	}
	for file, body := range sources {
		b, err := os.ReadFile(filepath.Join(src, file))
		if err != nil || string(b) != body {
			t.Fatal("source changed")
		}
	}
}

func TestTranspileNativeUnitFlagValidation(t *testing.T) {
	for _, args := range [][]string{{"--bashpp", "--go-native-unit"}, {"--bashpp", "--source=go", "--go-native-unit"}} {
		code, diagnostic := captureTranspileStderr(t, args)
		if code != 2 || !strings.Contains(diagnostic, "--go-native-unit requires") {
			t.Fatalf("code=%d diagnostic=%s", code, diagnostic)
		}
	}
	for _, flag := range []string{"--go-test-main", "--go-library=out"} {
		code, diagnostic := captureTranspileStderr(t, []string{"--bashpp", "--source=go", "--go-native-unit", "--go-import-path=p", flag})
		if code != 2 || !strings.Contains(diagnostic, "cannot be combined") {
			t.Fatalf("code=%d diagnostic=%s", code, diagnostic)
		}
	}

}
