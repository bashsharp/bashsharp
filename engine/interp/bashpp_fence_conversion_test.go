//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
)

func TestFenceTypedArgumentConversion(t *testing.T) {
	const fence = "~~~python as py\ndef typed(value: int) -> int:\n    return value + 1\n~~~\n"
	t.Run("opt_in", func(t *testing.T) {
		out, diagnostic, err := runPolyglot(t, fence+"v, err := py.typed(nope)\necho \"v=$v err=$err\"\n")
		if err != nil || diagnostic != "" || !strings.HasPrefix(out, "v=0 err=") || !strings.Contains(out, "nope") {
			t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
		}
	})
	t.Run("plain", func(t *testing.T) {
		_, diagnostic, err := runPolyglot(t, fence+"py.typed(nope)\n")
		if status, ok := interp.IsExitStatus(err); !ok || status != 1 || !strings.Contains(diagnostic, "nope") {
			t.Fatalf("diagnostic=%q err=%v", diagnostic, err)
		}
	})
}
