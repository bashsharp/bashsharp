//go:build full

package interp_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestS243OriginalConvert5ThreeModes(t *testing.T) {
	t.Setenv("GOFLAGS", "-gcflags=-d=converthash=qy")
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "convert5.go"))
	if err != nil {
		t.Fatal(err)
	}
	const digest = "5e19e2641db072bd9c5c3e094b874f87a7c33388df72c585ea2b1a121fc6cd64"
	if got := fmt.Sprintf("%x", sha256.Sum256(source)); got != digest {
		t.Fatalf("Go 1.27.1 convert5.go digest = %s", got)
	}
	typedSendThreeModes(t, string(source))
}

func TestS243OrdinaryConversionPolicyThreeModes(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	typedSendThreeModes(t, `package main
import "fmt"
func main(){v:=float64(-1);fmt.Println(uint32(v))}`)
}
