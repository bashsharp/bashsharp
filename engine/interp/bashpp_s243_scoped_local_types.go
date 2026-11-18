// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #243; Story: #674; Story-ID: 63073886bfce
//
// Identity of a reused local type name at the native bridge.
//
// A name declared in two scopes — `type s struct{ f int }` in one method
// and `type s struct{ g bool }` in another, or a package-level `Int`
// shadowed by a function-local `Int` — denotes distinct Go types. The
// helper's type registry is keyed by spelling, so the descriptor builder
// used to drop every declaration of such a name and every value of one
// was refused as an unregistered bridge type. The evaluator already
// resolves a named-type reference to its declaration lexically
// (bashpp_sprint162_type_scope.go); this file gives each function-local
// declaration of a reused name its own helper identity and spells a
// reference by that identity when it crosses. A package-level
// declaration keeps the plain name, which is what every reference outside
// the shadowing scopes means. A reference without a source position
// cannot be placed and still spells the plain name, so a program whose
// declarations are all function-local stays refused rather than guessed.

import (
	"crypto/sha256"
	"fmt"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPScopedLocalDecl is one function-local declaration of a reused
// type name: the declaration node and the scope key
// goSourceLocalTypeScope answers for a reference to it.
type bashPPScopedLocalDecl struct {
	decl  *syntax.BashPPDecl
	scope string
}

// bashPPScopedLocalKey is the table key of a declaration: the name and
// the scope key of the declaration a reference resolves to.
func bashPPScopedLocalKey(name, scope string) string {
	return name + "@" + scope
}

// bashPPScopedLocalName is the helper identity of one scoped declaration.
// It is a plain identifier, so a composed spelling — `[]s`, `*s`,
// `map[string]s` — parses in the helper's resolver and reaches the
// registered name through it.
func bashPPScopedLocalName(key string) string {
	return fmt.Sprintf("bppScoped_%x", sha256.Sum256([]byte(key)))
}

// bashPPScopedLocalDecls collects, per reused name, the function-local
// declarations that can be registered under their own identity, and
// reports which reused names keep a single package-level declaration
// under the plain name. A blank declaration is not scoped. Nor is a
// generic declaration inside a generic function or method: gc makes it a
// distinct type per instantiation of the enclosing function, spelled with
// that function's type arguments (`main.T[int;int]`), and one lexical
// identity cannot be that type.
func bashPPScopedLocalDecls(index *bashPPLocalTypeDeclCache, ambiguous map[string]bool) (scoped map[string]bashPPScopedLocalDecl, packageLevel map[string]*syntax.BashPPDecl) {
	scoped = map[string]bashPPScopedLocalDecl{}
	packageLevel = map[string]*syntax.BashPPDecl{}
	if index == nil || index.file == nil || len(ambiguous) == 0 {
		return scoped, packageLevel
	}
	for _, d := range index.typeDecls {
		name := d.Name.Value
		if !ambiguous[name] || name == "_" {
			continue
		}
		if index.topDecls[d] {
			packageLevel[name] = d
			continue
		}
		if len(d.TypeParams) > 0 && index.inGeneric[d] {
			continue
		}
		pos := d.Pos()
		if !pos.IsValid() {
			continue
		}
		scope := strconv.FormatUint(uint64(pos.Offset()), 10)
		scoped[bashPPScopedLocalKey(name, scope)] = bashPPScopedLocalDecl{decl: d, scope: scope}
	}
	return scoped, packageLevel
}

// bashPPScopedLocalTypeName is the bashPPBridgeTypeScope of this runner:
// a reference to a function-local declaration of a reused name spells
// the helper identity registered for that declaration.
func (r *Runner) bashPPScopedLocalTypeName(named *syntax.BashPPNamedType) (string, bool) {
	if !r.bashPPGoSource || named == nil || named.Name == nil {
		return "", false
	}
	r.bashPPLocalTypeDescriptors()
	cache := r.bashPPTools.localTypes
	if cache == nil {
		return "", false
	}
	if name, ok := r.bashPPNestTypeName(named, cache.nest, cache.scoped, r.bashPPTypeParamArgs); ok {
		return name, true
	}
	if len(cache.scoped) == 0 {
		return "", false
	}
	scope, known := r.goSourceLocalTypeScope(named)
	if !known {
		// A synthesized reference — the type a conversion `value(2)` boxes
		// with — carries no position. It can only mean the package-level
		// declaration when the program declares no local type of the name.
		if index := r.goSourceLocalTypes(); index != nil && len(index.decls[named.Name.Value]) == 0 {
			if name, ok := cache.scoped[bashPPScopedLocalKey(named.Name.Value, "")]; ok {
				return name, true
			}
		}
		return "", false
	}
	// Scope "" is a package-level type registered under a generated
	// identity because its name is reserved by the helper
	// (bashpp_s290_reserved_type_names.go); no other package-level key exists.
	name, ok := cache.scoped[bashPPScopedLocalKey(named.Name.Value, scope)]
	return name, ok
}

// bashPPPredeclaredTypeArgs reports whether every type argument of named
// is built only from predeclared types whose Go spelling reflect prints
// unchanged, so the plain instantiation text is the public type name.
// `rune`, `byte` and `any` are excluded: reflect spells them `int32`,
// `uint8` and `interface {}`.
func bashPPPredeclaredTypeArgs(named *syntax.BashPPNamedType) bool {
	var plain func(syntax.BashPPTypeExpr) bool
	plain = func(typ syntax.BashPPTypeExpr) bool {
		switch t := typ.(type) {
		case *syntax.BashPPNamedType:
			if t.Name == nil || len(t.TypeArgs) > 0 {
				return false
			}
			switch t.Name.Value {
			case "rune", "byte", "any":
				return false
			}
			return bashPPLocalScalarTypes[t.Name.Value]
		case *syntax.BashPPPointerType:
			return plain(t.Element)
		case *syntax.BashPPCollectionType:
			if t.Kind == "map" && !plain(t.Key) {
				return false
			}
			return t.Kind != "inferred-array" && plain(t.Element)
		}
		return false
	}
	for _, arg := range named.TypeArgs {
		if arg == nil || !plain(arg.ArgType) {
			return false
		}
	}
	return len(named.TypeArgs) > 0
}
