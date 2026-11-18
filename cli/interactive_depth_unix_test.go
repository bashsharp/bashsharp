//go:build unix

package cli

import (
	"github.com/bashsharp/bashsharp/internal/interactiveprobe"
	"testing"
)

func TestInteractiveDepthPTY(t *testing.T) {
	shell := builtBashBin(t)
	for _, p := range interactiveprobe.Cases {
		t.Run(p.Name, func(t *testing.T) {
			if out, err := interactiveprobe.Run(shell, p); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}
