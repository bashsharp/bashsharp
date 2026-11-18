//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestS243RuntimeLayoutArithmeticOriginal(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "fixedbugs", "issue60601.go"))
	if err != nil {
		t.Fatal(err)
	}
	typedSendThreeModes(t, string(source))
}

func TestS243RuntimeShiftWidths(t *testing.T) {
	typedSendThreeModes(t, `package main
import "unsafe"
func signed[T any]() int8 { return 1 << unsafe.Sizeof(*new(T)) }
func unsigned[T any]() uint8 { return 1 << unsafe.Sizeof(*new(T)) }
func main() {
 if signed[[7]byte]() != -128 || signed[[8]byte]() != 0 || unsigned[[7]byte]() != 128 || unsigned[[8]byte]() != 0 { panic("layout shift width") }
 n:=uint(8); s:=int8(-1); u:=uint8(255)
 if s>>n != -1 || u>>n != 0 || s<<n != 0 { panic("right shift width") }
 nneg := -1
 defer func(){ if recover()==nil { panic("negative shift missing") } }()
 _ = s << nneg
}`)
}

func TestS243RuntimeShiftConstantOverflowRejected(t *testing.T) {
	for _, source := range []string{
		`package main; const bad int8 = 1 << 7; func main(){}`,
		`package main; import "unsafe"; const bad int8 = 1 << unsafe.Sizeof([7]byte{}); func main(){}`,
		`package main; const bad = 1 << -1; func main(){}`,
	} {
		_, err := gosource.Parse(strings.NewReader(source), "shift-constant-rejection.go", gosource.Options{RunMain: true})
		if err == nil || (!strings.Contains(err.Error(), "overflow") && !strings.Contains(err.Error(), "negative")) {
			t.Fatalf("expected checked rejection: %v", err)
		}
	}
}
