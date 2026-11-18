//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/go-quicktest/qt"
)

func TestSprint162CollectionElementDestinationTypes(t *testing.T) {
	src := `type T struct { s string; f float64 }
func main() {
	t := T{"hello", 0.2}
	printf '%s:%s\n' t.s t.f
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, "hello:0.2\n"))
}

func TestSprint162CollectionElementRejectsIncompatibleValue(t *testing.T) {
	src := `func main() { _ = []float64{"nope"} }
main()
`
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.IsNotNil(err), qt.Commentf("stderr: %s", stderr))
}

func TestSprint162CollectionBridgeScalarElement(t *testing.T) {
	root := filepath.Join("testdata", "sprint162", "interp-collections")
	source, err := os.ReadFile(filepath.Join(root, "bridge_scalar.go"))
	qt.Assert(t, qt.IsNil(err))
	want, err := os.ReadFile(filepath.Join(root, "bridge_scalar.expected"))
	qt.Assert(t, qt.IsNil(err))
	out, stderr, err := runGoSource(t, "bridge_scalar", string(source))
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, string(want)))
}

func TestSprint162CollectionBridgeScalarElementNegative(t *testing.T) {
	path := filepath.Join("testdata", "sprint162", "interp-collections", "bridge_scalar_negative.go")
	source, err := os.ReadFile(path)
	qt.Assert(t, qt.IsNil(err))
	_, err = gosource.Parse(strings.NewReader(string(source)), path, gosource.Options{RunMain: true})
	qt.Assert(t, qt.ErrorMatches(err, `(?s).*cannot use .*string.* as float64.*`))
}

func TestSprint162CollectionComplexElement(t *testing.T) {
	root := filepath.Join("testdata", "sprint162", "interp-collections")
	source, err := os.ReadFile(filepath.Join(root, "complex_element.go"))
	qt.Assert(t, qt.IsNil(err))
	want, err := os.ReadFile(filepath.Join(root, "complex_element.expected"))
	qt.Assert(t, qt.IsNil(err))
	out, stderr, err := runGoSource(t, "complex_element", string(source))
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, string(want)))
}

func TestSprint162CollectionComplexElementNegative(t *testing.T) {
	path := filepath.Join("testdata", "sprint162", "interp-collections", "complex_element_negative.go")
	source, err := os.ReadFile(path)
	qt.Assert(t, qt.IsNil(err))
	_, err = gosource.Parse(strings.NewReader(string(source)), path, gosource.Options{RunMain: true})
	qt.Assert(t, qt.ErrorMatches(err, `(?s).*cannot use .*string.* as complex128.*`))
}

func TestSprint162CollectionBoundsPanic(t *testing.T) {
	root := filepath.Join("testdata", "sprint162", "interp-collections")
	for _, name := range []string{"bounds_recover", "bounds_no_panic"} {
		source, err := os.ReadFile(filepath.Join(root, name+".go"))
		qt.Assert(t, qt.IsNil(err))
		want, err := os.ReadFile(filepath.Join(root, name+".expected"))
		qt.Assert(t, qt.IsNil(err))
		out, stderr, err := runGoSource(t, name, string(source))
		qt.Assert(t, qt.IsNil(err), qt.Commentf("%s stderr: %s", name, stderr))
		qt.Assert(t, qt.Equals(out, string(want)), qt.Commentf("case %s", name))
	}
}

func TestSprint162NestedCollectionAssignment(t *testing.T) {
	root := filepath.Join("testdata", "sprint162", "interp-collections")
	source, err := os.ReadFile(filepath.Join(root, "nested_assign.go"))
	qt.Assert(t, qt.IsNil(err))
	want, err := os.ReadFile(filepath.Join(root, "nested_assign.expected"))
	qt.Assert(t, qt.IsNil(err))
	out, stderr, err := runGoSource(t, "nested_assign", string(source))
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, string(want)))
}

func TestSprint162NestedCollectionAssignmentNegative(t *testing.T) {
	path := filepath.Join("testdata", "sprint162", "interp-collections", "nested_assign_negative.go")
	source, err := os.ReadFile(path)
	qt.Assert(t, qt.IsNil(err))
	_, err = gosource.Parse(strings.NewReader(string(source)), path, gosource.Options{RunMain: true})
	qt.Assert(t, qt.ErrorMatches(err, `(?s).*cannot assign to value\[0\].*`))
}

func TestSprint162MakeChannelBuiltin(t *testing.T) {
	root := filepath.Join("testdata", "sprint162", "interp-collections")
	source, err := os.ReadFile(filepath.Join(root, "make_channel.go"))
	qt.Assert(t, qt.IsNil(err))
	want, err := os.ReadFile(filepath.Join(root, "make_channel.expected"))
	qt.Assert(t, qt.IsNil(err))
	out, stderr, err := runGoSource(t, "make_channel", string(source))
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, string(want)))
}

func TestSprint162MakeChannelBuiltinNegative(t *testing.T) {
	path := filepath.Join("testdata", "sprint162", "interp-collections", "make_channel_negative.go")
	source, err := os.ReadFile(path)
	qt.Assert(t, qt.IsNil(err))
	_, err = gosource.Parse(strings.NewReader(string(source)), path, gosource.Options{RunMain: true})
	qt.Assert(t, qt.ErrorMatches(err, `(?s).*cannot make int.*`))
}

func TestSprint162MakeSliceSizePanic(t *testing.T) {
	root := filepath.Join("testdata", "sprint162", "interp-collections")
	for _, name := range []string{"make_slice_size", "make_slice_size_no_panic"} {
		source, err := os.ReadFile(filepath.Join(root, name+".go"))
		qt.Assert(t, qt.IsNil(err))
		want, err := os.ReadFile(filepath.Join(root, name+".expected"))
		qt.Assert(t, qt.IsNil(err))
		out, stderr, err := runGoSource(t, name, string(source))
		qt.Assert(t, qt.IsNil(err), qt.Commentf("%s stderr: %s", name, stderr))
		qt.Assert(t, qt.Equals(out, string(want)), qt.Commentf("case %s", name))
	}
}
