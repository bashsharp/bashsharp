package polyglot

import (
	"context"
	"os/exec"
	"testing"
)

func TestCFileScopeDeclarationsRemainPrivate(t *testing.T) {
	testCDeclarationCall(t, `
int counter = 2;
static int count;
char buffer[80];
struct { unsigned int value : 4; } state = { 3 };
static int hidden(void) { return ++count; }
int example_main(void) {
    buffer[0] = 'a';
    return counter + state.value + hidden() + (buffer[0] == 'a');
}
`, int64(7), "")
}

func TestCGenericSelectionUsesC17InWorker(t *testing.T) {
	testCDeclarationCall(t, `
#include <stdio.h>
#define TYPE_NAME(X) _Generic((X), \
    int: "int", \
    char: "char", \
    double: "double", \
    default: "unknown")
int example_main(void) {
    printf("Type name: %s\n", TYPE_NAME(42.42));
    return 0;
}
`, int64(0), "Type name: double\n")
}

func testCDeclarationCall(t *testing.T, source string, want any, stdout string) {
	t.Helper()
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang unavailable")
	}
	runtime := C{Command: clang}
	plans, err := Prepare(context.Background(), []Block{{Language: "c", Source: source}}, map[string]Analyzer{"c": runtime})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].Exports) != 1 || plans[0].Exports[0].Name != "example_main" {
		t.Fatalf("private declarations leaked into exports: %#v", plans)
	}
	m := Start(plans[0], runtime)
	defer m.Close()
	got, err := m.Call(context.Background(), "example_main")
	if err != nil || got.Value != want || got.Stdout != stdout || got.Stderr != "" {
		t.Fatalf("example_main = %#v, %v; want value=%v stdout=%q", got, err, want, stdout)
	}
}
