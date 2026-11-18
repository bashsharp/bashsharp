//go:build full

package interp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceUnwrapCallbackProtocolAndStale(t *testing.T) {
	var r *Runner
	var old *bashPPBridgeValue
	var checks []error
	var output strings.Builder
	visits := 0
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		output.Write(p)
		if !strings.Contains(string(p), "probe") {
			return len(p), nil
		}
		visits++
		recv := bashPPBridgeValue{Kind: "struct", Type: "main.wrapped", Fields: map[string]bashPPBridgeValue{}, CallArgs: []bashPPBridgeValue{{Kind: "int", Type: "int", Text: "1"}}}
		if _, err := r.bashPPNativeCallback(context.Background(), "wrapped.Unwrap", recv); err == nil || !strings.Contains(err.Error(), "got 1 argument(s), want 0") {
			checks = append(checks, fmt.Errorf("callback arity accepted: %v", err))
		}
		errorType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "error"}}
		if _, _, err := r.bashPPBridgeContents(bashPPBridgeValue{Kind: "nil", Type: "int", Interface: "error"}, errorType); err == nil {
			checks = append(checks, fmt.Errorf("invalid nil dynamic type accepted"))
		}
		current := r.bashPPNativeCellValue("err")
		if current == nil {
			checks = append(checks, fmt.Errorf("missing native error"))
			return len(p), nil
		}
		if old == nil {
			copy := *current
			old = &copy
		} else {
			if _, _, err := r.bashPPBridgeContents(*old, errorType); err == nil || !strings.Contains(err.Error(), "another dependency session") {
				checks = append(checks, fmt.Errorf("stale callback field accepted: %v", err))
			}
		}
		return len(p), nil
	})
	var err error
	r, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, writer, writer))
	if err != nil {
		t.Fatal(err)
	}
	// err is used after the probe: a Go local stops being reachable at its
	// last use, and the interpreter drops its binding there.
	source := `package main
import "errors"
type wrapped struct{}
func(w wrapped)Error()string{return "wrapped"}
func(w wrapped)Unwrap()error{println("original-body");return nil}
func main(){err:=errors.New("x");println("probe");_=err}`
	for i := 0; i < 2; i++ {
		if i > 0 {
			r.Reset()
		}
		p, err := gosource.Parse(strings.NewReader(source), filepath.Join(r.Dir, "original.go"), gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Run(context.Background(), p.File); err != nil {
			t.Fatal(err)
		}
	}
	for _, err := range checks {
		t.Error(err)
	}
	if visits != 2 || strings.Contains(output.String(), "original-body") {
		t.Fatalf("protocol side effects: visits=%d output=%q", visits, output.String())
	}
}
