package gosource

import (
	"go/ast"
	"go/types"
)

// checkedType reparses the checked spelling. These fresh AST nodes are absent
// from types.Info, so channel allocation certificates would otherwise vanish
// on inferred declarations and assignment temporaries (including field make).
// Bind only type nodes, using the original checked structure as authority.
func (c *converter) bindSyntheticChannelTypes(e ast.Expr, t types.Type) {
	if e == nil || t == nil {
		return
	}
	c.info.Types[e] = types.TypeAndValue{Type: t}
	fields := func(list *ast.FieldList, tuple *types.Tuple) {
		if list == nil || tuple == nil {
			return
		}
		i := 0
		for _, f := range list.List {
			if i >= tuple.Len() {
				return
			}
			ft := tuple.At(i).Type()
			if ell, ok := f.Type.(*ast.Ellipsis); ok {
				if slice, ok := ft.Underlying().(*types.Slice); ok {
					c.bindSyntheticChannelTypes(ell.Elt, slice.Elem())
				}
			} else {
				c.bindSyntheticChannelTypes(f.Type, ft)
			}
			n := len(f.Names)
			if n == 0 {
				n = 1
			}
			i += n
		}
	}
	switch x := e.(type) {
	case *ast.ChanType:
		if ch, ok := t.Underlying().(*types.Chan); ok {
			c.bindSyntheticChannelTypes(x.Value, ch.Elem())
		}
	case *ast.StarExpr:
		if p, ok := t.Underlying().(*types.Pointer); ok {
			c.bindSyntheticChannelTypes(x.X, p.Elem())
		}
	case *ast.ArrayType:
		switch a := t.Underlying().(type) {
		case *types.Array:
			c.bindSyntheticChannelTypes(x.Elt, a.Elem())
		case *types.Slice:
			c.bindSyntheticChannelTypes(x.Elt, a.Elem())
		}
	case *ast.MapType:
		if m, ok := t.Underlying().(*types.Map); ok {
			c.bindSyntheticChannelTypes(x.Key, m.Key())
			c.bindSyntheticChannelTypes(x.Value, m.Elem())
		}
	case *ast.StructType:
		if st, ok := t.Underlying().(*types.Struct); ok {
			i := 0
			for _, f := range x.Fields.List {
				if i >= st.NumFields() {
					break
				}
				c.bindSyntheticChannelTypes(f.Type, st.Field(i).Type())
				n := len(f.Names)
				if n == 0 {
					n = 1
				}
				i += n
			}
		}
	case *ast.FuncType:
		if fn, ok := t.Underlying().(*types.Signature); ok {
			fields(x.Params, fn.Params())
			fields(x.Results, fn.Results())
		}
	case *ast.ParenExpr:
		c.bindSyntheticChannelTypes(x.X, t)
	case *ast.IndexExpr:
		if n, ok := types.Unalias(t).(*types.Named); ok && n.TypeArgs().Len() == 1 {
			c.bindSyntheticChannelTypes(x.Index, n.TypeArgs().At(0))
		}
	case *ast.IndexListExpr:
		if n, ok := types.Unalias(t).(*types.Named); ok && n.TypeArgs().Len() == len(x.Indices) {
			for i, a := range x.Indices {
				c.bindSyntheticChannelTypes(a, n.TypeArgs().At(i))
			}
		}
	}
}
