package syntax

import "testing"

// The rule is cmd/go's disallowInternal on identities; every row names the
// cmd/go case it mirrors. A user identity is dotted, so the negatives are the
// proof that no user program reaches an internal package through the rule.
func TestBashPPInternalImportVisible(t *testing.T) {
	cases := []struct {
		identity string
		path     string
		testMain bool
		want     bool
		why      string
	}{
		// no internal element: the rule does not apply
		{"example.com/other", "fmt", false, true, "no internal element"},
		{"", "fmt", false, true, "no internal element, even for an empty identity"},
		{"example.com/other", "internalx/y", false, true, "internalx is not the element internal"},
		{"example.com/other", "x/myinternal", false, true, "myinternal is not the element internal"},
		// cmd/go's module branch: the importer's path inside the parent of internal
		{"example.com/m/y", "example.com/m/internal/x", false, true, "identity inside the parent"},
		{"example.com/m", "example.com/m/internal/x", false, true, "identity is the parent"},
		{"example.com/m/internal/x", "example.com/m/internal", false, true, "trailing /internal"},
		{"example.com/other", "example.com/m/internal/x", false, false, "foreign module"},
		{"example.com/mx", "example.com/m/internal/x", false, false, "prefix must end at a path element"},
		{"cmd/compile/internal/foo", "cmd/compile/internal/base", false, true, "corpus: a cmd/compile package"},
		{"cmd/compile/internal/base.test", "cmd/compile/internal/ssagen", false, true, "corpus: cmd/go's <pkg>.test identity"},
		{"cmd/compile/internal/base_test", "cmd/compile/internal/base", false, true, "corpus: the external test package"},
		{"cmd/link/internal/ld", "cmd/compile/internal/base", false, false, "sibling command tree"},
		{"example.com/tool", "cmd/compile/internal/base", false, false, "dotted identity never inside cmd/"},
		// the final internal element governs
		{"a/internal/b", "a/internal/b/internal/c", false, true, "inside the parent of the final internal"},
		{"a/x", "a/internal/b/internal/c", false, false, "inside the first parent only"},
		// top-level internal/…: only a standard identity (cmd/go's IsStandardImportPath)
		{"cmd/compile/internal/foo", "internal/buildcfg", false, true, "corpus: 15 package rows"},
		{"main", "internal/runtime/sys", false, true, "corpus: intrinsic.go, the testdir harness's -p main"},
		{"p", "internal/runtime/atomic", false, true, "corpus: escape_runtime_atomic.go, the harness's -p p"},
		{"std", "internal/abi", false, true, "std-shaped identity"},
		{"example.com/foo", "internal/buildcfg", false, false, "dotted identity never standard"},
		{"example.com/foo", "internal", false, false, "the bare internal package"},
		{"gopkg.in/yaml.v3", "internal/abi", false, false, "dotted first element"},
		{"a.b", "internal/abi", false, false, "dotted single element"},
		{"", "internal/abi", false, false, "empty identity is never standard"},
		// testing/internal/…: the asserted test main only
		{"cmd/compile/internal/base.test", "testing/internal/testdeps", true, true, "the test main"},
		{"example.com/m.test", "testing/internal/testdeps", true, true, "the test main of a user module"},
		{"cmd/compile/internal/base.test", "testing/internal/testdeps", false, false, "the fact is not inferred from the suffix"},
		{"example.com/m.test", "testing/internal/testdeps", false, false, "the fact is not inferred from the suffix"},
		{"testing/x", "testing/internal/testdeps", false, true, "inside testing/ by the ordinary rule"},
		{"example.com/m.test", "internal/testenv", true, false, "test main exempts testing/internal only"},
		{"example.com/m.test", "cmd/compile/internal/base", true, false, "test main exempts testing/internal only"},
		{"example.com/m.test", "testing/internalx", true, true, "internalx is not the element internal: no rule applies"},
	}
	for _, c := range cases {
		if got := BashPPInternalImportVisible(c.identity, c.path, c.testMain); got != c.want {
			t.Errorf("visible(%q, %q, testMain=%v) = %v, want %v (%s)", c.identity, c.path, c.testMain, got, c.want, c.why)
		}
	}
}

func TestBashPPInternalImportElement(t *testing.T) {
	cases := []struct {
		path   string
		parent string
		ok     bool
	}{
		{"internal", "", true},
		{"internal/abi", "", true},
		{"internal/runtime/sys", "", true},
		{"cmd/compile/internal/base", "cmd/compile", true},
		{"a/internal", "a", true},
		{"a/internal/b/internal/c", "a/internal/b", true},
		{"testing/internal/testdeps", "testing", true},
		{"fmt", "", false},
		{"internalx", "", false},
		{"x/internalx/y", "", false},
		{"x/myinternal", "", false},
	}
	for _, c := range cases {
		parent, ok := BashPPInternalImportElement(c.path)
		if parent != c.parent || ok != c.ok {
			t.Errorf("element(%q) = (%q, %v), want (%q, %v)", c.path, parent, ok, c.parent, c.ok)
		}
	}
	for path, want := range map[string]bool{"cmd/x": true, "main": true, "p": true, "std": true, "": true, "example.com/x": false, "a.b": false, "a.b/c": false, "a/b.c": true} {
		if got := BashPPStandardImportPath(path); got != want {
			t.Errorf("standard(%q) = %v, want %v", path, got, want)
		}
	}
}
