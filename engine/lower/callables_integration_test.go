//go:build full

package lower_test

import "testing"

func TestSnapshotAndMethodDispatch(t *testing.T) {
	for name, source := range map[string]string{
		"pointer_snapshot": `func main() {
 x := 1
 p := &x
 ( *p = 2; printf 'sub:%s\n' "$x" )
 printf 'parent:%s\n' "$x"
}
main()
`,
		"readonly_snapshot": `cfg := map[string][]int{"ports":{80,443}}
readonly cfg
(
 cfg["ports"][0] = 8080
)
`,
		"marked_methods": `type Value int
agentic func (v Value) Show() { echo "method:$v" }
type Shower interface { Show() }
agentic {
 var v Value = 7
 v.Show()
 method := v.Show
 method()
 var view Shower = v
 view.Show()
}
`,
		"global_checked_binding": `type Value struct { N int }
var p *Value
x := p.N
println(x)
`,
	} {
		t.Run(name, func(t *testing.T) { execute(t, compile(t, source)) })
	}
}
