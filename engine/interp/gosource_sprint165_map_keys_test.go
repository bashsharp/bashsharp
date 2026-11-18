//go:build full

package interp_test

// Sprint: #165; Story: #97; Story-ID: 23e622ce643e
import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/go-quicktest/qt"
)

func TestSprint165ComparableMapKeys(t *testing.T) {
	root := filepath.Join("testdata", "sprint165", "interp-collections", "comparable-map-keys")
	for _, name := range []string{"comparable_map_keys", "dynamic_unhashable"} {
		source, err := os.ReadFile(filepath.Join(root, name+".go"))
		qt.Assert(t, qt.IsNil(err))
		want, err := os.ReadFile(filepath.Join(root, name+".expected"))
		qt.Assert(t, qt.IsNil(err))
		out, stderr, err := runGoSource(t, name, string(source))
		qt.Assert(t, qt.IsNil(err), qt.Commentf("%s stdout: %s stderr: %s", name, out, stderr))
		qt.Assert(t, qt.Equals(out, string(want)), qt.Commentf("case %s", name))
	}
}

func TestSprint270ImportedInterfaceMapKey(t *testing.T) {
	dep := gosource.PackageSpec{
		Path: "test/dep",
		Sources: []gosource.Source{{
			Name: "dep.go",
			Data: []byte(`package dep
type Node interface{ ID() int }
type Type interface{ Name() string }
`),
		}},
	}
	out, stderr := runGoSourcePackageSet(t, "s270-imported-interface-key", `package main
import ("fmt"; "./dep")
type localNode int
func (n localNode) ID() int { return int(n) }
type localType string
func (t localType) Name() string { return string(t) }
func main() {
	nodes := map[dep.Node]int{}
	var n dep.Node = localNode(7)
	nodes[n] = 11
	fmt.Println(nodes[n], nodes[localNode(7)], len(nodes))

	types := map[dep.Type]int{}
	var t dep.Type = localType("T")
	types[t] = 13
	fmt.Println(types[t], types[localType("T")], len(types))
}
`, []gosource.PackageSpec{dep})
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "11 11 1\n13 13 1\n"))
}

func TestSprint165ComparableMapKeysRejectStaticUnhashable(t *testing.T) {
	path := filepath.Join("testdata", "sprint165", "interp-collections", "comparable-map-keys", "static_unhashable.go")
	source, err := os.ReadFile(path)
	qt.Assert(t, qt.IsNil(err))
	_, err = gosource.Parse(strings.NewReader(string(source)), path, gosource.Options{RunMain: true})
	qt.Assert(t, qt.ErrorMatches(err, `(?s).*invalid map key type \[\]int.*`))
}

func TestSprint165CollectionMechanisms(t *testing.T) {
	root := filepath.Join("testdata", "sprint165", "interp-collections", "collection-mechanisms")
	for _, name := range []string{
		"blank_builtin_targets",
		"delete_interface_keys",
		"iife_index_bounds",
		"zero_map_len_delete",
	} {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join(root, name+".go"))
			qt.Assert(t, qt.IsNil(err))
			want, err := os.ReadFile(filepath.Join(root, name+".expected"))
			qt.Assert(t, qt.IsNil(err))
			out, stderr, err := runGoSource(t, name, string(source))
			qt.Assert(t, qt.IsNil(err), qt.Commentf("%s stdout: %s stderr: %s", name, out, stderr))
			qt.Assert(t, qt.Equals(out, string(want)), qt.Commentf("case %s", name))
		})
	}
}
