//go:build full

package interp_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
)

func TestFenceGoExitStatus(t *testing.T) {
	for _, call := range []string{"g.Main()", "err := g.Main()"} {
		for _, status := range []int{3, 42} {
			t.Run(fmt.Sprint(status)+"/"+call, func(t *testing.T) {
				source := fmt.Sprintf("~~~go as g\nimport \"os\"\nfunc Main() { os.Exit(%d) }\n~~~\n%s\n", status, call)
				out, diagnostic, err := runPolyglot(t, source)
				if got, ok := interp.IsExitStatus(err); !ok || int(got) != status || out != "" || !strings.Contains(diagnostic, "Go worker") {
					t.Fatalf("out=%q diagnostic=%q err=%v; want status %d", out, diagnostic, err, status)
				}
			})
		}
	}
}
