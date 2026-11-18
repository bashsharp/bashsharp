package gosource

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// resolveImportPath applies the compiler's own import-path rules
// (cmd/compile/internal/noder/import.go resolveImportPath and openPackage)
// to a path go/types has already validated, before any package map or
// importer is consulted. The checks are gc's, in gc's order, with gc's
// wording; go/types renders a rejection as "could not import P (E)" exactly
// as types2 does for gc, and keeps checking the file against a fake package,
// so every later import of the same file is still diagnosed.
//
// Covered: the reserved "main" path, an absolute local path, and a non-local
// path that is not in canonical form. A relative path is resolved by
// mapImporter.resolve (the `-D` rule) and is exempt from the canonical-form
// check, as gc's islocalname exempts it. The import-cycle check
// (path == the package being compiled) needs the `-p` identity, which a Load
// without Options.ImportPath does not carry, and is left to the checker.
func resolveImportPath(p string) error {
	if p == "main" {
		return errors.New(`cannot import "main"`)
	}
	if isLocalImportName(p) {
		if p[0] == '/' {
			return errors.New("import path cannot be absolute path")
		}
		return nil
	}
	// local imports should be canonicalized already.
	// don't want to see "encoding/../encoding/base64"
	// as different from "encoding/base64".
	if q := path.Clean(p); q != p {
		return fmt.Errorf("non-canonical import path %q (should be %q)", p, q)
	}
	return nil
}

// isLocalImportName mirrors gc's islocalname: a path that begins with "./",
// "../" or "/" (or is exactly "." / "..") is resolved relative to a directory
// and is never canonicalised.
func isLocalImportName(name string) bool {
	return strings.HasPrefix(name, "/") ||
		strings.HasPrefix(name, "./") || name == "." ||
		strings.HasPrefix(name, "../") || name == ".."
}
