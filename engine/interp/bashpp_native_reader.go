package interp

// Native Read buffers stay in the dependency process. Original Read bodies use
// authenticated handles, so indexed writes change the exact waiting buffer.
// No original method body is emitted or executed in native Go.
import (
	"bytes"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
)

func (r *Runner) bashPPNativeByteAssign(target, rhs syntax.BashPPExpr) bool {
	index, ok := target.(*syntax.BashPPIndexExpr)
	if !r.bashPPGoSource || !ok || !r.bashPPNativeExpr(index.X) {
		return false
	}
	fail := func(err error) {
		if err != nil {
			r.exit.fatal(&goSourceError{prefix: r.bashErrPrefix(target.Pos()), err: err})
		}
	}
	base, err := r.bashPPBridgeExpr(index.X)
	if err != nil {
		fail(err)
		return true
	}
	key, err := r.bashPPBridgeExpr(index.Index)
	if err != nil {
		fail(err)
		return true
	}
	if !bashPPNativeByteSliceType(base) {
		// Any other dependency-owned map, slice or array element takes the
		// assigned value at the element type the dependency declares
		// (scope.Objects[name] = obj).
		value, err := r.bashPPBridgeExpr(rhs)
		if err != nil {
			fail(err)
			return true
		}
		_, err = r.bashPPNativeAccess(r.ectx, "index-set", base, "", key, value)
		fail(err)
		return true
	}
	scalar, err := r.bashPPEvalScalarExpr(rhs)
	if err != nil {
		fail(err)
		return true
	}
	scalar, err = r.bashPPConvertScalar("uint8", scalar)
	if err != nil {
		fail(err)
		return true
	}
	value, err := bridgeScalar(scalar)
	if err != nil {
		fail(err)
		return true
	}
	_, err = r.bashPPNativeAccess(r.ectx, "byte-set", base, "", key, value)
	fail(err)
	return true
}
func synchronousReaderCallback(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Receiver != nil {
		return false
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if !ok {
		return false
	}
	path := req.Imports[alias]
	index := -1
	switch path + "." + name {
	case "golang.org/x/tour/reader.Validate", "io.ReadAll", "io.ReadFull", "io.ReadAtLeast":
		index = 0
	case "io.Copy":
		if len(q.Args) != 2 || q.Args[0].Kind != "handle" {
			return false
		}
		switch q.Args[0].NativeType {
		case "*os.File", "*bytes.Buffer", "*bufio.Writer", "*net.TCPConn", "*net.UnixConn":
		default:
			return false
		}
		index = 1
	}
	if index < 0 || index >= len(q.Args) {
		return false
	}
	reader := q.Args[index]
	name = strings.TrimPrefix(reader.Type, "main.")
	if reader.Kind == "pointer" && len(reader.Elements) == 1 {
		name = strings.TrimPrefix(reader.Elements[0].Type, "main.")
	}
	for _, typ := range req.localTypePlan().named(name) {
		for _, method := range typ.Methods {
			if method.Name == "Read" {
				return true
			}
		}
	}
	return false
}

// Rewrite only generated type selector identifiers; field tags and original
// names remain literal. This never receives an original function body.
func bashPPNativeTypeImports(decl string, aliases map[string]string) (string, error) {
	expr, err := parser.ParseExpr(decl)
	if err != nil {
		return "", err
	}
	ast.Inspect(expr, func(node ast.Node) bool {
		if sel, ok := node.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] != "" {
				id.Name = aliases[id.Name]
			}
		}
		return true
	})
	var out bytes.Buffer
	if err = format.Node(&out, token.NewFileSet(), expr); err != nil {
		return "", err
	}
	return out.String(), nil
}

func bashPPNativeDeclImports(decl string, aliases map[string]string) (string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "generated.go", "package main\n"+decl, 0)
	if err != nil {
		return "", err
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if sel, ok := node.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] != "" {
				id.Name = aliases[id.Name]
			}
		}
		return true
	})
	var out bytes.Buffer
	for _, item := range file.Decls {
		if err := format.Node(&out, token.NewFileSet(), item); err != nil {
			return "", err
		}
		out.WriteByte('\n')
	}
	return out.String(), nil
}

// bashPPNativeByteSliceType reports whether a dependency-owned indexed base is
// a byte slice, whose element writes keep the byte conversion of byte-set.
func bashPPNativeByteSliceType(base bashPPBridgeValue) bool {
	for _, typ := range []string{base.NativeType, base.Type} {
		switch typ {
		case "[]byte", "[]uint8":
			return true
		}
	}
	return base.NativeType == "" && base.Type == ""
}
