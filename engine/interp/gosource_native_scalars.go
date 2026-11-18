package interp

// Sprint: #118; Story: #67; Story-ID: 83b5cdc6fca6
import (
	"go/types"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceImportedScalarUnderlying resolves an imported defined scalar from
// the export metadata installed with its import. Package constants and
// contextual conversions can be prepared before the general type registry is
// visible to declaration evaluation, so this lookup is deliberately keyed by
// the authenticated go/types identity rather than by display text alone.
func (r *Runner) goSourceImportedScalarUnderlying(name string) (string, bool) {
	typ, ok := r.goSourceImportedScalarType(name)
	if !ok {
		return "", false
	}
	basic := types.Unalias(typ).Underlying().(*types.Basic)
	return basic.Name(), true
}

// goSourceImportedScalarType returns the authenticated export type for an
// imported defined scalar. Text is used only to select within the import's
// metadata; the returned go/types object is the authority for both its
// underlying representation and method set.
func (r *Runner) goSourceImportedScalarType(name string) (types.Type, bool) {
	if strings.HasPrefix(name, "*") {
		return nil, false
	}
	dot := strings.LastIndex(name, ".")
	if dot < 0 {
		return nil, false
	}
	qualifier, typeName := name[:dot], name[dot+1:]
	path := r.bashPPImports[qualifier]
	if path == "" {
		path = qualifier
	}
	typ := r.bashPPTools.nativeTypes[path+"."+typeName]
	if typ == nil {
		typ = r.goSourceImportedScalarByPackageName(qualifier, typeName)
	}
	if typ == nil {
		return nil, false
	}
	basic, ok := types.Unalias(typ).Underlying().(*types.Basic)
	if !ok || basic.Info()&(types.IsBoolean|types.IsInteger|types.IsFloat|types.IsComplex|types.IsString) == 0 {
		return nil, false
	}
	return typ, true
}

// goSourceScalarUnderlying resolves the basic representation of a scalar
// declared in interpreted Go source. Local defined types may stop at an
// imported defined type because the latter lives in export metadata rather
// than the interpreter's type registry (for example atPos -> token.Pos ->
// int). Keep that boundary typed instead of leaving package initialization to
// store the initializer's unevaluated source spelling.
func (r *Runner) goSourceScalarUnderlying(typ syntax.BashPPTypeExpr) (string, bool) {
	named, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPNamedType)
	if !ok || named.Name == nil {
		return "", false
	}
	name := named.Name.Value
	if bashPPBuiltinType(name) && bashPPScalarTypeName(name) {
		return name, true
	}
	return r.goSourceImportedScalarUnderlying(name)
}

func (r *Runner) goSourceImportedScalarByPackageName(packageName, typeName string) types.Type {
	var found types.Type
	for key, typ := range r.bashPPTools.nativeTypes {
		dot := strings.LastIndexByte(key, '.')
		if dot < 0 || key[dot+1:] != typeName {
			continue
		}
		path := key[:dot]
		if !r.goSourceImportsPath(path) {
			continue
		}
		named, ok := types.Unalias(typ).(*types.Named)
		if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
			continue
		}
		pkg := named.Obj().Pkg()
		if pkg.Path() != path || pkg.Name() != packageName {
			continue
		}
		if found != nil && found != typ {
			return nil
		}
		found = typ
	}
	return found
}

func (r *Runner) goSourceImportedScalarIdentity(name string) (string, bool) {
	typ, ok := r.goSourceImportedScalarType(name)
	if !ok {
		return "", false
	}
	named, ok := types.Unalias(typ).(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return "", false
	}
	return named.Obj().Pkg().Path() + "." + named.Obj().Name(), true
}

func (r *Runner) goSourceImportedScalarTypesIdentical(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	leftIdentity, leftOK := r.goSourceImportedScalarIdentity(left)
	rightIdentity, rightOK := r.goSourceImportedScalarIdentity(right)
	return leftOK && rightOK && leftIdentity == rightIdentity
}

func (r *Runner) goSourceImportsPath(path string) bool {
	for _, imported := range r.bashPPImports {
		if imported == path {
			return true
		}
	}
	return false
}

// goSourceNativeScalarReceiver reports whether expr names a plain interpreter
// scalar that still stands for a dependency-owned defined type, and so carries
// that type's method set.
//
// [Runner.bashPPBindNativeValue] deliberately stores a scalar native result — a
// time.Duration, an os.FileMode — as an ordinary shell string instead of a
// session handle, so that arithmetic, comparison and printing keep working
// without a round trip. Only the storage is ordinary: the defined type stays on
// the cell, and a method set hangs off the type rather than off the storage.
// `diff.Hours()` is therefore the dependency's method on time.Duration;
// resolving it against interpreter field storage instead reports the receiver
// as having no such method at all.
func (r *Runner) goSourceNativeScalarReceiver(expr syntax.BashPPExpr) bool {
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.goSourceNativeScalarReceiver(x.X)
	case *syntax.BashPPIdent:
		if r.bashPPScope == nil {
			return false
		}
		return r.goSourceNativeScalarCell(r.bashPPScope.lookup(x.Name.Value))
	case *syntax.BashPPCall:
		if len(x.ResultTypes) == 1 {
			_, imported := r.goSourceImportedScalarUnderlying(bashPPTypeText(x.ResultTypes[0]))
			return imported
		}
	}
	// A computed receiver can be produced by interpreted code while its
	// checked result type is still an imported defined scalar, as in
	// p.Pos().IsValid() where Pos returns token.Pos. Classification must stay
	// static here: evaluating the expression to discover its type would replay
	// the receiver when bashPPNativeMethodReceiver performs the actual call.
	typ, ok := r.goSourceStaticExprType(expr)
	if !ok {
		return false
	}
	_, imported := r.goSourceImportedScalarUnderlying(bashPPTypeText(typ))
	return imported
}

// goSourceNativeScalarCell reports whether cell holds a scalar of an imported
// defined type. A handle, pointer, interface or channel cell already reaches
// the dependency by its own route and is not this case.
func (r *Runner) goSourceNativeScalarCell(cell *bashPPCell) bool {
	cell = cell.view()
	if !r.bashPPGoSource || cell == nil {
		return false
	}
	if cell.vr.Kind == expand.Object || cell.pointer || cell.interfaceValue != nil || cell.channel != nil {
		return false
	}
	name := cell.typeName
	if name == "" {
		if named, ok := bashPPSelectorCellType(cell).(*syntax.BashPPNamedType); ok {
			name = named.Name.Value
		}
	}
	return r.goSourceImportedTypeName(name)
}

// goSourceBindImportedScalarMethod hands a selector path rooted in local
// storage back to the dependency when its terminal type is an imported
// defined scalar. The local aggregate owns the bytes, while the authenticated
// export type owns the method body and method set.
func (r *Runner) goSourceBindImportedScalarMethod(root *bashPPCell, edges []bashPPEmbedEdge, typ syntax.BashPPTypeExpr, method string) (*bashPPFunc, bool) {
	resolved, ok := r.goSourceImportedScalarMethodType(typ, method)
	if !ok {
		return nil, false
	}
	value, meta, err := r.bashPPReadCellValue(root)
	if err == nil {
		value, meta, err = bashPPReadSelection(value, meta, edges)
	}
	if err != nil {
		r.goSourceRuntimeFault(err)
		return nil, true
	}
	receiver, err := r.bashPPBridgeCollection(value, meta, resolved)
	if err != nil {
		r.exit.fatal(err)
		return nil, true
	}
	bound, err := r.bashPPBindNativeMethod(r.ectx, receiver, method)
	if err != nil {
		r.exit.fatal(err)
		return nil, true
	}
	fn := &bashPPFunc{native: &bound}
	if sig := syntax.BashPPTypeExprFromText(bound.Type); sig != nil {
		if ft, ok := sig.(*syntax.BashPPFuncType); ok {
			fn.lit = &syntax.BashPPFuncLit{Params: ft.Params, Results: ft.Results}
		}
	}
	return fn, true
}

func (r *Runner) goSourceImportedScalarMethodType(typ syntax.BashPPTypeExpr, method string) (syntax.BashPPTypeExpr, bool) {
	// Follow aliases, which inherit their target's methods, but never defined
	// types: a local `type P token.Pos` has the same representation and none of
	// token.Pos' methods.
	resolved := r.bashPPCanonicalAssignableType(typ)
	nativeType, ok := r.goSourceImportedScalarType(bashPPTypeText(resolved))
	if !ok || types.NewMethodSet(nativeType).Lookup(nil, method) == nil {
		return nil, false
	}
	return resolved, true
}

// goSourceImportedTypeName reports whether name qualifies a type with an
// imported package.
//
// Both spellings reach here. Source-written types keep the program's own import
// alias, which gosource rewrites for hygiene, so the alias is the key of
// [Runner.bashPPImports]. A type name that came back from the dependency is the
// package-path identity reflect reports — the spelling %T prints — which is the
// map's value instead. Recognising only the alias would silently drop every
// value the dependency itself named.
func (r *Runner) goSourceImportedTypeName(name string) bool {
	bare := strings.TrimLeft(name, "*")
	dot := strings.LastIndex(bare, ".")
	if dot < 0 {
		return false
	}
	qualifier := bare[:dot]
	if r.bashPPImports[qualifier] != "" {
		return true
	}
	for _, path := range r.bashPPImports {
		if path == qualifier || goSourcePackageBase(path) == qualifier {
			return true
		}
	}
	return false
}

// goSourceCanonicalNativeType reduces one dependency-owned type name to the
// single spelling both sides of a comparison can be written in.
//
// Three spellings for one type reach the interpreter. The original program
// wrote `exec.ExitError`, which gosource rewrote to a hygienic import alias.
// The dependency reports a defined type as its package PATH plus name
// (`os/exec.ExitError`, what reflect's PkgPath gives) but a pointer type as
// Go's printed form (`*exec.ExitError`, the package NAME). Comparing any two of
// those as text says they are different types, which is how an assertion that
// must succeed came to report no match at all.
func (r *Runner) goSourceCanonicalNativeType(name string) string {
	pointers := len(name) - len(strings.TrimLeft(name, "*"))
	bare := name[pointers:]
	dot := strings.LastIndex(bare, ".")
	if dot < 0 {
		return name
	}
	qualifier, typeName := bare[:dot], bare[dot+1:]
	if path := r.bashPPImports[qualifier]; path != "" {
		qualifier = path
	}
	return strings.Repeat("*", pointers) + goSourcePackageBase(qualifier) + "." + typeName
}

// goSourceNativeTypePathIdentity gives an imported named type one unambiguous
// package-path spelling. Source types arrive under a per-file import alias;
// worker-authenticated NativeType values already use the package path.
func (r *Runner) goSourceNativeTypePathIdentity(name string) (string, bool) {
	pointers := len(name) - len(strings.TrimLeft(name, "*"))
	bare := name[pointers:]
	dot := strings.LastIndex(bare, ".")
	if dot < 0 {
		return "", false
	}
	qualifier, typeName := bare[:dot], bare[dot+1:]
	if path := r.bashPPImports[qualifier]; path != "" {
		qualifier = path
	} else if !r.goSourceImportsPath(qualifier) {
		return "", false
	}
	return strings.Repeat("*", pointers) + qualifier + "." + typeName, true
}

// goSourceNativeTypeIdentical reports whether two names denote one imported
// type. It answers only for imported types; a local name is compared as text
// by the caller, exactly as before.
func (r *Runner) goSourceNativeTypeIdentical(left, right syntax.BashPPTypeExpr) bool {
	leftText, rightText := bashPPTypeText(left), bashPPTypeText(right)
	if !r.goSourceImportedTypeName(leftText) || !r.goSourceImportedTypeName(rightText) {
		return false
	}
	return r.goSourceCanonicalNativeType(leftText) == r.goSourceCanonicalNativeType(rightText)
}

func goSourcePackageBase(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}
