//go:build full

package lower_test

import "testing"

func TestCallableScalarShellRepairs(t *testing.T) {
	for _, src := range []string{
		`func f() {
 v=1
 defer func(seen int) { echo "seen=$seen" }($v)
 v=2
}
f()
`,
		`func bad() { return 7 }
func f() int { n=1; defer bad(); n=2; return n }
x := f()
echo "x=$x status=$?"
`,
		`type V struct { N int }
func (v V) Show() { printf '%s:' v.N }
var v V = V{N: 3}
v.Show()
func show() { s := []int{1,2,3}; printf '%s' s[1] }
show()
`,
		`func tag(p string, rest ...int) { echo "$p:${#rest[@]}:${rest[0]}" }
tag(hi, 7, 8)
`,
		`func any(...int) { echo ok }
any(1, 2)
`,
		`func sum(nums ...int) int {
 t=0
 for v in "${nums[@]}"; do t=$((t + v)); done
 return $t
}
func fwd(rest ...int) int { t := sum(rest...); return t }
x := fwd(4, 5)
y := fwd()
echo "$x:$y"
`,
		`import . "fmt"
Println("dot")
`,
		`type Name string
func (v Name) String() string { return v }
var n Name = "n"
x := n.String()
echo "$x"
`,
	} {
		t.Run(src, func(t *testing.T) { execute(t, compile(t, src)) })
	}
}
