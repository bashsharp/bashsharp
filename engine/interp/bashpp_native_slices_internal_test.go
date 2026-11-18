//go:build full

package interp

import (
	"bytes"
	"context"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoSourceNativeSliceStaleSession(t *testing.T) {
	var r *Runner
	var old bashPPBridgeRequest
	var probeErr error
	round := 0
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		if !bytes.Contains(p, []byte("capture")) {
			return len(p), nil
		}
		if round == 0 {
			b := &syntax.Lit{Value: "b"}
			old, probeErr = r.bashPPPrepareNativeCall(context.Background(), &syntax.BashPPCall{Fun: []*syntax.Lit{{Value: "reader"}, {Value: "Read"}}, Args: []*syntax.Word{{Parts: []syntax.WordPart{b}}}, ArgExprs: []syntax.BashPPExpr{&syntax.BashPPIdent{Name: b}}})
		} else {
			req, err := r.bashPPEvalRequest()
			if err != nil {
				probeErr = err
				return len(p), nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, probeErr = r.bashPPNativeRequest(ctx, req, old)
		}
		return len(p), nil
	})
	var err error
	r, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
	if err != nil {
		t.Fatal(err)
	}
	for round = 0; round < 2; round++ {
		if round > 0 {
			r.Reset()
		}
		text := "AB"
		if round > 0 {
			text = "CD"
		}
		source := fmt.Sprintf(`package main
import "strings"
func main(){reader:=strings.NewReader(%q);b:=[]byte{9,9};println("capture");reader.Read(b)}`, text)
		p, err := gosource.Parse(strings.NewReader(source), filepath.Join(r.Dir, "original.go"), gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Run(context.Background(), p.File); err != nil {
			t.Fatal(err)
		}
		if round == 0 && probeErr != nil {
			t.Fatal(probeErr)
		}
	}
	if probeErr == nil || !strings.Contains(probeErr.Error(), "another dependency session") {
		t.Fatalf("stale buffer accepted: %v", probeErr)
	}
	if old.Args[0].sliceView == nil || fmt.Sprint(old.Args[0].sliceView.view) != "[65 66]" {
		t.Fatalf("stale request changed original backing: %+v", old.Args)
	}
}
