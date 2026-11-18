// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"go/constant"
	"go/token"
	"go/types"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

var goSourceAMD64Sizes types.Sizes = &types.StdSizes{WordSize: 8, MaxAlign: 8}

// goSourceConstantCall reports the calls which Go permits in a constant
// expression. It is deliberately GoSource-only: Classic Bash++ keeps its own
// arithmetic and exact-rational rendering.
func (r *Runner) goSourceConstantCall(call *syntax.BashPPCall) bool {
	if !r.bashPPGoSource || call == nil {
		return false
	}
	if _, handled, err := r.goSourceUnsafeConstant(call); handled {
		return err == nil && !call.GoRuntime
	}
	if len(call.Fun) != 1 {
		return false
	}
	name := call.Fun[0].Value
	if _, declared := r.bashPPLookupFunc(call); declared {
		return false
	}
	switch name {
	case "real", "imag":
		return len(call.ArgExprs) == 1 && r.bashPPConstantScalarExpr(call.ArgExprs[0], "")
	case "complex":
		return len(call.ArgExprs) == 2 && r.bashPPConstantScalarExpr(call.ArgExprs[0], "") && r.bashPPConstantScalarExpr(call.ArgExprs[1], "")
	case "len", "cap":
		if len(call.ArgExprs) != 1 {
			return false
		}
		_, err := r.bashPPEvalScalarExpr(call)
		return err == nil
	}
	return false
}

// goSourceUnsafeConstant folds unsafe.Sizeof, Alignof, and Offsetof from the
// operand's declared type using the linux/amd64 layout used by the upstream
// harness. The operand is not evaluated. Checked nonconstant calls retain
// runtime arithmetic semantics even after generic instantiation resolves layout.
func (r *Runner) goSourceUnsafeConstant(call *syntax.BashPPCall) (bashPPScalar, bool, error) {
	if !r.bashPPGoSource || call == nil || len(call.Fun) != 2 || len(call.ArgExprs) != 1 || r.bashPPImports[call.Fun[0].Value] != "unsafe" {
		return bashPPScalar{}, false, nil
	}
	var size int64
	switch call.Fun[1].Value {
	case "Sizeof", "Alignof":
		typ, ok := r.goSourceStaticExprType(call.ArgExprs[0])
		if !ok {
			return bashPPScalar{}, true, &goSourceError{prefix: r.bashErrPrefix(call.Pos()), err: errGoSourceUnsafeType}
		}
		goType, ok := r.goSourceLayoutType(typ, map[string]bool{})
		if !ok {
			return bashPPScalar{}, true, &goSourceError{prefix: r.bashErrPrefix(call.Pos()), err: errGoSourceUnsafeType}
		}
		if call.Fun[1].Value == "Sizeof" {
			size = goSourceAMD64Sizes.Sizeof(goType)
		} else {
			size = goSourceAMD64Sizes.Alignof(goType)
		}
	case "Offsetof":
		var ok bool
		size, ok = r.goSourceOffsetof(call.ArgExprs[0])
		if !ok {
			return bashPPScalar{}, true, &goSourceError{prefix: r.bashErrPrefix(call.Pos()), err: errGoSourceUnsafeOffset}
		}
	default:
		return bashPPScalar{}, false, nil
	}
	return bashPPScalar{value: constant.MakeInt64(size), typ: "uintptr", runtime: call.GoRuntime}, true, nil
}

var (
	errGoSourceUnsafeType   = goSourceConstError("BASHPP-EUNSAFE-TYPE: operand type has no supported Go layout")
	errGoSourceUnsafeOffset = goSourceConstError("BASHPP-EUNSAFE-OFFSET: operand is not a supported struct field selector")
)

type goSourceConstError string

func (e goSourceConstError) Error() string { return string(e) }

func (r *Runner) goSourceStaticExprType(expr syntax.BashPPExpr) (syntax.BashPPTypeExpr, bool) {
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.goSourceStaticExprType(x.X)
	case *syntax.BashPPBasicLit:
		name := map[string]string{"INT": "int", "FLOAT": "float64", "IMAG": "complex128", "CHAR": "rune", "STRING": "string"}[x.Kind]
		return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}, name != ""
	case *syntax.BashPPIdent:
		if r.bashPPScope == nil {
			return nil, false
		}
		// The declared type, collection meta, type name, scalar kind and
		// pointee are consulted in turn; one snapshot answers for all.
		cell := r.bashPPScope.lookup(x.Name.Value).view()
		if cell == nil {
			return nil, false
		}
		if cell.declType != nil {
			return cell.declType, true
		}
		if meta := bashPPCellMeta(cell); meta != nil && meta.typ != nil {
			return meta.typ, true
		}
		if cell.typeName != "" {
			return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: cell.typeName}}, true
		}
		// A short declaration initialized from an untyped string may have no
		// named type metadata. Its retained scalar kind still proves the Go
		// default string type, without inspecting or evaluating its contents.
		if cell.scalarKind == constant.String {
			return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "string"}}, true
		}
		if cell.pointer {
			if cell.pointerValue == nil {
				return nil, false
			}
			return &syntax.BashPPPointerType{Element: cell.pointerValue.elem}, true
		}
	case *syntax.BashPPConvertExpr:
		if x.ConvTypeExpr == nil && x.ConvType != nil {
			// The constant boundary emits a conversion with only the type's
			// name (`unsafe.Sizeof(int(0))`).
			return &syntax.BashPPNamedType{Name: x.ConvType}, true
		}
		return x.ConvTypeExpr, x.ConvTypeExpr != nil
	case *syntax.BashPPCompositeLit:
		return x.LitType, x.LitType != nil
	case *syntax.BashPPNewExpr:
		// Inspect the allocated type without allocating or evaluating new(v).
		return &syntax.BashPPPointerType{Element: x.AllocType}, x.AllocType != nil
	case *syntax.BashPPAddressExpr:
		typ, ok := r.goSourceStaticExprType(x.X)
		return &syntax.BashPPPointerType{Element: typ}, ok
	case *syntax.BashPPDerefExpr:
		typ, ok := r.goSourceStaticExprType(x.X)
		pointer, pointerOK := r.bashPPUnderlyingType(typ).(*syntax.BashPPPointerType)
		if !ok || !pointerOK {
			// Unknown pointer shapes cannot provide a static pointee.
			return nil, false
		}
		return pointer.Element, true
	case *syntax.BashPPFuncLit:
		return &syntax.BashPPFuncType{Params: x.Params, Results: x.Results}, true
	case *syntax.BashPPCall:
		// Static inspection must not resolve a receiver/computed callee: the
		// invocation resolver may evaluate expressions or return native and
		// builtin callables without a source declaration or literal.
		var fields []*syntax.BashPPField
		if x.FuncLit != nil {
			fields = x.FuncLit.Results
		} else if x.CalleeExpr != nil {
			if typ, ok := r.goSourceStaticExprType(x.CalleeExpr); ok {
				if signature, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPFuncType); ok {
					fields = signature.Results
				}
			}
		} else if len(x.Fun) == 1 {
			var fn *bashPPFunc
			if r.bashPPScope != nil {
				if cell := r.bashPPScope.lookup(x.Fun[0].Value); cell != nil {
					if signature, ok := r.bashPPUnderlyingType(cell.declType).(*syntax.BashPPFuncType); ok {
						fields = signature.Results
					} else {
						fn, _ = r.bashPPClosure(cell.vr.Str)
					}
					if fields == nil && (fn == nil || fn.decl == nil && fn.lit == nil) {
						return nil, false
					}
				} else {
					fn = r.bashPPFuncs[x.Fun[0].Value]
				}
			}
			if fn != nil && (fn.decl != nil || fn.lit != nil) {
				fields = fn.results()
			}
		}
		if results := bashppResultTypeExprs(fields); len(results) == 1 {
			return results[0], true
		}
		if x.CalleeExpr == nil && x.FuncLit == nil && len(x.Fun) == 1 && r.bashPPGoSourceFile != nil {
			var result syntax.BashPPTypeExpr
			syntax.Walk(r.bashPPGoSourceFile, func(node syntax.Node) bool {
				// Package function declarations cannot occur inside function
				// bodies or literals. In particular, walking a large aggregate
				// initializer for every builtin call makes static lookup costly.
				switch node.(type) {
				case *syntax.BashPPCompositeLit, *syntax.BashPPFuncLit:
					return false
				}
				decl, ok := node.(*syntax.BashPPFuncDecl)
				if !ok {
					return result == nil
				}
				if decl.Receiver != nil || decl.Name.Value != x.Fun[0].Value {
					return false
				}
				results := bashppResultTypeExprs(decl.Results)
				if len(results) == 1 {
					result = results[0]
				}
				return false
			})
			if result != nil {
				return result, true
			}
		}
	case *syntax.BashPPSelectorExpr:
		parent, ok := r.goSourceStaticExprType(x.X)
		if !ok {
			return nil, false
		}
		sel := r.bashPPResolveField(parent, x.Sel.Value)
		if sel.fieldType == nil && !sel.ambiguous {
			// The parent may be the dependency's own struct, whose fields
			// this program never declared; see [Runner.goSourceNativeFieldType].
			return r.goSourceNativeFieldType(parent, x.Sel.Value)
		}
		return sel.fieldType, !sel.ambiguous && sel.fieldType != nil
	case *syntax.BashPPIndexExpr:
		parent, ok := r.goSourceStaticExprType(x.X)
		collection, collectionOK := r.bashPPUnderlyingType(parent).(*syntax.BashPPCollectionType)
		if !ok || !collectionOK {
			return nil, false
		}
		return collection.Element, true
	case *syntax.BashPPSliceExpr:
		parent, ok := r.goSourceStaticExprType(x.X)
		if !ok {
			return nil, false
		}
		if collection, collectionOK := r.bashPPUnderlyingType(parent).(*syntax.BashPPCollectionType); collectionOK {
			return &syntax.BashPPCollectionType{Kind: "slice", Element: collection.Element}, true
		}
		return parent, true
	}
	return nil, false
}

func (r *Runner) goSourceLayoutType(typ syntax.BashPPTypeExpr, seen map[string]bool) (types.Type, bool) {
	if typ == nil {
		return nil, false
	}
	if named, ok := typ.(*syntax.BashPPNamedType); ok {
		if basic, ok := bashPPGoBasicType(named.Name.Value); ok {
			return basic, true
		}
		// Imported fields and array elements retain their authentic layout
		// even when nested inside an interpreter-owned aggregate.
		if native := r.bashPPEmbeddedNativeType(named); native != nil {
			return native, true
		}
		key := bashPPTypeText(named)
		if seen[key] {
			return nil, false
		}
		seen[key] = true
		defer delete(seen, key)
		underlying := r.bashPPUnderlyingType(named)
		if bashPPTypeText(underlying) == key {
			return nil, false
		}
		return r.goSourceLayoutType(underlying, seen)
	}
	switch x := typ.(type) {
	case *syntax.BashPPPointerType:
		return types.NewPointer(types.Typ[types.Byte]), true
	case *syntax.BashPPFuncType:
		return types.NewSignatureType(nil, nil, nil, nil, nil, false), true
	case *syntax.BashPPChanType:
		return types.NewChan(types.SendRecv, types.Typ[types.Byte]), true
	case *syntax.BashPPInterfaceType:
		return types.NewInterfaceType(nil, nil).Complete(), true
	case *syntax.BashPPCollectionType:
		elem, ok := r.goSourceLayoutType(x.Element, seen)
		if !ok {
			return nil, false
		}
		switch x.Kind {
		case "slice":
			return types.NewSlice(elem), true
		case "map":
			key, ok := r.goSourceLayoutType(x.Key, seen)
			if !ok {
				return nil, false
			}
			return types.NewMap(key, elem), true
		case "array":
			length, err := r.bashPPArrayLength(x.Length.Value)
			if err != nil {
				return nil, false
			}
			return types.NewArray(elem, int64(length)), true
		}
	case *syntax.BashPPStructType:
		var fields []*types.Var
		var tags []string
		for _, field := range x.Fields {
			fieldType, ok := r.goSourceLayoutType(field.FieldTypeExpr, seen)
			if !ok {
				return nil, false
			}
			names := field.Names
			if len(names) == 0 && field.Embedded {
				name, ok := bashPPEmbeddedFieldName(field)
				if !ok {
					return nil, false
				}
				names = []*syntax.Lit{{Value: name}}
			}
			for _, name := range names {
				fields = append(fields, types.NewVar(token.NoPos, nil, name.Value, fieldType))
				tag := ""
				if field.Tag != nil {
					tag = field.Tag.Value
				}
				tags = append(tags, tag)
			}
		}
		return types.NewStruct(fields, tags), true
	}
	return nil, false
}

func (r *Runner) goSourceOffsetof(expr syntax.BashPPExpr) (int64, bool) {
	selector, ok := expr.(*syntax.BashPPSelectorExpr)
	if !ok {
		return 0, false
	}
	parent, ok := r.goSourceStaticExprType(selector.X)
	if !ok {
		return 0, false
	}
	// Go permits the selector receiver itself to be a pointer to a struct
	// (`unsafe.Offsetof(p.f)`).  This implicit outer dereference does not
	// make the field path indirect; only an embedded pointer does.  Mirror
	// go/types' derefStructPtr before both resolving the selector and
	// calculating its layout.
	if pointer, ok := r.bashPPUnderlyingType(parent).(*syntax.BashPPPointerType); ok {
		if _, ok := r.bashPPUnderlyingType(pointer.Element).(*syntax.BashPPStructType); !ok {
			return 0, false
		}
		parent = pointer.Element
	}
	selection := r.bashPPResolveField(parent, selector.Sel.Value)
	if selection.ambiguous || len(selection.edges) == 0 {
		return 0, false
	}
	var total int64
	current := parent
	for i, edge := range selection.edges {
		if edge.pointer && i+1 < len(selection.edges) {
			return 0, false
		}
		structure, ok := r.bashPPUnderlyingType(current).(*syntax.BashPPStructType)
		if !ok {
			return 0, false
		}
		goType, ok := r.goSourceLayoutType(structure, map[string]bool{})
		if !ok {
			return 0, false
		}
		goStruct := goType.(*types.Struct)
		offsets := goSourceAMD64Sizes.Offsetsof(structFields(goStruct))
		found := false
		for fieldIndex := 0; fieldIndex < goStruct.NumFields(); fieldIndex++ {
			if goStruct.Field(fieldIndex).Name() == edge.name {
				total += offsets[fieldIndex]
				for _, field := range structure.Fields {
					for _, name := range field.Names {
						if name.Value == edge.name {
							current = field.FieldTypeExpr
							found = true
						}
					}
					if field.Embedded {
						if name, _ := bashPPEmbeddedFieldName(field); name == edge.name {
							current = field.FieldTypeExpr
							found = true
						}
					}
				}
				break
			}
		}
		if !found {
			return 0, false
		}
	}
	return total, true
}

func structFields(structure *types.Struct) []*types.Var {
	fields := make([]*types.Var, structure.NumFields())
	for i := range fields {
		fields[i] = structure.Field(i)
	}
	return fields
}
