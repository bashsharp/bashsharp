//go:build full

package lower_test

import "testing"

func TestProjectedNativeBindings(t *testing.T) {
	for _, src := range []string{
		`func main() {
 n := 1.5
 printf '%s\n' "$n"
 println(n)
}
main()
`,
		`type Data struct { N int; Text string }
func main() {
 value := Data{N: 7, Text: "yes"}
 printf '%s\n' "$value"
 println(value)
 var numbers []int
 var table map[string]int
 printf '%s:%s\n' "$numbers" "$table"
 var pointer *int
 printf 'pointer:%s\n' "$pointer"
}
main()
`,
		`func main() {
 table := map[string][]int{"set": {1}}
 missing := table["missing"]
 slicePointer := new([]int)
 emptySlice := *slicePointer
 mapPointer := new(map[string]int)
 emptyMap := *mapPointer
 printf '%s:%s:%s\n' "$missing" "$emptySlice" "$emptyMap"
}
main()
`,
		`import "strings"
func main() {
 text := strings.ToUpper("hello")
 printf '%s\n' "$text"
 println(text)
}
main()
`,
	} {
		t.Run(src, func(t *testing.T) { execute(t, compile(t, src)) })
	}
}
