//go:build full

package lower_test

import "testing"

func TestCompiledLexicalLocals(t *testing.T) {
	for name, source := range map[string]string{
		"parameter": `func show(x int) { x=42; printf '%s\n' "$x"; println(x) }
show(1)
`,
		"local_capture": `func main() {
 var x int = 1
 peek() { echo "peek=$x"; }
 peek
 x=42
 peek
 println(x)
}
main()
`,
		"later_local": `func main() {
 before() { echo "before=${x-unset}"; }
 var x int = 1
 after() { echo "after=$x"; }
 before
 after
}
main()
`,
		"block_shadow": `func main() {
 var x int = 1
 if true { var x string = "inner"; x="changed"; printf '%s\n' "$x" }
 x=42
 println(x)
}
main()
`,
		"subshell_global_function": `var x int = 1
func show() { printf '%s\n' "$x" }
(
 x=2
 show()
)
show()
`,
		"named_result": `func named() (x int) { x=42; return }
x := named()
println(x)
`,
	} {
		t.Run(name, func(t *testing.T) { execute(t, compile(t, source)) })
	}
}
