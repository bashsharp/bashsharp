package interp

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceImageNativePositionalProtocol(t *testing.T) {
	var r *Runner
	var probeErr error
	visits := 0
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		if !strings.Contains(string(p), "probe") {
			return len(p), nil
		}
		visits++
		alias := ""
		for a, path := range r.bashPPImports {
			if path == "image/color" {
				alias = a
			}
		}
		typ := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: alias + ".RGBA"}}
		values := []bashPPBridgeValue{{Kind: "uint", Type: "uint8", Text: "1"}, {Kind: "uint", Type: "uint8", Text: "2"}, {Kind: "uint", Type: "uint8", Text: "3"}, {Kind: "uint", Type: "uint8", Text: "255"}}
		for _, bad := range []bashPPBridgeValue{
			{Kind: "struct", Type: alias + ".RGBA", Elements: values[:3]},
			{Kind: "struct", Type: alias + ".RGBA", Elements: append(append([]bashPPBridgeValue(nil), values...), values[0])},
			{Kind: "struct", Type: alias + ".RGBA", Elements: values, Fields: map[string]bashPPBridgeValue{"R": values[0]}},
		} {
			if _, err := r.bashPPNativeTypeRequest("construct", typ, bad); err == nil || !strings.Contains(err.Error(), "positional field count or mixed fields") {
				probeErr = fmt.Errorf("invalid native positional protocol accepted: %v", err)
				return len(p), nil
			}
		}
		value, err := r.bashPPNativeTypeRequest("construct", typ, bashPPBridgeValue{Kind: "struct", Type: alias + ".RGBA", Elements: values})
		if err != nil {
			probeErr = err
			return len(p), nil
		}
		got, err := r.bashPPNativeAccess(context.Background(), "member", value, "A")
		if err != nil || got.Text != "255" {
			probeErr = fmt.Errorf("native declared field order: %v %v", got, err)
		}
		return len(p), nil
	})
	var err error
	r, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader("package main\nimport \"image/color\"\nvar _=color.RGBA{}\nfunc main(){println(\"probe\")}\n"), "original.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Run(context.Background(), program.File); err != nil {
		t.Fatal(err)
	}
	if visits != 1 || probeErr != nil {
		t.Fatalf("protocol visits=%d err=%v", visits, probeErr)
	}
}
