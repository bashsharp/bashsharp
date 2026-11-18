//go:build full

package interp_test

import (
	"strings"
	"testing"
)

func TestFenceUnderscoreUnknownCall(t *testing.T) {
	for _, call := range []string{"py._load()", "x := py._load()"} {
		t.Run(call, func(t *testing.T) {
			out, diagnostic, err := runPolyglot(t, "~~~python as py\ndef _load() -> int:\n    return 7\n~~~\n"+call+"\necho after\n")
			if err == nil || !strings.Contains(err.Error(), "unknown function py._load") || out != "" {
				t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
			}
		})
	}
}
