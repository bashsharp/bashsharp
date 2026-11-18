// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/go-quicktest/qt"
)

func TestBashPPStructLiteralsSelectorsAndZeroValues(t *testing.T) {
	src := `type Inner struct { Name string; Count int }
type Config struct { Inner Inner; Fixed [1]Inner; Ports []int; Labels map[string]string }
func main() {
	var zero Config
	var initialized Config = Config{Inner: Inner{Name: "decl"}}
 keyed := Config{Inner: Inner{Name: "prod"}, Fixed: [1]Inner{{Name: "fixed"}}, Ports: []int{80}, Labels: map[string]string{"tier": "edge"}}
 positional := Inner{"pos", 2}
 anon := struct{Name string}{Name: "anon"}
 keyed.Inner.Count = 4
	printf '%s:%s:%s:%s:%s:%s:%s:%s\n' zero.Inner.Name zero.Inner.Count initialized.Inner.Name keyed.Inner.Name keyed.Inner.Count keyed.Fixed[0].Name positional.Name anon.Name
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, ":0:decl:prod:4:fixed:pos:anon\n"))
}

func TestBashPPStructAndNestedArrayCopySliceMapReference(t *testing.T) {
	src := `type Inner struct { Name string }
type Config struct { Inner Inner; Fixed [1]Inner; Ports []int; Labels map[string][]int }
func main() {
 a := Config{Inner: Inner{Name: "a"}, Fixed: [1]Inner{{Name: "fixed"}}, Ports: []int{1}, Labels: map[string][]int{"x": {2}}}
 b := a
 b.Inner.Name = "b"
 b.Fixed[0].Name = "copy"
 b.Ports[0] = 3
 b.Labels["x"][0] = 4
 printf '%s:%s:%s:%s:%s:%s:%s:%s\n' a.Inner.Name b.Inner.Name a.Fixed[0].Name b.Fixed[0].Name a.Ports[0] b.Ports[0] a.Labels["x"][0] b.Labels["x"][0]
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "a:b:fixed:copy:3:3:4:4\n"))
}

func TestBashPPStructSubshellAndTaskSnapshots(t *testing.T) {
	src := `type Config struct { Name string }
c := Config{Name: "parent"}
func mutate() {
 c.Name = "task"
 printf 'task:%s\n' c.Name
}
func main() {
 (
  c.Name = "subshell"
  printf 'sub:%s\n' c.Name
 )
 go mutate()
 printf 'parent:%s\n' c.Name
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	for _, line := range []string{"sub:subshell\n", "parent:parent\n", "task:task\n"} {
		qt.Assert(t, qt.StringContains(out, line))
	}
	qt.Assert(t, qt.Equals(strings.Count(out, "\n"), 3))
}

func TestBashPPStructDiagnostics(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"mixed", `x := Pair{A: 1, 2}`, "BASHPP-ESTRUCT-MIXED:"},
		{"duplicate literal", `x := Pair{A: 1, A: 2}`, "BASHPP-ESTRUCT-DUPLICATE:"},
		{"unknown", `x := Pair{C: 1}`, "BASHPP-ESTRUCT-UNKNOWN:"},
		{"missing positional", `x := Pair{1}`, "BASHPP-ESTRUCT-POSITIONAL:"},
		{"extra positional", `x := Pair{1, 2, 3}`, "BASHPP-ESTRUCT-POSITIONAL:"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			src := "type Pair struct { A int; B int }\nfunc main() {\n " + test.body + "\n}\nmain()\n"
			_, stderr, err := runBashSharpCall(t, src)
			qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
			if !strings.HasPrefix(stderr, test.want) {
				t.Fatalf("stderr %q does not start with %q", stderr, test.want)
			}
		})
	}
}

func TestBashPPStructCopyRetainsDeepReadonlyReferenceIdentity(t *testing.T) {
	src := `type Config struct { Ports []int }
a := Config{Ports: []int{1}}
b := a
readonly a
b.Ports[0] = 2
`
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.Equals(stderr, "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value \"a\" through alias \"b\" and path .Ports[0]\n"))
}

func TestBashPPStructInvalidAssignmentNeverPanics(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "selector through scalar",
			body: "s.A.Bad = 2",
			want: "BASHPP-ESELECTOR-TYPE: assignment parent is not a structured value\n",
		},
		{
			name: "readonly struct index",
			body: "readonly s\ns[0] = 2",
			want: "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value \"s\" through field [0]\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			src := "type S struct { A int }\ns := S{A: 1}\n" + test.body + "\n"
			_, stderr, err := runBashSharpCall(t, src)
			qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
			qt.Assert(t, qt.Equals(stderr, test.want))
		})
	}
}
