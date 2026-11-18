package interp

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
//
// Original locally declared named types are materialised in the dependency
// helper as real Go declarations, so an imported call such as
// fmt.Println(Vertex{1, 2}) receives a value with the original field names,
// field types and defined type name. The helper never contains an original
// method body: a local String/Error method is mirrored by a generated stub
// whose whole body asks the interpreter to run the original body.

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPLocalMethod is one interface method the dependency must be able to
// invoke on a materialised local type. String/Error, exact Read([]byte) (int, error)
// and the image.Image method set are mirrored; the mirror never carries the
// original body.
//
// Params and Results are the helper Go source spellings of the reviewed
// signature, already flattened one entry per value. They are empty for the
// fixed String/Error and Read protocol stubs, which have their own hand-written
// mirrors; a method carrying them is emitted by the generalised stub instead.
type bashPPLocalMethod struct {
	Name              string
	Pointer           bool
	Variadic          bool
	ReaderLocalBuffer bool
	Params            []string
	Results           []string
	// Receiver is the generic receiver spelling of a stub emitted on a real
	// generic helper declaration — `X[T]` — with the method's own receiver
	// type-parameter names, which its Params and Results mention. Empty for
	// a stub on a concrete type.
	Receiver string
	// General marks a method mirrored by the generalised stub regardless of
	// arity, so a zero-parameter zero-result method is not mistaken for the
	// fixed one-string protocol stubs.
	General bool
	// refs are the local names this mirrored signature mentions; host-only,
	// used to drop a mirror whose types the helper does not materialise
	// without dropping the type that owns it.
	refs map[string]bool
}

// bashPPLocalType is the transportable descriptor of one original named type.
// Decl is the generated Go type body; it is structural, so the dependency's
// reflect view has exactly the original field names and element types.
type bashPPLocalType struct {
	Identity *syntax.BashPPTypeIdentity
	Name     string
	// PublicType is the real helper type expression for a package-level
	// generic instantiation. Name remains its private registry key.
	PublicType  string
	GenericDecl string
	// GenericMethods are the mirrored method stubs emitted beside GenericDecl
	// on the real generic declaration; each carries its generic Receiver.
	GenericMethods []bashPPLocalMethod
	Alias          bool
	WireType       string
	Decl           string
	Methods        []bashPPLocalMethod
	OmittedMethods []string
	// Callback overrides the selector base of mirrored method stubs. An
	// instantiated generic type is materialised under a generated name, but
	// its original method bodies live on the base generic declaration, which
	// is how the interpreter's method table knows them.
	Callback string
	// Nest is the helper source of the functions that mirror local types
	// declared inside generic functions (bashpp_s275_nested_local_types.go),
	// emitted before every other declaration.
	Nest string
	// refs are the other local names this rendered declaration mentions;
	// host-only, used to keep the materialised set dependency-closed.
	refs map[string]bool
}

// bashPPHelperReserved are identifiers the fixed helper template already
// defines. An original type using one of these names is left unregistered
// rather than silently renamed, so the failure stays honest.
var bashPPHelperReserved = map[string]bool{
	"pinHandleValue": true, "acceptHandleReply": true,
	"handleOwner": true, "ownHandleLocked": true, "dropHandle": true, "dropHandleLocked": true,
	"releaseHandles": true, "exportHandleValue": true, "exportHandles": true,
	"releaseOrigins": true, "originPointerAddrs": true, "typedPointerOriginKeys": true,
	"pointeeBytes": true, "bytesValue": true, "bytePointeeUpdate": true,
	"value": true, "bppProtocolEntry": true, "request": true, "response": true,
	"originalCallbackPanic": true, "originalPointers": true, "symbols": true, "types": true, "handles": true, "callbacks": true,
	"bppTextTemplate": true, "bppTemplateParse": true, "primitiveTemplateTree": true,
	"sliceBuffer": true, "localTypeKey": true, "localStructCodec": true, "localStructCodecs": true,
	"outbound": true, "sendMessage": true, "bppOutBatch": true, "bppOutQueue": true, "flushOutQueue": true, "failure": true, "encode": true, "structural": true,
	"decode": true, "resolveType": true, "typeID": true, "access": true,
	"dispatch": true, "main": true, "callback": true, "callbackFailed": true,
	"takeFailure": true, "bridgeAddress": true, "bridgeAuth": true,
	"originalTypeIdentity": true, "originalTypeIdentities": true, "originalTypeString": true, "hasOriginalTypeIdentity": true,
	"mirroredMethodSet": true, "mirroredMethodTypes": true, "mirroredMethodExpression": true,
}

// bashPPLocalScalarTypes are the predeclared names the helper's base type
// registry already resolves, so a named type over one of them renders as-is.
var bashPPLocalScalarTypes = map[string]bool{
	"bool": true, "string": true, "error": true, "any": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "float32": true, "float64": true, "rune": true, "byte": true,
	"complex64": true, "complex128": true,
}

// Immutable descriptors are shared with copied toolchains. Both the syntax
// file and import bindings determine their identity; a new file or changed
// import must be rebuilt so the native session still rejects namespace drift.
type bashPPLocalTypeCache struct {
	file    *syntax.File
	imports map[string]string
	types   []bashPPLocalType
	// scoped maps a function-local declaration of a reused name
	// (bashPPScopedLocalKey) to the helper identity it is registered
	// under; see bashpp_s243_scoped_local_types.go.
	scoped map[string]string
	// nest maps a local declaration of a mirrored function to its
	// enclosing function's type parameters; see
	// bashpp_s275_nested_local_types.go.
	nest map[string]bashPPNestDecl
}

// bashPPLocalTypeDescriptors renders every original named type the helper can
// mirror faithfully. The set is read from the loaded program rather than from
// declarations executed so far, because the dependency session starts as soon
// as the imports do — before the original type declarations run — and its
// materialised type namespace is fixed for the life of that session.
//
// Package declarations, aliases, uniquely named local declarations, and
// anonymous shapes retain their actual Go identity. Reused local names are
// registered by lexical declaration identity, including inside anonymous shapes.
// Shapes which cannot be expressed faithfully remain unregistered.
func (r *Runner) bashPPLocalTypeDescriptors() []bashPPLocalType {
	if !r.bashPPGoSource || r.bashPPGoSourceFile == nil {
		return nil
	}
	if cache := r.bashPPTools.localTypes; cache != nil && cache.file == r.bashPPGoSourceFile && maps.Equal(cache.imports, r.bashPPImports) {
		return cache.types
	}
	types, scoped := r.bashPPBuildLocalTypeDescriptors()
	nest, mirror := r.bashPPNestMirror(types, scoped)
	if mirror != "" {
		types = append([]bashPPLocalType{{Name: bashPPNestMirrorName, Decl: "struct{}", Nest: mirror}}, types...)
	}
	r.bashPPTools.localTypes = &bashPPLocalTypeCache{file: r.bashPPGoSourceFile, imports: maps.Clone(r.bashPPImports), types: types, scoped: scoped, nest: nest}
	return types
}

func (r *Runner) bashPPBuildLocalTypeDescriptors() ([]bashPPLocalType, map[string]string) {
	// A public generic whose materialisation does not survive closure — its
	// constraint or a stub signature names a type the helper cannot declare —
	// is withdrawn and the namespace rebuilt with its instantiations
	// materialised concretely instead, exactly as before it was public.
	withdrawn := map[string]bool{}
	for {
		types, scoped, dropped := r.bashPPBuildLocalTypeDescriptorsWithout(withdrawn)
		if len(dropped) == 0 {
			return types, scoped
		}
		for name := range dropped {
			withdrawn[name] = true
		}
	}
}

func (r *Runner) bashPPBuildLocalTypeDescriptorsWithout(withdrawn map[string]bool) ([]bashPPLocalType, map[string]string, map[string]bool) {
	index := r.bashPPLocalTypeDeclarationIndex()
	declared := map[string]syntax.BashPPTypeExpr{}
	identities := map[string]*syntax.BashPPTypeIdentity{}
	aliases := map[string]bool{}
	ambiguous := map[string]bool{}
	generics := map[string]*syntax.BashPPDecl{}
	instantiations := map[string]*syntax.BashPPNamedType{}
	for _, d := range index.typeDecls {
		if d.Name.Value == "_" {
			// A blank declaration introduces no type name to register. Keep
			// its anonymous shapes, which the immutable index collected too.
			continue
		}
		if len(d.TypeParams) == 0 {
			name := d.Name.Value
			if _, exists := declared[name]; exists {
				ambiguous[name] = true
			}
			declared[name] = d.DeclTypeExpr
			identities[name] = d.GoTypeIdentity
			aliases[name] = d.Alias
		}
		if len(d.TypeParams) > 0 {
			if _, exists := generics[d.Name.Value]; exists {
				ambiguous[d.Name.Value] = true
			}
			identities[d.Name.Value] = d.GoTypeIdentity
			generics[d.Name.Value] = d
		}
	}
	// A name used in two scopes denotes distinct Go types even when their
	// fields are identical. A package-level declaration keeps the plain
	// name; each function-local declaration is registered under its own
	// identity and a reference is spelled by the declaration it resolves
	// to (bashpp_s243_scoped_local_types.go). A generic reused name is
	// scoped the same way unless it is declared in a generic function.
	scopedDecls, packageLevel := bashPPScopedLocalDecls(index, ambiguous)
	scopedNames := map[string]string{}
	for key := range scopedDecls {
		scopedNames[key] = bashPPScopedLocalName(key)
		if d := scopedDecls[key].decl; len(d.TypeParams) > 0 {
			generics[scopedNames[key]] = d
		}
	}
	for name := range ambiguous {
		delete(declared, name)
		delete(generics, name)
		if d := packageLevel[name]; d != nil && !bashPPHelperReserved[name] {
			if len(d.TypeParams) > 0 {
				generics[name] = d
			} else if len(d.TypeParams) == 0 {
				declared[name] = d.DeclTypeExpr
				aliases[name] = d.Alias
			}
		}
	}
	// A package-level type named like one of the helper's own identifiers
	// (`request`, `value`, `response`, ...) cannot be declared under its
	// name in the helper. It is registered under a generated identity keyed
	// by the package scope "", and every reference spells that identity, the
	// way a reused local name is (bashpp_s290_reserved_type_names.go).
	reservedPackage, reservedDecls := index.reservedNames, index.reservedDecls
	for _, name := range reservedPackage {
		key := bashPPScopedLocalKey(name, "")
		scopedNames[key] = bashPPScopedLocalName(key)
	}
	resolveScoped := func(named *syntax.BashPPNamedType) (string, bool) {
		if len(scopedNames) == 0 || named.Name == nil {
			return "", false
		}
		scope, known := r.goSourceLocalTypeScope(named)
		if !known {
			return "", false
		}
		name, ok := scopedNames[bashPPScopedLocalKey(named.Name.Value, scope)]
		return name, ok
	}
	// An instantiation is keyed by the spelling the interpreter transports:
	// a type argument naming a function-local declaration of a reused name
	// is spelled by that declaration's identity, so `T[Int]` in two scopes
	// with two local `Int`s is two instantiations.
	for _, named := range index.spelled {
		wire := bashPPTypeText(named)
		if scoped := bashPPBridgeTypeTextIn(named, resolveScoped); scoped != bashPPBridgeTypeText(named) {
			wire = scoped
		}
		instantiations[wire] = named
	}
	// The instantiations the program only reaches — through a generic
	// function's result, a generic method body, a nested instantiation —
	// carry the same run-time identity as the ones it spells; see
	// bashpp_sprint165_runtime_instantiations.go.
	for wire, named := range r.bashPPReachedInstantiations() {
		if _, spelled := instantiations[wire]; !spelled {
			instantiations[wire] = named
		}
	}
	methods := index.methods
	names := make([]string, 0, len(declared))
	for name := range declared {
		// Go lets a program redefine a predeclared name. The helper's base
		// registry resolves those names to the predeclared types, so nothing
		// here would be faithful; materialise nothing rather than a namespace
		// that silently means something else.
		if bashPPLocalScalarTypes[name] {
			return nil, nil, nil
		}
		names = append(names, name)
	}
	for key := range scopedDecls {
		if bashPPLocalScalarTypes[scopedDecls[key].decl.Name.Value] {
			return nil, nil, nil
		}
	}
	sort.Strings(names)
	local := &bashPPLocalTypeSet{declared: declared, imports: r.bashPPImports, generics: generics, identities: identities, resolve: resolveScoped}
	// A package-level defined generic whose declaration, constraints and
	// mirrored method stubs are all expressible is materialised as itself:
	// its instantiations are aliases of the real generic instance, so fmt
	// and reflect see X[Arg], an embedding keeps the field name X and the
	// promoted method set is the helper's own Go semantics. Every public
	// generic is settled before any declaration renders, so an embedded
	// instantiation in another declaration can rely on it.
	local.public = map[string]bool{}
	genericNames := make([]string, 0, len(index.packageGenerics))
	for name := range index.packageGenerics {
		genericNames = append(genericNames, name)
	}
	sort.Strings(genericNames)
	genericDecls := map[string]string{}
	genericMethods := map[string][]bashPPLocalMethod{}
	genericRefs := map[string]map[string]bool{}
	for _, name := range genericNames {
		base := index.packageGenerics[name]
		if base.Alias || generics[name] != base || bashPPHelperReserved[name] || withdrawn[name] {
			continue
		}
		local.public[name] = true
	}
	for settled := false; !settled; {
		settled = true
		for _, name := range genericNames {
			if !local.public[name] {
				continue
			}
			base := index.packageGenerics[name]
			refs := map[string]bool{}
			local.refs = refs
			decl, ok := local.genericHeader(base)
			var stubs []bashPPLocalMethod
			if ok {
				stubs, ok = local.genericStubs(base, methods[name], refs)
			}
			local.refs = nil
			if !ok {
				// Withdrawing one public generic changes how the others
				// spell it, so the set is recomputed until it is stable.
				delete(local.public, name)
				settled = false
				continue
			}
			genericDecls[name], genericMethods[name], genericRefs[name] = decl, stubs, refs
		}
	}
	var out []bashPPLocalType
	// Each function-local declaration of a reused name is its own helper
	// type. Such a declaration cannot carry methods (Go declares methods
	// at package level only), so nothing is mirrored for it.
	scopedKeys := make([]string, 0, len(scopedDecls))
	for key := range scopedDecls {
		scopedKeys = append(scopedKeys, key)
	}
	sort.Strings(scopedKeys)
	for _, key := range scopedKeys {
		d := scopedDecls[key].decl
		if len(d.TypeParams) > 0 {
			continue
		}
		local.refs = map[string]bool{}
		decl, ok := local.source(d.DeclTypeExpr, 0)
		if !ok {
			continue
		}
		out = append(out, bashPPLocalType{Name: scopedNames[key], Identity: d.GoTypeIdentity, Decl: decl, Alias: d.Alias, refs: local.refs})
	}
	for _, name := range reservedPackage {
		d := reservedDecls[name]
		local.refs = map[string]bool{}
		decl, ok := local.source(d.DeclTypeExpr, 0)
		if !ok {
			continue
		}
		materialised := bashPPLocalType{Name: scopedNames[bashPPScopedLocalKey(name, "")], Identity: d.GoTypeIdentity, Decl: decl, Callback: name, refs: local.refs}
		if decl != "any" && !strings.HasPrefix(decl, "interface") {
			materialised.Methods = local.mirrored(methods[name])
			for _, method := range methods[name] {
				mirrored := false
				for _, m := range materialised.Methods {
					if m.Name == method.Name.Value {
						mirrored = true
					}
				}
				if !mirrored {
					materialised.OmittedMethods = append(materialised.OmittedMethods, method.Name.Value)
				}
			}
		}
		out = append(out, materialised)
	}
	for _, name := range names {
		if bashPPHelperReserved[name] {
			continue
		}
		local.refs = map[string]bool{}
		decl, ok := local.source(declared[name], 0)
		if !ok {
			continue
		}
		materialised := bashPPLocalType{Name: name, Identity: identities[name], Decl: decl, Alias: aliases[name], refs: local.refs}
		// A defined interface type cannot carry a method declaration, so its
		// implementations are mirrored instead — the dynamic value is what
		// crosses the boundary.
		if !materialised.Alias && decl != "any" && !strings.HasPrefix(decl, "interface") {
			materialised.Methods = local.mirrored(methods[name])
			for _, method := range methods[name] {
				mirrored := false
				for _, m := range materialised.Methods {
					if m.Name == method.Name.Value {
						mirrored = true
					}
				}
				if !mirrored {
					materialised.OmittedMethods = append(materialised.OmittedMethods, method.Name.Value)
				}
			}
		}
		out = append(out, materialised)
	}
	// Anonymous shapes keep their Go identity through aliases, never invented
	// defined types. Existing typed codecs provide legal private field access.
	shapes := map[string]syntax.BashPPTypeExpr{}
	for _, shape := range index.anonymous {
		// Use the same scoped spelling as value transport and descriptor source.
		// Two identical-looking shapes may contain distinct local named types.
		shapes[bashPPBridgeTypeTextIn(shape, resolveScoped)] = shape
	}
	keys := make([]string, 0, len(shapes))
	for key := range shapes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		local.refs = map[string]bool{}
		decl, ok := local.source(shapes[key], 0)
		if !ok {
			continue
		}
		// Field tracking requires a defined struct type. An anonymous
		// descriptor containing a tracked field cannot be emitted as a Go
		// alias; named declarations of the same shape are handled above.
		if strings.Contains(decl, `go:"track"`) {
			continue
		}
		name := fmt.Sprintf("bppAnonymous_%x", sha256.Sum256([]byte(key)))
		for declared[name] != nil || bashPPHelperReserved[name] {
			name += "_"
		}
		out = append(out, bashPPLocalType{Name: name, Decl: decl, Alias: true, WireType: key, refs: local.refs})
	}
	// An instantiated local generic type is materialised under a generated
	// name: the generic body rendered with the instantiation's type arguments
	// substituted, registered under the original instantiation spelling so
	// transported values resolve. Mirrored method stubs call back through the
	// base generic name, which is how the interpreter's method table knows the
	// original bodies. The generated identity means %T does not report the
	// original instantiated spelling; that fidelity gap is recorded, not
	// silently wrong data.
	wires := make([]string, 0, len(instantiations))
	for wire := range instantiations {
		wires = append(wires, wire)
	}
	sort.Strings(wires)
	for _, wire := range wires {
		named := instantiations[wire]
		base := generics[named.Name.Value]
		if base == nil {
			// A scoped generic's public identity is its plain spelling
			// (bashPPTypeText), which is gc's only when every type argument
			// spells as reflect does; any other instantiation stays refused.
			if scoped, ok := resolveScoped(named); ok && bashPPPredeclaredTypeArgs(named) {
				base = generics[scoped]
			}
		}
		if base == nil {
			continue
		}
		var params []string
		for _, group := range base.TypeParams {
			for _, name := range group.Names {
				params = append(params, name.Value)
			}
		}
		if len(params) != len(named.TypeArgs) {
			continue
		}
		subst := map[string]string{}
		expressible := true
		// The argument spellings' own local references belong to this
		// instantiation, not to whichever descriptor was rendered last: a
		// shared refs map would attribute them to the previous entry and
		// drop it from the dependency-closed set for a name it never
		// mentioned.
		local.refs = map[string]bool{}
		for i, arg := range named.TypeArgs {
			rendered, ok := local.source(arg.ArgType, 0)
			if !ok {
				expressible = false
				break
			}
			subst[params[i]] = rendered
		}
		if !expressible {
			continue
		}
		local.subst = subst
		local.instantiated = true
		decl, ok := local.source(base.DeclTypeExpr, 0)
		if ok && (decl == "any" || strings.HasPrefix(decl, "interface")) {
			ok = false
		}
		var mirrored []bashPPLocalMethod
		if ok {
			mirrored = local.mirrored(methods[named.Name.Value])
		}
		local.subst = nil
		local.instantiated = false
		if !ok {
			continue
		}
		name := local.instanceName(wire)
		identity := base.GoTypeIdentity
		if identity != nil {
			public := *identity
			// gc separates type arguments with a bare comma.
			args := make([]string, len(named.TypeArgs))
			for i, arg := range named.TypeArgs {
				args[i] = local.identityTypeText(arg.ArgType)
			}
			public.Name = named.Name.Value + "[" + strings.Join(args, ",") + "]"
			identity = &public
		}
		// An instantiated generic alias has the identity of its substituted
		// target, not of its declaration. It still needs a registry entry under
		// the linked package's flattened spelling: that is the spelling the
		// interpreter transports to a dependency helper. Mark it as an alias so
		// it cannot overwrite the target's reflection identity.
		materialised := bashPPLocalType{Name: name, Identity: identity, Alias: base.Alias, Decl: decl, WireType: wire, Callback: named.Name.Value, Methods: mirrored, refs: local.refs}
		// A package-level defined generic with no mirrored methods can retain its
		// actual declaration in the helper. The generated name is then only an
		// alias and registry key, so fmt and reflect see F[Arg], never the key.
		// Generic aliases deliberately retain the structural path repaired by
		// 15455fc6; local generics retain their lexical-identity materialisation.
		if local.public[named.Name.Value] && index.packageGenerics[named.Name.Value] == base {
			if _, publicType, ok := local.genericDeclaration(base, named); ok {
				materialised.PublicType = publicType
				// The instance mirrors exactly the stubs the generic
				// declaration carries; anything else is omitted below.
				stubbed := map[string]bool{}
				for _, m := range genericMethods[named.Name.Value] {
					stubbed[m.Name] = true
				}
				kept := materialised.Methods[:0:0]
				for _, m := range materialised.Methods {
					if stubbed[m.Name] {
						kept = append(kept, m)
					}
				}
				materialised.Methods = kept
				for ref := range genericRefs[named.Name.Value] {
					materialised.refs[ref] = true
				}
			}
		}
		for _, method := range methods[named.Name.Value] {
			seen := false
			for _, m := range materialised.Methods {
				if m.Name == method.Name.Value {
					seen = true
				}
			}
			if !seen {
				materialised.OmittedMethods = append(materialised.OmittedMethods, method.Name.Value)
			}
		}
		out = append(out, materialised)
	}
	// The materialised set must be dependency-closed: a declaration naming a
	// local type that itself stays unregistered — generic, ambiguous, or any
	// other refusal — would not compile in the helper. Dropping it cascades
	// until every remaining declaration mentions only emitted names.
	emitted := map[string]bool{}
	for _, materialised := range out {
		emitted[materialised.Name] = true
	}
	droppedPublic := map[string]bool{}
	for changed := true; changed; {
		changed = false
		kept := out[:0]
		for _, materialised := range out {
			closed := true
			for ref := range materialised.refs {
				if !emitted[ref] {
					closed = false
					break
				}
			}
			if !closed {
				delete(emitted, materialised.Name)
				changed = true
				if materialised.PublicType != "" && local.public[materialised.Callback] {
					droppedPublic[materialised.Callback] = true
				}
				continue
			}
			kept = append(kept, materialised)
		}
		out = kept
	}
	if len(droppedPublic) > 0 {
		return nil, nil, droppedPublic
	}
	// Each public generic is declared once, beside the first of its
	// instantiations that survived closure; the others are aliases of it.
	declaredGeneric := map[string]bool{}
	for i := range out {
		name := out[i].Callback
		if out[i].PublicType == "" || declaredGeneric[name] || genericDecls[name] == "" {
			continue
		}
		out[i].GenericDecl, out[i].GenericMethods = genericDecls[name], genericMethods[name]
		declaredGeneric[name] = true
	}
	// A scoped identity whose declaration was dropped must not be spelled
	// either: the reference falls back to the plain name and is refused.
	for key, name := range scopedNames {
		registered := emitted[name]
		if !registered {
			prefix := name + "["
			for _, materialised := range out {
				if strings.HasPrefix(materialised.WireType, prefix) {
					registered = true
					break
				}
			}
		}
		if !registered {
			delete(scopedNames, key)
		}
	}
	// A generally mirrored signature may name local types of its own; one that
	// names an unmaterialised type cannot compile in the helper. The mirror is
	// dropped back into OmittedMethods — the owning type stays materialised.
	for i := range out {
		methods := out[i].Methods[:0]
		for _, method := range out[i].Methods {
			closed := true
			for ref := range method.refs {
				if !emitted[ref] {
					closed = false
					break
				}
			}
			if closed {
				methods = append(methods, method)
			} else {
				out[i].OmittedMethods = append(out[i].OmittedMethods, method.Name)
			}
		}
		out[i].Methods = methods
	}
	return out, scopedNames, nil
}

// genericHeader renders a package-level generic declaration alone, without
// an instantiation of it.
func (l *bashPPLocalTypeSet) genericHeader(base *syntax.BashPPDecl) (string, bool) {
	var args []*syntax.BashPPTypeArg
	for _, group := range base.TypeParams {
		if group == nil {
			return "", false
		}
		for range group.Names {
			args = append(args, &syntax.BashPPTypeArg{ArgType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}}})
		}
	}
	decl, _, ok := l.genericDeclaration(base, &syntax.BashPPNamedType{Name: base.Name, TypeArgs: args})
	return decl, ok
}

// genericDeclaration renders a package-level generic declaration and one
// concrete instantiation. It succeeds only when the constraints, body, and
// arguments are all expressible exactly in the native helper.
func (l *bashPPLocalTypeSet) genericDeclaration(base *syntax.BashPPDecl, instance *syntax.BashPPNamedType) (string, string, bool) {
	if base == nil || base.Name == nil || len(base.TypeParams) == 0 || len(instance.TypeArgs) == 0 {
		return "", "", false
	}
	bindings := make(map[string]string)
	var groups []string
	for _, group := range base.TypeParams {
		if group == nil || len(group.Names) == 0 || group.Constraint == nil {
			return "", "", false
		}
		for _, name := range group.Names {
			bindings[name.Value] = name.Value
		}
	}
	oldSubst, oldMode := l.subst, l.genericMode
	l.subst, l.genericMode = bindings, true
	defer func() { l.subst, l.genericMode = oldSubst, oldMode }()
	for _, group := range base.TypeParams {
		constraint, ok := l.source(group.Constraint, 0)
		if !ok {
			return "", "", false
		}
		names := make([]string, len(group.Names))
		for i, name := range group.Names {
			names[i] = name.Value
		}
		groups = append(groups, strings.Join(names, ", ")+" "+constraint)
	}
	body, ok := l.source(base.DeclTypeExpr, 0)
	if !ok {
		return "", "", false
	}
	l.subst, l.genericMode = oldSubst, oldMode
	args := make([]string, len(instance.TypeArgs))
	for i, arg := range instance.TypeArgs {
		rendered, ok := l.source(arg.ArgType, 0)
		if !ok {
			return "", "", false
		}
		args[i] = rendered
	}
	if len(args) != len(bindings) {
		return "", "", false
	}
	name := base.Name.Value
	return "type " + name + "[" + strings.Join(groups, ", ") + "] " + body + "\n", name + "[" + strings.Join(args, ", ") + "]", true
}

// bashPPLocalTypeSet is the original program's own type namespace as the
// helper will materialise it.
type bashPPLocalTypeSet struct {
	declared map[string]syntax.BashPPTypeExpr
	imports  map[string]string
	// identities are the source package identities of package-level
	// declarations. They are used only for public reflection strings, not for
	// helper source or transport registry names.
	identities map[string]*syntax.BashPPTypeIdentity
	// generics are the local generic type declarations, so an instantiated
	// spelling inside a rendered body resolves to its materialised name.
	generics map[string]*syntax.BashPPDecl
	// refs records every declared local name the current rendering mentions,
	// so a declaration is only emitted when everything it names is too.
	refs map[string]bool
	// subst maps a generic declaration's type-parameter names to the rendered
	// argument spellings of one instantiation while its body is rendered.
	subst map[string]string
	// instantiated marks that mirrored may accept receivers with type
	// parameters: every parameter is bound by the instantiation, and only the
	// fixed-signature String/Error/Read stubs are mirrored for them.
	instantiated bool
	// public names the package-level generic declarations the helper
	// materialises as real generic declarations, so an embedded
	// instantiation keeps its promoted field name and an instantiation
	// spelled inside a generic rendering keeps the public spelling.
	public map[string]bool
	// genericMode marks a rendering of a public generic declaration or its
	// method stubs, where type-parameter spellings stay symbolic.
	genericMode bool
	// resolve spells a reference to a function-local declaration of a
	// reused name by its own helper identity; nil or false keeps the plain
	// name (bashpp_s243_scoped_local_types.go).
	resolve func(*syntax.BashPPNamedType) (string, bool)
}

// identityTypeText renders a type argument the way gc includes it in the
// reflection name of an instantiated defined type. Unlike helper source text,
// named package types are package-qualified in this public identity string.
func (l *bashPPLocalTypeSet) identityTypeText(typ syntax.BashPPTypeExpr) string {
	switch t := typ.(type) {
	case *syntax.BashPPNamedType:
		base := t.Name.Value
		if id := l.identities[base]; id != nil {
			base = id.PackageName + "." + id.Name
		}
		if len(t.TypeArgs) == 0 {
			return base
		}
		args := make([]string, len(t.TypeArgs))
		for i, arg := range t.TypeArgs {
			args[i] = l.identityTypeText(arg.ArgType)
		}
		return base + "[" + strings.Join(args, ",") + "]"
	case *syntax.BashPPPointerType:
		return "*" + l.identityTypeText(t.Element)
	case *syntax.BashPPCollectionType:
		if t.Kind == "map" {
			return "map[" + l.identityTypeText(t.Key) + "]" + l.identityTypeText(t.Element)
		}
		length := ""
		if t.Length != nil {
			length = t.Length.Value
		}
		return "[" + length + "]" + l.identityTypeText(t.Element)
	case *syntax.BashPPChanType:
		prefix := "chan "
		if t.Direction == "send" {
			prefix = "chan<- "
		} else if t.Direction == "recv" {
			prefix = "<-chan "
		}
		if t.Element == nil {
			if t.Elem != nil {
				return prefix + t.Elem.Value
			}
			return prefix
		}
		return prefix + l.identityTypeText(t.Element)
	}
	return bashPPTypeText(typ)
}

// embeddedInstantiation reports an embedded field spelled as a local generic
// instantiation, directly or through a pointer.
func (l *bashPPLocalTypeSet) embeddedInstantiation(typ syntax.BashPPTypeExpr) bool {
	if ptr, ok := typ.(*syntax.BashPPPointerType); ok {
		typ = ptr.Element
	}
	named, ok := typ.(*syntax.BashPPNamedType)
	return ok && named.Name != nil && len(named.TypeArgs) > 0 && l.generics[named.Name.Value] != nil
}

// embeddedPublic reports an embedded instantiation of a generic the helper
// declares as itself: instanceRef spells it `X[args]`, so the helper's
// embedded field is named X exactly as the interpreter's promoted field is.
func (l *bashPPLocalTypeSet) embeddedPublic(typ syntax.BashPPTypeExpr) bool {
	if ptr, ok := typ.(*syntax.BashPPPointerType); ok {
		typ = ptr.Element
	}
	named, ok := typ.(*syntax.BashPPNamedType)
	return ok && named.Name != nil && l.public[named.Name.Value]
}

// genericStubs renders the mirrored method stubs of a public generic
// declaration once, over symbolic receiver type parameters. Each receiver
// type parameter is renamed positionally to a generated identifier, so a
// program parameter named like a helper identifier (value, reflect) cannot
// shadow the stub's own protocol code. Methods are grouped by their receiver
// spelling, since each group binds its own names. It reports false when a
// receiver cannot be spelled, and records every local name the stubs mention
// in refs.
func (l *bashPPLocalTypeSet) genericStubs(base *syntax.BashPPDecl, decls []*syntax.BashPPFuncDecl, refs map[string]bool) ([]bashPPLocalMethod, bool) {
	arity := 0
	for _, group := range base.TypeParams {
		arity += len(group.Names)
	}
	fresh := make([]string, arity)
	for i := range fresh {
		fresh[i] = fmt.Sprintf("bppTypeParam%d", i)
		for l.declared[fresh[i]] != nil || l.generics[fresh[i]] != nil {
			fresh[i] += "_"
		}
	}
	groups := map[string][]*syntax.BashPPFuncDecl{}
	var keys []string
	for _, decl := range decls {
		if decl.Receiver == nil || len(decl.Receiver.TypeParams) != arity {
			return nil, false
		}
		names := make([]string, arity)
		for i, param := range decl.Receiver.TypeParams {
			if param == nil {
				return nil, false
			}
			names[i] = param.Value
		}
		key := strings.Join(names, ",")
		if _, seen := groups[key]; !seen {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], decl)
	}
	sort.Strings(keys)
	receiver := base.Name.Value + "[" + strings.Join(fresh, ", ") + "]"
	var methods []bashPPLocalMethod
	seen := map[string]bool{}
	oldSubst, oldInstantiated, oldMode, oldRefs := l.subst, l.instantiated, l.genericMode, l.refs
	defer func() { l.subst, l.instantiated, l.genericMode, l.refs = oldSubst, oldInstantiated, oldMode, oldRefs }()
	for _, key := range keys {
		bindings := map[string]string{}
		for i, name := range strings.Split(key, ",") {
			if name != "_" {
				bindings[name] = fresh[i]
			}
		}
		l.subst, l.instantiated, l.genericMode, l.refs = bindings, true, true, refs
		for _, method := range l.mirrored(groups[key]) {
			if seen[method.Name] {
				continue
			}
			seen[method.Name] = true
			method.Receiver = receiver
			for ref := range method.refs {
				refs[ref] = true
			}
			methods = append(methods, method)
		}
	}
	return methods, true
}

// instanceName is the generated helper name of one instantiation spelling.
func (l *bashPPLocalTypeSet) instanceName(wire string) string {
	name := fmt.Sprintf("bppInstance_%x", sha256.Sum256([]byte(wire)))
	for l.declared[name] != nil || bashPPHelperReserved[name] {
		name += "_"
	}
	return name
}

// instanceRef renders an instantiated local generic type — `Box[T]` inside
// a generic body, `Wrap[Box[int]]` as a type argument — as the generated
// name its own materialisation carries, with the bindings in force applied
// to the arguments. The reference keeps the materialised set
// dependency-closed: a spelling whose instantiation is not itself emitted
// drops the declaration that names it.
func (l *bashPPLocalTypeSet) instanceRef(t *syntax.BashPPNamedType, depth int) (string, bool) {
	if t.Name == nil || l.generics[t.Name.Value] == nil {
		return "", false
	}
	args := make([]string, len(t.TypeArgs))
	for i, arg := range t.TypeArgs {
		rendered, ok := l.source(arg.ArgType, depth+1)
		if !ok {
			return "", false
		}
		args[i] = rendered
	}
	name := l.instanceName(t.Name.Value + "[" + strings.Join(args, ", ") + "]")
	if l.public[t.Name.Value] {
		// A real generic declaration is spelled as itself. Inside a
		// generic rendering the arguments may be type parameters, which
		// name no materialised instance; a concrete spelling still keeps
		// the instance it denotes in the dependency-closed set.
		if !l.genericMode && l.refs != nil {
			l.refs[name] = true
		}
		return t.Name.Value + "[" + strings.Join(args, ", ") + "]", true
	}
	if l.refs != nil {
		l.refs[name] = true
	}
	return name, true
}

// mirrored reports the method set the helper stubs out. String/Error and Read
// are mirrored by fixed protocol stubs; each original body runs in the interpreter.
func (l *bashPPLocalTypeSet) mirrored(decls []*syntax.BashPPFuncDecl) []bashPPLocalMethod {
	var methods []bashPPLocalMethod
	for _, want := range []string{"Error", "String"} {
		for _, decl := range decls {
			if decl.Name == nil || decl.Name.Value != want {
				continue
			}
			if len(decl.TypeParams) > 0 || (len(decl.Receiver.TypeParams) > 0 && !l.instantiated) || len(decl.Params) > 0 {
				continue
			}
			if len(decl.Results) != 1 || len(decl.Results[0].Names) > 1 {
				continue
			}
			if text, ok := l.source(decl.Results[0].FieldTypeExpr, 0); !ok || text != "string" {
				continue
			}
			methods = append(methods, bashPPLocalMethod{Name: want, Pointer: decl.Receiver.Pointer})
			break
		}
	}
	methods = append(methods, l.mirroredUnwrap(decls)...)
	methods = append(methods, l.mirroredImage(decls)...)
	for _, decl := range decls {
		if decl.Name == nil || decl.Name.Value != "Read" || len(decl.TypeParams) > 0 || (len(decl.Receiver.TypeParams) > 0 && !l.instantiated) || len(decl.Params) != 1 || len(decl.Params[0].Names) > 1 || decl.Params[0].Variadic() || len(decl.Results) != 2 {
			continue
		}
		param, ok := l.source(decl.Params[0].FieldTypeExpr, 0)
		if !ok || (param != "[]byte" && param != "[]uint8") {
			continue
		}
		a, ok := l.source(decl.Results[0].FieldTypeExpr, 0)
		if !ok || a != "int" || len(decl.Results[0].Names) > 1 {
			continue
		}
		b, ok := l.source(decl.Results[1].FieldTypeExpr, 0)
		if !ok || b != "error" || len(decl.Results[1].Names) > 1 {
			continue
		}
		methods = append(methods, bashPPLocalMethod{Name: "Read", Pointer: decl.Receiver.Pointer, ReaderLocalBuffer: bashPPReaderLocalBufferProof(decl)})
		break
	}
	// Every remaining method with an expressible signature is
	// mirrored by the generalised stub — exported or not, any result arity —
	// so the materialised type presents the original method set to reflect
	// and the dependency can invoke any of them through the callback. Each
	// signature's own local-name references are recorded separately: a mirror
	// naming an unmaterialised type is dropped later without dropping the
	// type that owns it.
	seen := map[string]bool{}
	for _, m := range methods {
		seen[m.Name] = true
	}
	shared := l.refs
	for _, decl := range decls {
		if decl.Name == nil || seen[decl.Name.Value] {
			continue
		}
		if len(decl.TypeParams) > 0 || (len(decl.Receiver.TypeParams) > 0 && !l.instantiated) {
			continue
		}
		l.refs = map[string]bool{}
		params, variadic, okParams := l.methodFieldTypes(decl.Params, true)
		results, _, okResults := l.methodFieldTypes(decl.Results, false)
		refs := l.refs
		l.refs = shared
		if !okParams || !okResults {
			continue
		}
		seen[decl.Name.Value] = true
		methods = append(methods, bashPPLocalMethod{Name: decl.Name.Value, Pointer: decl.Receiver.Pointer, Variadic: variadic, Params: params, Results: results, General: true, refs: refs})
	}
	return methods
}

// methodFieldTypes renders a parameter or result list one entry per declared
// value. A final variadic parameter is represented by its element type and the
// separate variadic result; generated stubs receive it as Go's one []T value.
func (l *bashPPLocalTypeSet) methodFieldTypes(fields []*syntax.BashPPField, allowVariadic bool) ([]string, bool, bool) {
	var out []string
	variadic := false
	for i, field := range fields {
		if field.Variadic() {
			if !allowVariadic || variadic || i != len(fields)-1 || len(field.Names) > 1 {
				return nil, false, false
			}
			variadic = true
		}
		text, ok := l.source(field.FieldTypeExpr, 0)
		if !ok {
			return nil, false, false
		}
		count := max(len(field.Names), 1)
		if field.Variadic() {
			count = 1
		}
		for range count {
			out = append(out, text)
		}
	}
	return out, variadic, true
}

// importedType reports whether typ names exactly the symbol path.name through
// one of the original program's own imports. The alias is whatever the original
// wrote; the dependency path is what is actually checked.
func (l *bashPPLocalTypeSet) importedType(typ syntax.BashPPTypeExpr, path, name string) bool {
	named, ok := typ.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil || len(named.TypeArgs) > 0 {
		return false
	}
	alias, symbol, ok := strings.Cut(named.Name.Value, ".")
	return ok && symbol == name && l.imports[alias] == path
}

// mirroredImage reports the image.Image method set, or nothing. The Go Tour
// image exercise hands an original value to pic.ShowImage and image/png then
// drives ColorModel, Bounds and At from dependency code, so all three must
// cross together: a partial mirror would present itself to the dependency as an
// image.Image it cannot actually serve. Every signature is matched exactly —
// ColorModel() color.Model, Bounds() image.Rectangle, At(x, y int) color.Color —
// and each original body still runs in the interpreter.
func (l *bashPPLocalTypeSet) mirroredImage(decls []*syntax.BashPPFuncDecl) []bashPPLocalMethod {
	found := map[string]*syntax.BashPPFuncDecl{}
	for _, decl := range decls {
		if decl.Name == nil || len(decl.TypeParams) > 0 || len(decl.Receiver.TypeParams) > 0 {
			continue
		}
		switch decl.Name.Value {
		case "ColorModel", "Bounds", "At":
			if found[decl.Name.Value] != nil {
				return nil
			}
			found[decl.Name.Value] = decl
		}
	}
	if len(found) != 3 {
		return nil
	}
	// One unnamed-or-singly-named result of exactly the reviewed imported type.
	result := func(decl *syntax.BashPPFuncDecl, path, name string) (string, bool) {
		if len(decl.Results) != 1 || len(decl.Results[0].Names) > 1 {
			return "", false
		}
		if !l.importedType(decl.Results[0].FieldTypeExpr, path, name) {
			return "", false
		}
		return l.source(decl.Results[0].FieldTypeExpr, 0)
	}
	colorModel, ok := result(found["ColorModel"], "image/color", "Model")
	if !ok || len(found["ColorModel"].Params) != 0 {
		return nil
	}
	rectangle, ok := result(found["Bounds"], "image", "Rectangle")
	if !ok || len(found["Bounds"].Params) != 0 {
		return nil
	}
	colorValue, ok := result(found["At"], "image/color", "Color")
	if !ok {
		return nil
	}
	// At takes exactly two ints, however the original spelled them: `x, y int`
	// is one field with two names, `x int, y int` is two fields with one each.
	coordinates := 0
	for _, param := range found["At"].Params {
		if param.Variadic() {
			return nil
		}
		if text, ok := l.source(param.FieldTypeExpr, 0); !ok || text != "int" {
			return nil
		}
		coordinates += max(len(param.Names), 1)
	}
	if coordinates != 2 {
		return nil
	}
	return []bashPPLocalMethod{
		{Name: "At", Pointer: found["At"].Receiver.Pointer, Params: []string{"int", "int"}, Results: []string{colorValue}},
		{Name: "Bounds", Pointer: found["Bounds"].Receiver.Pointer, Results: []string{rectangle}},
		{Name: "ColorModel", Pointer: found["ColorModel"].Receiver.Pointer, Results: []string{colorModel}},
	}
}

// bashPPLocalTypeSource renders one original type expression as helper Go
// source. It reports false for any shape the helper cannot reproduce exactly.
func (l *bashPPLocalTypeSet) source(typ syntax.BashPPTypeExpr, depth int) (string, bool) {
	if typ == nil || depth > 16 {
		return "", false
	}
	switch t := typ.(type) {
	case *syntax.BashPPTypeParamType:
		if s, ok := l.subst[t.Name.Value]; ok {
			return s, true
		}
		return "", false
	case *syntax.BashPPNamedType:
		if len(t.TypeArgs) > 0 {
			return l.instanceRef(t, depth)
		}
		name := t.Name.Value
		if s, ok := l.subst[name]; ok {
			return s, true
		}
		if l.resolve != nil {
			if scoped, ok := l.resolve(t); ok {
				if l.refs != nil {
					l.refs[scoped] = true
				}
				return scoped, true
			}
		}
		if bashPPLocalScalarTypes[name] {
			return name, true
		}
		if name == "interface{}" {
			return "any", true
		}
		// comparable is predeclared but only meaningful as a constraint,
		// which only a generic rendering spells.
		if name == "comparable" && l.genericMode && l.declared[name] == nil {
			return name, true
		}
		if _, local := l.declared[name]; local && !bashPPHelperReserved[name] {
			if l.refs != nil {
				l.refs[name] = true
			}
			return name, true
		}
		if alias, symbol, ok := strings.Cut(name, "."); ok && l.imports[alias] != "" && symbol != "" {
			return name, true
		}
		return "", false
	case *syntax.BashPPPointerType:
		element, ok := l.source(t.Element, depth+1)
		if !ok {
			return "", false
		}
		return "*" + element, true
	case *syntax.BashPPChanType:
		element, ok := l.source(t.Element, depth+1)
		if !ok {
			return "", false
		}
		prefix := "chan "
		if t.Direction == "recv" {
			prefix = "<-chan "
		}
		if t.Direction == "send" {
			prefix = "chan<- "
		}
		return prefix + element, true
	case *syntax.BashPPFuncType:
		text, ok := l.signature(&syntax.BashPPMethodSpec{Params: t.Params, Results: t.Results}, depth+1)
		return "func" + text, ok
	case *syntax.BashPPCollectionType:
		element, ok := l.source(t.Element, depth+1)
		if !ok {
			return "", false
		}
		switch t.Kind {
		case "slice":
			return "[]" + element, true
		case "array":
			// Only a plain integer literal is expressible: a constant name or
			// expression (`[N]int`, `[C * C]byte`, `[unsafe.Sizeof(x)]T`) has
			// no meaning inside the helper, which never sees the original
			// program's constant declarations.
			if t.Length == nil {
				return "", false
			}
			if _, err := strconv.ParseUint(t.Length.Value, 0, 63); err != nil {
				return "", false
			}
			return "[" + t.Length.Value + "]" + element, true
		case "map":
			key, ok := l.source(t.Key, depth+1)
			if !ok {
				return "", false
			}
			return "map[" + key + "]" + element, true
		}
		return "", false
	case *syntax.BashPPStructType:
		var fields []string
		for _, field := range t.Fields {
			// An embedded field is emitted as real embedding of the rendered
			// element type, so promotion and the promoted method set are the
			// dependency's own Go semantics rather than an imitation.
			if field.Embedded {
				// An embedded instantiation of a public generic is spelled
				// X[Arg], so the helper field is X and matches the promoted
				// name the interpreter transports. Any other instantiated
				// generic stays refused: the helper would embed it under its
				// generated name while the interpreter transports the storage
				// under the promoted original name, and the two would not
				// address the same field (Sprint 153's recorded refusal).
				if l.embeddedInstantiation(field.FieldTypeExpr) && !l.embeddedPublic(field.FieldTypeExpr) {
					return "", false
				}
				element, ok := l.source(field.FieldTypeExpr, depth+1)
				if !ok {
					return "", false
				}
				if field.Tag != nil {
					element += " " + field.Tag.Value
				}
				fields = append(fields, element)
				continue
			}
			if len(field.Names) == 0 {
				return "", false
			}
			element, ok := l.source(field.FieldTypeExpr, depth+1)
			if !ok {
				return "", false
			}
			names := make([]string, len(field.Names))
			for i, name := range field.Names {
				names[i] = name.Value
			}
			if field.Tag != nil {
				element += " " + field.Tag.Value
			}
			fields = append(fields, strings.Join(names, ", ")+" "+element)
		}
		return "struct {\n" + strings.Join(fields, "\n") + "\n}", true
	case *syntax.BashPPInterfaceType:
		if len(t.Elems) == 0 && len(t.Methods) == 0 {
			return "any", true
		}
		var specs []string
		if len(t.Elems) > 0 {
			for _, elem := range t.Elems {
				if elem.Method != nil {
					text, ok := l.signature(elem.Method, depth+1)
					if !ok {
						return "", false
					}
					specs = append(specs, elem.Method.Name.Value+text)
					continue
				}
				if elem.Embedded == nil || !l.ordinaryInterface(elem.Embedded, map[string]bool{}) {
					return "", false
				}
				text, ok := l.source(elem.Embedded, depth+1)
				if !ok {
					return "", false
				}
				specs = append(specs, text)
			}
		} else {
			for _, spec := range t.Methods {
				text, ok := l.signature(spec, depth+1)
				if !ok {
					return "", false
				}
				specs = append(specs, spec.Name.Value+text)
			}
		}
		return "interface {\n" + strings.Join(specs, "\n") + "\n}", true
	}
	return "", false
}

// ordinaryInterface reports whether an embedded interface element is a local
// method-set interface, rather than a union or another constraint-only type.
// Following local aliases is cycle-safe; source records the same named-type
// edge in refs, so the descriptor closure later keeps all embedded interfaces
// and their users together.
func (l *bashPPLocalTypeSet) ordinaryInterface(typ syntax.BashPPTypeExpr, seen map[string]bool) bool {
	named, ok := typ.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil || len(named.TypeArgs) > 0 {
		return false
	}
	name := named.Name.Value
	if name == "any" || name == "interface{}" {
		return true
	}
	if seen[name] {
		return false
	}
	decl := l.declared[name]
	if decl == nil {
		return false
	}
	seen[name] = true
	defer delete(seen, name)
	if alias, ok := decl.(*syntax.BashPPNamedType); ok {
		return l.ordinaryInterface(alias, seen)
	}
	iface, ok := decl.(*syntax.BashPPInterfaceType)
	if !ok {
		return false
	}
	for _, elem := range iface.Elems {
		if elem.Method == nil && (elem.Embedded == nil || !l.ordinaryInterface(elem.Embedded, seen)) {
			return false
		}
	}
	return true
}

// signature renders one interface method specification.
func (l *bashPPLocalTypeSet) signature(spec *syntax.BashPPMethodSpec, depth int) (string, bool) {
	render := func(fields []*syntax.BashPPField) ([]string, bool) {
		var out []string
		for _, field := range fields {
			text, ok := l.source(field.FieldTypeExpr, depth+1)
			if !ok {
				return nil, false
			}
			if field.Ellipsis.IsValid() {
				text = "..." + text
			}
			count := len(field.Names)
			if count == 0 {
				count = 1
			}
			for range count {
				out = append(out, text)
			}
		}
		return out, true
	}
	params, ok := render(spec.Params)
	if !ok {
		return "", false
	}
	results, ok := render(spec.Results)
	if !ok {
		return "", false
	}
	text := "(" + strings.Join(params, ", ") + ")"
	switch len(results) {
	case 0:
	case 1:
		text += " " + results[0]
	default:
		text += " (" + strings.Join(results, ", ") + ")"
	}
	return text, true
}

// bashPPLocalTypeGo emits the helper declarations for one descriptor: the type
// itself, and one stub per mirrored method. The stub body is fixed generated
// protocol code; the original body stays interpreted on the other side of the
// callback.
func bashPPLocalTypeGo(local bashPPLocalType) string {
	if local.Nest != "" {
		return "type " + local.Name + " " + local.Decl + "\n" + local.Nest
	}
	if local.PublicType != "" {
		var b strings.Builder
		b.WriteString(local.GenericDecl)
		// A generic stub's receiver reflects as the public instance X[Arg];
		// the interpreter resolves a callback receiver by the registry name
		// of its materialised instance, which each instance registers here.
		names, receiver := "bppInstanceNames_"+local.Callback, "bppGenericReceiver_"+local.Callback
		if len(local.GenericMethods) > 0 {
			fmt.Fprintf(&b, "var %s = map[reflect.Type]string{}\n", names)
			fmt.Fprintf(&b, "func %s(v reflect.Value) value { recv:=callbackReceiver(v); if name,ok:=%s[v.Type()];ok{recv.Type=name}; return recv }\n", receiver, names)
		}
		for _, method := range local.GenericMethods {
			recv := method.Receiver
			if method.Pointer {
				recv = "*" + recv
			}
			stub := bashPPLocalMethodStubGo(local.Callback, recv, method)
			b.WriteString(strings.ReplaceAll(stub, "callbackReceiver(reflect.ValueOf(bpprecv))", receiver+"(reflect.ValueOf(bpprecv))"))
		}
		b.WriteString("type " + local.Name + " = " + local.PublicType + "\n")
		if len(local.Methods) > 0 {
			fmt.Fprintf(&b, "func init(){%s[reflect.TypeFor[%s]()] = %q}\n", names, local.Name, "main."+local.Name)
		}
		return b.String()
	}
	var b strings.Builder
	alias := ""
	if local.Alias {
		alias = "= "
	}
	fmt.Fprintf(&b, "type %s %s%s\n", local.Name, alias, local.Decl)
	selectorBase := local.Name
	if local.Callback != "" {
		selectorBase = local.Callback
	}
	for _, method := range local.Methods {
		receiver := local.Name
		if method.Pointer {
			receiver = "*" + local.Name
		}
		b.WriteString(bashPPLocalMethodStubGo(selectorBase, receiver, method))
	}
	// Register the mirrored method set for mirroredMethodExpression. An alias
	// declares no methods of its own; its target registers them.
	if len(local.Methods) > 0 && !local.Alias {
		names := make([]string, len(local.Methods))
		for i, method := range local.Methods {
			names[i] = fmt.Sprintf("%q:true", method.Name)
		}
		fmt.Fprintf(&b, "func init(){mirroredMethodTypes=append(mirroredMethodTypes,mirroredMethodSet{types:[]reflect.Type{reflect.TypeFor[%s](),reflect.TypeFor[*%s]()},names:map[string]bool{%s}})}\n", local.Name, local.Name, strings.Join(names, ","))
	}
	return b.String()
}

// bashPPLocalMethodStubGo emits the stub of one mirrored method on receiver.
func bashPPLocalMethodStubGo(selectorBase, receiver string, method bashPPLocalMethod) string {
	var b strings.Builder
	switch {
	case method.General || len(method.Params) > 0 || len(method.Results) > 0:
		b.WriteString(bashPPLocalMethodGo(selectorBase, receiver, method))
	case method.Name == "Read":
		fmt.Fprintf(&b, `func (bpprecv %s) Read(p []byte)(int,error) {
 recv:=callbackReceiver(reflect.ValueOf(bpprecv));recv.CallArgs=[]value{encode(reflect.ValueOf(p))}
 if %t { recv.CallArgs=append(recv.CallArgs,value{Kind:"reader-buffer",ReaderBuffer:append([]byte(nil),p[:cap(p)]...),ReaderLength:len(p)}) }
 out,err:=callback(%q,recv);if err!=nil{panic(err)}
 if len(out)==3 { if out[2].Kind!="reader-buffer" || len(out[2].ReaderBuffer)!=cap(p){panic(fmt.Errorf("original Read buffer writeback mismatch"))};copy(p[:cap(p)],out[2].ReaderBuffer);out=out[:2] }
 if len(out)!=2{panic(fmt.Errorf("original Read result count mismatch"))}
 count,err:=decode(out[0],reflect.TypeFor[int]());if err!=nil{panic(err)}
 failure,err:=decode(out[1],reflect.TypeFor[error]());if err!=nil{panic(err)}
 var readErr error;if failure.IsValid(){readErr,_=failure.Interface().(error)}
 return int(count.Int()),readErr
}
`, receiver, method.ReaderLocalBuffer, selectorBase+".Read")
	default:
		fmt.Fprintf(&b, `func (bpprecv %s) %s() string {
 out, err := callback(%q, callbackReceiver(reflect.ValueOf(bpprecv)))
 if err != nil { return callbackFailed(err) }
 if len(out) != 1 || out[0].Kind != "string" { return callbackFailed(fmt.Errorf("original %s.%s did not answer one string")) }
 return stringText(out[0])
}
`, receiver, method.Name, selectorBase+"."+method.Name, selectorBase, method.Name)
	}
	return b.String()
}

// bashPPLocalMethodGo emits the generalised transport stub for one mirrored
// method with typed parameters and typed results. The whole body is generated
// protocol: arguments are encoded onto the callback request, the interpreter
// runs the original body, and each result is decoded back at its declared type
// so a native handle stays a native handle. No original statement is compiled.
func bashPPLocalMethodGo(typeName, receiver string, method bashPPLocalMethod) string {
	var b strings.Builder
	params := make([]string, len(method.Params))
	encoded := make([]string, len(method.Params))
	for i, typ := range method.Params {
		if method.Variadic && i == len(method.Params)-1 {
			params[i] = fmt.Sprintf("bpparg%d ...%s", i, typ)
		} else {
			params[i] = fmt.Sprintf("bpparg%d %s", i, typ)
		}
		encoded[i] = fmt.Sprintf("encode(reflect.ValueOf(bpparg%d))", i)
	}
	results := strings.Join(method.Results, ", ")
	if len(method.Results) > 1 {
		results = "(" + results + ")"
	}
	if results != "" {
		results = " " + results
	}
	selector := typeName + "." + method.Name
	fmt.Fprintf(&b, "func (bpprecv %s) %s(%s)%s {\n", receiver, method.Name, strings.Join(params, ", "), results)
	b.WriteString(" recv:=callbackReceiver(reflect.ValueOf(bpprecv))\n")
	if len(encoded) > 0 {
		fmt.Fprintf(&b, " recv.CallArgs=[]value{%s}\n", strings.Join(encoded, ","))
	}
	fmt.Fprintf(&b, " out,err:=callback(%q,recv);if err!=nil{panic(err)}\n", selector)
	fmt.Fprintf(&b, " if len(out)!=%d{panic(fmt.Errorf(%q))}\n", len(method.Results),
		fmt.Sprintf("original %s result count mismatch", selector))
	names := make([]string, len(method.Results))
	for i, typ := range method.Results {
		names[i] = fmt.Sprintf("bppres%d", i)
		fmt.Fprintf(&b, " bppval%d,err:=decode(out[%d],reflect.TypeFor[%s]());if err!=nil{panic(err)}\n", i, i, typ)
		fmt.Fprintf(&b, " var bppres%d %s;if bppval%d.IsValid(){bppres%d,_=bppval%d.Interface().(%s)}\n", i, typ, i, i, i, typ)
	}
	if len(names) > 0 {
		fmt.Fprintf(&b, " return %s\n", strings.Join(names, ", "))
	}
	b.WriteString("}\n")
	return b.String()
}

// bashPPBridgeResolvableArrayType reports whether the helper's type resolver
// can already resolve the declared spelling of an array value: a materialised
// local name, an imported identity registered from export data, or a literal
// length. Anything else needs the realised structural spelling instead.
func (r *Runner) bashPPBridgeResolvableArrayType(typ syntax.BashPPTypeExpr, collection *syntax.BashPPCollectionType) bool {
	if named, ok := typ.(*syntax.BashPPNamedType); ok && named.Name != nil {
		name := named.Name.Value
		if strings.Contains(name, ".") {
			return true
		}
		for _, local := range r.bashPPLocalTypeDescriptors() {
			if local.Name == name || local.WireType == name {
				return true
			}
		}
		return false
	}
	if collection.Length == nil {
		return false
	}
	_, err := strconv.ParseUint(collection.Length.Value, 0, 63)
	return err == nil
}

// bashPPLocalTypeIdentity is the comparison key that decides whether a running
// dependency session already materialises the current local type namespace.
func bashPPLocalTypeIdentity(locals []bashPPLocalType) string {
	var b strings.Builder
	for _, local := range locals {
		fmt.Fprintf(&b, "%s|%s|%s|%s|%t|%s|%s|%s|", local.Name, local.PublicType, local.GenericDecl, local.Decl, local.Alias, local.WireType, local.Callback, local.Nest)
		for _, method := range local.GenericMethods {
			fmt.Fprintf(&b, "%s:%s:%t:%t:%t:%t:%v:%v,", method.Receiver, method.Name, method.Pointer, method.Variadic, method.ReaderLocalBuffer, method.General, method.Params, method.Results)
		}
		fmt.Fprintf(&b, "%v|", local.Identity)
		for _, method := range local.Methods {
			fmt.Fprintf(&b, "%s:%t:%t:%t:%t:%v:%v,", method.Name, method.Pointer, method.Variadic, method.ReaderLocalBuffer, method.General, method.Params, method.Results)
		}
		fmt.Fprintf(&b, "%v;", local.OmittedMethods)
	}
	return b.String()
}
