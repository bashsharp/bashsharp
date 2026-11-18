package gosource

import "github.com/bashsharp/bashsharp/engine/syntax"

// GoSourceAST exposes the positioned package tree to consumers that do not
// depend on the Go-source frontend. A nil Program has no tree.
func (p *Program) GoSourceAST() *syntax.File {
	if p == nil {
		return nil
	}
	return p.File
}

// GoSourcePackage returns the original package name.
func (p *Program) GoSourcePackage() string {
	if p == nil {
		return ""
	}
	return p.Package
}

// GoSourceInitializers returns initializer names in checked execution order.
func (p *Program) GoSourceInitializers() []string {
	if p == nil {
		return nil
	}
	return p.InitFunctions
}
