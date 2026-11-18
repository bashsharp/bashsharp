package interp

import (
	"context"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"io"
	"strings"
	"testing"
	"time"
)

func TestGoSourceChannelBindingRejectsInvalidTypes(t *testing.T) {
	var runner *Runner
	var probeErr error
	probed := false
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		if !strings.Contains(string(p), "probe") {
			return len(p), nil
		}
		probed = true
		cell := runner.bashPPScope.lookup("send")
		channel, ok := runner.goSourceNativeChannel(cell)
		if !ok {
			probeErr = fmt.Errorf("missing real channel")
			return len(p), nil
		}
		for _, target := range []string{"chan int", "<-chan int", "chan string", "int"} {
			_, err := runner.bashPPNativeTypeRequest("channel-bind", &syntax.BashPPNamedType{Name: &syntax.Lit{Value: target}}, *channel)
			if err == nil {
				probeErr = fmt.Errorf("invalid binding accepted: %s", target)
			}
		}
		return len(p), nil
	})
	var err error
	runner, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
	if err != nil {
		t.Fatal(err)
	}
	// keep stores the channel in an interface, which keeps it in the native
	// channel domain: since Sprint 374 a channel that never leaves interpreted
	// code is interpreter-resident and has no native binding to probe.
	source := `package main;var c=make(chan int,1);var keep any=c;var send chan<- int=c;func main(){_=keep;c<-7;println("probe");if <-c!=7{panic("binding consumed value")}}`
	p, err := gosource.Parse(strings.NewReader(source), "original.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runner.Run(ctx, p.File); err != nil || probeErr != nil || !probed {
		t.Fatalf("run=%v probe=%v observed=%v", err, probeErr, probed)
	}
}
