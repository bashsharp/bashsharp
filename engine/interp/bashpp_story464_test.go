//go:build full

// Sprint: #209; Story: #464; Story-ID: d993e87f95b7
//
// Barrier D execute-phase repair: an untyped nil bound to a parameter or
// result whose declared type is a *native* (imported) interface — io.Reader,
// io.Writer, and the like — was refused at run time with
//
//	Go nil is not assignable to io.Reader
//
// even though the front end's go/types pass had already accepted the program.
// bashPPInterfaceType and bashPPUnderlyingType cannot see through the
// dependency boundary, so a native interface reached goSourceExpectedCell's
// refusal branch: the argument path (bashpp_func.go binds each call cell with
// goSourceExpectedCell) turned a legal nil into a hard execute-phase fault.
// The predeclared `error` interface was already recognised, so only qualified
// interfaces from an import were affected. The repair is Go-source only
// (bashPPNativeType and goSourceExpectedCell both gate on r.bashPPGoSource),
// so the classic/POSIX/cert dialect is unchanged.
package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

func TestStory464NilArgNativeInterface(t *testing.T) {
	// Positive: an untyped nil passed to an io.Reader parameter binds and the
	// body observes it as the nil interface it is.
	src := "package main\nimport (\"fmt\"; \"io\")\nfunc sink(r io.Reader) bool { return r == nil }\nfunc main() { fmt.Println(sink(nil)) }\n"
	out, stderr, err := runGoSource(t, "story464argreader", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "true\n"))
}

func TestStory464NilResultNativeInterface(t *testing.T) {
	// Positive: a function whose result type is a native interface may return
	// an untyped nil, and the caller reads it back as nil.
	src := "package main\nimport (\"fmt\"; \"io\")\nfunc give() io.Writer { return nil }\nfunc main() { fmt.Println(give() == nil) }\n"
	out, stderr, err := runGoSource(t, "story464retwriter", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "true\n"))
}

func TestStory464ConcreteArgNativeInterfaceUnaffected(t *testing.T) {
	// Negative guard: a concrete value assigned to the same native interface
	// parameter is still boxed with its dynamic type, so it is not seen as nil.
	src := "package main\nimport (\"fmt\"; \"io\"; \"strings\")\nfunc sink(r io.Reader) bool { return r != nil }\nfunc main() { fmt.Println(sink(strings.NewReader(\"x\"))) }\n"
	out, stderr, err := runGoSource(t, "story464concrete", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "true\n"))
}

func TestStory464NilArgPredeclaredErrorStillWorks(t *testing.T) {
	// The predeclared error interface kept working before this repair; assert
	// it still does, so the native-interface branch does not shadow it.
	src := "package main\nimport \"fmt\"\nfunc sink(e error) bool { return e == nil }\nfunc main() { fmt.Println(sink(nil)) }\n"
	out, stderr, err := runGoSource(t, "story464error", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "true\n"))
}

// The classic (non Go-source) Bash# dialect never reaches the native-interface
// branch: goSourceExpectedCell is a no-op outside Go source, so a bare `nil`
// bound to a value keeps its established BASHPP-EEXPR-NIL diagnostic. This
// guards the gating — the relaxation is Go-source only.
func TestStory464ClassicNilStaysScalarError(t *testing.T) {
	src := "func main() {\n x := nil\n}\nmain()\n"
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.IsTrue(strings.HasPrefix(stderr, "BASHPP-EEXPR-NIL:")), qt.Commentf("stderr: %s", stderr))
}
