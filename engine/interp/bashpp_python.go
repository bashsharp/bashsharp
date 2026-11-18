package interp

import (
	"fmt"
	"go/constant"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (r *Runner) bashPPPythonValue(expr syntax.BashPPExpr) (any, bool, error) {
	switch x := expr.(type) {
	case *syntax.BashPPIdent:
		if r.bashPPScope == nil {
			return nil, false, nil
		}
		cell := r.bashPPScope.lookup(x.Name.Value).view()
		if cell == nil || cell.vr.Kind != expand.Object {
			return nil, false, nil
		}
		handle, ok := cell.vr.Obj.(*polyglot.Handle)
		return handle, ok, nil
	case *syntax.BashPPSelectorExpr:
		if id, ok := x.X.(*syntax.BashPPIdent); ok {
			// `mod.attr` on a direct import reads the module attribute,
			// unless a local value shadows the alias.
			if module := r.bashPPImportModule(id.Name.Value); module != nil && (r.bashPPScope == nil || r.bashPPScope.lookup(id.Name.Value) == nil) {
				result, err := module.Attr(r.ectx, x.Sel.Value)
				if result.Stdout != "" {
					fmt.Fprint(r.stdout, result.Stdout)
				}
				if result.Stderr != "" {
					fmt.Fprint(r.stderr, result.Stderr)
				}
				return result.Value, true, err
			}
		}
		base, handled, err := r.bashPPPythonValue(x.X)
		if err != nil || !handled {
			return nil, handled, err
		}
		handle, ok := base.(*polyglot.Handle)
		if !ok {
			return nil, true, fmt.Errorf("Python selector parent is not an object")
		}
		result, err := handle.GetAttr(r.ectx, x.Sel.Value)
		if result.Stdout != "" {
			fmt.Fprint(r.stdout, result.Stdout)
		}
		if result.Stderr != "" {
			fmt.Fprint(r.stderr, result.Stderr)
		}
		return result.Value, true, err
	}
	return nil, false, nil
}

func bashPPPythonScalar(value any) (bashPPScalar, error) {
	switch value := value.(type) {
	case string:
		return bashPPScalar{value: constant.MakeString(value), runtime: true}, nil
	case bool:
		return bashPPScalar{value: constant.MakeBool(value), runtime: true}, nil
	case int64:
		return bashPPScalar{value: constant.MakeInt64(value), runtime: true}, nil
	case float64:
		return bashPPScalar{value: constant.MakeFloat64(value), runtime: true}, nil
	}
	return bashPPScalar{}, fmt.Errorf("Python value %T is not scalar", value)
}
