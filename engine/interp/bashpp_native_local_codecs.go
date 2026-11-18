package interp

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
// Codecs contain typed field selectors only. Original method bodies never
// enter the helper; private field access is legal because mirrored declarations
// and their codecs belong to the same generated Go package.
import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"sort"
	"strings"
)

// bashPPCodecFieldNames lists the storage names one struct field contributes
// to a codec: each declared name, or the promoted name of an embedded field,
// which Go's field selector resolves on the enclosing type.
func bashPPCodecFieldNames(field *ast.Field) []string {
	if len(field.Names) == 0 {
		var name func(ast.Expr) (string, bool)
		name = func(expr ast.Expr) (string, bool) {
			switch e := expr.(type) {
			case *ast.Ident:
				return e.Name, true
			case *ast.StarExpr:
				return name(e.X)
			case *ast.SelectorExpr:
				return e.Sel.Name, true
			case *ast.IndexExpr:
				// An embedded generic instantiation X[A] is promoted as X.
				return name(e.X)
			case *ast.IndexListExpr:
				return name(e.X)
			}
			return "", false
		}
		if promoted, ok := name(field.Type); ok {
			return []string{promoted}
		}
		return nil
	}
	var out []string
	for _, id := range field.Names {
		if id.Name == "_" {
			continue
		}
		out = append(out, id.Name)
	}
	return out
}

func bashPPLocalCodecsGo(locals []bashPPLocalType) (string, error) {
	declarations := make(map[string]ast.Expr)
	for _, local := range locals {
		expr, err := parser.ParseExpr(local.Decl)
		if err != nil {
			return "", fmt.Errorf("local codec %s: %w", local.Name, err)
		}
		declarations[local.Name] = expr
	}
	render := func(expr ast.Expr) string {
		var b bytes.Buffer
		_ = format.Node(&b, token.NewFileSet(), expr)
		return b.String()
	}
	shapes := make(map[string]*ast.StructType)
	var underlying func(ast.Expr, map[string]bool) *ast.StructType
	underlying = func(expr ast.Expr, seen map[string]bool) *ast.StructType {
		switch e := expr.(type) {
		case *ast.StructType:
			return e
		case *ast.Ident:
			if seen[e.Name] {
				return nil
			}
			seen[e.Name] = true
			return underlying(declarations[e.Name], seen)
		}
		return nil
	}
	for _, local := range locals {
		expr := declarations[local.Name]
		if shape := underlying(expr, map[string]bool{}); shape != nil {
			shapes[local.Name] = shape
		}
		ast.Inspect(expr, func(node ast.Node) bool {
			if shape, ok := node.(*ast.StructType); ok {
				// A go:"track" field is legal in a named declaration under
				// fieldtrack, but not in the anonymous type expression used
				// to register a structural codec. The named codec above
				// already covers this shape.
				tracked := false
				for _, field := range shape.Fields.List {
					tracked = tracked || field.Tag != nil && strings.Contains(field.Tag.Value, `go:"track"`)
				}
				if !tracked {
					shapes[render(shape)] = shape
				}
			}
			return true
		})
	}
	names := make([]string, 0, len(shapes))
	for name := range shapes {
		names = append(names, name)
	}
	sort.Strings(names)
	// Local variables must not shadow an original type referenced by a codec.
	prefix := "bppCodec"
	for {
		collision := false
		for name := range declarations {
			if strings.HasPrefix(name, prefix) {
				collision = true
				break
			}
		}
		if !collision {
			break
		}
		prefix += "_"
	}
	var b strings.Builder
	for _, name := range names {
		shape := shapes[name]
		fmt.Fprintf(&b, "func init() {\nlocalStructCodecs[reflect.TypeFor[%s]()] = localStructCodec{\n", name)
		fmt.Fprintf(&b, "decode: func(%sState *decodeState, %sWire value) (reflect.Value,error) {\nvar %sDst %s\nfor %sName,%sField := range %sWire.Fields { _ = %sField; switch %sName {\n", prefix, prefix, prefix, name, prefix, prefix, prefix, prefix, prefix)
		for _, field := range shape.Fields.List {
			for _, name := range bashPPCodecFieldNames(field) {
				fmt.Fprintf(&b, "case %q: %sItem,%sErr := decodeIn(%sState,%sField,reflect.TypeOf(&%sDst.%s).Elem()); if %sErr != nil { return reflect.Value{},%sErr }; reflect.ValueOf(&%sDst.%s).Elem().Set(%sItem)\n", name, prefix, prefix, prefix, prefix, prefix, name, prefix, prefix, prefix, name, prefix)
			}
		}
		fmt.Fprintf(&b, "default: return reflect.Value{},fmt.Errorf(\"unknown field %%s of %%T\",%sName,%sDst)\n} }; return reflect.ValueOf(%sDst),nil },\n", prefix, prefix, prefix)
		// The structural direction carries the walk's pointer path so a
		// field that refers back to a pointer on it is a back-reference;
		// see bashpp_sprint165_runtime2_cycle.go.
		fmt.Fprintf(&b, "structural: func(%sRV reflect.Value, %sPath map[uintptr]bool) value {\n%sSrc := %sRV.Interface().(%s)\n_ = %sSrc\nreturn value{Kind:\"struct\",Type:typeID(%sRV.Type()),Fields:map[string]value{\n", prefix, prefix, prefix, prefix, name, prefix, prefix)
		for _, field := range shape.Fields.List {
			for _, name := range bashPPCodecFieldNames(field) {
				fmt.Fprintf(&b, "%q: structuralPath(reflect.ValueOf(&%sSrc.%s).Elem(), %sPath),\n", name, prefix, name, prefix)
			}
		}
		b.WriteString("}} },\n}\n")
		fmt.Fprintf(&b, "types[%q] = reflect.TypeFor[%s](); types[typeID(reflect.TypeFor[%s]())] = reflect.TypeFor[%s]()\n}\n", name, name, name, name)
	}
	return b.String(), nil
}
