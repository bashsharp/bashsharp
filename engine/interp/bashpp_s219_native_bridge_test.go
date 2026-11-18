//go:build full

// Sprint: #219; Story: #463; Story-ID: a6f104b906d9
//
// Native dependency bridge: slice writeback identity and the fmt direct-slice
// callback boundary. Outside-corpus reductions of the roots:
//
//   - fixedbugs/issue30606, fixedbugs/issue49110, reflectmethod7
//     a slice of native structs (reflect.StructField, reflect.Value) is
//     handed to a dependency call; the worker hands the elements back as
//     fresh handles nesting this session's own reflect.Type/Value, and the
//     writeback must accept them as it does a pointer writeback's pointee.
//   - typeparam/issue51303
//     a local slice type whose only method (Equal) fmt never looks up is
//     printed directly; the stale-alias refusal is for protocol methods.
//
// A String or Error method fmt looks up over a direct original slice used to be
// refused wholesale, on the theory it could write the slice while fmt reads a
// decoded copy. That boundary is now coherence-checked instead (Sprint #281):
// the callback runs, the direct slice is synchronised around it, and the
// post-callback coherence re-read fails loudly if the body wrote a copied
// source. A read-only protocol method prints exactly as Go does; a write that
// the copy could not observe fails the callback rather than printing stale.
package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

// issue49110 / issue30606: reflect.StructOf over an original []reflect.StructField
// whose Type fields are this session's reflect.Type handles. The rebuilt
// elements must still be usable afterwards, and the constructed type must
// reflect them.
func TestS219SliceWritebackNestedHandles(t *testing.T) {
	src := `package main
import "reflect"
func typ(x interface{}) reflect.Type { return reflect.ValueOf(x).Type() }
func main() {
	fields := []reflect.StructField{
		{Name: "A", Type: reflect.TypeOf(int(0))},
		{Name: "B", Type: reflect.ArrayOf(3, reflect.SliceOf(typ(uint64(0))))},
	}
	st := reflect.StructOf(fields)
	println(st.NumField(), st.Field(0).Name, st.Field(1).Type.String())
	println(fields[1].Name, fields[1].Type.Kind() == reflect.Array)
	blank := reflect.StructOf([]reflect.StructField{
		{Name: "_", PkgPath: "main", Type: reflect.TypeOf(int(0))},
		{Name: "_", PkgPath: "main", Type: reflect.TypeOf(int(0))},
	})
	println(blank.NumField(), reflect.New(st).Elem().Field(0).Int())
}`
	_, stderr, err := runGoSource(t, "s219structof", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, "2 A [3][]uint64\nB true\n2 0\n"))
}

// reflectmethod7: the []reflect.Value argument to Func.Call nests a
// reflect.Value handle, and the callee is a reflect.Value the transport cannot
// see into; the parked Call request must serve the original method it
// re-enters. The value method promoted onto *S runs on a copy of the
// worker-owned pointee, exactly as Go calls it.
func TestS219SliceWritebackReflectValueCall(t *testing.T) {
	src := `package main
import "reflect"
type S int
var called = 0
func (s S) M() { called++ }
func main() {
	t := reflect.TypeOf(S(0))
	fn, ok := reflect.PointerTo(t).MethodByName("M")
	if !ok { panic("FAIL") }
	args := []reflect.Value{reflect.New(t)}
	fn.Func.Call(args)
	println(called, args[0].Kind() == reflect.Pointer)
}`
	_, stderr, err := runGoSource(t, "s219reflectcall", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, "1 true\n"))
}

// The boundary that stays: a pointer-receiver original method invoked on a
// pointee the worker owns (reflect.New, no origin) has no interpreter storage
// to bind its writes to, and is refused rather than run on a detached copy.
func TestS219ReflectValueCallPointerReceiverRefused(t *testing.T) {
	src := `package main
import "reflect"
type S int
func (s *S) M() { *s = 7 }
func main() {
	t := reflect.TypeOf(S(0))
	fn, _ := reflect.PointerTo(t).MethodByName("M")
	fn.Func.Call([]reflect.Value{reflect.New(t)})
	println("after")
}`
	out, stderr, err := runGoSource(t, "s219reflectcallptr", src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(err.Error(), "pointer callback requires original receiver identity"))
	qt.Assert(t, qt.IsFalse(strings.Contains(out+stderr, "after")), qt.Commentf("program continued: %q %q", out, stderr))
}

// issue51303: a local generic slice type carrying a method fmt never invokes
// prints directly. The elements are interpreter storage and the emitter only
// reads its copy.
func TestS219FmtDirectSliceWithoutProtocolMethod(t *testing.T) {
	src := `package main
import "fmt"
type ss[E comparable, T []E] []T
func (ss[E, T]) Equal(a, b T) bool { return len(a) == len(b) }
type ints []int
func (s ints) Sum() int { n := 0; for _, v := range s { n += v }; return n }
func main() {
	x := ss[int, []int]{{1}, {2, 3}}
	fmt.Println("x", x, x.Equal(x[0], x[1]))
	s := ints{4, 5}
	fmt.Printf("%v %d\n", s, s.Sum())
}`
	out, stderr, err := runGoSource(t, "s219fmtslice", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, "x [[1] [2 3]] false\n[4 5] 9\n"))
}

// A protocol method fmt looks up — String, Error — over a direct original
// slice now runs coherently instead of being refused. A read-only method prints
// exactly as Go does; a method that writes a copied source (the stringer's
// b[0] = 7) has that slice synchronised around the callback, so fmt reads the
// same value Go's left-to-right walk would. Compare each against the native
// Go oracle's output, captured with `go run`.
func TestS219FmtDirectSliceProtocolMethodCoherent(t *testing.T) {
	cases := []struct {
		name, src, wantOut, wantErrOut string
	}{{
		// A value String that writes a global byte slice fmt also prints. Go's
		// walk formats Tag(1) first (running String, which sets b[0] = 7) and
		// then b, so it prints the written value; synchronisation matches that.
		name: "stringer",
		src: `package main
import "fmt"
var b = []byte{1}
type Tag int
func (t Tag) String() string { println("original-read"); b[0] = 7; return "tag" }
func main() { fmt.Println(Tag(1), b); println("after") }`,
		wantOut:    "tag [7]\n",
		wantErrOut: "original-read\nafter\n",
	}, {
		// A read-only Error over a slice element.
		name: "error_in_slice",
		src: `package main
import "fmt"
type E int
func (e E) Error() string { println("original-read"); return "e" }
func main() { es := []E{1}; fmt.Println(es); println("after") }`,
		wantOut:    "[e]\n",
		wantErrOut: "original-read\nafter\n",
	}, {
		// A read-only String promoted from an embedded field.
		name: "embedded_stringer",
		src: `package main
import "fmt"
type inner int
func (inner) String() string { println("original-read"); return "in" }
type outer struct{ inner; N int }
func main() { os := []outer{{1, 2}}; fmt.Println(os); println("after") }`,
		wantOut:    "[in]\n",
		wantErrOut: "original-read\nafter\n",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runGoSource(t, "s219coherent-"+tc.name, tc.src)
			qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
			qt.Assert(t, qt.Equals(out, tc.wantOut))
			qt.Assert(t, qt.Equals(stderr, tc.wantErrOut))
		})
	}
}
