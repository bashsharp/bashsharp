//go:build full

package lower_test

import "testing"

func TestPositionedCallableConsumers(t *testing.T) {
	for _, src := range []string{
		`func pair() (int, string) { return 7, "seven" }
func forwarded() (int, string) {
 return pair()
}
a, b := forwarded()
printf '%s:%s' "$a" "$b"
`,
		`func nonempty(xs []int) bool {
 if xs == nil || len(xs) == 0 { return false }
 return true
}
func main() {
 var xs []int
 result := nonempty(xs)
 println(result)
}
main()
`,
		`func first() int { return 7 }
func second() int {
 return first()
}
x := second()
printf '%s' "$x"
`,
	} {
		t.Run(src, func(t *testing.T) { execute(t, compile(t, src)) })
	}
}
