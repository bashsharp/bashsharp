//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestS243AddressLayoutOriginalRoots(t *testing.T) {
	for _, name := range []string{"nilptr2.go"} {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", name))
			if err != nil {
				t.Fatal(err)
			}
			typedSendThreeModes(t, string(source))
		})
	}
}

func TestS243AddressLayoutUnevaluatedNew(t *testing.T) {
	typedSendThreeModes(t, `package main
import "unsafe"
func fail() int { panic("evaluated") }
func main() { if unsafe.Sizeof(*new(fail())) != unsafe.Sizeof(int(0)) { panic("size") }; var p *int; defer func(){if recover()==nil{panic("missing nil fault")}}(); println(&*p) }
`)
}
