//go:build unix

package polyglot

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestGoCommandSignalAfterReadiness(t *testing.T) {
	const source = `import ("fmt"; "os"; "syscall"; "time")
func Signal() {
	fmt.Println("ready")
	time.Sleep(100*time.Millisecond)
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
}
`
	plans, err := Prepare(context.Background(), []Block{{Language: "go", Source: source}}, map[string]Analyzer{"go": Go{}})
	if err != nil {
		t.Fatal(err)
	}
	module := Start(plans[0], Go{})
	defer module.Close()
	ready := make(chan struct{}, 1)
	writer := &goReadyWriter{ready: ready}
	status, err := module.CommandStreams(context.Background(), "Signal", nil, nil, writer, io.Discard)
	if err != nil || status != 143 || !strings.Contains(writer.String(), "ready\n") {
		t.Fatalf("signal = status %d, output %q, error %v", status, writer.String(), err)
	}
	select {
	case <-ready:
	default:
		t.Fatal("readiness was not streamed")
	}
}
