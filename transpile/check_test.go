// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package transpile

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/front"
)

func TestCheckShellInput(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"clean", "echo hi\n", ""},
		{"side effects are not run", "touch /nonexistent-dir/never\nwhile true; do :; done\n", ""},
		{"null dereference", "func deref(p *int) int { return *p }\n", "x.bsh:1:33: BASHPP-ENULL-DEREF: p may be nil when dereferenced"},
		{"arg count", "func f(a int = 1) {}\nf(1, 2)\n", "BASHPP-EARG-COUNT: f accepts at most 1 arguments; got 2"},
		{"syntax", "if then fi (\n", "x.bsh:1:1: `if` must be followed by a statement list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := front.CheckShellInput(front.GoSourceInput{Files: []front.GoSourceFile{{Name: "x.bsh", Data: []byte(tc.src)}}})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if !front.IsGoSourceError(err) || strings.HasPrefix(err.Error(), front.Prog+": ") {
				t.Fatalf("diagnostic must be final text with no program prefix: %v", err)
			}
		})
	}
}
