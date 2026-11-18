//go:build full

package interp

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceReaderBufferStaleSession(t *testing.T) {
	var runner *Runner
	var old *bashPPBridgeValue
	var probeErr error
	var visited int
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		if !strings.Contains(string(p), "buffer-ready") {
			return len(p), nil
		}
		visited++
		buffer := runner.bashPPNativeCellValue("p")
		if buffer == nil {
			probeErr = fmt.Errorf("Read parameter lost native ownership")
			return len(p), nil
		}
		if old == nil {
			copy := *buffer
			old = &copy
		} else {
			_, probeErr = runner.bashPPNativeAccess(context.Background(), "byte-set", *old, "", bashPPBridgeValue{Kind: "int", Type: "int", Text: "0"}, bashPPBridgeValue{Kind: "uint", Type: "uint8", Text: "99"})
		}
		return len(p), nil
	})
	var err error
	runner, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
	if err != nil {
		t.Fatal(err)
	}
	source := `package main
import "io"
type reader struct{}
func(r reader)Read(p []byte)(int,error){p[0]='A';println("buffer-ready");return 1,io.EOF}
func main(){io.ReadAll(reader{})}`
	for round := 0; round < 2; round++ {
		if round > 0 {
			runner.Reset()
		}
		p, err := gosource.Parse(strings.NewReader(source), filepath.Join(runner.Dir, "original.go"), gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		if err = runner.Run(context.Background(), p.File); err != nil {
			t.Fatal(err)
		}
	}
	if visited != 2 || old == nil || probeErr == nil || !strings.Contains(probeErr.Error(), "another dependency session") {
		t.Fatalf("stale native callback buffer: visits=%d error=%v", visited, probeErr)
	}
}
