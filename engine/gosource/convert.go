package gosource

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	s "github.com/bashsharp/bashsharp/engine/syntax"
)

type converter struct {
	packagePath   string
	importAliases map[string]string
	// syntheticImports are packages named only by a checked type which differs
	// from the source spelling, such as os.FileMode's alias target fs.FileMode.
	// The generated type must have a matching import in both execution modes.
	syntheticImports map[string]syntheticImport
	// dotImports are, per file, the paths that file binds by dot import:
	// their types are spelled bare while that file is being lowered.
	dotImports  map[*ast.File]map[string]bool
	currentFile *ast.File
	// perFileImports is set when each input file is emitted as its own Go
	// file (a native library unit): an import binding written by one file is
	// not in scope in another, so a type spelled in a file that does not
	// import its package takes a synthetic import owned by that file.
	perFileImports bool
	// fileBindings caches, per file, the spelling of each package path the
	// file binds by a named or implicit import (see fileImportBinding).
	fileBindings map[*ast.File]map[string]string
	// mapped maps each explicit package path linked into the same file to
	// its map index. A selector on one of their import bindings collapses
	// to the selected object's rename, and their package-level names are
	// spelled by that rename everywhere (see mangledName); the program's
	// own names are never renamed.
	mapped map[string]int
	// mappedPkgs lists the linked packages by map index, so a type string's
	// marker qualifier can be resolved back to the named object.
	mappedPkgs []*types.Package
	// resolveImport applies the relative-import rule to an import path as
	// written, so an import without a binding (blank) can still be matched
	// against mapped.
	resolveImport func(string) (string, error)
	syntheticPos  token.Pos
	prefix        string

	// mappedTypePattern recognizes the marker qualifier types.TypeString emits
	// for a package linked into the flat file. The prefix is immutable once
	// conversion starts, so compile its Unicode-aware pattern at most once per
	// converter instead of once per checked type.
	mappedTypePattern *regexp.Regexp

	fset    *token.FileSet
	files   []*ast.File
	sources []Source
	info    *types.Info
	renames map[types.Object]string
	// checkerNames restores identifiers hygienically renamed solely to model
	// cmd/cgo's non-binding pseudo-package C during go/types checking.
	checkerNames map[string]string
	// shadowedBuiltins names predeclared type names a package redeclares at
	// package scope as a non-type (e.g. `const int = 15`). expr() must not
	// materialize an untyped constant through such a name: `int(x)` would call
	// the const, not convert to the type.
	shadowedBuiltins map[string]bool
	// constSpecs maps each package-level or local constant this package
	// declares to its spec, so a use site can tell whether the declaration
	// was emitted as written or materialized by value (see constAsWritten).
	// Built on first use from files.
	constSpecs map[*types.Const]constSpec
	// shiftOperand is the constant left operand of a non-constant shift
	// about to be converted; its contextual type is always spelled.
	shiftOperand            ast.Expr
	rawConstantExpr         bool
	suppressSyntheticImport bool
	err                     error
	branchScopes            []converterBranchScope
	statementLabel          string
	// gotoTargets holds the labels a goto names, built on first use by
	// gotoTarget; nil until then.
	gotoTargets            map[types.Object]bool
	localChannelTypes      map[string]bool
	syncPlanned, syncLocal bool
	// innerNames caches, per file, the set of identifiers declared in any scope
	// strictly inside the file scope — function, block, case, and other body
	// scopes. A spelling head absent from this set cannot be shadowed by a local
	// declaration at any position, so spellingShadowed resolves it against the
	// file scope directly instead of walking the whole scope tree with
	// types.Scope.Innermost once per reference (O(references × top-level-decls)
	// on constant-saturated generated code such as cmd/compile/internal/ssa).
	innerNamesFile *ast.File
	innerNames     map[string]bool
}

// syntheticImport is an import created while spelling a checked type whose
// package was not written by the source. sources records every input file
// whose generated Go refers to the synthetic binding. Library lowering emits
// one Go file per input file, so a synthetic import must retain that ownership.
type syntheticImport struct {
	alias   string
	sources map[token.Pos]bool
}

type converterBranchScope struct {
	label string
	loop  bool
}

func (c *converter) pushBranchScope(label string, loop bool) func() {
	c.branchScopes = append(c.branchScopes, converterBranchScope{label: label, loop: loop})
	return func() { c.branchScopes = c.branchScopes[:len(c.branchScopes)-1] }
}

func (c *converter) takeStatementLabel() string {
	label := c.statementLabel
	c.statementLabel = ""
	return label
}

func (c *converter) labeledBranchDepth(branch *ast.BranchStmt) int {
	depth := 0
	for i := len(c.branchScopes) - 1; i >= 0; i-- {
		scope := c.branchScopes[i]
		if branch.Tok == token.CONTINUE && !scope.loop {
			continue
		}
		depth++
		if scope.label == branch.Label.Name {
			return depth
		}
	}
	c.fail(branch, "labeled branch")
	return 0
}

// scopedTypeSwitch reports whether st is a type switch whose init statement
// converts to an enclosing block (see the TypeSwitchStmt case of statements).
func (c *converter) scopedTypeSwitch(st ast.Stmt) bool {
	x, ok := st.(*ast.TypeSwitchStmt)
	return ok && x.Init != nil
}

// gotoTarget reports whether a goto anywhere in the package names the label
// declared by ident. Labels are function-scoped objects in go/types, so the
// answer is exact across nested function literals. The goto set is built on
// first use from every file's branches.
func (c *converter) gotoTarget(ident *ast.Ident) bool {
	if c.gotoTargets == nil {
		c.gotoTargets = map[types.Object]bool{}
		for _, f := range c.files {
			ast.Inspect(f, func(n ast.Node) bool {
				if branch, ok := n.(*ast.BranchStmt); ok && branch.Tok == token.GOTO && branch.Label != nil {
					if obj := c.info.Uses[branch.Label]; obj != nil {
						c.gotoTargets[obj] = true
					}
				}
				return true
			})
		}
	}
	obj := c.info.Defs[ident]
	return obj != nil && c.gotoTargets[obj]
}

func (c *converter) pos(p token.Pos) s.Pos {
	if c.syntheticPos.IsValid() && p.IsValid() {
		p = c.syntheticPos
	}
	if !p.IsValid() {
		return s.Pos{}
	}
	v := c.fset.Position(p)
	return s.NewPos(uint(p-1), uint(v.Line), uint(v.Column))
}
func (c *converter) fail(n ast.Node, what string) {
	if c.err == nil {
		p := n.Pos()
		if c.syntheticPos.IsValid() {
			p = c.syntheticPos
		}
		c.err = fmt.Errorf("%s: gosource: unsupported %s", c.fset.Position(p), what)
	}
}
func (c *converter) lit(p token.Pos, v string) *s.Lit {
	return &s.Lit{Value: v, ValuePos: c.pos(p), ValueEnd: c.pos(p + token.Pos(len(v)))}
}
func (c *converter) ident(n *ast.Ident) *s.Lit {
	v := n.Name
	obj := c.info.ObjectOf(n)
	if rename := c.renames[obj]; rename != "" {
		v = rename
	} else if rename := c.checkerNames[v]; rename != "" {
		v = rename
	}
	out := c.lit(n.Pos(), v)
	out.ValueEnd = c.pos(n.End())
	return out
}

func (c *converter) qualifiedIdent(n *ast.Ident) (*s.Lit, *s.Lit, bool) {
	rename := c.renames[c.info.ObjectOf(n)]
	qualifier, member, ok := strings.Cut(rename, ".")
	if !ok || qualifier == "" || member == "" {
		return nil, nil, false
	}
	return c.lit(n.Pos(), qualifier), c.lit(n.Pos(), member), true
}

// mappedPkgName reports whether e is the import binding of a linked explicit
// package. A selector through it collapses to the selected object's rename:
// the package's declarations were lowered into the same flat file.
func (c *converter) mappedPkgName(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	if !ok || len(c.mapped) == 0 {
		return false
	}
	pkgname, ok := c.info.ObjectOf(id).(*types.PkgName)
	if !ok {
		return false
	}
	_, mapped := c.mapped[pkgname.Imported().Path()]
	return mapped
}

// mappedMarker is the qualifier a type string spells a mapped package by:
// the hygiene prefix, which is free in every linked source, and the map
// index. typeString rewrites "<marker>.Name" into the object's rename.
func (c *converter) mappedMarker(index int) string {
	return fmt.Sprintf("%spkg_%d", c.prefix, index)
}

// mangledName is the flat-file spelling of a mapped package's package-level
// object: the marker followed by the declared name.
func (c *converter) mangledName(index int, name string) string {
	return c.mappedMarker(index) + "_" + name
}

// bridgedType reports whether a named type is declared by a package the
// dependency helper serves: neither the program's own package nor a package
// linked into the file by the explicit map, whose declarations the runtime
// holds. A predeclared name (error) has no package and is not bridged.
func (c *converter) bridgedType(named *types.Named) bool {
	pkg := named.Obj().Pkg()
	if pkg == nil || pkg.Path() == c.packagePath {
		return false
	}
	_, mapped := c.mapped[pkg.Path()]
	return !mapped
}

// qualifier spells a package in a type string: unqualified for the program
// package, by marker for a linked explicit package, by hoisted alias for an
// import, and by declared name otherwise. Only typeString may use it, since
// the marker must be rewritten before the text is read.
func (c *converter) qualifier(p *types.Package) string {
	if index, ok := c.mapped[p.Path()]; ok {
		return c.mappedMarker(index)
	}
	if p.Path() == c.packagePath {
		return ""
	}
	if c.currentFile != nil && c.dotImports[c.currentFile][p.Path()] {
		return ""
	}
	if c.currentFile != nil {
		if name, ok := c.fileImportBinding(c.currentFile, p.Path()); ok {
			return name
		}
	}
	if imp, ok := c.syntheticImports[p.Path()]; ok {
		if c.currentFile != nil {
			imp.sources[c.currentFile.FileStart] = true
			c.syntheticImports[p.Path()] = imp
		}
		return imp.alias
	}
	if alias := c.importAliases[p.Path()]; alias != "" && !(c.perFileImports && c.currentFile != nil) {
		return alias
	}
	if c.suppressSyntheticImport {
		return p.Name()
	}
	// A checked type may expose a package which the source did not import
	// directly. Give that package a collision-free synthetic binding and let
	// Load hoist the matching import ahead of all declarations.
	imp, ok := c.syntheticImports[p.Path()]
	if !ok {
		imp = syntheticImport{
			alias:   fmt.Sprintf("%simport_type_%d", c.prefix, len(c.syntheticImports)),
			sources: map[token.Pos]bool{},
		}
	}
	if c.currentFile != nil {
		imp.sources[c.currentFile.FileStart] = true
	}
	if c.importAliases[p.Path()] == "" {
		c.importAliases[p.Path()] = imp.alias
	}
	c.syntheticImports[p.Path()] = imp
	return imp.alias
}

// fileImportBinding reports the spelling f's own import of path has in the
// generated Go: the binding's rename when it took one, else its name. Blank
// and dot imports bind no qualifier. An alias another file minted for the
// same path is not in f's scope once files are emitted separately, so the
// file's own binding is preferred in both modes.
func (c *converter) fileImportBinding(f *ast.File, path string) (string, bool) {
	if c.fileBindings == nil {
		c.fileBindings = map[*ast.File]map[string]string{}
	}
	bindings, ok := c.fileBindings[f]
	if !ok {
		bindings = map[string]string{}
		for _, spec := range f.Imports {
			var obj types.Object
			if spec.Name != nil {
				obj = c.info.Defs[spec.Name]
			} else {
				obj = c.info.Implicits[spec]
			}
			pkgname, ok := obj.(*types.PkgName)
			if !ok || pkgname.Name() == "_" || pkgname.Name() == "." {
				continue
			}
			name := c.renames[obj]
			if name == "" {
				name = pkgname.Name()
			}
			if _, seen := bindings[pkgname.Imported().Path()]; !seen {
				bindings[pkgname.Imported().Path()] = name
			}
		}
		c.fileBindings[f] = bindings
	}
	name, ok := bindings[path]
	return name, ok
}

// typeString spells a checked type for the flat file. types.TypeString can
// qualify a package but not rename an object, so a mapped package's names
// come out as "<marker>.Name" and are rewritten here: a package-level name
// becomes its rename, and any other (a function-local type, which the
// checker qualifies by package too) stays bare, as it is declared. A local
// type spelled like a package-level one of the same package takes the
// latter's rename; the runtime has no lexical type namespace either.
func (c *converter) typeString(t types.Type) string {
	text := types.TypeString(t, c.qualifier)
	for private, source := range c.checkerNames {
		text = strings.ReplaceAll(text, private, source)
	}
	if len(c.mapped) == 0 || !strings.Contains(text, c.prefix+"pkg_") {
		return text
	}
	return c.rewriteMappedTypeNames(text)
}

func (c *converter) rewriteMappedTypeNames(text string) string {
	pattern := c.mappedTypeNamePattern()
	return pattern.ReplaceAllStringFunc(text, func(match string) string {
		sub := pattern.FindStringSubmatch(match)
		index, err := strconv.Atoi(sub[1])
		if err != nil || index >= len(c.mappedPkgs) {
			return match
		}
		if obj := c.mappedPkgs[index].Scope().Lookup(sub[2]); obj != nil {
			if rename := c.renames[obj]; rename != "" {
				return rename
			}
		}
		return sub[2]
	})
}

func (c *converter) mappedTypeNamePattern() *regexp.Regexp {
	if c.mappedTypePattern == nil {
		c.mappedTypePattern = regexp.MustCompile(regexp.QuoteMeta(c.prefix+"pkg_") + `([0-9]+)\.([\pL\pN_]+)`)
	}
	return c.mappedTypePattern
}
func (c *converter) text(n ast.Node) string {
	var b bytes.Buffer
	original := map[*ast.Ident]string{}
	// A mapped package binding is spelled as a marker the hygiene prefix
	// guarantees absent from the source, then dropped with its dot.
	marker := c.prefix + "mapped"
	ast.Inspect(n, func(node ast.Node) bool {
		if sel, ok := node.(*ast.SelectorExpr); ok && c.mappedPkgName(sel.X) {
			id := sel.X.(*ast.Ident)
			original[id] = id.Name
			id.Name = marker
			return true
		}
		if id, ok := node.(*ast.Ident); ok {
			name := c.renames[c.info.ObjectOf(id)]
			if name == "" {
				name = c.checkerNames[id.Name]
			}
			if name != "" && original[id] == "" {
				original[id] = id.Name
				id.Name = name
			}
		}
		return true
	})
	defer func() {
		for id, name := range original {
			id.Name = name
		}
	}()
	_ = format.Node(&b, c.fset, n)
	if len(c.mapped) == 0 {
		return b.String()
	}
	return strings.ReplaceAll(b.String(), marker+".", "")
}
func (c *converter) word(n ast.Expr) *s.Word {
	if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			c.fail(n, "string literal")
		}
		return &s.Word{Parts: []s.WordPart{&s.SglQuoted{Left: c.pos(n.Pos()), Right: c.pos(n.End() - 1), Value: value}}}
	}
	// Preserve Go quotes as literal data. Legacy runtime consumers parse these
	// words as Go operands, never through a shell parser.
	return &s.Word{Parts: []s.WordPart{&s.Lit{Value: c.text(n), ValuePos: c.pos(n.Pos()), ValueEnd: c.pos(n.End())}}}
}
func (c *converter) stmt(cmd s.Command) *s.Stmt {
	if cmd == nil {
		return nil
	}
	return &s.Stmt{Cmd: cmd, Position: cmd.Pos()}
}
func (c *converter) block(b *ast.BlockStmt) *s.Block {
	if b == nil {
		return nil
	}
	o := &s.Block{Lbrace: c.pos(b.Lbrace), Rbrace: c.pos(b.Rbrace)}
	for _, st := range b.List {
		o.Stmts = append(o.Stmts, c.statements(st)...)
	}
	return o
}

// isNewType reports whether x is a call to the predeclared new builtin with a
// type operand. Go 1.27 also allows new(v) over a value, whose argument is not
// convertible by typ; those calls stay on the generic call path.
func (c *converter) isNewType(x *ast.CallExpr) bool {
	id, ok := ast.Unparen(x.Fun).(*ast.Ident)
	if !ok || len(x.Args) != 1 {
		return false
	}
	obj, ok := c.info.Uses[id].(*types.Builtin)
	if !ok || obj.Name() != "new" {
		return false
	}
	return c.info.Types[x.Args[0]].IsType()
}

func (c *converter) isNewBuiltin(x *ast.CallExpr) bool {
	id, ok := ast.Unparen(x.Fun).(*ast.Ident)
	if !ok || len(x.Args) != 1 {
		return false
	}
	obj, ok := c.info.Uses[id].(*types.Builtin)
	return ok && obj.Name() == "new"
}

// isConstantCall reports whether call is a constant expression — the
// checker evaluated it (len, cap, real, imag, complex over constant
// operands; unsafe.Sizeof, Alignof, Offsetof of a fixed layout) — or an
// unsafe layout operator over a type parameter, whose value the instantiated
// frame settles. Neither evaluates its operand nor runs a function, so every
// statement position treats it as an expression, the way a conversion is,
// rather than as a call to dispatch.
func (c *converter) isConstantCall(x *ast.CallExpr) bool {
	if c.info.Types[x].Value != nil {
		return true
	}
	sel, ok := ast.Unparen(x.Fun).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	obj, ok := c.info.Uses[sel.Sel].(*types.Builtin)
	if !ok || obj.Pkg() != types.Unsafe {
		return false
	}
	switch obj.Name() {
	case "Sizeof", "Alignof", "Offsetof":
		return true
	}
	return false
}

// valueType converts the go/types result of an expression back into the typed
// syntax representation. Go 1.27's new(v) needs both v and its defaulted type:
// the former initializes the fresh cell while the latter is the pointer's
// element identity. Positions are anchored at the original expression without
// inventing source text or reparsing the program.
func (c *converter) valueType(e ast.Expr) s.BashPPTypeExpr {
	typ := c.info.Types[e].Type
	if typ == nil {
		return nil
	}
	return c.checkedType(types.Default(typ), e, "inferred value type")
}

// checkedType converts a go/types type into the typed syntax representation,
// anchored at the source node it stands for. The checker's spelling of the
// type is reparsed rather than reconstructed so that every form typ already
// understands — instantiated named types, type parameters of the enclosing
// declaration, function and interface literals — comes out the same way it
// would have from the source.
func (c *converter) checkedType(typ types.Type, at ast.Node, what string) s.BashPPTypeExpr {
	typeName := c.typeString(typ)
	if typeName == "any" && types.Identical(typ, types.Universe.Lookup("any").Type()) {
		typeName = "interface{}"
	}
	parsed, err := parser.ParseExpr(typeName)
	if err != nil {
		c.fail(at, what)
		return nil
	}
	c.bindSyntheticChannelTypes(parsed, typ)
	c.syntheticPos = at.Pos()
	result := c.typ(parsed)
	c.syntheticPos = token.NoPos
	return result
}

// instanceTypeArgs spells the type arguments the checker instantiated a
// generic function callee with: the full list whether the call wrote them
// all, wrote a prefix and left the rest to inference, or wrote none. The
// runtime then binds the callee's type parameters from the call itself and
// never has to infer them from argument values — go/types' inference, which
// accepted the program, is the only inference in the pipeline. Generic types
// are not callees (a conversion is lowered before reaching the call form),
// so only a function instance qualifies.
func (c *converter) instanceTypeArgs(fun ast.Expr) []*s.BashPPTypeArg {
	base := fun
	for {
		switch v := base.(type) {
		case *ast.ParenExpr:
			base = v.X
			continue
		case *ast.IndexExpr:
			base = v.X
			continue
		case *ast.IndexListExpr:
			base = v.X
			continue
		}
		break
	}
	var id *ast.Ident
	switch v := base.(type) {
	case *ast.Ident:
		id = v
	case *ast.SelectorExpr:
		id = v.Sel
	default:
		return nil
	}
	inst, ok := c.info.Instances[id]
	if !ok || inst.TypeArgs == nil || inst.TypeArgs.Len() == 0 {
		return nil
	}
	if _, ok := inst.Type.(*types.Signature); !ok {
		return nil
	}
	out := make([]*s.BashPPTypeArg, 0, inst.TypeArgs.Len())
	for i := range inst.TypeArgs.Len() {
		typ := c.checkedType(inst.TypeArgs.At(i), id, "instantiated type argument")
		if typ == nil {
			return nil
		}
		out = append(out, &s.BashPPTypeArg{ArgType: typ})
	}
	return out
}

func (c *converter) stringValue(e ast.Expr) bool {
	typ := c.info.TypeOf(e)
	if typ == nil {
		return false
	}
	basic, ok := typ.Underlying().(*types.Basic)
	return ok && (basic.Kind() == types.String || basic.Kind() == types.UntypedString)
}
func (c *converter) typ(e ast.Expr) s.BashPPTypeExpr {
	if e == nil {
		return nil
	}
	switch x := e.(type) {
	case *ast.Ident:
		if c.info.ObjectOf(x) == types.Universe.Lookup("any") {
			return &s.BashPPInterfaceType{Interface: c.ident(x), Lbrace: c.pos(x.End() - 1), Rbrace: c.pos(x.End() - 1)}
		}
		if object := c.info.ObjectOf(x); object != nil {
			if _, parameter := object.Type().(*types.TypeParam); parameter {
				return &s.BashPPTypeParamType{Name: c.ident(x)}
			}
		}
		return &s.BashPPNamedType{Name: c.ident(x), LocalSync: c.localSyncType(c.info.TypeOf(x))}
	case *ast.SelectorExpr:
		if c.mappedPkgName(x.X) {
			return &s.BashPPNamedType{Name: c.ident(x.Sel)}
		}
		return &s.BashPPNamedType{Name: c.lit(x.Pos(), c.ident(x.X.(*ast.Ident)).Value+"."+x.Sel.Name), LocalSync: c.localSyncType(c.info.TypeOf(x))}
	case *ast.StarExpr:
		return &s.BashPPPointerType{Star: c.pos(x.Star), Element: c.typ(x.X)}
	case *ast.ArrayType:
		o := &s.BashPPCollectionType{Start: c.pos(x.Pos()), Lbrack: c.pos(x.Lbrack), Rbrack: c.pos(x.Elt.Pos() - 1), Element: c.typ(x.Elt), Kind: "slice"}
		if x.Len != nil {
			o.Kind = "array"
			o.Length = c.lit(x.Len.Pos(), c.arrayLengthText(x.Len))
			if _, ok := x.Len.(*ast.Ellipsis); ok {
				o.Kind = "inferred-array"
			}
		}
		return o
	case *ast.MapType:
		return &s.BashPPCollectionType{Kind: "map", Start: c.pos(x.Map), Lbrack: c.pos(x.Map + 3), Rbrack: c.pos(x.Value.Pos() - 1), Key: c.typ(x.Key), Element: c.typ(x.Value)}
	case *ast.StructType:
		return &s.BashPPStructType{Struct: c.lit(x.Struct, "struct"), Lbrace: c.pos(x.Fields.Opening), Rbrace: c.pos(x.Fields.Closing), Fields: c.fields(x.Fields, true)}
	case *ast.ChanType:
		dir := ""
		if x.Dir == ast.SEND {
			dir = "send"
		}
		if x.Dir == ast.RECV {
			dir = "recv"
		}
		return &s.BashPPChanType{Chan: c.pos(x.Begin), Arrow: c.pos(x.Arrow), Direction: dir, LocalDomain: c.localChannelType(c.info.TypeOf(x)), Element: c.typ(x.Value), Elem: c.lit(x.Value.Pos(), c.text(x.Value))}
	case *ast.FuncType:
		return &s.BashPPFuncType{Func: c.pos(x.Func), Lparen: c.pos(x.Params.Opening), Rparen: c.pos(x.Params.Closing), Params: c.fields(x.Params, false), Results: c.fields(x.Results, false)}
	case *ast.InterfaceType:
		out := &s.BashPPInterfaceType{Interface: c.lit(x.Interface, "interface"), Lbrace: c.pos(x.Methods.Opening), Rbrace: c.pos(x.Methods.Closing)}
		for _, f := range x.Methods.List {
			if ft, ok := f.Type.(*ast.FuncType); ok && len(f.Names) > 0 {
				m := &s.BashPPMethodSpec{Name: c.ident(f.Names[0]), Params: c.fields(ft.Params, false), Results: c.fields(ft.Results, false), Lparen: c.pos(ft.Params.Opening), Rparen: c.pos(ft.Params.Closing)}
				out.Methods = append(out.Methods, m)
				out.Elems = append(out.Elems, &s.BashPPInterfaceElem{Method: m})
			} else {
				out.Elems = append(out.Elems, &s.BashPPInterfaceElem{Embedded: c.typ(f.Type)})
			}
		}
		return out
	case *ast.IndexExpr:
		if named, ok := c.typ(x.X).(*s.BashPPNamedType); ok {
			named.TypeArgs = []*s.BashPPTypeArg{{ArgType: c.typ(x.Index)}}
			return named
		}
	case *ast.IndexListExpr:
		if o, ok := c.typ(x.X).(*s.BashPPNamedType); ok {
			for _, a := range x.Indices {
				o.TypeArgs = append(o.TypeArgs, &s.BashPPTypeArg{ArgType: c.typ(a)})
			}
			return o
		}
	case *ast.UnaryExpr:
		if x.Op == token.TILDE {
			return &s.BashPPApproxType{Tilde: c.pos(x.OpPos), Term: c.typ(x.X)}
		}
	case *ast.BinaryExpr:
		if x.Op == token.OR {
			return &s.BashPPUnionType{Terms: []s.BashPPTypeExpr{c.typ(x.X), c.typ(x.Y)}, Bars: []*s.Lit{c.lit(x.OpPos, "|")}}
		}
	case *ast.ParenExpr:
		return c.typ(x.X)
	}
	c.fail(e, fmt.Sprintf("type %T", e))
	return &s.BashPPNamedType{Name: c.lit(e.Pos(), "invalid")}
}
func (c *converter) fields(list *ast.FieldList, structure bool) []*s.BashPPField {
	if list == nil {
		return nil
	}
	var out []*s.BashPPField
	for _, f := range list.List {
		t := f.Type
		v := &s.BashPPField{Embedded: structure && len(f.Names) == 0}
		if e, ok := t.(*ast.Ellipsis); ok {
			v.Ellipsis = c.pos(e.Ellipsis)
			t = e.Elt
		}
		v.FieldType = c.lit(t.Pos(), c.text(t))
		v.FieldTypeExpr = c.typ(t)
		if f.Tag != nil {
			v.Tag = c.lit(f.Tag.Pos(), f.Tag.Value)
		}
		for _, n := range f.Names {
			v.Names = append(v.Names, c.ident(n))
		}
		out = append(out, v)
	}
	return out
}
func (c *converter) typeParams(list *ast.FieldList) []*s.BashPPTypeParam {
	if list == nil {
		return nil
	}
	var out []*s.BashPPTypeParam
	for _, f := range list.List {
		v := &s.BashPPTypeParam{Constraint: c.typ(f.Type)}
		for _, n := range f.Names {
			v.Names = append(v.Names, c.ident(n))
		}
		out = append(out, v)
	}
	return out
}
func (c *converter) function(f *ast.FuncDecl) *s.BashPPFuncDecl {
	o := &s.BashPPFuncDecl{Kw: c.lit(f.Pos(), "func"), Name: c.ident(f.Name), Params: c.fields(f.Type.Params, false), Results: c.fields(f.Type.Results, false), TypeParams: c.typeParams(f.Type.TypeParams), Lparen: c.pos(f.Type.Params.Opening), Rparen: c.pos(f.Type.Params.Closing), Body: c.block(f.Body)}
	if f.Type.Results != nil {
		o.ResLparen = c.pos(f.Type.Results.Opening)
		o.ResRparen = c.pos(f.Type.Results.Closing)
	}
	if f.Recv != nil {
		r := f.Recv.List[0]
		t := r.Type
		recv := &s.BashPPReceiver{Lparen: c.pos(f.Recv.Opening), Rparen: c.pos(f.Recv.Closing)}
		if len(r.Names) > 0 {
			recv.Name = c.ident(r.Names[0])
		}
		if ptr, ok := t.(*ast.StarExpr); ok {
			recv.Pointer = true
			t = ptr.X
		}
		named, ok := c.typ(t).(*s.BashPPNamedType)
		if target := c.receiverAliasTarget(t); ok && target != nil {
			named = &s.BashPPNamedType{Name: target}
		}
		if !ok {
			c.fail(t, fmt.Sprintf("method receiver type %T", t))
			recv.RecvType = c.lit(t.Pos(), c.text(t))
		} else {
			recv.RecvType = named.Name
			for _, arg := range named.TypeArgs {
				param, ok := arg.ArgType.(*s.BashPPTypeParamType)
				if !ok {
					c.fail(t, fmt.Sprintf("method receiver type argument %T", arg.ArgType))
					continue
				}
				recv.TypeParams = append(recv.TypeParams, param.Name)
			}
		}
		o.Receiver = recv
	}
	return o
}
func (c *converter) importSpec(g *ast.GenDecl, i *ast.ImportSpec) *s.BashPPImport {
	path, _ := strconv.Unquote(i.Path.Value)
	if c.importedPathIsMapped(i, path) {
		return nil
	}
	out := &s.BashPPImport{Site: s.StartImport, Class: s.ClassR, Kw: c.lit(g.TokPos, "import"), Path: &s.DblQuoted{Left: c.pos(i.Path.Pos()), Right: c.pos(i.Path.End() - 1), Parts: []s.WordPart{c.lit(i.Path.Pos()+1, path)}}}
	if i.Name != nil {
		if rename := c.renames[c.info.Defs[i.Name]]; rename != "" {
			out.Alias = c.lit(i.Name.Pos(), rename)
		} else {
			out.Alias = c.ident(i.Name)
		}
	} else if obj := c.info.Implicits[i]; c.renames[obj] != "" {
		out.Alias = c.lit(i.Path.Pos(), c.renames[obj])
	}
	return out
}

// importedPathIsMapped reports whether an import spec names a package in the
// explicit package map, through its binding when it has one and through the
// relative-import rule otherwise.
func (c *converter) importedPathIsMapped(i *ast.ImportSpec, path string) bool {
	if len(c.mapped) == 0 {
		return false
	}
	var obj types.Object
	if i.Name != nil {
		obj = c.info.Defs[i.Name]
	} else {
		obj = c.info.Implicits[i]
	}
	if pkgname, ok := obj.(*types.PkgName); ok {
		_, mapped := c.mapped[pkgname.Imported().Path()]
		return mapped
	}
	if c.resolveImport != nil {
		if resolved, err := c.resolveImport(path); err == nil {
			path = resolved
		}
	}
	_, mapped := c.mapped[path]
	return mapped
}
func (c *converter) typeDecl(g *ast.GenDecl, t *ast.TypeSpec) *s.BashPPDecl {
	out := &s.BashPPDecl{Site: s.StartTypeDecl, Kw: c.lit(g.TokPos, "type"), Name: c.ident(t.Name), DeclType: c.lit(t.Type.Pos(), c.text(t.Type)), DeclTypeExpr: c.typ(t.Type), Alias: t.Assign.IsValid(), TypeParams: c.typeParams(t.TypeParams), End_: c.pos(t.End())}
	if obj := c.info.Defs[t.Name]; obj != nil && obj.Pkg() != nil {
		out.GoTypeIdentity = &s.BashPPTypeIdentity{Name: obj.Name(), PackageName: obj.Pkg().Name(), PackagePath: obj.Pkg().Path()}
	}
	if st, ok := out.DeclTypeExpr.(*s.BashPPStructType); ok {
		out.DeclType.Value = "struct"
		out.StructFields = st.Fields
		out.Lbrace = st.Lbrace
		out.Rbrace = st.Rbrace
	}
	return out
}
func (c *converter) valueDecl(g *ast.GenDecl, v *ast.ValueSpec, n *ast.Ident, index int) *s.BashPPDecl {
	out := &s.BashPPDecl{Kw: c.lit(g.TokPos, g.Tok.String()), Name: c.ident(n), Site: s.StartVar, End_: c.pos(v.End())}
	// A Go-source `var x = 5` keeps its inferred type implicit: the
	// declaration is emitted as written when the value is a constant whose
	// form already spells the type Go infers (C3). Any other initializer —
	// a call, a composite, a mixed-kind constant — still carries the type,
	// which the interpreter needs to bind a package-level value it cannot
	// type from the word alone (`var a = f()`).
	if v.Type == nil && g.Tok == token.VAR && !c.constInferredAsWritten(v, n, index) {
		if obj := c.info.Defs[n]; obj != nil {
			typeName := c.typeString(obj.Type())
			// The synthetic AST has no go/types object bindings. Expand the
			// predeclared any alias so it keeps its interface shape instead
			// of becoming an unresolved named type during initialization.
			if typeName == "any" && types.Identical(obj.Type(), types.Universe.Lookup("any").Type()) {
				typeName = "interface{}"
			}
			parsed, err := parser.ParseExpr(typeName)
			if err != nil {
				c.fail(n, "inferred variable type")
			} else {
				out.DeclType = c.lit(n.Pos(), typeName)
				c.syntheticPos = n.Pos()
				out.DeclTypeExpr = c.typ(parsed)
				c.syntheticPos = token.NoPos
			}
		}
	}
	if g.Tok == token.CONST {
		out.Site = s.StartConst
	}
	if v.Type != nil {
		out.DeclType = c.lit(v.Type.Pos(), c.text(v.Type))
		out.DeclTypeExpr = c.typ(v.Type)
	} else if g.Tok == token.CONST {
		// A const without an explicit type but with a defined (named) type —
		// `const C2 = C1` where C1 has type E — inherits that type, so the
		// method set and identity travel with it. Emitting only the untyped
		// value drops E and the emitter rejects any later C2.P(). Spell the
		// inferred type; untyped and predeclared-basic consts keep the value
		// as written.
		if obj, ok := c.info.Defs[n].(*types.Const); ok {
			if _, named := obj.Type().(*types.Named); named {
				if typeExpr := c.checkedType(obj.Type(), n, "inferred constant type"); typeExpr != nil {
					out.DeclType = c.lit(n.Pos(), c.typeString(obj.Type()))
					out.DeclTypeExpr = typeExpr
				}
			}
		}
	}
	if g.Tok == token.CONST {
		// C3: a constant whose value is written out in a form the
		// interpreter evaluates exactly — literals, constant names and
		// operators, no iota or call — is emitted as written, so the
		// generated Go is the source and an untyped rune keeps its kind.
		if c.constAsWritten(v, index) {
			out.Init = []*s.Word{c.word(v.Values[index])}
			out.InitExpr = c.expr(v.Values[index])
			return out
		}
		// A const outside a group — a function-local declaration, or a
		// single top-level spec — whose initializer selects through an
		// imported package (unsafe.Sizeof, math.MaxInt8) keeps its source
		// form as the group path does: folding it would erase the file's only
		// use of the import in the generated Go. A spec that also mentions
		// iota stays on the folded path: this per-spec site has no group
		// carrier, so a textual iota would re-evaluate at position 0.
		if len(v.Values) == len(v.Names) && index < len(v.Values) &&
			c.usesImportedPackage(v.Values[index]) && !c.usesIota(v.Values[index]) {
			out.Init = []*s.Word{c.word(v.Values[index])}
			if obj, ok := c.info.Defs[n].(*types.Const); ok {
				// Keep the source expression for compiled output, but carry the
				// checker's exact value to the interpreter. An imported named
				// constant is not a dependency runtime read.
				out.InitExpr = c.constantValueExpr(n.Pos(), obj.Val())
			}
			return out
		}
		if obj, ok := c.info.Defs[n].(*types.Const); ok {
			value := obj.Val().ExactString()
			out.InitExpr = c.constantValueExpr(n.Pos(), obj.Val())
			// Init is the WRITTEN carrier: compiled Go re-emits the source
			// expression and lets gc evaluate it, so the folded value must
			// never replace it — ExactString spells a rational (`1/3 +
			// 2/3i`) that Go would parse as `1/3 + 2/(3i)`. Only a constant
			// with no written initializer (an implicit repetition) carries
			// its value here.
			if len(v.Values) == len(v.Names) && index < len(v.Values) {
				out.Init = []*s.Word{c.word(v.Values[index])}
			} else {
				out.Init = []*s.Word{{Parts: []s.WordPart{c.lit(n.Pos(), value)}}}
			}
		}
		return out
	}
	if len(v.Values) > 0 {
		if len(v.Values) != len(v.Names) {
			c.fail(v, "tuple variable declaration")
			return out
		}
		out.Init = []*s.Word{c.word(v.Values[index])}
		out.InitExpr = c.expr(v.Values[index])
		if c.usesImportedPackage(v.Values[index]) {
			if tv := c.info.Types[v.Values[index]]; tv.Value != nil {
				// The written initializer is retained above for native lowering;
				// interpreted initialization consumes the exact checked constant.
				out.InitExpr = c.constantValueExpr(n.Pos(), tv.Value)
			}
		}
	}
	return out
}

func (c *converter) constGroup(g *ast.GenDecl) *s.BashPPConstGroup {
	out := &s.BashPPConstGroup{Kw: c.lit(g.TokPos, "const"), Lparen: c.pos(g.Lparen), Rparen: c.pos(g.Rparen)}
	if !g.Lparen.IsValid() {
		out.Lparen = c.pos(g.TokPos + token.Pos(len("const")))
		out.Rparen = c.pos(g.End())
	}
	for specIndex, raw := range g.Specs {
		v := raw.(*ast.ValueSpec)
		for i, name := range v.Names {
			// iota is the ConstSpec's index within the group, so every name
			// of a multi-name spec (`abit, amask = 1<<iota, 1<<iota-1`)
			// shares one value; the flattened per-name position must not
			// advance it.
			spec := &s.BashPPConstSpec{Name: c.ident(name), ImplicitInit: len(v.Values) == 0, Iota: uint32(specIndex)}
			if v.Type != nil {
				spec.DeclType = c.lit(v.Type.Pos(), c.text(v.Type))
				spec.DeclTypeExpr = c.typ(v.Type)
			} else if obj, ok := c.info.Defs[name].(*types.Const); ok {
				// An implicit repetition of `_ token = iota` with token an
				// alias of a defined type has that defined type.
				typ := obj.Type()
				if named, ok := types.Unalias(typ).(*types.Named); ok {
					typ = named
				}
				if _, named := typ.(*types.Named); named {
					if typeExpr := c.checkedType(typ, name, "inferred constant type"); typeExpr != nil {
						spec.DeclType = c.lit(name.Pos(), c.typeString(typ))
						spec.DeclTypeExpr = typeExpr
					}
				}
			}
			if len(v.Values) > 0 {
				if len(v.Values) != len(v.Names) || i >= len(v.Values) {
					c.fail(v, "constant declaration")
					continue
				}
				spec.Init = []*s.Word{c.word(v.Values[i])}
			}
			// go/types has already evaluated every valid constant in package
			// dependency order. Carry that exact value into the interpreter so
			// forward references, repeated iota expressions, and huge rationals
			// do not depend on source-order runtime cells. Init above remains the
			// written expression for compiled output and import accounting.
			if obj, ok := c.info.Defs[name].(*types.Const); ok {
				if len(v.Values) == len(v.Names) && i < len(v.Values) &&
					c.constAsWritten(v, i) && !c.usesImportedPackage(v.Values[i]) &&
					!c.constHasForwardReference(v.Values[i], name.Pos()) {
					spec.InitExpr = c.expr(v.Values[i])
				} else {
					spec.InitExpr = c.constantValueExpr(name.Pos(), obj.Val())
				}
			}
			out.Specs = append(out.Specs, spec)
		}
	}
	return out
}

// constHasForwardReference reports a dependency whose declaration follows the
// current ConstSpec. Such dependencies are valid Go, but cannot be recovered
// from source-ordered interpreter cells; go/types' checked value must carry
// them instead.
func (c *converter) constHasForwardReference(e ast.Expr, declaration token.Pos) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return !found
		}
		obj, ok := c.info.Uses[id].(*types.Const)
		if ok && obj.Pos().IsValid() && obj.Pos() > declaration {
			found = true
		}
		return !found
	})
	return found
}

// usesImportedPackage reports whether e selects through an imported package
// name (unsafe.Sizeof, pkg.Const): folding such a constant would leave the
// import unused in the generated Go.
func (c *converter) usesImportedPackage(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if found {
			return false
		}
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				if _, isPkg := c.info.Uses[id].(*types.PkgName); isPkg {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// constGroupUsesImports reports whether any initializer in a const group
// selects through an imported package name.
func (c *converter) constGroupUsesImports(g *ast.GenDecl) bool {
	for _, raw := range g.Specs {
		if v, ok := raw.(*ast.ValueSpec); ok {
			for _, value := range v.Values {
				if c.usesImportedPackage(value) {
					return true
				}
			}
		}
	}
	return false
}

func (c *converter) expr(e ast.Expr) s.BashPPExpr {
	if e == nil {
		return nil
	}
	result := c.exprValue(e)
	if c.rawConstantExpr {
		return result
	}
	// go/types records the concrete type only at a constant's contextual
	// conversion/defaulting boundary. Inner untyped operands remain exact.
	// Preserve that boundary, particularly float and rune defaults in any.
	// C3: the boundary is spelled only when the constant's own form does
	// not reveal its kind — a call, a foreign or by-value materialized
	// constant, mixed-kind arithmetic defaulting into any. A literal, a
	// constant name or same-kind arithmetic is left as written: the
	// interpreter's exact scalar evaluation reproduces the kind from the
	// form, and the generated Go stays the source.
	if tv := c.info.Types[e]; tv.Value != nil {
		if named, ok := tv.Type.(*types.Named); ok {
			if basic, scalar := named.Underlying().(*types.Basic); scalar && basic.Info()&(types.IsBoolean|types.IsInteger|types.IsFloat|types.IsComplex|types.IsString) != 0 {
				// Context can convert an untyped constant directly to a defined
				// scalar type without an explicit conversion in the source, as in
				// time.Sleep(1e7). Preserve that checked boundary so the bridge
				// sends an integer time.Duration rather than the literal's float
				// spelling and lets reflect reject it at the dependency boundary.
				// A boundary the use site cannot spell — an unexported type of
				// another package, or a name a local declaration shadows there —
				// is left as written: the checked source already converts it.
				if !c.namedSpellable(named) {
					return result
				}
				name := c.typeString(named)
				if c.spellingShadowed(name, e.Pos(), named.Origin().Obj()) {
					return result
				}
				return &s.BashPPConvertExpr{
					ConvType:     c.lit(e.Pos(), name),
					ConvTypeExpr: c.checkedType(named, e, "constant conversion type"),
					Lparen:       c.pos(e.Pos()),
					Rparen:       c.pos(e.End() - 1),
					X:            result,
				}
			}
		}
		if basic, ok := tv.Type.(*types.Basic); ok && basic.Info()&types.IsUntyped == 0 {
			// A package that redeclares this predeclared type name as a
			// non-type has no usable `basic.Name()` conversion; emitting one
			// calls the redeclared object. Leave the constant as written.
			if c.shadowedBuiltins[basic.Name()] || c.spellingShadowed(basic.Name(), e.Pos(), types.Universe.Lookup(basic.Name())) {
				return result
			}
			if c.constAsWrittenIn(e, basic) {
				return result
			}
			return &s.BashPPConvertExpr{ConvType: c.lit(e.Pos(), basic.Name()), Lparen: c.pos(e.Pos()), Rparen: c.pos(e.End() - 1), X: result}
		}
	}
	return result
}

// namedSpellable reports whether a named type can be spelled outside its
// declaring package: exported, or declared by the converter's own package or
// a package linked into the same flat file (whose names are renamed).
func (c *converter) namedSpellable(named *types.Named) bool {
	obj := named.Obj()
	if obj.Pkg() == nil || obj.Exported() || obj.Pkg().Path() == c.packagePath {
		return true
	}
	_, mapped := c.mapped[obj.Pkg().Path()]
	return mapped
}

// spellingShadowed reports whether the leading identifier of a type spelling
// (a predeclared or package-level type name, or a package qualifier) resolves
// at pos to a different object than the one the spelling means, because a
// local declaration of the same name is in scope there: `rune(x)` inside a
// loop whose variable is named rune calls the variable. Hygienic names the
// lowering minted cannot be shadowed. want, when known, is the type name an
// unqualified spelling must resolve to.
func (c *converter) spellingShadowed(spelling string, pos token.Pos, want types.Object) bool {
	if c.currentFile == nil || c.info == nil || !pos.IsValid() {
		return false
	}
	head := spelling
	for head != "" && (head[0] == '*' || head[0] == '[' || head[0] == ']') {
		head = head[1:]
	}
	end := strings.IndexFunc(head, func(r rune) bool {
		return !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r))
	})
	qualified := false
	if end >= 0 {
		qualified = head[end] == '.'
		head = head[:end]
	}
	if head == "" || strings.HasPrefix(head, c.prefix) {
		return false
	}
	scope := c.info.Scopes[c.currentFile]
	if scope == nil {
		return false
	}
	// A name no scope inside the file declares cannot be shadowed by a local at
	// any position: the descent through inner scopes contributes nothing, so
	// inner.LookupParent(head, pos) resolves head to exactly the object the file
	// scope resolves it to. Skip the per-reference Innermost tree walk in that
	// (overwhelmingly common) case and look it up on the file scope directly.
	inner := scope
	if c.fileInnerNames()[head] {
		inner = scope.Innermost(pos)
		if inner == nil {
			return false
		}
	}
	_, obj := inner.LookupParent(head, pos)
	if obj == nil {
		return false
	}
	if qualified {
		_, isPkg := obj.(*types.PkgName)
		return !isPkg
	}
	if want == nil {
		_, isType := obj.(*types.TypeName)
		return !isType
	}
	return obj != want
}

// fileInnerNames returns the set of identifiers declared in any scope strictly
// inside the current file's scope (every function, block, case and other body
// scope, but not the file or package scope). It is built once per file and
// memoized: spellingShadowed consults it to decide whether a given spelling
// head could possibly be shadowed by a local declaration before paying for a
// types.Scope.Innermost descent.
func (c *converter) fileInnerNames() map[string]bool {
	if c.innerNamesFile == c.currentFile && c.innerNames != nil {
		return c.innerNames
	}
	names := map[string]bool{}
	if fileScope := c.info.Scopes[c.currentFile]; fileScope != nil {
		var collect func(scope *types.Scope)
		collect = func(scope *types.Scope) {
			for i := 0; i < scope.NumChildren(); i++ {
				child := scope.Child(i)
				for _, name := range child.Names() {
					names[name] = true
				}
				collect(child)
			}
		}
		// The file scope's own names (imports) are not inner declarations; a
		// package-qualifier or type spelling that resolves to one is handled by
		// the file-scope lookup itself, so only descendant scopes are collected.
		collect(fileScope)
	}
	c.innerNamesFile = c.currentFile
	c.innerNames = names
	return names
}

// constSpec locates a constant's declaring spec and the index of its name.
type constSpec struct {
	spec  *ast.ValueSpec
	index int
}

// constKind reports the untyped kind a constant expression has as written —
// the kind the interpreter's exact scalar evaluation reproduces from the
// source form — and false when the form does not reveal it: a call, a
// conversion, a selector into a linked or native package, a constant this
// package materialized by value, or arithmetic that mixes kinds and so
// depends on Go's defaulting.
func (c *converter) constKind(e ast.Expr) (types.BasicKind, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		switch x.Kind {
		case token.INT:
			return types.UntypedInt, true
		case token.FLOAT:
			return types.UntypedFloat, true
		case token.CHAR:
			return types.UntypedRune, true
		case token.STRING:
			return types.UntypedString, true
		}
	case *ast.ParenExpr:
		return c.constKind(x.X)
	case *ast.UnaryExpr:
		if x.Op == token.NOT {
			return types.UntypedBool, true
		}
		if x.Op == token.SUB || x.Op == token.ADD || x.Op == token.XOR {
			return c.constKind(x.X)
		}
	case *ast.BinaryExpr:
		left, ok := c.constKind(x.X)
		if !ok {
			return 0, false
		}
		switch x.Op {
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ, token.LAND, token.LOR:
			if _, ok := c.constKind(x.Y); ok {
				return types.UntypedBool, true
			}
			return 0, false
		case token.SHL, token.SHR:
			if _, ok := c.constKind(x.Y); ok {
				return left, true
			}
			return 0, false
		}
		right, ok := c.constKind(x.Y)
		if !ok {
			return 0, false
		}
		if right == left {
			return left, true
		}
		// One typed operand types the whole expression.
		if types.Typ[left].Info()&types.IsUntyped == 0 {
			return left, true
		}
		if types.Typ[right].Info()&types.IsUntyped == 0 {
			return right, true
		}
		// Untyped numeric kinds mix by rank: int < rune < float < complex.
		if rank := untypedRank(left); rank > 0 && untypedRank(right) > 0 {
			if untypedRank(right) > rank {
				return right, true
			}
			return left, true
		}
	case *ast.Ident:
		obj, ok := c.info.Uses[x].(*types.Const)
		if !ok {
			return 0, false
		}
		basic, ok := obj.Type().(*types.Basic)
		if !ok {
			return 0, false
		}
		if basic.Info()&types.IsUntyped == 0 {
			return basic.Kind(), true
		}
		if obj == types.Universe.Lookup("iota") || obj == types.Universe.Lookup("true") || obj == types.Universe.Lookup("false") {
			return basic.Kind(), true
		}
		// A named rune constant reaches the interpreter as an exact
		// integer whether its declaration is written `'a'` or
		// materialized as 97: the constant cell has no rune kind. The
		// use site keeps its conversion.
		if basic.Kind() == types.UntypedRune {
			return 0, false
		}
		if _, ok := c.constSpecOf(obj); !ok {
			// Declared by another package: whether its value reaches the
			// interpreter with the kind of its form is not this
			// converter's to know.
			return 0, false
		}
		// Declared here: written out or materialized by value, an int,
		// float, string or bool keeps its kind.
		return basic.Kind(), true
	}
	return 0, false
}

// constAsWrittenIn reports whether a constant expression of typed basic type
// typ may be emitted without the conversion spelling that type: its kind is
// evident from its form and the interpreter reproduces typ from that form in
// this context. That is the case when typ is the kind's own default (an int
// literal as int, a float as float64, a rune as rune, a string, a bool), and
// for an int kind in a floating-point context, which the interpreter's exact
// scalar arithmetic widens (`f != 0`, `2 * g`). Any other context — a rune
// into a byte, an int beyond int64 into a uint64, a float into float32, a
// constant shifted by a variable count — keeps the conversion.
func (c *converter) constAsWrittenIn(e ast.Expr, typ *types.Basic) bool {
	if e == c.shiftOperand {
		c.shiftOperand = nil
		return false
	}
	kind, evident := c.constKind(e)
	if !evident {
		return false
	}
	// The scalar carrier stores rune literals as constant.Int too. Their
	// written syntax cannot preserve default int32 identity across a native
	// argument boundary. Keep the checker's contextual conversion; inner
	// untyped expressions still retain arbitrary-precision constant values.
	if kind == types.UntypedRune {
		return false
	}
	if types.Identical(types.Default(types.Typ[kind]), typ) {
		return true
	}
	if kind == types.UntypedInt && typ.Info()&types.IsFloat != 0 {
		if v := c.info.Types[e].Value; v != nil {
			_, exact := constant.Int64Val(constant.ToInt(v))
			return exact
		}
	}
	return false
}

// untypedRank orders the untyped numeric kinds as Go's constant expression
// rules promote them; 0 for a kind that does not mix.
func untypedRank(kind types.BasicKind) int {
	switch kind {
	case types.UntypedInt:
		return 1
	case types.UntypedRune:
		return 2
	case types.UntypedFloat:
		return 3
	case types.UntypedComplex:
		return 4
	}
	return 0
}

// constSpecOf finds the spec declaring a constant of this package, building
// the cache from every const declaration, package-level or local, on first
// use.
func (c *converter) constSpecOf(obj *types.Const) (constSpec, bool) {
	if c.constSpecs == nil {
		c.constSpecs = map[*types.Const]constSpec{}
		for _, f := range c.files {
			ast.Inspect(f, func(n ast.Node) bool {
				gd, ok := n.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					return true
				}
				for _, raw := range gd.Specs {
					v := raw.(*ast.ValueSpec)
					for i, name := range v.Names {
						if obj, ok := c.info.Defs[name].(*types.Const); ok {
							c.constSpecs[obj] = constSpec{spec: v, index: i}
						}
					}
				}
				return true
			})
		}
	}
	decl, ok := c.constSpecs[obj]
	return decl, ok
}

// constAsWritten reports whether the constant declared at spec.Names[index]
// is emitted with its value as written rather than materialized by value:
// it has an explicit value of evident kind that mentions no iota.
func (c *converter) constAsWritten(spec *ast.ValueSpec, index int) bool {
	if len(spec.Values) != len(spec.Names) || index >= len(spec.Values) {
		return false
	}
	value := spec.Values[index]
	if c.usesIota(value) {
		return false
	}
	// The RHS of `iota = iota` (go.dev/issue/53585) resolves to the very
	// constant being declared. Its written spelling names nothing that exists
	// before this declaration, so only the folded value can carry it.
	if def := c.info.Defs[spec.Names[index]]; def != nil {
		self := false
		ast.Inspect(value, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && c.info.Uses[id] == def {
				self = true
			}
			return !self
		})
		if self {
			return false
		}
	}
	_, evident := c.constKind(value)
	return evident
}

// usesIota reports whether e mentions the predeclared iota.
func (c *converter) usesIota(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && c.info.Uses[id] == types.Universe.Lookup("iota") {
			found = true
		}
		return !found
	})
	return found
}

// constInferredAsWritten reports whether a `var` spec without a type may be
// emitted without its inferred type: the value at index is an int, float or
// string literal whose default is the very type Go infers, so the
// declaration as written already spells it for the interpreter and the
// generated Go alike. The interpreter binds a package-level value without a
// declared type from its word, which only such a literal survives: a rune
// literal would read as a quoted string and an expression as its text.
func (c *converter) constInferredAsWritten(spec *ast.ValueSpec, n *ast.Ident, index int) bool {
	if len(spec.Values) != len(spec.Names) || index >= len(spec.Values) {
		return false
	}
	lit, ok := spec.Values[index].(*ast.BasicLit)
	if !ok || (lit.Kind != token.INT && lit.Kind != token.FLOAT && lit.Kind != token.STRING) {
		return false
	}
	kind, _ := c.constKind(lit)
	obj := c.info.Defs[n]
	return obj != nil && types.Identical(types.Default(types.Typ[kind]), obj.Type())
}

func (c *converter) exprValue(e ast.Expr) s.BashPPExpr {
	if e == nil {
		return nil
	}
	switch x := e.(type) {
	case *ast.FuncLit:
		return c.funlit(x)
	case *ast.BasicLit:
		return &s.BashPPBasicLit{Kind: x.Kind.String(), Value: c.lit(x.ValuePos, x.Value)}
	case *ast.Ident:
		if value := c.genericFuncValue(x); value != nil {
			return value
		}
		if pkg, sel, ok := c.qualifiedIdent(x); ok {
			return &s.BashPPSelectorExpr{X: &s.BashPPIdent{Name: pkg}, Dot: c.pos(x.Pos()), Sel: sel, FuncType: c.functionValueType(x)}
		}
		return &s.BashPPIdent{Name: c.ident(x)}
	case *ast.ParenExpr:
		return &s.BashPPParenExpr{Lparen: c.pos(x.Lparen), Rparen: c.pos(x.Rparen), X: c.expr(x.X)}
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			return &s.BashPPAddressExpr{Amp: c.pos(x.OpPos), X: c.expr(x.X)}
		}
		return &s.BashPPUnaryExpr{Op: c.lit(x.OpPos, x.Op.String()), X: c.expr(x.X)}
	case *ast.StarExpr:
		return &s.BashPPDerefExpr{Star: c.pos(x.Star), X: c.expr(x.X)}
	case *ast.BinaryExpr:
		if (x.Op == token.SHL || x.Op == token.SHR) && c.info.Types[x].Value == nil {
			// A constant shifted by a variable count takes the type of
			// its context, which the form does not reveal (`1 << n` is
			// an int, a uint64, ... by use); the interpreter widens the
			// result from the spelled type. Keep that spelling.
			c.shiftOperand = x.X
		}
		return &s.BashPPBinaryExpr{X: c.expr(x.X), Op: c.lit(x.OpPos, x.Op.String()), Y: c.expr(x.Y)}
	case *ast.SelectorExpr:
		if value := c.genericFuncValue(x); value != nil {
			return value
		}
		if value := c.typeParamMethodExpr(x); value != nil {
			return value
		}
		if c.mappedPkgName(x.X) {
			return &s.BashPPIdent{Name: c.ident(x.Sel)}
		}
		return c.selectorValue(x, x)
	case *ast.IndexExpr:
		if value := c.genericFuncValue(x); value != nil {
			return value
		}
		return &s.BashPPIndexExpr{GoString: c.stringValue(x.X), X: c.expr(x.X), Lbrack: c.pos(x.Lbrack), Rbrack: c.pos(x.Rbrack), Index: c.expr(x.Index)}
	case *ast.IndexListExpr:
		if value := c.genericFuncValue(x); value != nil {
			return value
		}
	case *ast.SliceExpr:
		return &s.BashPPSliceExpr{GoString: c.stringValue(x.X), X: c.expr(x.X), Lbrack: c.pos(x.Lbrack), Rbrack: c.pos(x.Rbrack), Low: c.expr(x.Low), High: c.expr(x.High), Max: c.expr(x.Max), Colon: c.pos(x.Lbrack + 1), SecondColon: func() s.Pos {
			if x.Slice3 {
				return c.pos(x.Max.Pos() - 1)
			}
			return s.Pos{}
		}()}
	case *ast.CompositeLit:
		out := &s.BashPPCompositeLit{LitType: c.typ(x.Type), Lbrace: c.pos(x.Lbrace), Rbrace: c.pos(x.Rbrace)}
		for _, el := range x.Elts {
			v := &s.BashPPCompositeElem{}
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				v.Key = c.expr(kv.Key)
				v.Value = c.expr(kv.Value)
				v.Colon = c.pos(kv.Colon)
			} else {
				v.Value = c.expr(el)
			}
			out.Elems = append(out.Elems, v)
		}
		return out
	case *ast.TypeAssertExpr:
		out := &s.BashPPTypeAssertExpr{X: c.expr(x.X), Dot: c.pos(x.Lparen - 1), Lparen: c.pos(x.Lparen), Rparen: c.pos(x.Rparen), Assert: c.typ(x.Type)}
		if x.Type == nil {
			out.TypeToken = c.lit(x.Lparen+1, "type")
		}
		return out
	case *ast.CallExpr:
		if c.isNewType(x) {
			id := ast.Unparen(x.Fun).(*ast.Ident)
			return &s.BashPPNewExpr{New: c.ident(id), Lparen: c.pos(x.Lparen), Rparen: c.pos(x.Rparen), AllocType: c.typ(x.Args[0])}
		}
		if c.isNewBuiltin(x) {
			id := ast.Unparen(x.Fun).(*ast.Ident)
			return &s.BashPPNewExpr{New: c.ident(id), Lparen: c.pos(x.Lparen), Rparen: c.pos(x.Rparen), AllocType: c.valueType(x.Args[0]), Init: c.expr(x.Args[0])}
		}
		if c.info.Types[x.Fun].IsType() && len(x.Args) == 1 {
			var typeLit *s.Lit
			if id, ok := x.Fun.(*ast.Ident); ok {
				typeLit = c.ident(id)
			} else {
				typeLit = c.lit(x.Fun.Pos(), c.text(x.Fun))
				typeLit.ValueEnd = c.pos(x.Fun.End())
			}
			value := c.info.Types[x.Args[0]].Value
			if value != nil && value.Kind() == constant.Int && constant.Sign(value) == 0 && types.Identical(c.info.TypeOf(x.Fun), types.Typ[types.UnsafePointer]) {
				// unsafe.Pointer(uintptr(0)) is the nil unsafe.Pointer: the
				// type checker has proven the operand the integer constant 0,
				// so it lowers to the same typed nil as unsafe.Pointer(nil).
				// No other integer spells an address here; a nonzero or
				// runtime uintptr keeps its conversion and is refused (or
				// authenticated) by the interpreter.
				nilIdent := &s.BashPPIdent{Name: c.lit(x.Args[0].Pos(), "nil")}
				return &s.BashPPConvertExpr{ConvType: typeLit, ConvTypeExpr: c.typ(x.Fun), Lparen: c.pos(x.Lparen), Rparen: c.pos(x.Rparen), X: nilIdent}
			}
			stringConstant := value != nil && value.Kind() == constant.String
			return &s.BashPPConvertExpr{GoStringConstant: stringConstant, ConvType: typeLit, ConvTypeExpr: c.typ(x.Fun), Lparen: c.pos(x.Lparen), Rparen: c.pos(x.Rparen), X: c.expr(x.Args[0])}
		}
		return c.call(x)
	}
	c.fail(e, fmt.Sprintf("expression %T", e))
	return &s.BashPPIdent{Name: c.lit(e.Pos(), "invalid")}
}
func (c *converter) funlit(x *ast.FuncLit) *s.BashPPFuncLit {
	return &s.BashPPFuncLit{Kw: c.lit(x.Type.Func, "func"), Params: c.fields(x.Type.Params, false), Results: c.fields(x.Type.Results, false), Lparen: c.pos(x.Type.Params.Opening), Rparen: c.pos(x.Type.Params.Closing), Body: c.block(x.Body)}
}
func (c *converter) call(x *ast.CallExpr) *s.BashPPCall {
	out := &s.BashPPCall{GoRuntime: c.info.Types[x].Value == nil, Lparen: c.pos(x.Lparen), Rparen: c.pos(x.Rparen), Ellipsis: c.pos(x.Ellipsis), ResultFuncType: c.functionValueType(x), ResultTypes: c.callResultTypes(x)}
	out.ExclusiveSliceArgs = c.exclusiveSliceArgs(x)
	isInstantiation := func(e ast.Expr) bool {
		var base ast.Expr
		var args []ast.Expr
		switch v := e.(type) {
		case *ast.IndexExpr:
			base, args = v.X, []ast.Expr{v.Index}
		case *ast.IndexListExpr:
			base, args = v.X, v.Indices
		default:
			return false
		}
		var id *ast.Ident
		switch v := base.(type) {
		case *ast.Ident:
			id = v
		case *ast.SelectorExpr:
			id = v.Sel
		}
		if id != nil {
			if _, ok := c.info.Instances[id]; ok {
				return true
			}
		}
		baseType := c.info.Types[base]
		if !baseType.IsType() {
			if _, ok := baseType.Type.(*types.Signature); !ok {
				return false
			}
		}
		for _, arg := range args {
			if !c.info.Types[arg].IsType() {
				return false
			}
		}
		return true
	}
	var simple func(ast.Expr) bool
	simple = func(e ast.Expr) bool {
		switch v := e.(type) {
		case *ast.Ident, *ast.FuncLit:
			return true
		case *ast.SelectorExpr:
			return simple(v.X)
		case *ast.IndexExpr:
			return isInstantiation(v) && simple(v.X)
		case *ast.IndexListExpr:
			return isInstantiation(v) && simple(v.X)
		}
		return false
	}
	if selector, typeArgs, ok := c.instantiatedMethodValue(x.Fun); ok {
		// A generic method selected from a computed receiver is still a
		// method call, not an index operation on a function value. Keep the
		// receiver expression in the existing computed-callee carrier and
		// put the checker's complete inferred/explicit method arguments on
		// the call. This also evaluates the receiver before the arguments.
		out.CalleeExpr = c.selectorValue(selector, x.Fun)
		out.TypeArgs = typeArgs
	} else if _, _, ok := c.instantiatedMethodExpression(x.Fun); ok {
		// A method expression is an unbound function whose first argument is
		// its receiver. genericFuncValue builds precisely that forwarding
		// closure from the checker's instantiated signature.
		out.FuncLit, _ = c.genericFuncValue(x.Fun).(*s.BashPPFuncLit)
	} else if closure, receiver, ok := c.computedImportedMethodCall(x); ok {
		// Bind the computed receiver as the forwarding closure's first
		// argument. Go evaluates it once, before the written arguments.
		out.FuncLit = closure
		out.Args = append(out.Args, c.word(receiver))
		out.ArgExprs = append(out.ArgExprs, c.expr(receiver))
		out.ExclusiveSliceArgs = append([]bool{false}, out.ExclusiveSliceArgs...)
	} else if simple(x.Fun) {
		c.callee(out, x.Fun)
	} else {
		out.CalleeExpr = c.expr(x.Fun)
	}
	for i, a := range x.Args {
		// A type operand — make's and new's first argument — is a type, not a
		// value; it is lowered as ArgType whatever its form (a name, a struct,
		// array or interface literal), never as an expression.
		if i == 0 && len(out.Fun) == 1 && c.info.Types[a].IsType() {
			out.ArgType = c.typ(a)
			out.Args = append(out.Args, c.word(a))
			out.ArgExprs = append(out.ArgExprs, nil)
			continue
		}
		out.Args = append(out.Args, c.word(a))
		out.ArgExprs = append(out.ArgExprs, c.expr(a))
	}
	return out
}

// computedImportedMethodCall lowers a method selected from a computed value
// of an imported named type through a typed forwarding closure. The closure
// receives the already-evaluated receiver, preventing classification and
// invocation from re-running expressions such as xs[index()].Pos().
func (c *converter) computedImportedMethodCall(call *ast.CallExpr) (*s.BashPPFuncLit, ast.Expr, bool) {
	selector, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return nil, nil, false
	}
	selection := c.info.Selections[selector]
	if selection == nil || selection.Kind() != types.MethodVal {
		return nil, nil, false
	}
	receiverType := selection.Recv()
	if pointer, ok := receiverType.(*types.Pointer); ok {
		receiverType = pointer.Elem()
	}
	named, ok := types.Unalias(receiverType).(*types.Named)
	if !ok || !c.bridgedType(named) {
		return nil, nil, false
	}
	// A plain name or selector chain is already read once by the normal call
	// carrier. This handoff is only needed when evaluating the receiver has
	// observable work of its own.
	var simpleReceiver func(ast.Expr) bool
	simpleReceiver = func(expr ast.Expr) bool {
		switch x := ast.Unparen(expr).(type) {
		case *ast.Ident:
			return true
		case *ast.SelectorExpr:
			return simpleReceiver(x.X)
		}
		return false
	}
	if simpleReceiver(selector.X) {
		return nil, nil, false
	}
	index, indexed := ast.Unparen(selector.X).(*ast.IndexExpr)
	// An index into a computed result already uses the computed-callee path.
	// Wrapping it here would execute its producer again during method lookup.
	if !indexed || !simpleReceiver(index.X) {
		return nil, nil, false
	}
	// A pointer method selected on an addressable value operates on the
	// operand itself (Go takes its address implicitly): hand the closure the
	// address, never a copy (a copied sync.Pool loses what Put stores).
	receiverExpr := selector.X
	receiverOf := c.info.TypeOf(selector.X)
	if fn, ok := selection.Obj().(*types.Func); ok && !selection.Indirect() {
		if recv := fn.Signature().Recv(); recv != nil {
			if _, byPointer := recv.Type().(*types.Pointer); byPointer {
				receiverOf = types.NewPointer(receiverOf)
				address := &ast.UnaryExpr{OpPos: selector.X.Pos(), Op: token.AND, X: selector.X}
				c.info.Types[address] = types.TypeAndValue{Type: receiverOf}
				receiverExpr = address
			}
		}
	}
	signature, _ := c.checkedType(c.info.TypeOf(call.Fun), call.Fun, "computed imported method type").(*s.BashPPFuncType)
	receiver := c.checkedType(receiverOf, selector.X, "computed imported method receiver type")
	if signature == nil || receiver == nil {
		return nil, nil, false
	}
	copySignature := *signature
	copySignature.Params = append([]*s.BashPPField{{FieldType: c.lit(selector.X.Pos(), c.typeString(receiverOf)), FieldTypeExpr: receiver}}, signature.Params...)
	closure, _ := c.forwardingClosure(call.Fun, &copySignature, func(inner *s.BashPPCall, args []*s.Lit) {
		inner.Fun = []*s.Lit{args[0], c.ident(selector.Sel)}
		inner.Args = inner.Args[1:]
		inner.ArgExprs = inner.ArgExprs[1:]
	}).(*s.BashPPFuncLit)
	return closure, receiverExpr, true
}

// exclusiveSliceArgs proves the narrow ownership shape used by generated
// test drivers: a package variable initialized directly from a fresh slice
// literal and used exactly once in the entire package, as this bare argument.
// Counting the checker object rather than spellings rejects aliases, address
// taking, closure capture, reassignment, append/subslice operands, and a
// duplicate argument. Parentheses do not create an alias.
func (c *converter) exclusiveSliceArgs(call *ast.CallExpr) []bool {
	var proven []bool
	for i, arg := range call.Args {
		id, ok := ast.Unparen(arg).(*ast.Ident)
		if !ok {
			continue
		}
		obj, ok := c.info.Uses[id].(*types.Var)
		if !ok || obj.Pkg() == nil || obj.Parent() != obj.Pkg().Scope() {
			continue
		}
		fresh := false
		for _, file := range c.files {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.VAR {
					continue
				}
				for _, spec := range gen.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok || len(value.Names) != len(value.Values) {
						continue
					}
					for j, name := range value.Names {
						if c.info.Defs[name] != obj {
							continue
						}
						lit, ok := ast.Unparen(value.Values[j]).(*ast.CompositeLit)
						if !ok || c.info.Types[lit].Type == nil {
							continue
						}
						_, fresh = c.info.Types[lit].Type.Underlying().(*types.Slice)
					}
				}
			}
		}
		if !fresh {
			continue
		}
		uses := 0
		soleUse := false
		for ident, used := range c.info.Uses {
			if used == obj {
				uses++
				soleUse = ident == id
			}
		}
		if uses != 1 || !soleUse {
			continue
		}
		if proven == nil {
			proven = make([]bool, len(call.Args))
		}
		proven[i] = true
	}
	return proven
}

// selectorValue lowers a selector without treating an instantiated generic
// method as a standalone function value. Call sites use this direct form so
// the receiver remains a receiver and the method type arguments remain on the
// call; ordinary value positions still go through genericFuncValue above.
func (c *converter) selectorValue(x *ast.SelectorExpr, value ast.Expr) *s.BashPPSelectorExpr {
	out := &s.BashPPSelectorExpr{X: c.expr(x.X), Dot: c.pos(x.Sel.Pos() - 1), Sel: c.ident(x.Sel), FuncType: c.functionValueType(value)}
	if selection := c.info.Selections[x]; selection != nil && selection.Kind() == types.MethodVal {
		out.MethodValue = true
		out.ReceiverAddressable = c.info.Types[x.X].Addressable()
	}
	return out
}

// instantiatedMethodValue recognizes a generic method value whether its type
// arguments were written after the selector or inferred by go/types. Method
// expressions are deliberately excluded: typeParamMethodExpr lowers those
// through a receiver parameter rather than a bound receiver value.
func (c *converter) instantiatedMethodValue(e ast.Expr) (*ast.SelectorExpr, []*s.BashPPTypeArg, bool) {
	return c.instantiatedMethod(e, types.MethodVal)
}

func (c *converter) instantiatedMethodExpression(e ast.Expr) (*ast.SelectorExpr, []*s.BashPPTypeArg, bool) {
	return c.instantiatedMethod(e, types.MethodExpr)
}

func (c *converter) instantiatedMethod(e ast.Expr, kind types.SelectionKind) (*ast.SelectorExpr, []*s.BashPPTypeArg, bool) {
	base := ast.Unparen(e)
	switch x := base.(type) {
	case *ast.IndexExpr:
		base = ast.Unparen(x.X)
	case *ast.IndexListExpr:
		base = ast.Unparen(x.X)
	}
	selector, ok := base.(*ast.SelectorExpr)
	if !ok {
		return nil, nil, false
	}
	selection := c.info.Selections[selector]
	if selection == nil || selection.Kind() != kind {
		return nil, nil, false
	}
	typeArgs := c.instanceTypeArgs(e)
	if len(typeArgs) == 0 {
		return nil, nil, false
	}
	return selector, typeArgs, true
}

// callee lowers a simple callee — a name, a selector chain, a literal, or
// one of those instantiated — onto the call's Fun/FuncLit/TypeArgs.
func (c *converter) callee(out *s.BashPPCall, e ast.Expr) {
	switch v := e.(type) {
	case *ast.Ident:
		if pkg, sel, ok := c.qualifiedIdent(v); ok {
			out.Fun = append(out.Fun, pkg, sel)
			return
		}
		out.Fun = append(out.Fun, c.ident(v))
	case *ast.SelectorExpr:
		// A method expression the runtime cannot select from a declaration
		// is called as the closure it lowers to; see typeParamMethodExpr.
		if lit, ok := c.typeParamMethodExpr(v).(*s.BashPPFuncLit); ok {
			out.FuncLit = lit
			return
		}
		if !c.mappedPkgName(v.X) {
			c.callee(out, v.X)
		}
		out.Fun = append(out.Fun, c.ident(v.Sel))
	case *ast.FuncLit:
		out.FuncLit = c.funlit(v)
	case *ast.IndexExpr:
		c.callee(out, v.X)
		out.TypeArgs = append(out.TypeArgs, &s.BashPPTypeArg{ArgType: c.typ(v.Index)})
	case *ast.IndexListExpr:
		c.callee(out, v.X)
		for _, t := range v.Indices {
			out.TypeArgs = append(out.TypeArgs, &s.BashPPTypeArg{ArgType: c.typ(t)})
		}
	default:
		c.fail(e, "call target")
		return
	}
	if args := c.instanceTypeArgs(e); args != nil {
		out.TypeArgs = args
	}
}

// genericFuncValue lowers a generic function used as a VALUE — `Abs[T]`,
// `pair[string, int]`, `slices.Max[[]int]`, or a bare `Abs` whose type
// arguments the checker inferred from the assignment context — as the
// closure that forwards to the instantiated function:
//
//	func(p0 T0, p1 ...T1) R { return Abs[T](p0, p1...) }
//
// The closure is the value's instantiated signature exactly, so every
// consumer — a parameter, a variable, a struct field, a map value — sees an
// ordinary function value and nothing downstream needs a notion of a
// partially applied generic. Go forbids comparing functions, so the extra
// indirection is unobservable. Returns nil when e is not such a value.
func (c *converter) genericFuncValue(e ast.Expr) s.BashPPExpr {
	base := ast.Unparen(e)
	switch v := base.(type) {
	case *ast.IndexExpr:
		base = v.X
	case *ast.IndexListExpr:
		base = v.X
	}
	var id *ast.Ident
	switch v := ast.Unparen(base).(type) {
	case *ast.Ident:
		id = v
	case *ast.SelectorExpr:
		id = v.Sel
	default:
		return nil
	}
	inst, ok := c.info.Instances[id]
	if !ok || inst.TypeArgs == nil || inst.TypeArgs.Len() == 0 {
		return nil
	}
	instantiated, ok := inst.Type.(*types.Signature)
	if !ok {
		return nil
	}
	if _, _, methodExpr := c.instantiatedMethodExpression(e); methodExpr {
		// Info.Instances records the generic method itself, whose signature
		// omits the receiver. The type of a method expression includes that
		// receiver as its first parameter, which the forwarding closure must
		// preserve.
		instantiated, _ = c.info.TypeOf(e).Underlying().(*types.Signature)
		if instantiated == nil {
			return nil
		}
	}
	// The instance carries the instantiated signature; the expression's own
	// type still spells the type parameter list when the name is bare.
	signature, _ := c.checkedType(instantiated, e, "instantiated function value type").(*s.BashPPFuncType)
	if signature == nil {
		return nil
	}
	return c.forwardingClosure(e, signature, func(call *s.BashPPCall, args []*s.Lit) {
		if selector, typeArgs, ok := c.instantiatedMethodValue(e); ok {
			call.CalleeExpr = c.selectorValue(selector, e)
			call.TypeArgs = typeArgs
			return
		}
		if selector, typeArgs, ok := c.instantiatedMethodExpression(e); ok {
			call.Fun = []*s.Lit{args[0], c.ident(selector.Sel)}
			call.Args, call.ArgExprs = call.Args[1:], call.ArgExprs[1:]
			call.TypeArgs = typeArgs
			return
		}
		c.callee(call, ast.Unparen(e))
	})
}

// typeParamMethodExpr lowers a method expression whose receiver type is not
// a plain declared name — a type parameter (`T.String` inside
// `func f[T Stringer]`), a type literal (`interface{ M() }.M`,
// `struct{ I }.M`) or an instantiated generic type (`S[int, string].M`,
// `(*S[K, V]).M`) — as the closure that calls the method on its first
// argument, `func(r T, …) R { return r.String(…) }`. The receiver is a value
// at run time, and a method selected on a value is what the runtime
// resolves; a type parameter's NAME has no declaration until the frame binds
// it, an unnamed type has none at all, and an instantiation's type arguments
// belong to the receiver's type, which the closure's parameter spells.
// An imported package's named type (`reflect.Type.Method`,
// `(*bytes.Buffer).Write`) takes the same closure: the program has no
// declaration of it to select the method from, only values of it, which the
// dependency resolves the method on. Returns nil for every other selector:
// a declared name's method expression is spelled as written.
func (c *converter) typeParamMethodExpr(x *ast.SelectorExpr) s.BashPPExpr {
	selection := c.info.Selections[x]
	if selection == nil || selection.Kind() != types.MethodExpr {
		return nil
	}
	recv := selection.Recv()
	if pointer, ok := recv.(*types.Pointer); ok {
		recv = pointer.Elem()
	}
	switch r := recv.(type) {
	case *types.TypeParam, *types.Struct, *types.Interface:
	case *types.Named:
		if r.TypeArgs().Len() == 0 && !c.bridgedType(r) {
			return nil
		}
	default:
		return nil
	}
	signature, _ := c.checkedType(c.info.TypeOf(x), x, "method expression type").(*s.BashPPFuncType)
	if signature == nil {
		return nil
	}
	return c.forwardingClosure(x, signature, func(call *s.BashPPCall, args []*s.Lit) {
		call.Fun = []*s.Lit{args[0], c.ident(x.Sel)}
		call.Args, call.ArgExprs = call.Args[1:], call.ArgExprs[1:]
	})
}

// forwardingClosure builds `func(a0 T0, a1 ...T1) R { return <callee>(a0,
// a1...) }` for a signature: a closure with that exact signature whose body
// forwards every parameter to a call the caller completes. The callee hook
// receives the call with its arguments already in place and the parameter
// names, so it can take one of them as a receiver.
func (c *converter) forwardingClosure(e ast.Expr, signature *s.BashPPFuncType, callee func(call *s.BashPPCall, args []*s.Lit)) s.BashPPExpr {
	c.syntheticPos = e.Pos()
	defer func() { c.syntheticPos = token.NoPos }()
	at := e.Pos()
	call := &s.BashPPCall{Lparen: c.pos(at), Rparen: c.pos(at)}
	lit := &s.BashPPFuncLit{Kw: c.lit(at, "func"), Lparen: c.pos(at), Rparen: c.pos(at)}
	var spelled []string
	var names []*s.Lit
	for _, group := range signature.Params {
		count := len(group.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			name := fmt.Sprintf("%sarg%d", c.prefix, len(spelled))
			field := *group
			field.Names = []*s.Lit{c.lit(at, name)}
			lit.Params = append(lit.Params, &field)
			text := name
			if field.Variadic() {
				text += "..."
				call.Ellipsis = c.pos(at)
			}
			spelled = append(spelled, text)
			names = append(names, c.lit(at, name))
			call.Args = append(call.Args, &s.Word{Parts: []s.WordPart{c.lit(at, text)}})
			call.ArgExprs = append(call.ArgExprs, &s.BashPPIdent{Name: c.lit(at, name)})
		}
	}
	callee(call, names)
	for _, group := range signature.Results {
		field := *group
		field.Names = nil
		lit.Results = append(lit.Results, &field)
	}
	body := &s.Block{Lbrace: c.pos(at), Rbrace: c.pos(at)}
	if len(lit.Results) == 0 {
		body.Stmts = []*s.Stmt{c.stmt(call)}
	} else {
		spelling := c.text(e) + "(" + strings.Join(spelled, ", ") + ")"
		ret := &s.BashPPReturn{Kw: c.lit(at, "return"), Call: call, Results: []*s.Word{{Parts: []s.WordPart{c.lit(at, spelling)}}}}
		body.Stmts = []*s.Stmt{c.stmt(ret)}
	}
	lit.Body = body
	return lit
}

func (c *converter) statements(st ast.Stmt) []*s.Stmt {
	var cmd s.Command
	switch x := st.(type) {
	case *ast.EmptyStmt:
		return nil
	case *ast.BlockStmt:
		cmd = c.block(x)
	case *ast.ExprStmt:
		expr := ast.Unparen(x.X)
		if call, ok := expr.(*ast.CallExpr); ok {
			if c.waitGroupGoStmt(call) && c.localSyncType(waitGroupGoRecv(c.info.Selections[ast.Unparen(call.Fun).(*ast.SelectorExpr)])) != "" {
				return c.lowerWaitGroupGo(call)
			}
			cmd = c.call(call)
		} else if recv, ok := expr.(*ast.UnaryExpr); ok && recv.Op == token.ARROW {
			cmd = &s.BashPPReceive{Arrow: c.pos(recv.OpPos), Chan: c.word(recv.X), ChanExpr: c.expr(recv.X)}
		} else {
			c.fail(x, "expression statement")
		}
	case *ast.DeclStmt:
		g := x.Decl.(*ast.GenDecl)
		var out []*s.Stmt
		for _, spec := range g.Specs {
			switch v := spec.(type) {
			case *ast.ValueSpec:
				if g.Tok == token.VAR && len(v.Values) == 1 && len(v.Names) > 1 {
					out = append(out, c.tupleValueDecls(g, v)...)
					continue
				}
				for i, n := range v.Names {
					out = append(out, c.stmt(c.valueDecl(g, v, n, i)))
				}
			case *ast.TypeSpec:
				out = append(out, c.stmt(c.typeDecl(g, v)))
			}
		}
		return out
	case *ast.AssignStmt:
		if x.Tok == token.DEFINE {
			out := &s.BashPPShortDecl{Class: s.ClassR, GoRegion: true, OpPos: c.pos(x.TokPos)}
			for _, e := range x.Lhs {
				out.Lhs = append(out.Lhs, c.ident(e.(*ast.Ident)))
			}
			for _, e := range x.Rhs {
				out.Rhs = append(out.Rhs, c.word(e))
				if len(x.Rhs) > 1 {
					out.RhsExprs = append(out.RhsExprs, c.expr(e))
				}
			}
			if len(x.Rhs) == 1 {
				switch rhs := x.Rhs[0].(type) {
				case *ast.FuncLit:
					out.FuncLit = c.funlit(rhs)
					out.Rhs = nil
				case *ast.CallExpr:
					if c.isNewBuiltin(rhs) {
						out.Expr = c.expr(rhs)
						out.Rhs = nil
						break
					}
					if c.info.Types[rhs.Fun].IsType() || c.isConstantCall(rhs) {
						out.Expr = c.expr(rhs)
						out.Rhs = nil
						break
					}
					out.Call = c.call(rhs)
					if len(out.Call.Fun) == 1 && out.Call.Fun[0].Value == "make" {
						if ch, ok := out.Call.ArgType.(*s.BashPPChanType); ok {
							out.MakeChan = &s.BashPPMakeChan{Make: out.Call.Fun[0], ChanType: ch, Lparen: out.Call.Lparen, Rparen: out.Call.Rparen}
							if len(rhs.Args) > 1 {
								out.MakeChan.Capacity = c.word(rhs.Args[1])
								out.MakeChan.CapacityExpr = c.expr(rhs.Args[1])
							}
							out.Call = nil
							out.Rhs = nil
						}
					}
				case *ast.UnaryExpr:
					if rhs.Op == token.ARROW {
						out.Recv = &s.BashPPReceive{Arrow: c.pos(rhs.OpPos), Chan: c.word(rhs.X), ChanExpr: c.expr(rhs.X)}
						out.Rhs = nil
					} else {
						out.Expr = c.expr(rhs)
					}
				default:
					out.Expr = c.expr(rhs)
					// An instantiated generic function value lowers to a
					// closure; it takes the literal's slot, as a literal does.
					if lit, ok := out.Expr.(*s.BashPPFuncLit); ok {
						out.FuncLit, out.Expr, out.Rhs = lit, nil, nil
					}
				}
			}
			cmd = out
		} else if x.Tok == token.ASSIGN {
			if len(x.Lhs) > 1 && !tuplePlainTargets(x.Lhs) {
				return c.tupleAssignStmts(x)
			}
			// Parentheses around an lvalue are meaningless in Go: `(_) = v`
			// and `(x) = v` assign exactly as their unparenthesized forms. Peel
			// them so a parenthesized blank still reaches the blank-name list
			// and a parenthesized name is not lowered as a value read.
			target := ast.Unparen(x.Lhs[0])
			out := &s.BashPPAssign{Eq: c.pos(x.TokPos), Target: c.word(target), Value: c.word(x.Rhs[0]), TargetExpr: c.expr(target)}
			for _, e := range x.Lhs {
				if id, ok := ast.Unparen(e).(*ast.Ident); ok {
					out.Names = append(out.Names, c.ident(id))
				}
			}
			for _, e := range x.Rhs {
				out.Values = append(out.Values, c.word(e))
				out.ValueExprs = append(out.ValueExprs, c.expr(e))
			}
			if len(x.Rhs) == 1 {
				out.ValueExpr = c.expr(x.Rhs[0])
				if call, ok := x.Rhs[0].(*ast.CallExpr); ok && !c.info.Types[call.Fun].IsType() && !c.isConstantCall(call) {
					out.Call = c.call(call)
				}
			}
			cmd = out
		} else {
			cmd = &s.BashPPUpdate{TargetWord: c.word(x.Lhs[0]), Target: c.expr(x.Lhs[0]), Op: c.lit(x.TokPos, x.Tok.String()), ValueWord: c.word(x.Rhs[0]), Value: c.expr(x.Rhs[0])}
		}
	case *ast.IncDecStmt:
		out := &s.BashPPIncDec{TargetWord: c.word(x.X), Target: c.expr(x.X), Op: c.lit(x.TokPos, x.Tok.String())}
		if id, ok := x.X.(*ast.Ident); ok {
			out.Name = c.ident(id)
		}
		cmd = out
	case *ast.ReturnStmt:
		out := &s.BashPPReturn{Kw: c.lit(x.Return, "return")}
		for _, e := range x.Results {
			out.Results = append(out.Results, c.word(e))
			if len(x.Results) > 1 {
				out.ResultExprs = append(out.ResultExprs, c.expr(e))
			}
		}
		if len(x.Results) == 1 {
			switch e := x.Results[0].(type) {
			case *ast.CallExpr:
				// A conversion and an allocation are expressions, as in every
				// other position: `return new(T)` is the NewExpr the type
				// spells, whether T is a name or a type literal.
				if c.info.Types[e.Fun].IsType() || c.isNewType(e) || c.isNewBuiltin(e) || c.isConstantCall(e) {
					out.Expr = c.expr(e)
				} else {
					out.Call = c.call(e)
				}
			case *ast.FuncLit:
				out.FuncLit = c.funlit(e)
				out.Results = nil
			default:
				out.Expr = c.expr(e)
				if lit, ok := out.Expr.(*s.BashPPFuncLit); ok {
					out.FuncLit, out.Expr, out.Results = lit, nil, nil
				}
			}
		}
		cmd = out
	case *ast.IfStmt:
		out := &s.BashPPIf{Site: s.StartGoIf, If: c.pos(x.If), Cond: c.expr(x.Cond), Then: c.block(x.Body)}
		if x.Init != nil {
			init := c.one(x.Init)
			if decl, ok := init.(*s.BashPPShortDecl); ok {
				out.Init = decl
			} else {
				out.InitStmt = init
			}
			out.Semicolon = c.headerToken(x.If, x.Body.Lbrace, token.SEMICOLON, 0)
		}
		if x.Else != nil {
			out.Else = c.one(x.Else)
			out.ElsePos = c.pos(x.Else.Pos())
		}
		cmd = out
	case *ast.ForStmt:
		leaveBranch := c.pushBranchScope(c.takeStatementLabel(), true)
		body := c.block(x.Body)
		leaveBranch()
		cmd = &s.BashPPFor{For: c.pos(x.For), Init: c.one(x.Init), Cond: c.expr(x.Cond), Post: c.one(x.Post), Body: body, FirstSemi: c.headerToken(x.For, x.Body.Lbrace, token.SEMICOLON, 0), SecondSemi: c.headerToken(x.For, x.Body.Lbrace, token.SEMICOLON, 1)}
	case *ast.RangeStmt:
		leaveBranch := c.pushBranchScope(c.takeStatementLabel(), true)
		body := c.block(x.Body)
		leaveBranch()
		out := &s.BashPPRange{For: c.pos(x.For), Range: c.pos(x.Range), Chan: c.word(x.X), Expr: c.expr(x.X), Body: body}
		var assignments []*s.Stmt
		var targets, temps []ast.Expr
		for i, e := range []ast.Expr{x.Key, x.Value} {
			if e != nil {
				if x.Tok == token.DEFINE {
					out.Names = append(out.Names, c.ident(e.(*ast.Ident)))
				} else {
					temp := &ast.Ident{NamePos: e.Pos(), Name: fmt.Sprintf("%srange_%d_%d", c.prefix, x.TokPos, i)}
					out.Names = append(out.Names, c.ident(temp))
					targets = append(targets, e)
					temps = append(temps, temp)
				}
			}
		}
		if len(targets) == 1 || tuplePlainTargets(targets) {
			// Plain identifiers assign the same values either way, so each
			// keeps its own statement.
			for i, e := range targets {
				assign := &ast.AssignStmt{Lhs: []ast.Expr{e}, TokPos: x.TokPos, Tok: token.ASSIGN, Rhs: []ast.Expr{temps[i]}}
				assignments = append(assignments, c.statements(assign)...)
			}
		} else if len(targets) > 1 {
			// `for i, x[i] = range y` assigns as one assignment statement:
			// x[i]'s operands are evaluated before the new i is stored, so
			// the pair must go through the tuple-assignment split, not two
			// sequential assignments.
			assign := &ast.AssignStmt{Lhs: targets, TokPos: x.TokPos, Tok: token.ASSIGN, Rhs: temps}
			assignments = c.statements(assign)
		}
		if x.Tok == token.DEFINE || len(assignments) > 0 {
			out.Define = c.pos(x.TokPos)
		}
		out.Body.Stmts = append(assignments, out.Body.Stmts...)
		cmd = out
	case *ast.BranchStmt:
		if x.Tok == token.GOTO {
			cmd = &s.BashPPGoto{Kw: c.lit(x.TokPos, "goto"), Label: c.ident(x.Label)}
			break
		}
		depth := 0
		if x.Label != nil {
			depth = c.labeledBranchDepth(x)
		}
		cmd = &s.BashPPBranch{Kw: c.lit(x.TokPos, x.Tok.String()), Depth: uint(depth)}
	case *ast.DeferStmt:
		cmd = &s.BashPPDefer{Kw: c.lit(x.Defer, "defer"), Call: c.call(x.Call)}
	case *ast.GoStmt:
		cmd = &s.BashPPGo{Kw: c.lit(x.Go, "go"), Call: c.call(x.Call)}
	case *ast.SendStmt:
		cmd = &s.BashPPSend{Chan: c.word(x.Chan), Arrow: c.pos(x.Arrow), Value: c.word(x.Value), ValueExpr: c.expr(x.Value), ChanExpr: c.expr(x.Chan)}
	case *ast.SelectStmt:
		leaveBranch := c.pushBranchScope(c.takeStatementLabel(), false)
		defer leaveBranch()
		out := &s.BashPPSelect{Select: c.pos(x.Select), Lbrace: c.pos(x.Body.Lbrace), Rbrace: c.pos(x.Body.Rbrace)}
		for _, st := range x.Body.List {
			cc := st.(*ast.CommClause)
			// A RecvStmt's receive may be parenthesized (spec: RecvExpr;
			// go/types stmt.go unparens it before the receive check), and
			// the checker has already required it to be a receive; the
			// lowered case carries the receive itself.
			switch comm := cc.Comm.(type) {
			case *ast.AssignStmt:
				if len(comm.Rhs) == 1 {
					comm.Rhs[0] = ast.Unparen(comm.Rhs[0])
				}
			case *ast.ExprStmt:
				comm.X = ast.Unparen(comm.X)
			}
			comm, prefix := c.selectComm(cc.Comm)
			v := &s.BashPPSelectCase{Case: c.pos(cc.Case), Colon: c.pos(cc.Colon), Default: cc.Comm == nil, Comm: comm}
			v.Stmts = append(v.Stmts, prefix...)
			for _, body := range cc.Body {
				v.Stmts = append(v.Stmts, c.statements(body)...)
			}
			out.Cases = append(out.Cases, v)
		}
		cmd = out
	case *ast.TypeSwitchStmt:
		label := c.takeStatementLabel()
		guard := &s.BashPPShortDecl{Class: s.ClassR, GoRegion: true}
		switch assign := x.Assign.(type) {
		case *ast.ExprStmt:
			guard.Expr = c.expr(assign.X.(*ast.TypeAssertExpr))
		case *ast.AssignStmt:
			guard.OpPos = c.pos(assign.TokPos)
			for _, lhs := range assign.Lhs {
				guard.Lhs = append(guard.Lhs, c.ident(lhs.(*ast.Ident)))
			}
			guard.Expr = c.expr(assign.Rhs[0].(*ast.TypeAssertExpr))
		}
		leaveBranch := c.pushBranchScope(label, false)
		defer leaveBranch()
		out := &s.BashPPSwitch{Switch: c.pos(x.Switch), TypeSwitch: true, Init: guard, Lbrace: c.pos(x.Body.Lbrace), Rbrace: c.pos(x.Body.Rbrace)}
		for _, st := range x.Body.List {
			cc := st.(*ast.CaseClause)
			v := &s.BashPPSwitchArm{Case: c.pos(cc.Case), Colon: c.pos(cc.Colon)}
			for _, e := range cc.List {
				v.Types = append(v.Types, c.typ(e))
			}
			for _, body := range cc.Body {
				v.Stmts = append(v.Stmts, c.statements(body)...)
			}
			out.Arms = append(out.Arms, v)
		}
		if x.Init == nil {
			cmd = out
			break
		}
		// Go scopes a type switch's init statement to the switch: the spec
		// places init, guard and body in one implicit block. That block is
		// spelled here — the init statements, then the switch — the same
		// shape lowering gives an expression switch with an init.
		block := &s.Block{Lbrace: c.pos(x.Switch), Rbrace: c.pos(x.Body.Rbrace)}
		block.Stmts = append(c.statements(x.Init), c.stmt(out))
		cmd = block
	case *ast.SwitchStmt:
		leaveBranch := c.pushBranchScope(c.takeStatementLabel(), false)
		defer leaveBranch()
		out := &s.BashPPSwitch{Switch: c.pos(x.Switch), Init: c.one(x.Init), Tag: c.expr(x.Tag), Lbrace: c.pos(x.Body.Lbrace), Rbrace: c.pos(x.Body.Rbrace)}
		for _, st := range x.Body.List {
			cc := st.(*ast.CaseClause)
			v := &s.BashPPSwitchArm{Case: c.pos(cc.Case), Colon: c.pos(cc.Colon)}
			for _, e := range cc.List {
				v.Exprs = append(v.Exprs, c.expr(e))
			}
			for _, body := range cc.Body {
				v.Stmts = append(v.Stmts, c.statements(body)...)
			}
			out.Arms = append(out.Arms, v)
		}
		cmd = out
	case *ast.LabeledStmt:
		// The label is carried for goto and for lowering. A labeled loop,
		// switch, or select additionally becomes a named branch scope so a
		// labeled break/continue resolves to a depth; any other statement is
		// only a goto target, so a loop nested inside it must not take the
		// label.
		out := &s.BashPPLabeled{Label: c.ident(x.Label), Colon: c.pos(x.Colon)}
		previous := c.statementLabel
		c.statementLabel = ""
		switch x.Stmt.(type) {
		case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
			c.statementLabel = x.Label.Name
		}
		inner := c.statements(x.Stmt)
		c.statementLabel = previous
		// A statement that converts to several (a range with assignment
		// targets, a tuple assignment) keeps its first as the labeled one; the
		// rest follow it in the block, which is where execution resumes after a
		// goto to the label.
		if len(inner) > 0 {
			// A type switch with an init statement converts to the block
			// scoping that init around the switch. A break names the switch
			// by depth, so the label belongs on the switch itself, inside
			// the block, unless a goto names it: a goto resumes at the
			// labeled statement, which must then be the block so the init
			// runs again, as Go re-evaluates it on every entry.
			if block, ok := inner[0].Cmd.(*s.Block); ok && c.scopedTypeSwitch(x.Stmt) && !c.gotoTarget(x.Label) {
				last := len(block.Stmts) - 1
				out.Stmt = block.Stmts[last]
				block.Stmts[last] = c.stmt(out)
				return inner
			}
			out.Stmt = inner[0]
			inner = inner[1:]
		}
		return append([]*s.Stmt{c.stmt(out)}, inner...)
	default:
		c.fail(st, strings.TrimPrefix(fmt.Sprintf("%T", st), "*ast."))
	}
	if cmd == nil {
		return nil
	}
	return []*s.Stmt{c.stmt(cmd)}
}

// selectComm keeps a select receive atomic even when its assignment has
// complex targets. The select engine receives into hidden names; only after
// that arm wins do the ordinary tuple-assignment rules evaluate and commit the
// original targets, which is the order required by the Go specification.
func (c *converter) selectComm(st ast.Stmt) (s.Command, []*s.Stmt) {
	assign, ok := st.(*ast.AssignStmt)
	if !ok || assign.Tok != token.ASSIGN || tuplePlainTargets(assign.Lhs) {
		return c.one(st), nil
	}
	if len(assign.Lhs) < 2 && !c.mapElementTarget(assign.Lhs[0]) {
		return c.one(st), nil
	}
	temps := make([]ast.Expr, len(assign.Lhs))
	for i, lhs := range assign.Lhs {
		temps[i] = &ast.Ident{NamePos: ast.Unparen(lhs).Pos(), Name: fmt.Sprintf("%sselect_%d_%d", c.prefix, assign.TokPos, i)}
	}
	recv := &ast.AssignStmt{Lhs: temps, TokPos: assign.TokPos, Tok: token.DEFINE, Rhs: assign.Rhs}
	commit := &ast.AssignStmt{Lhs: assign.Lhs, TokPos: assign.TokPos, Tok: token.ASSIGN, Rhs: temps}
	return c.one(recv), c.statements(commit)
}

// mapElementTarget reports whether target is a map index expression. A map
// element is not addressable, so a select receive into one commits through an
// ordinary map assignment after the case is chosen, which is also when the
// spec evaluates the receive's left-hand side.
func (c *converter) mapElementTarget(target ast.Expr) bool {
	index, ok := ast.Unparen(target).(*ast.IndexExpr)
	if !ok {
		return false
	}
	typ := c.info.TypeOf(index.X)
	if typ == nil {
		return false
	}
	_, isMap := typ.Underlying().(*types.Map)
	return isMap
}

func (c *converter) one(st ast.Stmt) s.Command {
	if st == nil {
		return nil
	}
	v := c.statements(st)
	if len(v) != 1 {
		c.fail(st, "compound simple statement")
		return nil
	}
	return v[0].Cmd
}

// headerToken finds a punctuation token at header nesting depth zero. Go AST
// omits semicolon positions; scanning the original bytes preserves them.
func (c *converter) headerToken(from, to token.Pos, want token.Token, ordinal int) s.Pos {
	file := c.fset.File(from)
	if file == nil {
		return s.Pos{}
	}
	var data []byte
	for i, f := range c.files {
		if c.fset.File(f.Pos()) == file {
			data = c.sources[i].Data
			break
		}
	}
	start, end := file.Offset(from), file.Offset(to)
	if start < 0 || end > len(data) || start > end {
		return s.Pos{}
	}
	fragment := data[start:end]
	fset := token.NewFileSet()
	sf := fset.AddFile(file.Name(), -1, len(fragment))
	var scan scanner.Scanner
	scan.Init(sf, fragment, nil, 0)
	depth := 0
	for {
		p, tok, _ := scan.Scan()
		if tok == token.EOF {
			break
		}
		switch tok {
		case token.LPAREN, token.LBRACE, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACE, token.RBRACK:
			depth--
		}
		if depth == 0 && tok == want {
			if ordinal == 0 {
				return c.pos(from + token.Pos(sf.Offset(p)))
			}
			ordinal--
		}
	}
	return s.Pos{}
}

// receiverAliasTarget spells the defined type a method receiver names through
// a non-generic alias. Go admits `type token = Token; func (t token) M()` and
// the method belongs to Token; the lowered declaration names Token itself.
func (c *converter) receiverAliasTarget(t ast.Expr) *s.Lit {
	id, ok := t.(*ast.Ident)
	if !ok {
		return nil
	}
	tn, ok := c.info.ObjectOf(id).(*types.TypeName)
	if !ok || !tn.IsAlias() {
		return nil
	}
	named, ok := types.Unalias(tn.Type()).(*types.Named)
	if !ok || named.TypeArgs().Len() > 0 || named.TypeParams().Len() > 0 || named.Obj().Pkg() != tn.Pkg() {
		return nil
	}
	v := named.Obj().Name()
	if rename := c.renames[named.Obj()]; rename != "" {
		v = rename
	} else if rename := c.checkerNames[v]; rename != "" {
		v = rename
	}
	out := c.lit(id.Pos(), v)
	out.ValueEnd = c.pos(id.End())
	return out
}

// arrayLengthText spells an array length. A length that names a constant of
// another package (`[ir.OEND][]bool`, `[utf8.UTFMax]byte`) is spelled as the
// integer the checker computed: the dependency's constant is not a value the
// evaluator can read while it lays out a type, and the type is the same one.
func (c *converter) arrayLengthText(n ast.Expr) string {
	qualified := false
	ast.Inspect(n, func(node ast.Node) bool {
		if _, ok := node.(*ast.SelectorExpr); ok {
			qualified = true
		}
		return !qualified
	})
	if qualified {
		if tv, ok := c.info.Types[n]; ok && tv.Value != nil && tv.Value.Kind() == constant.Int {
			return tv.Value.ExactString()
		}
	}
	return c.text(n)
}
