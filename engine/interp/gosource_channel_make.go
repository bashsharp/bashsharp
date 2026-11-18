package interp

// Sprint: #118; Story: #51; Story-ID: 825f8083451e
import (
	"context"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"go/constant"
	"strconv"
	"strings"
	"time"
)

// Keep original reference-bearing values in their interpreter-owned storage.
// Scalar channels and native-owned element types can use actual Go channels
// in the same dependency session without copying original reference identity.
func (r *Runner) goSourceNativeChannelElement(typ syntax.BashPPTypeExpr, seen map[string]bool) bool {
	if typ == nil {
		return false
	}
	if r.bashPPNativeType(typ) {
		return true
	}
	switch t := typ.(type) {
	case *syntax.BashPPStructType:
		return len(t.Fields) == 0
	case *syntax.BashPPNamedType:
		name := t.Name.Value
		for _, local := range r.bashPPLocalTypeDescriptors() {
			if local.Name == name && (len(local.Methods) > 0 || len(local.OmittedMethods) > 0) {
				return false
			}
		}
		switch name {
		case "bool", "string", "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte", "rune", "float32", "float64", "complex64", "complex128":
			return true
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		if decl, ok := r.bashPPTypes[name]; ok {
			return r.goSourceNativeChannelElement(decl.typeExpr, seen)
		}
	}
	return false
}
func (r *Runner) goSourceChannelCapacity(expr syntax.BashPPExpr, word *syntax.Word) (int, error) {
	if expr == nil {
		if word == nil {
			return 0, nil
		}
		n, err := strconv.Atoi(r.literal(word))
		if err != nil {
			return 0, fmt.Errorf("gosource: channel capacity requires an integer")
		}
		if n < 0 {
			r.bashPPRaise("makechan: size out of range")
			return 0, errBashPPScalarInterrupted
		}
		return n, nil
	}
	value, err := r.bashPPEvalScalarExpr(expr)
	if err != nil {
		return 0, err
	}
	n, ok := constant.Int64Val(constant.ToInt(value.value))
	if !ok || n < 0 || int64(int(n)) != n {
		r.bashPPRaise("makechan: size out of range")
		return 0, errBashPPScalarInterrupted
	}
	return int(n), nil
}
func (r *Runner) goSourceMakeNativeChannel(typ *syntax.BashPPChanType, expr syntax.BashPPExpr, word *syntax.Word, declared ...syntax.BashPPTypeExpr) (*bashPPCell, bool, error) {
	if !r.bashPPGoSource || typ == nil || typ.LocalDomain || !r.goSourceNativeChannelElement(typ.Element, map[string]bool{}) {
		return nil, false, nil
	}
	capacity, err := r.goSourceChannelCapacity(expr, word)
	if err != nil {
		return nil, true, err
	}
	req, err := r.bashPPEvalRequest()
	if err != nil {
		return nil, true, err
	}
	selector := goSourceNativeChannelTypeText(typ)
	if len(declared) > 0 {
		selector = r.bashPPBridgeTypeIdentity(declared[0])
	}
	values, err := r.bashPPNativeRequest(r.bashPPTaskContext(r.ectx), req, bashPPBridgeRequest{Op: "channel-make", Selector: selector, Args: []bashPPBridgeValue{{Kind: "int", Type: "int", Text: strconv.Itoa(capacity)}}})
	if err != nil {
		if message, ok := goSourceNativeMakeChannelPanic(err); ok {
			r.goSourceRuntimePanic(message)
			return nil, true, errBashPPScalarInterrupted
		}
		return nil, true, err
	}
	if len(values) != 1 {
		return nil, true, fmt.Errorf("gosource: channel make returned no value")
	}
	cell := goSourceNativeValueCell(values[0])
	cell.declType = typ
	return cell, true, nil
}

// A panic reported by this request came from the worker's channel-make
// dispatch. Preserve only panic values the Go runtime classifier recognizes;
// bridge validation failures and arbitrary reflect panics remain ordinary
// errors. Keeping this at the operation boundary avoids granting the same
// authority to an error string returned by any other native request.
func goSourceNativeMakeChannelPanic(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	message, ok := strings.CutPrefix(err.Error(), "native dependency panic: ")
	if !ok {
		return "", false
	}
	_, ok = bashPPRuntimeErrorPayload(message)
	if !ok {
		return "", false
	}
	return message, true
}
func (r *Runner) goSourceCloseChannel(expr syntax.BashPPExpr) {
	channel, ok := r.goSourceChannelOperand(expr, nil, "close")
	if !ok {
		return
	}
	r.goSourceCloseChannelValue(channel)
}

func (r *Runner) goSourceCloseChannelValue(channel *bashPPChannel) {
	if channel.native != nil {
		req, err := r.bashPPEvalRequest()
		if err == nil {
			_, err = r.bashPPNativeRequest(r.bashPPTaskContext(r.ectx), req, bashPPBridgeRequest{Op: "channel-close", Receiver: channel.native})
		}
		if err != nil {
			message := strings.TrimPrefix(err.Error(), "native dependency panic: ")
			if message == "close of nil channel" || message == "close of closed channel" {
				r.bashPPRaise(message)
				return
			}
			r.goSourceNativeChannelError(err)
		}
		return
	}
	if channel.ch == nil {
		r.bashPPRaise("close of nil channel")
		return
	}
	if !channel.close() {
		r.bashPPRaise("close of closed channel")
	}
}

// The range expression was evaluated once by the native range dispatcher.
func (r *Runner) goSourceRangeNativeChannel(ctx context.Context, rng *syntax.BashPPRange, value *bashPPBridgeValue) {
	if len(rng.Names) > 1 {
		r.bashPPRangeError(rng, "channel range permits at most one iteration variable")
		return
	}
	for {
		cell, open := r.goSourceNativeReceive(ctx, &bashPPChannel{native: value}, nil)
		if cell == nil || !open {
			return
		}
		leave := r.bashPPPushScope()
		if len(rng.Names) == 1 {
			r.bashPPBindReceivedCell(rng.Names[0].Value, cell)
		}
		r.cmd(r.bashPPTaskContext(ctx), rng.Body)
		leave()
		if r.exit.exiting || r.exit.returning || r.exit.fatalExit || r.loopControlPending() {
			return
		}
		switch r.bashPPBranch {
		case bashPPBranchBreak:
			if r.bashPPBranchEscapesEligible() {
				return
			}
			r.bashPPClearBranch()
			return
		case bashPPBranchContinue:
			if r.bashPPBranchEscapesEligible() {
				return
			}
			r.bashPPClearBranch()
		case bashPPBranchGoto:
			return
		}
	}
}

func goSourceNativeChannelTypeText(typ *syntax.BashPPChanType) string {
	return goSourceNativeChannelTypeTextIn(typ, nil)
}

func goSourceNativeChannelTypeTextIn(typ *syntax.BashPPChanType, scope bashPPBridgeTypeScope) string {
	prefix := "chan "
	if typ.Direction == "recv" {
		prefix = "<-chan "
	}
	if typ.Direction == "send" {
		prefix = "chan<- "
	}
	return prefix + bashPPBridgeTypeTextIn(typ.Element, scope)
}
func (r *Runner) goSourceReceiveAssign(assign *syntax.BashPPAssign) bool {
	if !r.bashPPGoSource || len(assign.Names) != 2 || len(assign.ValueExprs) != 1 {
		return false
	}
	receive, ok := assign.ValueExprs[0].(*syntax.BashPPUnaryExpr)
	if !ok || receive.Op == nil || receive.Op.Value != "<-" {
		return false
	}
	for _, name := range assign.Names {
		if name.Value == "_" {
			continue
		}
		cell := r.bashPPScope.lookup(name.Value).view()
		if cell == nil || cell.constant || cell.vr.ReadOnly {
			r.exit.fatal(fmt.Errorf("gosource: receive assignment target %s is not mutable", name.Value))
			return true
		}
	}
	cell, open := r.bashPPReceiveCell(r.ectx, &syntax.BashPPReceive{Arrow: receive.Pos(), ChanExpr: receive.X}, nil)
	if cell == nil {
		return true
	}
	r.bashPPCommitTupleAssign(assign, []*bashPPCell{cell, goSourceNativeValueCell(bashPPBridgeValue{Kind: "bool", Type: "bool", Text: strconv.FormatBool(open)})})
	return true
}

// time.Sleep is a known blocking imported operation. Its callee and argument
// are already captured; release the Go task launch handshake before waiting.
func (r *Runner) goSourceNativeSleepBoundary(ctx context.Context, req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if !r.bashPPGoSource || !r.bashPPGoTask || q.Op != "call" || len(q.Args) != 1 {
		return true
	}
	sleep := false
	alias, name, ok := strings.Cut(q.Selector, ".")
	if ok && req.Imports[alias] == "time" && name == "Sleep" {
		sleep = true
	}
	if q.Receiver != nil && q.Receiver.Callable == "time.Sleep" {
		sleep = true
	}
	if !sleep {
		return true
	}
	n, err := strconv.ParseInt(q.Args[0].Text, 10, 64)
	if err != nil || n <= 0 {
		return true
	}
	return r.bashPPArmBeforeBlock(ctx)
}

// goSourceLocalTimeSleep keeps a main-task time.Sleep in the interpreter.
// The dependency process owns time.Timer channels, but sleeping this goroutine
// advances the same monotonic clock without paying a request/reply round trip.
// That distinction matters on Windows, where the bridge latency between a
// select default arm and its 50 ms sleep can consume the next timer boundary.
// Launched tasks retain the dependency path and its launch-handshake rules,
// and so does a sleep inside a callback: cancelling it must tear down the
// dependency call that is waiting on the callback, not fail the callback.
func (r *Runner) goSourceLocalTimeSleep(ctx context.Context, req bashPPEvalRequest, q bashPPBridgeRequest) (bool, error) {
	if !r.bashPPGoSource || r.bashPPGoTask || req.CallbackDepth > 0 || q.Op != "call" || len(q.Args) != 1 || q.Spread {
		return false, nil
	}
	sleep := false
	alias, name, ok := strings.Cut(q.Selector, ".")
	if ok && req.Imports[alias] == "time" && name == "Sleep" {
		sleep = true
	}
	if q.Receiver != nil && q.Receiver.Callable == "time.Sleep" {
		sleep = true
	}
	if !sleep {
		return false, nil
	}
	n, err := strconv.ParseInt(q.Args[0].Text, 10, 64)
	if err != nil {
		return false, nil
	}
	if n <= 0 {
		return true, nil
	}
	timer := time.NewTimer(time.Duration(n))
	defer timer.Stop()
	select {
	case <-timer.C:
		return true, nil
	case <-ctx.Done():
		return true, ctx.Err()
	}
}

// time.Millisecond is a fixed typed constant. Resolve this authenticated
// import locally so a polling loop does not request it from the dependency
// process on every evaluation of a duration expression.
func (r *Runner) goSourceLocalTimeMillisecond(req bashPPEvalRequest, q bashPPBridgeRequest) (bashPPBridgeValue, bool) {
	if !r.bashPPGoSource || q.Op != "get" || q.Receiver != nil || len(q.Args) != 0 {
		return bashPPBridgeValue{}, false
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if !ok || req.Imports[alias] != "time" || name != "Millisecond" {
		return bashPPBridgeValue{}, false
	}
	return bashPPBridgeValue{Kind: "int", Type: "time.Duration", Text: strconv.FormatInt(int64(time.Millisecond), 10)}, true
}

// Round on a scalar time.Duration is pure. Its receiver and argument are
// already transported as signed nanoseconds, so use the standard library's
// implementation without a dependency-process request on the main task.
func (r *Runner) goSourceLocalDurationRound(q bashPPBridgeRequest) (bashPPBridgeValue, bool) {
	if !r.bashPPGoSource || r.bashPPGoTask || q.Op != "call" || q.Selector != "Round" ||
		q.Receiver == nil || q.Receiver.Kind != "int" || q.Receiver.Type != "time.Duration" ||
		len(q.Args) != 1 || q.Args[0].Kind != "int" || q.Spread {
		return bashPPBridgeValue{}, false
	}
	duration, err := strconv.ParseInt(q.Receiver.Text, 10, 64)
	if err != nil {
		return bashPPBridgeValue{}, false
	}
	unit, err := strconv.ParseInt(q.Args[0].Text, 10, 64)
	if err != nil {
		return bashPPBridgeValue{}, false
	}
	result := time.Duration(duration).Round(time.Duration(unit))
	return bashPPBridgeValue{Kind: "int", Type: "time.Duration", Text: strconv.FormatInt(int64(result), 10)}, true
}
