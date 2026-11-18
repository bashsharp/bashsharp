//go:build full

package lower_test

import "testing"

func TestRawShellAssignmentSpelling(t *testing.T) {
	for _, source := range []string{
		"func main() {\nvar x int = 1\nx=010\necho \"$x\"\n}\nmain()\n",
		"func main() {\nvar x int = 1\nx=abc\necho \"$x\"\n}\nmain()\n",
		"func main() {\nvar x int8 = 1\nx=128\necho \"$x\"\n}\nmain()\n",
	} {
		t.Run(source, func(t *testing.T) { execute(t, compile(t, source)) })
	}
}
