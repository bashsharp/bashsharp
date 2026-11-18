//go:build full

// Sprint: #209; Story: #460; Story-ID: d8e7d58f362b
package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestStory460NativeCollectionElements(t *testing.T) {
	src := `package main
import (
	"fmt"
	"math"
	"math/big"
	"runtime"
)
func main() {
	framesIter := runtime.CallersFrames(nil)
	frame, _ := framesIter.Next()
	frames := []runtime.Frame{frame}
	floats := []float64{math.NaN()}
	ints := []*big.Int{big.NewInt(7)}
	fmt.Println(frames[0].Function == "", math.IsNaN(floats[0]), ints[0].Int64())
}`
	out, stderr, err := runGoSource(t, "story460nativeelements", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "true true 7\n"))
}

// The composite-literal element claim alone did not close the Story #460
// roots: issue43619.go and zerodivide.go feed bridge-wrapped non-finite
// floats into float64 struct fields, and issue9604b.go / issue19467.go
// append native handles (*big.Int, runtime.Frame). Both destinations used to
// reject the transport wrapper before it reached the bridge claim.
func TestStory460NativeStructFieldElements(t *testing.T) {
	src := `package main
import (
	"fmt"
	"math"
)
type testCase struct {
	a, b float64
}
func main() {
	NaN := math.NaN()
	inf := math.Inf(-1)
	direct := testCase{1.0, NaN}
	fmt.Println(direct.a, math.IsNaN(direct.b))
	for _, t := range []testCase{{1.0, NaN}, {2.0, inf}} {
		fmt.Println(t.a, math.IsNaN(t.b) || math.IsInf(t.b, -1))
	}
}`
	out, stderr, err := runGoSource(t, "story460structfields", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "1 true\n1 true\n2 true\n"))
}

func TestStory460NativeAppendElements(t *testing.T) {
	src := `package main
import (
	"fmt"
	"math"
	"math/big"
	"runtime"
)
func main() {
	ints := []*big.Int{}
	ints = append(ints, big.NewInt(7))
	floats := []float64{}
	floats = append(floats, math.NaN())
	framesIter := runtime.CallersFrames([]uintptr{0})
	frame, _ := framesIter.Next()
	frames := make([]runtime.Frame, 0, 4)
	frames = append(frames, frame)
	fmt.Println(ints[0].Int64(), math.IsNaN(floats[0]), frames[0].Function == "")
}`
	out, stderr, err := runGoSource(t, "story460appendelements", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "7 true true\n"))
}

func TestStory460NativeStructFieldMismatch(t *testing.T) {
	src := `package main
import "math"
type box struct {
	s string
}
func main() {
	_ = box{s: math.NaN()}
}`
	_, err := gosource.Parse(strings.NewReader(src), "story460_field_mismatch.go", gosource.Options{RunMain: true})
	qt.Assert(t, qt.ErrorMatches(err, `(?s).*cannot use math\.NaN\(\).* as string.*`))
}

func TestStory460NativeAppendMismatch(t *testing.T) {
	src := `package main
import "math/big"
func main() {
	var floats []float64
	floats = append(floats, big.NewInt(1))
}`
	_, err := gosource.Parse(strings.NewReader(src), "story460_append_mismatch.go", gosource.Options{RunMain: true})
	qt.Assert(t, qt.ErrorMatches(err, `(?s).*cannot use big\.NewInt\(1\).* as float64.*`))
}

func TestStory460NativeCollectionElementTypeMismatch(t *testing.T) {
	src := `package main
import (
	"math/big"
	"runtime"
)
func main() {
	_ = []runtime.Frame{big.NewInt(7)}
}`
	_, err := gosource.Parse(strings.NewReader(src), "story460_native_mismatch.go", gosource.Options{RunMain: true})
	qt.Assert(t, qt.ErrorMatches(err, `(?s).*cannot use .*big\.NewInt.* as runtime\.Frame.*`))
}
