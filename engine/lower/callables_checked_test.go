//go:build full

package lower_test

import (
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strings"
	"testing"
)

func TestCheckedNativeFailures(t *testing.T) {
	for name, source := range map[string]string{
		"nil_pointer": `func main() {
 var p *int
 x := *p
}
main()
`,
		"nil_pointer_continues": `func main() {
 var p *int
 x := *p
 println(x)
}
main()
`,
		"runtime_nil_pointer_continues": `func main() {
 agentic { echo scope; }
 var p *int
 x := *p
 println(x)
}
main()
`,
		"assert_failure": `type T int
func (v T) M(s string) { }
type U int
func (v U) M(s string) { }
type I interface { M(string) }
func main() { var v T = 1; var i I = v; x := i.(U); echo $x }
main()
`,
		"assert_impossible": `type T int
func (v T) M(s string) { }
type U int
type I interface { M(string) }
func main() { var v T = 1; var i I = v; x, ok := i.(U); echo $x $ok }
main()
`,
	} {
		t.Run(name, func(t *testing.T) {
			if name == "assert_impossible" {
				file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "input.bpp")
				if err != nil {
					t.Fatal(err)
				}
				result, err := lower.Compile(file, lower.Options{})
				if result != nil || err == nil || err.Error() != "bash: line 5: BASHPP-EASSERT-IMPOSSIBLE: U cannot be asserted from I" {
					t.Fatalf("semantic rejection result=%v err=%v", result, err)
				}
				return
			}
			execute(t, compile(t, source))
		})
	}
}

func TestRuntimeReviewRegressions(t *testing.T) {
	for name, source := range map[string]string{
		"defer_default_recover": `func catcher(prefix string = "caught") {
 payload := recover()
 echo "$prefix:$payload"
}
func main() {
 agentic { echo scope; }
 defer catcher()
 panic("boom")
}
main()
`,
		"defer_named_recover": `func catcher(prefix string = "unused") {
 payload := recover()
 echo "$prefix:$payload"
}
func main() {
 agentic { echo scope; }
 defer catcher(prefix: "caught")
 panic("boom")
}
main()
`,
		"task_typed_constant": `func worker(n int8) { println(n) }
func main() { go worker(7) }
main()
`,
		"task_denial": `agentic func marked() { echo forbidden }
func main() { go marked() }
main()
`,
		"task_panic": `func worker() { panic("boom") }
func main() { go worker() }
main()
`,
	} {
		t.Run(name, func(t *testing.T) { execute(t, compile(t, source)) })
	}
}
