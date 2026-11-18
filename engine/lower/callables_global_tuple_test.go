//go:build full

package lower_test

import "testing"

func TestCompiledGlobalNamedTupleResults(t *testing.T) {
	source := `func pair(a, b int) (x, y int) {
 x=a
 y=b
 return
}
x, y := pair(1, 2)
echo "$x:$y"
`
	out, diagnostic, status := genericMethodOracle(t, source)
	if out != "1:2\n" || diagnostic != "" || status != 0 {
		t.Fatalf("unexpected source oracle: %q %q %d", out, diagnostic, status)
	}
	execute(t, compile(t, source))
}

func TestCompiledScalarCopyPreservesShellSpelling(t *testing.T) {
	source := `func main() {
 var a int = 1
 a=010
 var x int = 0
 x=a
 echo "$x"
 println(x)
}
main()
`
	out, diagnostic, status := genericMethodOracle(t, source)
	if out != "010\n8\n" || diagnostic != "" || status != 0 {
		t.Fatalf("unexpected source oracle: %q %q %d", out, diagnostic, status)
	}
	execute(t, compile(t, source))
}
