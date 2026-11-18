//go:build full

package lower_test

import "testing"

func TestCallableNativeClosures(t *testing.T) {
	for _, src := range []string{
		`var c = 1
bump := func() { echo "c=$c" }
c=9
bump()
`,
		`func mk(n int) func {
 return func(add int) int { return $((n + add)) }
}
a := mk(1)
b := mk(2)
x := a(10)
y := b(10)
echo "$x $y"
`,
		`func counter() func {
 var n = 0
 return func() int {
 n=$((n + 1))
 return n
 }
}
next := counter()
p := next()
q := next()
echo "$p $q"
`,
		`func main() {
 for i := 0; i < 3; i++ {
 defer func() { echo "$i" }()
 }
}
main()
`,
		`func pair(a int) (n int) {
 defer func() { n=$((n + 1)) }()
 return a
}
x := pair(4)
echo "$x"
`,
		`func show(who string) { echo "$who" }
action := show
action(hello)
`,
		`type Counter int
func (c Counter) Add(n Counter) Counter {
 result := c + n
 return result
}
var c Counter = 3
f := c.Add
x := f(4)
echo "$x"
`,
	} {
		t.Run(src, func(t *testing.T) { execute(t, compile(t, src)) })
	}
}

func TestCallableGlobalsSourceOrder(t *testing.T) {
	execute(t, compile(t, `printf 'before\n'
var x int = 3
func show() { echo "$x" }
show()
x=7
show()
`))
}
func TestCallableSwitch(t *testing.T) {
	execute(t, compile(t, `func describe(n int) string {
 switch n {
 case 1: return "one"
 case 2: return "two"
 default: return "other"
 }
}
x := describe(2)
echo "$x"
`))
}

func TestCallablePanicAndRecover(t *testing.T) {
	for _, src := range []string{
		`func f() { echo before; panic(boom); echo after }
f()
echo unreachable
`,
		`func f() {
 defer func() { v := recover(); echo "got=$v" }()
 panic(boom)
}
f()
echo resumed
`,
		`func f() {
 defer func() { v := recover(); echo "empty=[$v] status=$?" }()
 echo body
}
f()
`,
		`func g(v) { echo "g:$v" }
func f() {
 defer g(1)
 defer g(2)
 panic(p)
}
f()
`,
		`func f() {
 defer func() {
 panic(second)
 }()
 panic(first)
}
f()
`,
		`func f() {
 defer func() {
 v := recover()
 echo "caught=$v"
 panic(again)
 }()
 panic(first)
}
f()
`,
		`func f() {
 defer func() {
  defer func() { v := recover(); echo "inner=$v" }()
  panic(second)
 }()
 panic(first)
}
f()
`,
		`func f() (n int) {
 defer func() { recover(); n=99 }()
 n=1
 panic(boom)
}
x := f()
echo "x=$x"
`,
		`func deep() { v := recover(); echo "deep=[$v] status=$?" }
func f() {
 defer func() {
 deep()
 }()
 panic(x)
}
f()
`,
	} {
		t.Run(src, func(t *testing.T) { execute(t, compile(t, src)) })
	}
}

func TestCallableFuncParameter(t *testing.T) {
	execute(t, compile(t, `func apply(cb func, n int) {
 cb($n)
}
show := func(v int) { echo "v=$v" }
apply($show, 4)
`))
}
func TestCallableConstantsAndRange(t *testing.T) {
	execute(t, compile(t, `const (
 Zero = iota
 One
 Two
)
func total() int {
 result := 0
 for i := range 4 { result += i }
 return result
}
x := total()
println(Zero, One, Two, x)
`))
}
func TestCallableImportedFunction(t *testing.T) {
	execute(t, compile(t, `import "fmt"
fmt.Println("abc")
`))
}
