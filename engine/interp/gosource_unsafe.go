// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	goast "go/ast"
	"go/constant"
	gotoken "go/token"
	"go/types"
	"runtime"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPGoSizes is the size/alignment model the target platform uses, so
// `unsafe.Sizeof(int32(0))` folds to 4 and a pointer-shaped value folds to the
// word size. It falls back to a 64-bit model when the architecture is unknown
// rather than refusing to fold at all.
func bashPPGoSizes() types.Sizes {
	if sizes := types.SizesFor("gc", runtime.GOARCH); sizes != nil {
		return sizes
	}
	return &types.StdSizes{WordSize: 8, MaxAlign: 8}
}

// bashPPUnsafeConstOperator evaluates an `unsafe.Sizeof`/`unsafe.Alignof` call
// as the compile-time uintptr constant it is, reading the result from the
// operand's static type rather than trying to run the pseudo-function through a
// dependency (where it has no callable symbol). It reports ok=false for a call
// that is not one of these operators, or one whose operand type it cannot
// settle from the operand's own syntax, so the caller keeps its own handling.
//
// Offsetof needs a struct's field layout and SliceData/String/StringData are
// runtime conversions, not constants; neither is handled here.
func (r *Runner) bashPPUnsafeConstOperator(call *goast.CallExpr) (constant.Value, bool) {
	sel, ok := call.Fun.(*goast.SelectorExpr)
	if !ok {
		return nil, false
	}
	pkg, ok := sel.X.(*goast.Ident)
	if !ok || r.bashPPImports[pkg.Name] != "unsafe" || len(call.Args) != 1 {
		return nil, false
	}
	var measure func(types.Type) int64
	switch sel.Sel.Name {
	case "Sizeof":
		measure = bashPPGoSizes().Sizeof
	case "Alignof":
		measure = bashPPGoSizes().Alignof
	default:
		return nil, false
	}
	typ, ok := r.bashPPGoAstOperandType(call.Args[0])
	if !ok {
		return nil, false
	}
	return constant.MakeInt64(measure(typ)), true
}

// bashPPGoAstOperandType settles the static type of an operand spelled in Go
// source text (as parsed for a constant array length), for the operand shapes
// whose type is fixed by the operand alone: a conversion to a basic type, a
// call of a declared function with a basic result, a function value, and a
// basic literal. It does not attempt full type inference.
func (r *Runner) bashPPGoAstOperandType(expr goast.Expr) (types.Type, bool) {
	switch x := expr.(type) {
	case *goast.ParenExpr:
		return r.bashPPGoAstOperandType(x.X)
	case *goast.FuncLit:
		// A function value is word-shaped; the signature's own contents do not
		// change its size. An empty signature measures as any func does.
		return types.NewSignatureType(nil, nil, nil, nil, nil, false), true
	case *goast.BasicLit:
		switch x.Kind {
		case gotoken.INT:
			return types.Typ[types.Int], true
		case gotoken.FLOAT:
			return types.Typ[types.Float64], true
		case gotoken.IMAG:
			return types.Typ[types.Complex128], true
		case gotoken.CHAR:
			return types.Typ[types.Int32], true
		case gotoken.STRING:
			return types.Typ[types.String], true
		}
	case *goast.CallExpr:
		name, ok := x.Fun.(*goast.Ident)
		if !ok {
			return nil, false
		}
		// A conversion `T(v)` to a basic type takes that type.
		if basic, ok := bashPPGoBasicType(name.Name); ok {
			return basic, true
		}
		// Type declarations are lowered before functions are registered, so
		// constant array lengths may need a later function's declared result.
		if results, ok := r.bashPPUnsafeOperandFuncResults(name.Name); ok {
			if bashppResultCount(results) == 1 && len(results) == 1 && results[0].FieldTypeExpr != nil {
				if basic, ok := bashPPGoTypeExprBasic(results[0].FieldTypeExpr); ok {
					return basic, true
				}
			}
		}
	}
	return nil, false
}

func (r *Runner) bashPPUnsafeOperandFuncResults(name string) ([]*syntax.BashPPField, bool) {
	if fn := r.bashPPFuncs[name]; fn != nil {
		return fn.results(), true
	}
	if !r.bashPPGoSource || r.bashPPGoSourceFile == nil {
		return nil, false
	}
	for _, stmt := range r.bashPPGoSourceFile.Stmts {
		decl, ok := stmt.Cmd.(*syntax.BashPPFuncDecl)
		if ok && decl.Receiver == nil && decl.Name != nil && decl.Name.Value == name {
			return decl.Results, true
		}
	}
	return nil, false
}

// bashPPGoTypeExprBasic maps a named basic type expression to its go/types
// type, so a function result such as `int64` measures correctly.
func bashPPGoTypeExprBasic(typ syntax.BashPPTypeExpr) (types.Type, bool) {
	named, ok := typ.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil {
		return nil, false
	}
	return bashPPGoBasicType(named.Name.Value)
}

// bashPPGoBasicType maps a predeclared basic type name to its go/types type.
func bashPPGoBasicType(name string) (types.Type, bool) {
	switch name {
	case "bool":
		return types.Typ[types.Bool], true
	case "int":
		return types.Typ[types.Int], true
	case "int8":
		return types.Typ[types.Int8], true
	case "int16":
		return types.Typ[types.Int16], true
	case "int32", "rune":
		return types.Typ[types.Int32], true
	case "int64":
		return types.Typ[types.Int64], true
	case "uint":
		return types.Typ[types.Uint], true
	case "uint8", "byte":
		return types.Typ[types.Uint8], true
	case "uint16":
		return types.Typ[types.Uint16], true
	case "uint32":
		return types.Typ[types.Uint32], true
	case "uint64":
		return types.Typ[types.Uint64], true
	case "uintptr":
		return types.Typ[types.Uintptr], true
	case "float32":
		return types.Typ[types.Float32], true
	case "float64":
		return types.Typ[types.Float64], true
	case "complex64":
		return types.Typ[types.Complex64], true
	case "complex128":
		return types.Typ[types.Complex128], true
	case "string":
		return types.Typ[types.String], true
	}
	return nil, false
}
