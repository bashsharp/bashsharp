//go:build full

package lower_test

import "testing"

func TestTupleAssignmentCommitsAtomically(t *testing.T) {
	for name, source := range map[string]string{
		"rollback": `func pair() (int, bool) { return 7, true }
func main() {
var x int = 1
var y int = 2
x, y = pair()
printf '|%s:%s|' "$x" "$y"
}
main()
`,
		"single_evaluation": `func pair() (int, bool) { println("called"); return 7, true }
func main() {
 var x int = 1
 var y bool
 x, y = pair()
 printf '|%s:%s|' "$x" "$y"
}
main()
`,
	} {
		t.Run(name, func(t *testing.T) { execute(t, compile(t, source)) })
	}
}
