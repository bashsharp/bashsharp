//go:build full

package lower_test

import "testing"

func TestCompiledNumericContinuation(t *testing.T) {
	for name, source := range map[string]string{
		"integer_divzero": `func main() {
 var value int = 7
 value /= 0
 printf ':immediate=%s' "$value"
 value += 2
 printf ':subsequent=%s' "$value"
}
main()
`,
		"float64_nonfinite": `func main() {
 var value float64 = 1
 value /= 0
 printf ':immediate=%s' "$value"
 value += 2
 printf ':subsequent=%s' "$value"
}
main()
`,
		"operand_overflow": `func main() {
 var value int8 = 1
 value += 128
 printf ':immediate=%s' "$value"
 value += 2
 printf ':subsequent=%s' "$value"
}
main()
`,
		"finite_float64": `func main() {
 var value float64 = 1
 value /= 2
 printf '%s' "$value"
}
main()
`,
	} {
		t.Run(name, func(t *testing.T) { execute(t, compile(t, source)) })
	}
}
