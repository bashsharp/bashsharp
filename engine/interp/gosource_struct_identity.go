package interp

// Sprint: #153; Story: S153.4; Story-ID: e58cccba74f8
import (
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// THE IDENTITY OF A STRUCT LITERAL TYPE.
//
// Two struct types are identical when they have the same sequence of fields
// with the same names, identical types and the same embedding; an unexported
// field name is qualified by its package, so `struct{ int }` spelled in two
// packages are two types. The type text every other check reads spells a
// struct literal as just "struct", which is fine for a declared type (it is
// named) but makes every anonymous struct the same type to an assertion, a
// type switch and an interface comparison.
//
// The runtime has no package on a type literal; what it has is the source
// the literal was spelled in, and the marker the Go front end gives every
// top-level declaration of a linked package. A source's package is the
// package its declarations carry.

// goSourceStructIdentity spells a struct literal type for identity.
func (r *Runner) goSourceStructIdentity(x *syntax.BashPPStructType) string {
	pkg := ""
	if r.bashPPGoSource {
		pkg = r.goSourcePackageAt(x.Pos())
	}
	var b strings.Builder
	b.WriteString("struct{")
	for i, field := range x.Fields {
		if i > 0 {
			b.WriteString("; ")
		}
		typ := r.goSourceDynamicTypeIdentity(field.FieldTypeExpr)
		names := field.Names
		if len(names) == 0 {
			// An embedded field's name is its type's base name — the name as
			// declared, without a linked package's marker.
			name := goSourceDeclaredName(bashPPNamedTypeBase(field.FieldTypeExpr))
			b.WriteString(r.goSourceFieldIdentity(name, pkg))
			b.WriteString(" ")
			b.WriteString(typ)
			if field.Tag != nil {
				b.WriteString(" ")
				b.WriteString(goSourceCanonicalStructTag(field.Tag.Value))
			}
			b.WriteString(" embedded")
			continue
		}
		for j, name := range names {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(r.goSourceFieldIdentity(name.Value, pkg))
		}
		b.WriteString(" ")
		b.WriteString(typ)
		if field.Tag != nil {
			b.WriteString(" ")
			b.WriteString(goSourceCanonicalStructTag(field.Tag.Value))
		}
	}
	b.WriteString("}")
	return b.String()
}

// goSourceFieldIdentity qualifies an unexported field name by its package.
func (r *Runner) goSourceFieldIdentity(name, pkg string) string {
	if pkg != "" && (name == "_" || goSourceUnexportedName(name)) {
		return pkg + "." + name
	}
	return name
}

// goSourcePackageAt reports the linked-package tag of the source a position
// lies in, or "" for the program package. A source's package is read from
// the marker on its top-level declarations, once per file.
func (r *Runner) goSourcePackageAt(pos syntax.Pos) string {
	file := r.bashPPGoSourceFile
	if file == nil || len(file.Sources) == 0 {
		return ""
	}
	if r.bashPPSourcePackages == nil || r.bashPPSourcePackagesFile != file {
		packages := make(map[uint]string, len(file.Sources))
		for _, stmt := range file.Stmts {
			name := ""
			switch d := stmt.Cmd.(type) {
			case *syntax.BashPPDecl:
				name = d.Name.Value
			case *syntax.BashPPFuncDecl:
				name = d.Name.Value
				if d.Receiver != nil && d.Receiver.RecvType != nil {
					name = d.Receiver.RecvType.Value
				}
			default:
				continue
			}
			source, ok := file.SourceAt(stmt.Pos())
			if !ok {
				continue
			}
			if _, seen := packages[source.Base]; !seen {
				packages[source.Base] = goSourceLinkedPackage(name)
			}
		}
		r.bashPPSourcePackages, r.bashPPSourcePackagesFile = packages, file
	}
	source, ok := file.SourceAt(pos)
	if !ok {
		return ""
	}
	return r.bashPPSourcePackages[source.Base]
}

// goSourceReflectTypeText spells a type the way Go's runtime does in a
// diagnostic: a struct literal as `struct { f T; g U }`.
func goSourceReflectTypeText(typ syntax.BashPPTypeExpr) string {
	switch x := typ.(type) {
	case *syntax.BashPPStructType:
		var fields []string
		for _, field := range x.Fields {
			text := goSourceReflectTypeText(field.FieldTypeExpr)
			if len(field.Names) == 0 {
				fields = append(fields, text)
				continue
			}
			for _, name := range field.Names {
				fields = append(fields, name.Value+" "+text)
			}
		}
		if len(fields) == 0 {
			return "struct {}"
		}
		return "struct { " + strings.Join(fields, "; ") + " }"
	case *syntax.BashPPPointerType:
		return "*" + goSourceReflectTypeText(x.Element)
	case *syntax.BashPPFuncType:
		return bashPPGoCanonicalTypeText(x)
	case *syntax.BashPPCollectionType:
		if x.Kind == "map" {
			return "map[" + goSourceReflectTypeText(x.Key) + "]" + goSourceReflectTypeText(x.Element)
		}
		length := ""
		if x.Length != nil {
			length = x.Length.Value
		}
		return "[" + length + "]" + goSourceReflectTypeText(x.Element)
	}
	return bashPPTypeText(typ)
}

// Tags contribute their string value to identity, independently of literal spelling.
func goSourceCanonicalStructTag(literal string) string {
	value, err := strconv.Unquote(literal)
	if err != nil {
		return literal
	}
	return strconv.Quote(value)
}
