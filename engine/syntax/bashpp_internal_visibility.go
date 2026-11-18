package syntax

import "strings"

// BashPPInternalImportElement reports whether path carries an "internal"
// element in the position cmd/go's findInternal (load/pkg.go) recognises —
// the whole path, a leading "internal/", a trailing "/internal" or an inner
// "/internal/" — and returns the parent of the FINAL such element: the tree
// an importer must sit inside. The final element is the one that matters
// because it imposes the most restrictive requirement. The parent is "" for
// a top-level internal path such as "internal/abi".
func BashPPInternalImportElement(path string) (parentOfInternal string, ok bool) {
	switch {
	case strings.HasSuffix(path, "/internal"):
		return path[:len(path)-len("/internal")], true
	case strings.Contains(path, "/internal/"):
		return path[:strings.LastIndex(path, "/internal/")], true
	case path == "internal", strings.HasPrefix(path, "internal/"):
		return "", true
	}
	return "", false
}

// BashPPStandardImportPath is cmd/go's IsStandardImportPath: a path whose
// first element has no dot belongs to the standard library or the go
// command's own tree, never to a module. A user program's identity is
// dotted, so it is never standard.
func BashPPStandardImportPath(path string) bool {
	elem, _, _ := strings.Cut(path, "/")
	return !strings.Contains(elem, ".")
}

// BashPPInternalImportVisible is cmd/go's internal-visibility rule
// (golang.org/s/go14internal, load/pkg.go disallowInternal) expressed on
// import-path IDENTITIES instead of directories, for a checker whose
// importer's identity is declared (the compiler's -p) rather than derived
// from where its files happen to sit. It is the one identity-keyed branch in
// front of the reviewed standard-library inventory
// ([BashPPStdlibImportAllowed]), which it does not widen: an internal
// package is admitted only for an identity that cmd/go itself would admit.
//
//	no "internal" element in path                → visible
//	testMain and path is under testing/internal  → visible (cmd/go's generated
//	                                                testmain, load/pkg.go)
//	top-level internal/… (parent "")             → visible iff identity is standard
//	                                                (first element without a dot)
//	identity is inside the parent of internal    → visible (cmd/go's module branch:
//	                                                the importer's PATH, not its dir)
//	otherwise                                     → not visible
//
// The test-main exemption is a FACT the caller asserts (cmd/go knows when it
// is loading the testmain it generated); it is never inferred from a ".test"
// suffix. An empty identity is never inside any tree and never standard, so
// it sees no internal package at all.
func BashPPInternalImportVisible(identity, path string, testMain bool) bool {
	parent, ok := BashPPInternalImportElement(path)
	if !ok {
		return true
	}
	if testMain && (path == "testing/internal" || strings.HasPrefix(path, "testing/internal/")) {
		return true
	}
	if identity == "" {
		return false
	}
	if parent == "" {
		return BashPPStandardImportPath(identity)
	}
	return identity == parent || strings.HasPrefix(identity, parent+"/")
}
