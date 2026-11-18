package gosource

import (
	"go/ast"
	"go/types"
)

func (c *converter) localChannelType(t types.Type) bool {
	if c.localChannelTypes == nil {
		c.planLocalChannelTypes()
	}
	return c.localChannelTypes[channelTypeKey(t)]
}

func channelTypeKey(t types.Type) string {
	if t == nil {
		return ""
	}
	ch, ok := t.Underlying().(*types.Chan)
	if !ok {
		return ""
	}
	// Named and directional views can share a channel. Group their allocation
	// domains by the bidirectional underlying shape, conservatively.
	return types.TypeString(types.NewChan(types.SendRecv, ch.Elem()), func(p *types.Package) string { return p.Path() })
}

// channelTypeKeys finds channels hidden in tuples and aggregates. Native
// results such as *time.Timer therefore carry the provenance of Timer.C.
func channelTypeKeys(t types.Type) []string {
	seen, keys := map[types.Type]bool{}, map[string]bool{}
	var visit func(types.Type)
	visit = func(t types.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		if key := channelTypeKey(t); key != "" {
			keys[key] = true
			visit(t.Underlying().(*types.Chan).Elem())
			return
		}
		switch x := t.Underlying().(type) {
		case *types.Pointer:
			visit(x.Elem())
		case *types.Array:
			visit(x.Elem())
		case *types.Slice:
			visit(x.Elem())
		case *types.Map:
			visit(x.Key())
			visit(x.Elem())
		case *types.Struct:
			for i := 0; i < x.NumFields(); i++ {
				visit(x.Field(i).Type())
			}
		case *types.Signature:
			visit(x.Params())
			visit(x.Results())
		case *types.Interface:
			for i := 0; i < x.NumMethods(); i++ {
				visit(x.Method(i).Type())
			}
		case *types.Tuple:
			for i := 0; i < x.Len(); i++ {
				visit(x.At(i).Type())
			}
		}
	}
	visit(t)
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	return out
}

// channelTypeKeyMemo caches only completed top-level traversals. Keeping the
// traversal-local seen set in channelTypeKeys avoids publishing an incomplete
// result while walking a recursive type graph.
type channelTypeKeyMemo map[types.Type][]string

func (m channelTypeKeyMemo) keys(t types.Type) []string {
	if keys, ok := m[t]; ok {
		return keys
	}
	keys := channelTypeKeys(t)
	m[t] = keys
	return keys
}

func (c *converter) planLocalChannelTypes() {
	parent, candidates, boundary := map[string]string{}, map[string]bool{}, map[string]bool{}
	typeKeys := channelTypeKeyMemo{}
	opaque := false
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" {
			parent[x] = x
		}
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b string) {
		if a != "" && b != "" {
			a, b = find(a), find(b)
			if a != b {
				parent[b] = a
			}
		}
	}
	unionTypes := func(a, b types.Type) {
		aKeys, bKeys := typeKeys.keys(a), typeKeys.keys(b)
		for _, x := range aKeys {
			for _, y := range bKeys {
				union(x, y)
			}
		}
	}
	mark := func(t types.Type) {
		for _, key := range typeKeys.keys(t) {
			boundary[key] = true
		}
	}

	// Imported function values remain imported through arbitrary local aliases.
	nativeFuncs := map[types.Object]bool{}
	var native func(ast.Expr) bool
	native = func(e ast.Expr) bool {
		switch x := ast.Unparen(e).(type) {
		case *ast.Ident:
			o := c.info.ObjectOf(x)
			return nativeFuncs[o] || o != nil && o.Pkg() != nil && o.Pkg().Path() != c.packagePath
		case *ast.SelectorExpr:
			if s := c.info.Selections[x]; s != nil {
				o := s.Obj()
				if o != nil && o.Pkg() != nil && o.Pkg().Path() != c.packagePath {
					return true
				}
				if _, ok := s.Recv().Underlying().(*types.Interface); ok {
					return true
				}
				return false
			}
			o := c.info.ObjectOf(x.Sel)
			return o != nil && o.Pkg() != nil && o.Pkg().Path() != c.packagePath
		}
		return false
	}
	for changed := true; changed; {
		changed = false
		for _, f := range c.files {
			ast.Inspect(f, func(n ast.Node) bool {
				bind := func(l, r ast.Expr) {
					id, ok := ast.Unparen(l).(*ast.Ident)
					if ok && native(r) {
						if o := c.info.ObjectOf(id); o != nil && !nativeFuncs[o] {
							nativeFuncs[o], changed = true, true
						}
					}
				}
				switch x := n.(type) {
				case *ast.AssignStmt:
					if len(x.Lhs) == len(x.Rhs) {
						for i := range x.Lhs {
							bind(x.Lhs[i], x.Rhs[i])
						}
					}
				case *ast.ValueSpec:
					if len(x.Names) == len(x.Values) {
						for i := range x.Names {
							bind(x.Names[i], x.Values[i])
						}
					}
				}
				return true
			})
		}
	}

	for _, f := range c.files {
		ast.Inspect(f, func(n ast.Node) bool {
			if e, ok := n.(ast.Expr); ok {
				t := c.info.TypeOf(e)
				if channelOpaqueStorage(t, map[types.Type]bool{}) {
					opaque = true
				}
				for _, key := range typeKeys.keys(t) {
					candidates[key] = true
				}
			}
			switch x := n.(type) {
			case *ast.Ident:
				// An imported function or variable can cross through a local
				// parameter, return value, or aggregate before it is called/read.
				// Its complete signature/type is a boundary even without a
				// syntactically direct imported call.
				if obj := c.info.ObjectOf(x); obj != nil && obj.Pkg() != nil && obj.Pkg().Path() != c.packagePath {
					mark(obj.Type())
					if obj.Pkg().Path() == "unsafe" {
						opaque = true
					}
				}
			case *ast.TypeAssertExpr:
				// An opaque interface can conceal a native channel. A checked
				// assertion reveals its destination type but not its provenance.
				mark(c.info.TypeOf(x))
			case *ast.SelectStmt:
				var first string
				for _, item := range x.Body.List {
					clause, _ := item.(*ast.CommClause)
					if clause == nil || clause.Comm == nil {
						continue
					}
					var e ast.Expr
					switch y := clause.Comm.(type) {
					case *ast.SendStmt:
						e = y.Chan
					case *ast.ExprStmt:
						if r, ok := ast.Unparen(y.X).(*ast.UnaryExpr); ok {
							e = r.X
						}
					case *ast.AssignStmt:
						if len(y.Rhs) == 1 {
							if r, ok := ast.Unparen(y.Rhs[0]).(*ast.UnaryExpr); ok {
								e = r.X
							}
						}
					}
					key := channelTypeKey(c.info.TypeOf(e))
					if key == "" {
						continue
					}
					candidates[key] = true
					if first == "" {
						first = key
					} else {
						union(first, key)
					}
				}
				// Do not prune: native calls in arm bodies invalidate the proof.
			case *ast.CallExpr:
				// Only a statically bound local function, literal, builtin or
				// conversion is known not to call the dependency worker.
				if native(x.Fun) || !c.localChannelCall(x.Fun) {
					mark(c.info.TypeOf(x))
					if sel, ok := ast.Unparen(x.Fun).(*ast.SelectorExpr); ok {
						mark(c.info.TypeOf(sel.X))
					}
					for _, a := range x.Args {
						mark(c.info.TypeOf(a))
						if channelCallbackType(c.info.TypeOf(a), c.packagePath) {
							opaque = true
						}
					}
				}
			case *ast.AssignStmt:
				for _, l := range x.Lhs {
					for _, r := range x.Rhs {
						unionTypes(c.info.TypeOf(l), c.info.TypeOf(r))
						if t := c.info.TypeOf(l); t != nil {
							if _, ok := t.Underlying().(*types.Interface); ok {
								mark(c.info.TypeOf(r))
							}
						}
					}
				}
			case *ast.ValueSpec:
				for _, n := range x.Names {
					for _, v := range x.Values {
						unionTypes(c.info.TypeOf(n), c.info.TypeOf(v))
						if t := c.info.TypeOf(n); t != nil {
							if _, ok := t.Underlying().(*types.Interface); ok {
								mark(c.info.TypeOf(v))
							}
						}
					}
				}
			case *ast.SendStmt:
				unionTypes(c.info.TypeOf(x.Chan), c.info.TypeOf(x.Value))
			}
			return true
		})
	}
	bad := map[string]bool{}
	for key := range boundary {
		bad[find(key)] = true
	}
	c.localChannelTypes = map[string]bool{}
	for key := range candidates {
		if !opaque && !bad[find(key)] {
			c.localChannelTypes[key] = true
		}
	}
}

// Opaque storage loses the concrete provenance needed by a type-wide proof.
// Reject the package certificate rather than attempt dynamic migration. Do not
// descend into function signatures here: ordinary fmt calls have ...any formal
// parameters, but their actual arguments are examined at the call boundary.
func channelOpaqueStorage(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	switch x := t.Underlying().(type) {
	case *types.Interface:
		return true
	case *types.Pointer:
		return channelOpaqueStorage(x.Elem(), seen)
	case *types.Slice:
		return channelOpaqueStorage(x.Elem(), seen)
	case *types.Array:
		return channelOpaqueStorage(x.Elem(), seen)
	case *types.Map:
		return channelOpaqueStorage(x.Key(), seen) || channelOpaqueStorage(x.Elem(), seen)
	case *types.Struct:
		for i := 0; i < x.NumFields(); i++ {
			if channelOpaqueStorage(x.Field(i).Type(), seen) {
				return true
			}
		}
	}
	return false
}

// A callable or method-bearing object may expose captured package storage
// without mentioning it in its signature. Keep every channel native when such
// a value is offered to an unknown/native callee, even through an aggregate.
func channelCallbackType(t types.Type, packagePath string) bool {
	seen := map[types.Type]bool{}
	var visit func(types.Type) bool
	visit = func(t types.Type) bool {
		if t == nil || seen[t] {
			return false
		}
		seen[t] = true
		for _, receiver := range []types.Type{t, types.NewPointer(t)} {
			methods := types.NewMethodSet(receiver)
			for i := 0; i < methods.Len(); i++ {
				obj := methods.At(i).Obj()
				if obj.Pkg() != nil && obj.Pkg().Path() == packagePath {
					return true
				}
			}
		}
		switch x := t.Underlying().(type) {
		case *types.Signature:
			return true
		case *types.Pointer:
			return visit(x.Elem())
		case *types.Array:
			return visit(x.Elem())
		case *types.Slice:
			return visit(x.Elem())
		case *types.Map:
			return visit(x.Key()) || visit(x.Elem())
		case *types.Struct:
			for i := 0; i < x.NumFields(); i++ {
				if visit(x.Field(i).Type()) {
					return true
				}
			}
		}
		return false
	}
	return visit(t)
}

func (c *converter) localChannelCall(e ast.Expr) bool {
	e = ast.Unparen(e)
	if c.info.Types[e].IsType() {
		return true
	}
	switch x := e.(type) {
	case *ast.FuncLit:
		return true
	case *ast.IndexExpr:
		return c.localChannelCall(x.X)
	case *ast.IndexListExpr:
		return c.localChannelCall(x.X)
	case *ast.Ident:
		switch o := c.info.ObjectOf(x).(type) {
		case *types.Builtin:
			return true
		case *types.Func:
			return o.Pkg() != nil && o.Pkg().Path() == c.packagePath
		}
	case *ast.SelectorExpr:
		if sel := c.info.Selections[x]; sel != nil {
			_, iface := sel.Recv().Underlying().(*types.Interface)
			o, ok := sel.Obj().(*types.Func)
			return !iface && ok && o.Pkg() != nil && o.Pkg().Path() == c.packagePath
		}
	}
	return false
}
