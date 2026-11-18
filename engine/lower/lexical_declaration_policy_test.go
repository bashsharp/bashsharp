//go:build full

package lower

import (
	"context"
	"testing"
	"time"
)

func TestCompiledDeclarationUnsetPolicy(t *testing.T) {
	for name, source := range map[string]string{
		"unset":    "func main() {\nvar x int = 1\nunset 'x'\necho \"$x:$?\"\n}\nmain()\n",
		"subshell": "func main() {\nvar x int = 1\n(unset 'x'; echo \"$x\")\necho \"parent=$x\"\n}\nmain()\n",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			want := runMethodEngine(t, ctx, source)
			result, err := Compile(parseMethodFixture(t, source), Options{Entry: "Execute"})
			if err != nil {
				t.Fatal(err)
			}
			got := runMethodArtifact(t, ctx, result.Source)
			if got != want {
				t.Fatalf("artifact=%+v interpreter=%+v", got, want)
			}
		})
	}
}
