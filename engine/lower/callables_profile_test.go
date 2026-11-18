//go:build full

package lower_test

import "testing"

func TestCallableProfileBindings(t *testing.T) {
	for _, src := range []string{
		`x := 42
y, z := 1, 2
x, extra := 3, 4
printf '%s:%s:%s:%s' "$x" "$y" "$z" "$extra"
`,
		`const (
 iota = iota
 A = iota
)
func main() { printf '%s:%s\n' "$iota" "$A" }
main()
`,
		`func main() {
 if n := 2; n > 1 { echo "$n" }
 echo "after:${n-unset}"
 for i := 0; i < 2; i++ { echo "$i" }
 echo "after:${i-unset}"
}
main()
`,
		`func main() {
 x := make([]int, 3)
 x[1] = 4
 println(x[1])
}
main()
`,
		`type Count int
type View interface { Show() }
func (c Count) Show() { echo "$c" }
func main() {
 var c Count = 7
 var view View = c
 switch v := view.(type) {
 case Count: echo "count:$v"
 default: echo other
 }
 var empty View
 switch v := empty.(type) {
 case nil: echo nil
 default: echo "$v"
 }
}
main()
`,
	} {
		t.Run(src, func(t *testing.T) { execute(t, compile(t, src)) })
	}
}

func TestCallableExactLargeInferredInteger(t *testing.T) {
	execute(t, compile(t, `func main() {
 defer func() { recover(); }()
 n := 9223372036854775808 + 1
 printf '%s' "$n"
}
main()
`))
}
