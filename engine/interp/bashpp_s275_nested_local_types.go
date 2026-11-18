// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #275; Story: #758; Story-ID: 2577207b59b5
//
// Identity of a type declared inside a generic function.
//
// gc makes `type T[B any] struct{}` declared in `func F[A any]()` a distinct
// type per instantiation of F, and spells it with F's type arguments before
// its own: `main.T[int;int]`. A local type named as a type argument also
// carries gc's package-wide local-type index, `main.U[int;int]·3`. reflect
// cannot mint such a type, and a package-level helper declaration can never
// be spelled that way.
//
// So the helper declares these types the way the program does: each
// function that owns them is mirrored as a helper function of the same
// shape — the same type parameters, the same local type declarations in the
// same order — whose body is nothing but those declarations and the
// reflect.Type of every type expression the original body spells with them.
// Each explicit instantiation site `F[X]()` in a non-generic function is
// mirrored as a call of F's mirror with the same type arguments, so gc
// itself builds the instance types. Local type declarations the helper
// does not mirror are padded in file order, so the index gc assigns each
// mirrored declaration is the one it assigns the original.
//
// A reference to a mirrored declaration crosses the bridge spelled by the
// declaration and the enclosing instantiation's type arguments. An
// instantiation the helper did not mirror — inferred, reached through
// another generic function, or evaluated outside its frame — spells a name
// the helper never registered, and stays refused.

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPNestDecl is one local type declaration owned by a mirrored function.
// params are the enclosing function's type parameter names, empty when the
// function is not generic.
type bashPPNestDecl struct {
	decl   *syntax.BashPPDecl
	params []string
}

// bashPPNestMirrorName is the descriptor name of the mirror source entry.
const bashPPNestMirrorName = "bppNestMirror"

// bashPPNestName is the helper registry name of one mirrored declaration in
// one enclosing instantiation.
func bashPPNestName(key string, enclosing []string) string {
	return fmt.Sprintf("bppNest_%x", sha256.Sum256([]byte(key+"|"+strings.Join(enclosing, "\x00"))))
}

// bashPPNestTypeName spells a reference to a mirrored declaration. bindings
// are the type arguments of the running generic frame; a declaration of a
// generic function whose parameters they do not bind exactly spells a name
// the helper never registers, so the value is refused rather than conflated.
func (r *Runner) bashPPNestTypeName(named *syntax.BashPPNamedType, nest map[string]bashPPNestDecl, scoped map[string]string, bindings map[string]syntax.BashPPTypeExpr) (string, bool) {
	if len(nest) == 0 || named == nil || named.Name == nil {
		return "", false
	}
	scope, known := r.goSourceLocalTypeScope(named)
	if !known || scope == "" {
		return "", false
	}
	key := bashPPScopedLocalKey(named.Name.Value, scope)
	d, ok := nest[key]
	if !ok {
		return "", false
	}
	if len(d.params) == 0 {
		return bashPPNestName(key, nil), true
	}
	if len(bindings) != len(d.params) {
		return "bppNestUnbound_" + bashPPNestName(key, nil), true
	}
	args := make([]string, len(d.params))
	for i, param := range d.params {
		arg := bindings[param]
		if arg == nil {
			return "bppNestUnbound_" + bashPPNestName(key, nil), true
		}
		// A mirrored instantiation site is in a non-generic function, so
		// its type arguments bind nothing further.
		args[i] = bashPPBridgeTypeTextIn(arg, r.bashPPNestScope(nest, scoped, nil))
	}
	return bashPPNestName(key, args), true
}

// bashPPNestScope is the bridge type scope over mirrored declarations first
// and scoped reused names second; it is the runtime scope with explicit
// bindings, so the helper registers exactly the spellings the runner sends.
func (r *Runner) bashPPNestScope(nest map[string]bashPPNestDecl, scoped map[string]string, bindings map[string]syntax.BashPPTypeExpr) bashPPBridgeTypeScope {
	return func(named *syntax.BashPPNamedType) (string, bool) {
		if name, ok := r.bashPPNestTypeName(named, nest, scoped, bindings); ok {
			return name, true
		}
		if len(scoped) == 0 || named.Name == nil {
			return "", false
		}
		scope, known := r.goSourceLocalTypeScope(named)
		if !known || scope == "" {
			return "", false
		}
		name, ok := scoped[bashPPScopedLocalKey(named.Name.Value, scope)]
		return name, ok
	}
}

// bashPPNestFunc is one function considered for mirroring.
type bashPPNestFunc struct {
	fn     *syntax.BashPPFuncDecl
	params []string
	// decls are the function's local type declarations, all direct
	// statements of its body, in source order.
	decls []*syntax.BashPPDecl
	// exprs are the named-type references the body spells with them.
	exprs []*syntax.BashPPNamedType
	// sites are explicit instantiations of mirrored generic functions.
	sites []*syntax.BashPPCall
}

// bashPPNestMirror plans the mirrored functions of the program and renders
// their helper source. It returns no source when nothing is mirrored.
func (r *Runner) bashPPNestMirror(types []bashPPLocalType, scoped map[string]string) (map[string]bashPPNestDecl, string) {
	file := r.bashPPGoSourceFile
	if file == nil {
		return nil, ""
	}
	helperNames := map[string]bool{}
	for _, local := range types {
		if local.PublicType == "" && local.GenericDecl == "" && local.WireType == "" && !strings.HasPrefix(local.Name, "bpp") {
			helperNames[local.Name] = true
		}
	}
	// A generic function is mirrored when it owns local defined types and
	// every one of them and every reference to them renders in the helper.
	generic := map[string]*bashPPNestFunc{}
	var plain []*syntax.BashPPFuncDecl
	for _, stmt := range file.Stmts {
		fn, ok := stmt.Cmd.(*syntax.BashPPFuncDecl)
		if !ok || fn.Receiver != nil || fn.Name == nil || fn.Body == nil {
			continue
		}
		if len(fn.TypeParams) == 0 {
			plain = append(plain, fn)
			continue
		}
		if !bashPPNestHasLocalTypes(fn) {
			continue
		}
		if info := r.bashPPNestFuncInfo(fn); info != nil && len(info.decls) > 0 && r.bashPPNestRenders(info, helperNames) {
			generic[fn.Name.Value] = info
		}
	}
	// A generic function may reach a mirrored declaration through another
	// generic function. Mirror each explicit forwarder so the concrete caller
	// can register the descendant's types under its substituted arguments.
	for changed := true; changed; {
		changed = false
		for _, stmt := range file.Stmts {
			fn, ok := stmt.Cmd.(*syntax.BashPPFuncDecl)
			if !ok || fn.Receiver != nil || fn.Name == nil || fn.Body == nil || len(fn.TypeParams) == 0 || generic[fn.Name.Value] != nil {
				continue
			}
			info := r.bashPPNestFuncInfo(fn)
			if info == nil {
				continue
			}
			info.sites = bashPPNestSites(fn, generic)
			if len(info.sites) > 0 && r.bashPPNestRenders(info, helperNames) {
				generic[fn.Name.Value] = info
				changed = true
			}
		}
	}
	if len(generic) == 0 {
		return nil, ""
	}
	// A non-generic function is mirrored when it instantiates one of them
	// explicitly and it renders too.
	callers := map[*syntax.BashPPFuncDecl]*bashPPNestFunc{}
	used := map[string]bool{}
	for _, fn := range plain {
		sites := bashPPNestSites(fn, generic)
		if len(sites) == 0 {
			continue
		}
		info := r.bashPPNestFuncInfo(fn)
		if info == nil {
			continue
		}
		info.sites = sites
		if !r.bashPPNestRenders(info, helperNames) {
			continue
		}
		callers[info.fn] = info
		for _, site := range info.sites {
			used[site.Fun[0].Value] = true
		}
	}
	nest := map[string]bashPPNestDecl{}
	mirrored := map[*syntax.BashPPFuncDecl]*bashPPNestFunc{}
	for name, info := range generic {
		if used[name] {
			mirrored[info.fn] = info
		}
	}
	for changed := true; changed; {
		changed = false
		for _, info := range mirrored {
			for _, site := range info.sites {
				if target := generic[site.Fun[0].Value]; target != nil && mirrored[target.fn] == nil {
					mirrored[target.fn] = target
					changed = true
				}
			}
		}
	}
	for fn, info := range callers {
		mirrored[fn] = info
	}
	if len(mirrored) == 0 {
		return nil, ""
	}
	for _, info := range mirrored {
		for _, d := range info.decls {
			nest[bashPPScopedLocalKey(d.Name.Value, strconv.FormatUint(uint64(d.Pos().Offset()), 10))] = bashPPNestDecl{decl: d, params: info.params}
		}
	}
	var b strings.Builder
	b.WriteString("func bppNestReg(keys []string, ts []reflect.Type) { for i, key := range keys { types[localTypeKey(key)] = ts[i] } }\n")
	pad := 0
	var inits []string
	for _, stmt := range file.Stmts {
		fn, _ := stmt.Cmd.(*syntax.BashPPFuncDecl)
		if info := mirrored[fn]; fn != nil && info != nil {
			r.bashPPNestMirrorGo(&b, info, generic, nest, scoped)
			if len(info.params) == 0 {
				inits = append(inits, "bppNestFn_"+info.fn.Name.Value+"()")
			}
			continue
		}
		// gc numbers every defined local type of the package in file
		// order, aliases excepted; a declaration not mirrored still takes
		// its index.
		count := 0
		syntax.Walk(stmt, func(node syntax.Node) bool {
			if d, ok := node.(*syntax.BashPPDecl); ok && d != stmt.Cmd && d.Site == syntax.StartTypeDecl && d.Name != nil && !d.Alias {
				count++
			}
			return true
		})
		if count > 0 {
			fmt.Fprintf(&b, "func bppNestPad_%d() {", pad)
			pad++
			for range count {
				b.WriteString(" type _ struct{};")
			}
			b.WriteString(" }\n")
		}
	}
	fmt.Fprintf(&b, "func init() { %s }\n", strings.Join(inits, "; "))
	return nest, b.String()
}

func bashPPNestSites(fn *syntax.BashPPFuncDecl, generic map[string]*bashPPNestFunc) []*syntax.BashPPCall {
	var sites []*syntax.BashPPCall
	syntax.Walk(fn.Body, func(node syntax.Node) bool {
		call, ok := node.(*syntax.BashPPCall)
		if ok && len(call.Fun) == 1 && call.CalleeExpr == nil && call.FuncLit == nil {
			if target := generic[call.Fun[0].Value]; target != nil && len(call.TypeArgs) == len(target.params) {
				sites = append(sites, call)
			}
		}
		return true
	})
	return sites
}

// bashPPNestHasLocalTypes reports whether fn declares any local type.
func bashPPNestHasLocalTypes(fn *syntax.BashPPFuncDecl) bool {
	found := false
	syntax.Walk(fn.Body, func(node syntax.Node) bool {
		if d, ok := node.(*syntax.BashPPDecl); ok && d.Site == syntax.StartTypeDecl {
			found = true
		}
		return !found
	})
	return found
}

// bashPPNestFuncInfo collects a function's local type declarations and the
// references to them, or reports nil when a declaration is not a direct
// statement of the body (the mirror keeps one flat declaration sequence).
func (r *Runner) bashPPNestFuncInfo(fn *syntax.BashPPFuncDecl) *bashPPNestFunc {
	info := &bashPPNestFunc{fn: fn}
	for _, param := range fn.TypeParams {
		for _, name := range param.Names {
			info.params = append(info.params, name.Value)
		}
	}
	direct := map[*syntax.BashPPDecl]bool{}
	for _, stmt := range fn.Body.Stmts {
		if d, ok := stmt.Cmd.(*syntax.BashPPDecl); ok && d.Site == syntax.StartTypeDecl {
			direct[d] = true
			if d.Name != nil && !d.Alias && d.DeclTypeExpr != nil {
				info.decls = append(info.decls, d)
			}
		}
	}
	flat := true
	syntax.Walk(fn.Body, func(node syntax.Node) bool {
		if d, ok := node.(*syntax.BashPPDecl); ok && d.Site == syntax.StartTypeDecl && !direct[d] {
			flat = false
		}
		return flat
	})
	if !flat {
		return nil
	}
	own := map[string]bool{}
	for _, d := range info.decls {
		own[bashPPScopedLocalKey(d.Name.Value, strconv.FormatUint(uint64(d.Pos().Offset()), 10))] = true
	}
	// A reference inside a generic local declaration's own body names that
	// declaration's type parameters, which only exist there.
	var genericBodies [][2]syntax.Pos
	for _, d := range info.decls {
		if len(d.TypeParams) > 0 {
			genericBodies = append(genericBodies, [2]syntax.Pos{d.Pos(), d.End()})
		}
	}
	syntax.Walk(fn.Body, func(node syntax.Node) bool {
		named, ok := node.(*syntax.BashPPNamedType)
		if !ok || named.Name == nil {
			return true
		}
		for _, span := range genericBodies {
			if named.Pos().Offset() >= span[0].Offset() && named.Pos().Offset() < span[1].Offset() {
				return true
			}
		}
		scope, known := r.goSourceLocalTypeScope(named)
		if known && scope != "" && own[bashPPScopedLocalKey(named.Name.Value, scope)] {
			info.exprs = append(info.exprs, named)
		}
		return true
	})
	return info
}

// bashPPNestRenders reports whether every declaration, reference and
// instantiation argument of info spells in the helper exactly what it
// spells in the program: each name is predeclared, a type parameter or
// local declaration in scope, or a package-level type the helper declares
// under the same name.
func (r *Runner) bashPPNestRenders(info *bashPPNestFunc, helperNames map[string]bool) bool {
	params := map[string]bool{}
	for _, name := range info.params {
		params[name] = true
	}
	local := map[string]bool{}
	own := map[string]bool{}
	for _, d := range info.decls {
		local[d.Name.Value] = true
		own[bashPPScopedLocalKey(d.Name.Value, strconv.FormatUint(uint64(d.Pos().Offset()), 10))] = true
	}
	var check func(typ syntax.BashPPTypeExpr, extra map[string]bool) bool
	check = func(typ syntax.BashPPTypeExpr, extra map[string]bool) bool {
		if typ == nil {
			return false
		}
		if strings.Contains(bashPPBridgeTypeText(typ), "<inferred>") {
			return false
		}
		good := true
		syntax.Walk(typ, func(node syntax.Node) bool {
			switch t := node.(type) {
			case *syntax.BashPPNamedType:
				if t.Name == nil {
					good = false
					break
				}
				name := t.Name.Value
				switch {
				case strings.Contains(name, "."):
					good = false
				case bashPPHelperReserved[name]:
					good = false
				case extra[name], params[name], r.bashPPNestOwnReference(t, own):
				case local[name]:
					// A local name referenced before its declaration names
					// the package-level type, which the helper must declare.
					good = helperNames[name] && len(t.TypeArgs) == 0
				case bashPPLocalScalarTypes[name]:
				case helperNames[name] && len(t.TypeArgs) == 0:
				default:
					good = false
				}
			case *syntax.BashPPTypeParamType:
				if t.Name == nil || !(extra[t.Name.Value] || params[t.Name.Value]) {
					good = false
				}
			case *syntax.BashPPStructType:
				for _, field := range t.Fields {
					names := field.Names
					if field.Embedded {
						good = false
					}
					for _, n := range names {
						first, _ := utf8.DecodeRuneInString(n.Value)
						if !unicode.IsUpper(first) {
							good = false
						}
					}
				}
			case *syntax.BashPPCollectionType:
				if t.Kind == "inferred-array" {
					good = false
				} else if t.Length != nil {
					if _, err := strconv.ParseUint(t.Length.Value, 0, 63); err != nil {
						good = false
					}
				}
			case *syntax.BashPPInterfaceType, *syntax.BashPPFuncType, *syntax.BashPPChanType:
				good = false
			}
			return good
		})
		return good
	}
	for _, d := range info.decls {
		if bashPPHelperReserved[d.Name.Value] || d.Name.Value == "_" {
			return false
		}
		extra := map[string]bool{}
		for _, param := range d.TypeParams {
			for _, name := range param.Names {
				extra[name.Value] = true
			}
		}
		if !check(d.DeclTypeExpr, extra) {
			return false
		}
	}
	for _, named := range info.exprs {
		if !check(named, nil) {
			return false
		}
	}
	for _, site := range info.sites {
		for _, arg := range site.TypeArgs {
			if arg == nil || !check(arg.ArgType, nil) {
				return false
			}
		}
	}
	return true
}

// bashPPNestMirrorGo renders one mirrored function. Its body declares the
// local types in source order; each reference and instantiation site is
// emitted after the last declaration that precedes it, where the helper's
// lexical scope is the original's.
func (r *Runner) bashPPNestMirrorGo(b *strings.Builder, info *bashPPNestFunc, generic map[string]*bashPPNestFunc, nest map[string]bashPPNestDecl, scoped map[string]string) {
	name := info.fn.Name.Value
	if len(info.params) > 0 {
		fmt.Fprintf(b, "func bppNestFn_%s[%s any]() []reflect.Type {\n var bppNestTs []reflect.Type\n", name, strings.Join(info.params, ", "))
	} else {
		fmt.Fprintf(b, "func bppNestFn_%s() {\n", name)
	}
	type point struct {
		pos  syntax.Pos
		text string
		// key is the registration key of a reference a non-generic
		// mirror registers itself; ts marks a point that appends one.
		key string
		ts  bool
	}
	var points []point
	// The registration keys of a non-generic function's own references are
	// its runtime spellings with no bindings; a generic function's are
	// computed per instantiation site by its caller.
	for _, named := range info.exprs {
		p := point{pos: named.Pos(), text: fmt.Sprintf(" bppNestTs = append(bppNestTs, reflect.TypeFor[%s]())\n", bashPPBridgeTypeText(named)), ts: true}
		if len(info.params) == 0 {
			p.key = bashPPBridgeTypeTextIn(named, r.bashPPNestScope(nest, scoped, nil))
		}
		points = append(points, p)
	}
	for _, d := range info.decls {
		if len(d.TypeParams) > 0 {
			continue
		}
		key := bashPPScopedLocalKey(d.Name.Value, strconv.FormatUint(uint64(d.Pos().Offset()), 10))
		points = append(points, point{pos: d.End(), text: fmt.Sprintf(" bppNestTs = append(bppNestTs, reflect.TypeFor[%s]())\n", d.Name.Value), key: bashPPNestName(key, nil), ts: true})
	}
	for _, site := range info.sites {
		target := generic[site.Fun[0].Value]
		bindings := map[string]syntax.BashPPTypeExpr{}
		args := make([]string, len(site.TypeArgs))
		for i, arg := range site.TypeArgs {
			bindings[target.params[i]] = arg.ArgType
			args[i] = bashPPBridgeTypeText(arg.ArgType)
		}
		if len(info.params) > 0 {
			points = append(points, point{pos: site.Pos(), text: fmt.Sprintf(" bppNestTs = append(bppNestTs, bppNestFn_%s[%s]()...)\n", target.fn.Name.Value, strings.Join(args, ", "))})
			continue
		}
		keys := r.bashPPNestSiteKeys(target, bindings, generic, nest, scoped)
		quoted := make([]string, len(keys))
		for i, key := range keys {
			quoted[i] = strconv.Quote(key)
		}
		points = append(points, point{pos: site.Pos(), text: fmt.Sprintf(" bppNestReg([]string{%s}, bppNestFn_%s[%s]())\n", strings.Join(quoted, ", "), target.fn.Name.Value, strings.Join(args, ", "))})
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].pos.Offset() < points[j].pos.Offset() })
	var ownKeys []string
	for _, p := range points {
		if p.ts {
			ownKeys = append(ownKeys, p.key)
		}
	}
	if len(info.params) == 0 {
		b.WriteString(" var bppNestTs []reflect.Type\n")
	}
	next := 0
	for _, d := range info.decls {
		for next < len(points) && points[next].pos.Offset() < d.Pos().Offset() {
			b.WriteString(points[next].text)
			next++
		}
		fmt.Fprintf(b, " type %s", d.Name.Value)
		if len(d.TypeParams) > 0 {
			var names []string
			for _, param := range d.TypeParams {
				for _, n := range param.Names {
					names = append(names, n.Value)
				}
			}
			fmt.Fprintf(b, "[%s any]", strings.Join(names, ", "))
		}
		fmt.Fprintf(b, " %s\n", bashPPBridgeTypeText(d.DeclTypeExpr))
	}
	for ; next < len(points); next++ {
		b.WriteString(points[next].text)
	}
	if len(info.params) > 0 {
		b.WriteString(" return bppNestTs\n}\n")
		return
	}
	quoted := make([]string, len(ownKeys))
	for i, key := range ownKeys {
		quoted[i] = strconv.Quote(key)
	}
	fmt.Fprintf(b, " bppNestReg([]string{%s}, bppNestTs)\n}\n", strings.Join(quoted, ", "))
}

// bashPPNestSiteKeys are the registration keys of a generic mirror's
// results under one instantiation: the runtime spelling of each reference
// once the frame binds the site's type arguments, in the mirror's order.
func (r *Runner) bashPPNestSiteKeys(target *bashPPNestFunc, bindings map[string]syntax.BashPPTypeExpr, generic map[string]*bashPPNestFunc, nest map[string]bashPPNestDecl, scoped map[string]string) []string {
	scope := r.bashPPNestScope(nest, scoped, bindings)
	type keyed struct {
		pos  syntax.Pos
		keys []string
	}
	var keys []keyed
	for _, named := range target.exprs {
		keys = append(keys, keyed{named.Pos(), []string{bashPPBridgeTypeTextIn(bashPPSubstituteType(named, bindings), scope)}})
	}
	for _, d := range target.decls {
		if len(d.TypeParams) > 0 {
			continue
		}
		key := bashPPScopedLocalKey(d.Name.Value, strconv.FormatUint(uint64(d.Pos().Offset()), 10))
		args := make([]string, len(target.params))
		for i, param := range target.params {
			args[i] = bashPPBridgeTypeTextIn(bindings[param], r.bashPPNestScope(nest, scoped, nil))
		}
		keys = append(keys, keyed{d.End(), []string{bashPPNestName(key, args)}})
	}
	for _, site := range target.sites {
		child := generic[site.Fun[0].Value]
		if child == nil {
			continue
		}
		childBindings := make(map[string]syntax.BashPPTypeExpr, len(child.params))
		for i, param := range child.params {
			childBindings[param] = bashPPSubstituteType(site.TypeArgs[i].ArgType, bindings)
		}
		keys = append(keys, keyed{site.Pos(), r.bashPPNestSiteKeys(child, childBindings, generic, nest, scoped)})
	}
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].pos.Offset() < keys[j].pos.Offset() })
	var out []string
	for _, k := range keys {
		out = append(out, k.keys...)
	}
	return out
}

// bashPPConvertBoxedType is the dynamic type an interface records for a
// converted scalar. The scalar carries its type as a bare name, which drops
// a generic instance's type arguments (`U[int](0)` would record `U`) and the
// source position that tells a mirrored local declaration apart. When the
// conversion target is such a named type, the positioned target — its type
// parameters bound by the running frame — is recorded instead, as a
// composite literal records its own type.
func (r *Runner) bashPPConvertBoxedType(expr syntax.BashPPExpr, actual syntax.BashPPTypeExpr, name string) syntax.BashPPTypeExpr {
	if !r.bashPPGoSource {
		return actual
	}
	for {
		paren, ok := expr.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	convert, ok := expr.(*syntax.BashPPConvertExpr)
	if !ok {
		return actual
	}
	target, ok := convert.ConvTypeExpr.(*syntax.BashPPNamedType)
	if !ok || target.Name == nil || target.Name.Value != name || !target.Name.Pos().IsValid() {
		return actual
	}
	if len(target.TypeArgs) == 0 {
		cache := r.bashPPTools.localTypes
		if cache == nil {
			return actual
		}
		if _, nested := r.bashPPNestTypeName(target, cache.nest, cache.scoped, r.bashPPTypeParamArgs); !nested {
			return actual
		}
	}
	return bashPPSubstituteType(target, r.bashPPTypeParamArgs)
}

// bashPPNestOwnReference reports whether named resolves to one of the
// function's own local declarations (keyed as bashPPScopedLocalKey).
func (r *Runner) bashPPNestOwnReference(named *syntax.BashPPNamedType, own map[string]bool) bool {
	scope, known := r.goSourceLocalTypeScope(named)
	return known && scope != "" && own[bashPPScopedLocalKey(named.Name.Value, scope)]
}
