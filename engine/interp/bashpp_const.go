// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"fmt"
	"go/constant"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func bashPPIotaExpr(expr syntax.BashPPExpr, value int) syntax.BashPPExpr {
	switch x := expr.(type) {
	case *syntax.BashPPIdent:
		if x.Name.Value == "iota" {
			lit := *x.Name
			lit.Value = strconv.Itoa(value)
			return &syntax.BashPPBasicLit{Value: &lit, Kind: "INT"}
		}
	case *syntax.BashPPParenExpr:
		y := *x
		y.X = bashPPIotaExpr(x.X, value)
		return &y
	case *syntax.BashPPUnaryExpr:
		y := *x
		y.X = bashPPIotaExpr(x.X, value)
		return &y
	case *syntax.BashPPBinaryExpr:
		y := *x
		y.X, y.Y = bashPPIotaExpr(x.X, value), bashPPIotaExpr(x.Y, value)
		return &y
	case *syntax.BashPPConvertExpr:
		y := *x
		y.X = bashPPIotaExpr(x.X, value)
		return &y
	}
	return expr
}

func (r *Runner) bashPPConstGroup(ctx context.Context, group *syntax.BashPPConstGroup) {
	if !r.objectsEnabled() {
		r.errf("bash++ const declaration evaluated with extensions disabled\n")
		r.exit.code = 2
		return
	}
	if r.bashPPScope == nil {
		r.bashPPScope = newBashPPScope(nil)
	}
	seen := make(map[string]bool)
	for _, spec := range group.Specs {
		name := spec.Name.Value
		if name == "_" {
			continue
		}
		if !syntax.BashPPValidIdent(name) || seen[name] || r.bashPPScope.entries[name] != nil {
			r.errf("%sconstant %s redeclared in this scope\n", r.bashErrPrefix(spec.Pos()), name)
			r.exit.code = 2
			return
		}
		seen[name] = true
	}
	created := make([]string, 0, len(group.Specs))
	defer func() {
		if r.exit.code != 0 {
			for _, name := range created {
				delete(r.bashPPScope.entries, name)
			}
		}
	}()
	var previous *syntax.BashPPConstSpec
	for _, spec := range group.Specs {
		effective := spec
		if spec.InitExpr == nil {
			copySpec := *spec
			copySpec.DeclType, copySpec.DeclTypeExpr = previous.DeclType, previous.DeclTypeExpr
			copySpec.Init, copySpec.InitExpr = previous.Init, previous.InitExpr
			effective = &copySpec
		} else {
			previous = spec
		}
		expr := effective.InitExpr
		// iota is predeclared, so an ordinary lexical declaration with the
		// same name shadows it after that declaration's ConstSpec ends.
		if r.bashPPScope.lookup("iota") == nil && r.bashPPFuncs["iota"] == nil {
			expr = bashPPIotaExpr(expr, int(spec.Iota))
		}
		if !r.bashPPConstantScalarExpr(expr, "") {
			r.errf("%sBASHPP-ECONST-EXPR: const initializer is not a constant expression\n", r.bashErrPrefix(spec.Pos()))
			r.exit.code = 2
			return
		}
		var scalar bashPPScalar
		scalarKind := constant.Unknown
		var vr expand.Variable
		var err error
		if effective.DeclTypeExpr != nil {
			decl := &syntax.BashPPDecl{Site: syntax.StartConst, Kw: group.Kw, Name: spec.Name, DeclType: effective.DeclType, DeclTypeExpr: effective.DeclTypeExpr, Init: effective.Init, InitExpr: expr}
			var handled bool
			vr, scalarKind, handled, err = r.bashPPTypedScalarDeclValue(decl)
			if !handled && err == nil {
				err = fmt.Errorf("BASHPP-ECONST-TYPE: unsupported grouped constant type")
			}
		} else {
			scalar, err = r.bashPPEvalScalarExpr(expr)
			if err == nil {
				vr = expand.Variable{Set: true, Kind: expand.String, Str: bashPPScalarString(scalar.value)}
			}
		}
		if err != nil {
			r.errf("%s%v\n", r.bashErrPrefix(spec.Pos()), err)
			r.exit.code = 2
			return
		}
		// The blank identifier declares no binding, but its ConstSpec still
		// participates fully in expression validation and iota sequencing.
		if spec.Name.Value == "_" {
			continue
		}
		if err := r.bashPPScope.declare(spec.Name.Value, vr, true); err != nil {
			r.errf("%s%v\n", r.bashErrPrefix(spec.Pos()), err)
			r.exit.code = 2
			return
		}
		cell := r.bashPPScope.lookup(spec.Name.Value)
		cell.scalarKind = scalarKind
		if scalar.value != nil {
			cell.exactScalar = scalar.value
		} else if value, evalErr := r.bashPPEvalScalarExpr(expr); evalErr == nil {
			// Typed grouped constants travel through the declaration conversion
			// path above. Preserve the converted constant, including its required
			// rounding, rather than the untyped source expression.
			shape := r.bashPPUnderlyingType(effective.DeclTypeExpr)
			if named, ok := shape.(*syntax.BashPPNamedType); ok {
				base := named.Name.Value
				if r.bashPPGoSource {
					base, _ = r.goSourceScalarUnderlying(effective.DeclTypeExpr)
				}
				if converted, convertErr := r.bashPPConvertScalar(base, value); convertErr == nil {
					value = converted
				}
			}
			cell.exactScalar = value.value
		}
		if effective.DeclTypeExpr != nil {
			cell.declType = effective.DeclTypeExpr
			base := bashPPNamedTypeBase(effective.DeclTypeExpr)
			if _, named := r.bashPPTypes[base]; named {
				cell.typeName = base
			}
		} else if scalar.value != nil {
			cell.scalarKind = scalar.value.Kind()
			if scalar.typ != "" {
				cell.declType = &syntax.BashPPNamedType{Name: &syntax.Lit{Value: scalar.typ}}
				if _, named := r.bashPPTypes[scalar.typ]; named {
					cell.typeName = scalar.typ
				}
			}
		}
		cell.constant = true
		created = append(created, spec.Name.Value)
	}
}
