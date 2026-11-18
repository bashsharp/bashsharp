//go:build full

package interp_test

import (
	"os/exec"
	"testing"
)

func TestBashPPCFileScopeVariablesAndGenericSelection(t *testing.T) {
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("clang unavailable")
	}
	out, diagnostic, err := runPolyglot(t, `~~~c as c
#include <stdio.h>
#define TYPE_NAME(X) _Generic((X), \
    int: "int", \
    double: "double", \
    default: "unknown")
int counter = 2;
static int hidden(void) { return ++counter; }
int example_main(void) {
    printf("Type name: %s\n", TYPE_NAME(42.42));
    return hidden();
}
~~~
first := c.example_main()
second := c.example_main()
echo "$first,$second"
`)
	if err != nil || out != "Type name: double\nType name: double\n3,3\n" || diagnostic != "" {
		t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}
