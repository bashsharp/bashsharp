//go:build full

package interp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A native field returned inside an original method callback must retain its
// authenticated session identity through reconstruction and be rejected after Reset.
func TestGoSourceLocalCodecHandleStaleSession(t *testing.T) {
	var runner *Runner
	var old bashPPBridgeValue
	var probeErr error
	round := 0
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		if !bytes.Contains(p, []byte("capture")) {
			return len(p), nil
		}
		req, err := runner.bashPPEvalRequest()
		if err != nil {
			probeErr = err
			return len(p), nil
		}
		if round == 0 {
			expr := &syntax.BashPPSelectorExpr{X: &syntax.BashPPIdent{Name: &syntax.Lit{Value: "e"}}, Sel: &syntax.Lit{Value: "payload"}}
			field, _, err := runner.bashPPReadExpr(expr)
			if err != nil {
				probeErr = err
				return len(p), nil
			}
			value, ok := field.(*bashPPBridgeValue)
			if !ok {
				probeErr = fmt.Errorf("callback field is %T", field)
				return len(p), nil
			}
			old = *value
		} else {
			alias := ""
			for name, path := range req.Imports {
				if path == "fmt" {
					alias = name
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, probeErr = runner.bashPPNativeRequest(ctx, req, bashPPBridgeRequest{Op: "call", Selector: alias + ".Sprint", Args: []bashPPBridgeValue{old}})
		}
		return len(p), nil
	})
	var err error
	runner, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
	if err != nil {
		t.Fatal(err)
	}
	const source = `package main
import "fmt"
import "time"
type Event struct{ payload interface{} }
func(e Event)String()string{println("capture");return fmt.Sprint(e.payload)}
func main(){payload:=time.Date(2020,1,2,0,0,0,0,time.UTC);e:=Event{payload};fmt.Println(e)}`

	for round = 0; round < 2; round++ {
		if round > 0 {
			runner.Reset()
		}
		p, err := gosource.Parse(strings.NewReader(source), filepath.Join(runner.Dir, fmt.Sprintf("round%d.go", round)), gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = runner.Run(ctx, p.File)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	if old.Session == "" || old.Handle == 0 {
		t.Fatal("first native callback field was not registered")
	}
	if probeErr == nil || !strings.Contains(probeErr.Error(), "another dependency session") {
		t.Fatalf("stale callback field accepted: %v", probeErr)
	}
}

func TestGoSourceCallbackScalarResultKeepsDeclaredType(t *testing.T) {
	runner := &Runner{bashPPGoSource: true}
	typ := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "string"}}
	value, meta, err := runner.bashPPBridgeContents(bashPPBridgeString("_"), typ)
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.kind != "scalar" || bashPPTypeText(meta.typ) != "string" {
		t.Fatalf("callback scalar metadata = %#v", meta)
	}
	equal, err := runner.bashPPCompareValues(value, meta, false, "_", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !equal {
		t.Fatal("declared callback string did not compare equal to string literal")
	}
}
