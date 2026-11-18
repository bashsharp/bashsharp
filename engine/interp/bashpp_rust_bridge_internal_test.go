//go:build full

package interp

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPRustCallbackRestoresCallerFrame(t *testing.T) {
	for _, body := range []string{"return 7", "exit 3", `panic("boom")`} {
		t.Run(body, func(t *testing.T) {
			file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader("func callback() int { "+body+" }"), "callback.bpp")
			if err != nil {
				t.Fatal(err)
			}
			r, err := New(Lang(syntax.LangBashPP), StdIO(nil, io.Discard, io.Discard))
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			savedExit, savedPanic, savedContext := r.exit, r.bashPPPanic, r.ectx
			_, _ = r.bashPPForeignCallback(context.Background(), "callback", r.bashPPFuncs["callback"], nil)
			if !reflect.DeepEqual(r.exit, savedExit) || !reflect.DeepEqual(r.bashPPPanic, savedPanic) || r.ectx != savedContext {
				t.Fatalf("callback leaked control state: exit=%+v panic=%+v", r.exit, r.bashPPPanic)
			}
		})
	}
}
