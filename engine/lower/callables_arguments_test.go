//go:build full

package lower_test

import "testing"

func TestNativeTypedCallArguments(t *testing.T) {
	for name, source := range map[string]string{
		"nested_arithmetic": `func add(a int, b int) int { return a + b }
func main() {
 result := 1 + add(2 * 3, 4 + 5)
 println(result)
}
main()
`,
		"lazy_effects": `func touched(label string) bool { echo "$label"; return true }
func main() {
 a := false && touched("skip")
 b := true && touched("yes")
 println(a, b)
}
main()
`,
		"nested_calls": `func inc(value int) int { return value + 1 }
func main() {
 result := 1 + inc(inc(2))
 println(result)
}
main()
`,
	} {
		t.Run(name, func(t *testing.T) { execute(t, compile(t, source)) })
	}
}
