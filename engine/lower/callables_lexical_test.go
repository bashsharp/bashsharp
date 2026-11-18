//go:build full

package lower_test

import "testing"

func TestCompiledLexicalGlobals(t *testing.T) {
	for name, source := range map[string]string{
		"later_name": `before() { echo "before=${x-unset}"; }
var x = 1
after() { echo "after=${x-unset}"; }
before
after
`,
		"later_write": `var x = 1
peek() { echo "peek=$x"; }
peek
x=42
peek
`,
		"typed_function": `var x int = 1
func read() { println(x) }
read()
x=42
read()
`,
	} {
		t.Run(name, func(t *testing.T) { execute(t, compile(t, source)) })
	}
}
