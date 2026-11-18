package interp

// Sprint: #118; Story: #53; Story-ID: 99bd1de0093b
import (
	"fmt"
	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"go/constant"
	"strconv"
)

// goSourceCallArguments evaluates original expressions exactly once and retains
// each result cell. A computed struct, pointer, interface or channel cannot be
// reconstructed from the legacy argument word after its producer has returned.
func (r *Runner) goSourceCallArguments(call *syntax.BashPPCall, fn *bashPPFunc) ([]string, bool, error) {
	if len(call.ArgExprs) != len(call.Args) {
		return nil, false, fmt.Errorf("gosource: missing positioned call argument")
	}
	cells := make([]*bashPPCell, 0, len(call.ArgExprs))
	for _, expr := range call.ArgExprs {
		// `f(g())` with a multi-result g is the one place a call's results are
		// spread; g's every result is an argument, in order.
		results, err := r.goSourceValueCells(expr, len(call.ArgExprs) == 1)
		if err != nil {
			return nil, false, err
		}
		for _, cell := range results {
			// One snapshot: the task-group test and the argument copy must
			// describe the same store of a cell the callee's launcher may be
			// rebinding. See bashpp_cell_share.go.
			cell = cell.view()
			if cell.channel != nil && cell.channelOwner != r.bashPPConcurrent {
				return nil, false, fmt.Errorf("channel belongs to another task group")
			}
			cells = append(cells, bashPPCopyAssignmentCell(cell))
		}
	}
	if fn.skipArgs > len(cells) {
		return nil, false, fmt.Errorf("gosource: missing method expression receiver")
	}
	cells = cells[fn.skipArgs:]
	args := make([]string, len(cells))
	channels := make([]*bashPPChannel, len(cells))
	interfaces := make([]*bashPPInterfaceValue, len(cells))
	for i, cell := range cells {
		// Typed aggregate arguments already carry their complete value cell.
		// Stringifying them would traverse referenced storage that another
		// goroutine may legally mutate under its own synchronization.
		if cell.vr.Kind != expand.Object {
			args[i] = cell.vr.String()
		}
		channels[i] = cell.channel
		interfaces[i] = cell.interfaceValue
	}
	r.bashPPCallCells, r.bashPPCallChannels, r.bashPPCallInterfaces = cells, channels, interfaces
	r.bashPPCallSpread = call.Ellipsis.IsValid()
	return args, true, nil
}

// goSourceContextualFloatCallArgs applies a float parameter's destination type
// to the exact scalar cell that accompanies a Go-source call argument. The
// legacy args slice is still text and must remain strict: only a numeric cell
// produced by the Go expression evaluator is rewritten, so a string containing
// "6/5" keeps failing for a float parameter.
func (r *Runner) goSourceContextualFloatCallArgs(params []bashPPParam, args []string, cells []*bashPPCell, spread bool) ([]string, []*bashPPCell, error) {
	if !r.bashPPGoSource || spread || len(cells) == 0 {
		return args, cells, nil
	}
	var outArgs []string
	var outCells []*bashPPCell
	for i, cell := range cells {
		if i >= len(args) || cell == nil || len(params) == 0 {
			continue
		}
		// The trailing `...float64` parameter receives every remaining
		// argument as one element each (fixedbugs/issue58671 infers it for
		// `g(1, 'a', 2.3)`); a spread call was excluded above.
		if i >= len(params) && !params[len(params)-1].variadic {
			continue
		}
		param := params[min(i, len(params)-1)]
		expected, ok := r.bashPPUnderlyingType(param.typ).(*syntax.BashPPNamedType)
		if !ok || expected.Name == nil || (expected.Name.Value != "float32" && expected.Name.Value != "float64") {
			continue
		}
		scalar := r.bashPPScalarFromCell(cell)
		if scalar.value == nil || scalar.value.Kind() != constant.Float {
			continue
		}
		converted, err := r.bashPPConvertScalar(expected.Name.Value, scalar)
		if err != nil {
			return nil, nil, err
		}
		copy := bashPPCopyAssignmentCell(cell)
		copy.vr = expand.Variable{Set: true, Kind: expand.String, Str: bashPPScalarStorageString(converted)}
		copy.scalarKind = constant.Float
		copy.negativeZero = converted.negativeZero
		copy.nonFinite = converted.nonFinite
		copy.hasNonFinite = converted.hasNonFinite
		copy.declType = param.typ
		copy.typeName = param.declared
		if outArgs == nil {
			outArgs = append([]string(nil), args...)
			outCells = append([]*bashPPCell(nil), cells...)
		}
		outArgs[i] = goSourceRoundedFloatArgText(converted.value, expected.Name.Value)
		outCells[i] = copy
	}
	if outArgs != nil {
		return outArgs, outCells, nil
	}
	return args, cells, nil
}

func goSourceRoundedFloatArgText(value constant.Value, typ string) string {
	if typ == "float32" {
		f, _ := constant.Float32Val(value)
		return strconv.FormatFloat(float64(f), 'g', -1, 32)
	}
	f, _ := constant.Float64Val(value)
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func (r *Runner) goSourceBuiltinResult(call *syntax.BashPPCall) (*bashPPCell, bool, error) {
	if !r.bashPPEnabled() || r.PosixMode() || call.CalleeExpr != nil {
		return nil, false, nil
	}
	if cell, handled, err := r.goSourceComplexBuiltinCell(call); handled {
		return cell, true, err
	}
	name := bashPPPredeclaredCall(call)
	switch name {
	case "len", "cap", "append", "copy", "make", "new", "min", "max":
	default:
		return nil, false, nil
	}
	if r.bashPPFuncs[name] != nil || (r.bashPPScope != nil && r.bashPPScope.lookup(name) != nil) {
		return nil, false, nil
	}
	cell, ok := r.bashPPRunValueBuiltin(name, call)
	if !ok || cell == nil {
		return nil, true, errBashPPScalarInterrupted
	}
	return cell, true, nil
}
