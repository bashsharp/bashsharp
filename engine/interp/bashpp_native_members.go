package interp

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
import (
	"context"
	"fmt"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A receiver field remains in native storage while its method is resolved.
// Ordinary field reads still copy value types, as Go assignments require.
func (r *Runner) bashPPNativeReceiver(expr syntax.BashPPExpr) (bashPPBridgeValue, error) {
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.bashPPNativeReceiver(x.X)
	case *syntax.BashPPSelectorExpr:
		if r.bashPPNativeExpr(x.X) {
			base, err := r.bashPPNativeReceiver(x.X)
			if err != nil {
				return bashPPBridgeValue{}, err
			}
			return r.bashPPNativeAccess(r.ectx, "receiver_field", base, x.Sel.Value)
		}
	}
	return r.bashPPBridgeExpr(expr)
}

// Capture the receiver and typed operands now; later rebinding cannot redirect
// a deferred cleanup. The captured receiver is already the value Go evaluates
// at the defer statement, so keep its selector on the eventual call request.
// Binding a native method value first would preserve no additional semantics
// and would cost a second dependency round trip for every deferred method call.
// No original statement or expression reaches the helper.
func (r *Runner) bashPPNativeCapture(ctx context.Context, call *syntax.BashPPCall) (func(context.Context) error, bool, error) {
	if !r.bashPPBridgeHandles(call) {
		return nil, false, nil
	}
	q, err := r.bashPPPrepareNativeCall(ctx, call)
	if err != nil {
		return nil, true, err
	}
	return func(ctx context.Context) error {
		req, err := r.bashPPEvalRequest()
		if err != nil {
			return err
		}
		_, err = r.bashPPNativeRequest(ctx, req, q)
		return err
	}, true, nil
}

func (r *Runner) bashPPBindNativeMethod(ctx context.Context, receiver bashPPBridgeValue, selector string) (bashPPBridgeValue, error) {
	req, err := r.bashPPEvalRequest()
	if err != nil {
		return bashPPBridgeValue{}, err
	}
	values, err := r.bashPPNativeRequest(ctx, req, bashPPBridgeRequest{Op: "get", Selector: selector, Receiver: &receiver})
	if err != nil {
		return bashPPBridgeValue{}, err
	}
	if len(values) != 1 {
		return bashPPBridgeValue{}, fmt.Errorf("gosource: native method binding returned %d values", len(values))
	}
	bound := values[0]
	if receiver.NativeType != "" {
		bound.Callable = receiver.NativeType + "." + selector
	}
	return bound, nil
}

// bashPPNativeMethodReceiver evaluates the receiver of a dependency-owned
// method call. The receiver expression is evaluated once: either it is native
// itself, or method is promoted from an embedded imported field, in which case
// that field is read out of the already-walked local storage rather than
// re-evaluated. Sprint: #118; Story: #54; Story-ID: c3a60493cde9
func (r *Runner) bashPPNativeMethodReceiver(expr syntax.BashPPExpr, method string) (bashPPBridgeValue, error) {
	if !r.bashPPNativeExpr(expr) {
		if promoted := r.bashPPPromotedNativeReceiver(expr, method); promoted != nil {
			return *promoted, nil
		}
	}
	return r.bashPPNativeReceiver(expr)
}
