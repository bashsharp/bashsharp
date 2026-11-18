package interp

// Sprint: #118; Story: #51; Story-ID: 825f8083451e
import (
	"context"
	"errors"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strconv"
	"strings"
)

func (r *Runner) goSourceNativeChannel(cell *bashPPCell) (*bashPPBridgeValue, bool) {
	if !r.bashPPGoSource || cell == nil || cell.vr.Kind != expand.Object {
		return nil, false
	}
	value, ok := cell.vr.Obj.(*bashPPBridgeValue)
	if !ok || value == nil {
		return nil, false
	}
	name := value.Type
	if value.Kind != "handle" && value.Kind != "nil" {
		return nil, false
	}
	if !strings.HasPrefix(name, "chan ") && !strings.HasPrefix(name, "<-chan ") && !strings.HasPrefix(name, "chan<- ") {
		if _, typed := r.bashPPUnderlyingType(cell.declType).(*syntax.BashPPChanType); !typed {
			return nil, false
		}
	}
	copy := *value
	return &copy, true
}

// A channel retains its value after the call returns. Local reference storage
// cannot be copied into native memory without breaking its alias identity.
func (r *Runner) goSourceNativeChannelPayload(expr syntax.BashPPExpr) (bashPPBridgeValue, error) {
	value, err := r.bashPPBridgeExpr(expr)
	if err != nil {
		return value, err
	}
	callbacks := map[string]bool{}
	for _, typ := range r.bashPPLocalTypeDescriptors() {
		callbacks[typ.Name] = len(typ.Methods) > 0 || len(typ.OmittedMethods) > 0
	}
	var check func(bashPPBridgeValue) error
	check = func(v bashPPBridgeValue) error {
		switch v.Kind {
		case "slice", "map", "pointer", "callback":
			return fmt.Errorf("gosource: native channel cannot retain interpreter-owned reference values")
		}
		if v.Callbacks || v.Origin != 0 || callbacks[strings.TrimPrefix(strings.TrimPrefix(v.Type, "*"), "main.")] {
			return fmt.Errorf("gosource: native channel cannot retain original callback identity")
		}
		for _, e := range v.Elements {
			if err := check(e); err != nil {
				return err
			}
		}
		for _, e := range v.Fields {
			if err := check(e); err != nil {
				return err
			}
		}
		return nil
	}
	return value, check(value)
}
func (r *Runner) goSourceNativeChoose(ctx context.Context, cases []bashPPBridgeValue, hasDefault bool) (int, *bashPPCell, bool, error) {
	req, err := r.bashPPEvalRequest()
	if err != nil {
		return 0, nil, false, err
	}
	taskCtx := r.bashPPTaskContext(ctx)
	q := bashPPBridgeRequest{Op: "channel-select", Selector: "probe", Args: cases}
	values, err := r.bashPPNativeRequest(taskCtx, req, q)
	parse := func(values []bashPPBridgeValue) (int, *bashPPCell, bool, error) {
		if len(values) != 3 {
			return 0, nil, false, fmt.Errorf("gosource: incomplete native selection reply")
		}
		i, e := strconv.Atoi(values[0].Text)
		if e != nil || i < -1 || i >= len(cases) {
			return 0, nil, false, fmt.Errorf("gosource: invalid native selection index")
		}
		open, e := strconv.ParseBool(values[2].Text)
		if e != nil {
			return 0, nil, false, e
		}
		return i, goSourceNativeValueCell(values[1]), open, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	index, cell, open, err := parse(values)
	if err != nil || index >= 0 || hasDefault {
		return index, cell, open, err
	}
	if !r.bashPPArmBeforeBlock(ctx) {
		if err := taskCtx.Err(); err != nil {
			return 0, nil, false, err
		}
		return 0, nil, false, errBashPPScalarInterrupted
	}
	q.Selector = ""
	values, err = r.bashPPNativeRequest(taskCtx, req, q)
	if err != nil {
		return 0, nil, false, err
	}
	index, cell, open, err = parse(values)
	if err == nil && index < 0 {
		err = fmt.Errorf("gosource: blocking native selection returned default")
	}
	return index, cell, open, err
}
func (r *Runner) goSourceNativeChannelError(err error) {
	if err == nil || errors.Is(err, errBashPPScalarInterrupted) {
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		r.bashPPTaskCanceled = true
		r.exit = exitStatus{code: 1}
		return
	}
	if strings.Contains(err.Error(), "native dependency panic: send on closed channel") {
		r.bashPPClosedSend()
		return
	}
	if !r.bashPPPanicking() {
		r.exit.fatal(err)
	}
}
func (r *Runner) goSourceNativeReceive(ctx context.Context, c *bashPPChannel, lhs []*syntax.Lit) (*bashPPCell, bool) {
	_, cell, open, err := r.goSourceNativeChoose(ctx, []bashPPBridgeValue{{Kind: "recv", Elements: []bashPPBridgeValue{*c.native}}}, false)
	if err != nil {
		r.goSourceNativeChannelError(err)
		return nil, false
	}
	if len(lhs) > 0 {
		r.bashPPBindReceivedCell(lhs[0].Value, cell)
		if len(lhs) == 2 {
			r.bashPPDeclareName(lhs[1].Value, expand.Variable{Set: true, Kind: expand.String, Str: strconv.FormatBool(open)})
		}
	}
	return cell, open
}
func (r *Runner) goSourceNativeSend(ctx context.Context, c *bashPPChannel, send *syntax.BashPPSend) {
	value, err := r.goSourceNativeChannelPayload(send.ValueExpr)
	if err != nil {
		r.bashPPGoSendError(send.ValueExpr, err)
		return
	}
	_, _, _, err = r.goSourceNativeChoose(ctx, []bashPPBridgeValue{{Kind: "send", Elements: []bashPPBridgeValue{*c.native, value}}}, false)
	r.goSourceNativeChannelError(err)
}
func (r *Runner) goSourceNativeSelect(ctx context.Context, cases []bashPPBridgeValue, arms []*syntax.BashPPSelectCase, def *syntax.BashPPSelectCase) {
	i, cell, open, err := r.goSourceNativeChoose(ctx, cases, def != nil)
	if err != nil {
		r.goSourceNativeChannelError(err)
		return
	}
	arm := def
	if i >= 0 {
		arm = arms[i]
	}
	r.goSourceNativeSelectArm(ctx, arm, cell, open)
}

// goSourceNativeSelectArm binds a dependency-selected arm's received value
// and runs its body; a default arm receives nothing.
func (r *Runner) goSourceNativeSelectArm(ctx context.Context, arm *syntax.BashPPSelectCase, cell *bashPPCell, open bool) {
	if arm == nil {
		r.exit.fatal(fmt.Errorf("gosource: native selection has no winning arm"))
		return
	}
	leave := r.bashPPPushScope()
	defer leave()
	if decl, ok := arm.Comm.(*syntax.BashPPShortDecl); ok {
		r.bashPPBindReceivedCell(decl.Lhs[0].Value, cell)
		if len(decl.Lhs) == 2 {
			r.bashPPDeclareName(decl.Lhs[1].Value, expand.Variable{Set: true, Kind: expand.String, Str: strconv.FormatBool(open)})
		}
	} else if assign, ok := arm.Comm.(*syntax.BashPPAssign); ok {
		r.bashPPSelectReceiveAssign(assign, cell, open)
	}
	r.stmts(r.bashPPTaskContext(ctx), arm.Stmts)
	if r.bashPPBranch == bashPPBranchBreak {
		if r.bashPPBranchEscapesEligible() {
			return
		}
		r.bashPPClearBranch()
	}
}

// Assignability checks only types, never channel readiness or contents.
func (r *Runner) goSourceNativeChannelFits(cell *bashPPCell, target *syntax.BashPPChanType) bool {
	value, ok := r.goSourceNativeChannel(cell)
	if !ok {
		return false
	}
	req, err := r.bashPPEvalRequest()
	if err != nil {
		return false
	}
	values, err := r.bashPPNativeRequest(r.bashPPTaskContext(r.ectx), req, bashPPBridgeRequest{Op: "channel-type", Selector: goSourceNativeChannelTypeText(target), Receiver: value})
	return err == nil && len(values) == 1 && values[0].Kind == "bool" && values[0].Text == "true"
}

// goSourceNativeNilChannel reports a dependency channel operand whose value is
// nil: a descriptor with no handle and no session authority, or a handle the
// dependency itself marked as holding a nil channel. Communication on
// a nil channel never proceeds, so a select arm on it is disabled exactly as
// the language disables it, and it needs no dependency arbitration.
func goSourceNativeNilChannel(value *bashPPBridgeValue) bool {
	return value != nil && (value.Kind == "nil" && value.Handle == 0 || value.Kind == "handle" && value.NilChannel)
}
