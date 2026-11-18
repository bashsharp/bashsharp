package polyglot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPythonImportObjectsAndProtocolIsolation(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	source := `
import os, sys, time
os.write(1,b'import-fd\n')
class Thing:
    def __init__(self, value=1): self.value=value
    def add(self, amount=1):
        os.write(1,b'call-fd\n'); print('call-python')
        self.value += amount
        return self.value
def make(value=1): return Thing(value)
def kind(): return Thing
def nap(): time.sleep(30)
`
	if err := os.WriteFile(filepath.Join(dir, "fixturemod.py"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	module := StartImport(ImportPlan{
		ID: "fixture", Language: "python", Module: "fixturemod", Alias: "fixture",
		Environment: EnvironmentPlan{Executable: python, Dir: dir, PythonPath: []string{dir}, Env: os.Environ()},
	})
	defer module.Close()
	made, err := module.CallKeywords(context.Background(), "make", nil, map[string]any{"value": int64(4)})
	if err != nil {
		t.Fatal(err)
	}
	object, ok := made.Value.(*Handle)
	if !ok || !strings.Contains(made.Stdout, "import-fd") {
		t.Fatalf("make = %#v, stdout %q", made.Value, made.Stdout)
	}
	value, err := module.GetAttr(context.Background(), object, "value")
	if err != nil || value.Value != int64(4) {
		t.Fatalf("value = %#v, %v", value.Value, err)
	}
	added, err := module.CallAttr(context.Background(), object, "add", nil, map[string]any{"amount": int64(3)})
	if err != nil || added.Value != int64(7) || added.Stdout != "call-fd\ncall-python\n" {
		t.Fatalf("add = %#v stdout=%q err=%v", added.Value, added.Stdout, err)
	}
	classResult, err := module.Call(context.Background(), "kind")
	if err != nil {
		t.Fatal(err)
	}
	class := classResult.Value.(*Handle)
	name, err := module.GetAttr(context.Background(), class, "__name__")
	if err != nil || name.Value != "Thing" {
		t.Fatalf("name = %#v, %v", name.Value, err)
	}
	if _, err := module.GetAttr(context.Background(), class, "__dict__"); err == nil {
		t.Fatal("private attribute was allowed")
	}
	constructed, err := module.CallHandle(context.Background(), class, []any{int64(9)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := module.GetAttr(context.Background(), constructed.Value.(*Handle), "value"); err != nil || got.Value != int64(9) {
		t.Fatalf("constructed value = %#v, %v", got.Value, err)
	}
	for _, attribute := range []string{"missing", "value"} {
		if _, err := object.CallAttr(context.Background(), attribute, nil, nil); err == nil {
			t.Fatalf("%s unexpectedly callable", attribute)
		} else if _, ok := ForeignErrorDetail(err); !ok {
			t.Fatalf("%s error is not a domain error: %v", attribute, err)
		}
	}
	other := StartImport(ImportPlan{ID: "other", Language: "python", Module: "fixturemod", Alias: "other", Environment: EnvironmentPlan{Executable: python, Dir: dir, PythonPath: []string{dir}, Env: os.Environ()}})
	defer other.Close()
	if _, err := other.CallAttr(context.Background(), object, "add", nil, nil); err == nil {
		t.Fatal("cross-module handle accepted")
	} else if _, domain := ForeignErrorDetail(err); domain {
		t.Fatalf("ownership failure classified as domain error: %v", err)
	}
	if err := module.Release(context.Background(), object); err != nil {
		t.Fatal(err)
	}
	if _, err := object.CallAttr(context.Background(), "add", nil, nil); err == nil {
		t.Fatal("released handle remained usable")
	}
}

func TestPythonHandleStaleAfterCancellation(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cancelmod.py"), []byte("import time\nclass Item: pass\ndef item(): return Item()\ndef nap(): time.sleep(30)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	module := StartImport(ImportPlan{ID: "cancel", Language: "python", Module: "cancelmod", Alias: "cancel", Environment: EnvironmentPlan{Executable: python, Dir: dir, PythonPath: []string{dir}, Env: os.Environ()}})
	defer module.Close()
	result, err := module.Call(context.Background(), "item")
	if err != nil {
		t.Fatal(err)
	}
	handle := result.Value.(*Handle)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := module.Call(ctx, "nap"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("nap error = %v", err)
	}
	if _, err := handle.CallAttr(context.Background(), "anything", nil, nil); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale handle error = %v", err)
	}
}
