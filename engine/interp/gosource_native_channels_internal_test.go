//go:build full

package interp

// Sprint: #118; Story: #51; Story-ID: 825f8083451e
import (
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

// Exercise the real dependency protocol while an original Runner session is
// alive. Invalid later cases and stale handles must leave earlier cases intact.
func TestGoSourceNativeChannelValidationBeforeCommunication(t *testing.T) {
	var runner *Runner
	var old bashPPBridgeValue
	var probeErr error
	round := 0
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		if !strings.Contains(string(p), "probe") {
			return len(p), nil
		}
		req, err := runner.bashPPEvalRequest()
		if err != nil {
			probeErr = err
			return len(p), nil
		}
		alias := ""
		for a, path := range req.Imports {
			if path == "time" {
				alias = a
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		values, err := runner.bashPPNativeRequest(ctx, req, bashPPBridgeRequest{Op: "call", Selector: alias + ".After", Args: []bashPPBridgeValue{{Kind: "int", Type: "time.Duration", Text: "0"}}})
		if err != nil || len(values) != 1 {
			probeErr = fmt.Errorf("real channel: %v %v", err, values)
			return len(p), nil
		}
		channel := values[0]
		bad := bashPPBridgeValue{Kind: "invalid", Elements: []bashPPBridgeValue{channel}}
		if round == 1 {
			bad = bashPPBridgeValue{Kind: "recv", Elements: []bashPPBridgeValue{old}}
		}
		_, err = runner.bashPPNativeRequest(ctx, req, bashPPBridgeRequest{Op: "channel-select", Selector: "probe", Args: []bashPPBridgeValue{{Kind: "recv", Elements: []bashPPBridgeValue{channel}}, bad}})
		want := "invalid native select arm"
		if round == 1 {
			want = "another dependency session"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			probeErr = fmt.Errorf("expected %q, got %v", want, err)
			return len(p), nil
		}
		values, err = runner.bashPPNativeRequest(ctx, req, bashPPBridgeRequest{Op: "channel-select", Args: []bashPPBridgeValue{{Kind: "recv", Elements: []bashPPBridgeValue{channel}}}})
		if err != nil || len(values) != 3 || values[0].Text != "0" || values[2].Text != "true" {
			probeErr = fmt.Errorf("validation consumed earlier value: %v %v", err, values)
		}
		old = channel
		return len(p), nil
	})
	var err error
	runner, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
	if err != nil {
		t.Fatal(err)
	}
	source := `package main;import "time";func main(){_ = time.After;println("probe")}`
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
		if err != nil || probeErr != nil {
			t.Fatalf("Runner=%v; channel validation=%v", err, probeErr)
		}
	}
	if old.Session == "" || old.Handle == 0 {
		t.Fatal("no real native channel handle retained")
	}
}
