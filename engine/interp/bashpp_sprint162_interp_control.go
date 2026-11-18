package interp

import (
	"go/constant"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPRecoverInterfaceValue is the expression-position form of recover.
// Keeping the payload in an interface cell preserves its dynamic Go value for
// equality and type assertions; rendered shell text remains only a view.
func (r *Runner) bashPPRecoverInterfaceValue() (*bashPPInterfaceValue, bool) {
	value, ok := r.bashPPRecover()
	if !ok {
		return &bashPPInterfaceValue{nilIface: true}, false
	}
	return bashPPBoxPanicValue(value), true
}

// bashPPBoxPanicValue is the interface value a recovered panic payload
// presents: an interface value as itself, a scalar boxed by its kind.
func bashPPBoxPanicValue(value any) *bashPPInterfaceValue {
	if iv, ok := value.(*bashPPInterfaceValue); ok {
		return iv
	}
	constantValue := bashPPScalarConstant(value)
	kind := constantValue.Kind()
	typ := bashPPDefaultScalarTypeName(kind)
	cell := &bashPPCell{
		vr:          expand.Variable{Set: true, Kind: expand.String, Str: bashPPScalarString(constantValue)},
		exactScalar: constantValue, scalarKind: kind,
		declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: typ}},
	}
	return &bashPPInterfaceValue{dynamic: cell.declType, cell: cell}
}

func bashPPScalarConstant(value any) constant.Value {
	switch v := value.(type) {
	case string:
		return constant.MakeString(v)
	case int:
		return constant.MakeInt64(int64(v))
	case float64:
		return constant.MakeFloat64(v)
	case bool:
		return constant.MakeBool(v)
	default:
		return constant.MakeString("<panic>")
	}
}

func bashPPRecoverExpr(expr syntax.BashPPExpr) bool {
	call, ok := expr.(*syntax.BashPPCall)
	return ok && len(call.Fun) == 1 && call.Fun[0].Value == "recover" && len(call.Args) == 0 && call.CalleeExpr == nil
}
