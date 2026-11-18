package interp

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 165, lane frames-1: the frames of Go's runtime that a stack walk
// sees around the program's own, and the walk itself: runtime.Callers and
// runtime.CallersFrames over the interpreter's frame table.
//
// A Go program's main goroutine runs under runtime.main, which runs under
// runtime.goexit; every other goroutine runs under runtime.goexit alone. A
// deferred call running for a panic is called by runtime.gopanic, which
// sits between it and the panicking frame. runtime.Caller(skip) counts
// those frames like any other, and a walk that skips past the program's
// last frame lands on them — a program probing its stack depth expects
// runtime.main and runtime.goexit where Go has them. Tracebacks elide them,
// as Go's do, except that debug.Stack lists itself first.
//
// The runtime frames are reported where the interpreter's own runtime has
// them: the table is taken once from this process's main goroutine at
// package init, so the file and line are those of a real runtime.main and
// runtime.goexit, not invented ones.

// goSourceRuntimeFrames are the runtime's frames as this process's runtime
// reports them; see goSourceProbeRuntimeFrames.
var goSourceRuntimeFrames struct {
	main, goexit, gopanic, callers, debugStack goSourceStackFrame
}

func init() {
	goSourceProbeRuntimeFrames()
}

// goSourceProbeRuntimeFrames fills goSourceRuntimeFrames from the frames
// around package init, which runs on the main goroutine under
// runtime.main; the panic frame comes from a recovered panic of its own.
func goSourceProbeRuntimeFrames() {
	set := &goSourceRuntimeFrames
	set.main = goSourceStackFrame{name: "runtime.main", file: "runtime/proc.go"}
	set.goexit = goSourceStackFrame{name: "runtime.goexit", file: "runtime/asm.s"}
	set.gopanic = goSourceStackFrame{name: "runtime.gopanic", file: "runtime/panic.go"}
	set.callers = goSourceStackFrame{name: "runtime.Callers", file: "runtime/extern.go"}
	set.debugStack = goSourceStackFrame{name: "runtime/debug.Stack", file: "runtime/debug/stack.go"}
	find := func(pcs []uintptr, want *goSourceStackFrame) {
		frames := runtime.CallersFrames(pcs)
		for {
			frame, more := frames.Next()
			if frame.Function == want.name {
				want.file, want.line = frame.File, uint(frame.Line)
				return
			}
			if !more {
				return
			}
		}
	}
	pcs := make([]uintptr, 64)
	n := runtime.Callers(0, pcs)
	find(pcs[:n], &set.callers)
	find(pcs[:n], &set.main)
	find(pcs[:n], &set.goexit)
	func() {
		defer func() {
			_ = recover()
			n := runtime.Callers(0, pcs)
			find(pcs[:n], &set.gopanic)
		}()
		panic("probe")
	}()
	// debug.Stack names itself first; its own position is on that line.
	lines := strings.Split(string(debug.Stack()), "\n")
	if len(lines) >= 3 && strings.HasPrefix(lines[1], set.debugStack.name+"(") {
		location := strings.TrimSpace(lines[2])
		if at := strings.LastIndex(location, " +0x"); at >= 0 {
			location = location[:at]
		}
		if colon := strings.LastIndex(location, ":"); colon >= 0 {
			if line, err := strconv.ParseUint(location[colon+1:], 10, 64); err == nil {
				set.debugStack.file, set.debugStack.line = location[:colon], uint(line)
			}
		}
	}
}

// goSourceCallerFrames is the stack a runtime.Caller or runtime.Callers walk
// from a call at top sees, innermost first: the interpreted frames and the
// runtime frames below them.
func (r *Runner) goSourceCallerFrames(top syntax.Pos) []goSourceStackFrame {
	frames := r.goSourceStackFrames(top)
	if !r.bashPPGoTask {
		frames = append(frames, goSourceRuntimeFrames.main)
	}
	return append(frames, goSourceRuntimeFrames.goexit)
}

// goSourceCallersValue answers runtime.Callers(skip, pc): the walk begins at
// runtime.Callers itself, and the return program counters of the frames
// from skip on fill pc up to its length.
func (r *Runner) goSourceCallersValue(call *syntax.BashPPCall) ([]bashPPBridgeValue, bool, error) {
	skip, err := r.goSourceStackInt(call.ArgExprs[0])
	if err != nil {
		return nil, true, err
	}
	buffer, err := r.bashPPBridgeExpr(call.ArgExprs[1])
	if err != nil {
		return nil, true, err
	}
	target := buffer.sliceView
	if target == nil {
		return nil, true, fmt.Errorf("gosource: runtime.Callers requires a direct original uintptr slice")
	}
	frames := append([]goSourceStackFrame{goSourceRuntimeFrames.callers}, r.goSourceCallerFrames(call.Pos())...)
	if skip < 0 {
		skip = 0
	}
	if skip > int64(len(frames)) {
		skip = int64(len(frames))
	}
	frames = frames[skip:]
	n := min(len(frames), len(target.view))
	for i := 0; i < n; i++ {
		target.view[i] = int(r.goSourceFramePC(frames[i]))
	}
	return []bashPPBridgeValue{goSourceStackIntValue(n)}, true, nil
}

// goSourceFramesValue is the *runtime.Frames iterator runtime.CallersFrames
// returns over the program counters given: the counters in order, and the
// cursor of the next frame to report.
func (r *Runner) goSourceFramesValue(call *syntax.BashPPCall) ([]bashPPBridgeValue, bool, error) {
	pcs, err := r.bashPPBridgeExpr(call.ArgExprs[0])
	if err != nil {
		return nil, true, err
	}
	if pcs.Kind != "slice" && pcs.Kind != "nil" {
		return nil, true, fmt.Errorf("gosource: runtime.CallersFrames requires a uintptr slice")
	}
	elements := make([]bashPPBridgeValue, 0, len(pcs.Elements))
	for _, element := range pcs.Elements {
		if _, err := strconv.ParseUint(element.Text, 10, 64); err != nil {
			return nil, true, fmt.Errorf("gosource: runtime.CallersFrames: %s is not a program counter", element.Text)
		}
		elements = append(elements, bashPPBridgeValue{Kind: "uint", Type: "uintptr", Text: element.Text})
	}
	return []bashPPBridgeValue{{Kind: "frames", Type: "*runtime.Frames", Text: "0", Elements: elements}}, true, nil
}

// goSourceCallMethodName extracts the method name from a method call without
// allocating intermediate syntax nodes.
func goSourceCallMethodName(call *syntax.BashPPCall) (string, bool) {
	if call == nil {
		return "", false
	}
	if selector, ok := call.CalleeExpr.(*syntax.BashPPSelectorExpr); ok {
		if selector.Sel != nil {
			return selector.Sel.Value, true
		}
		return "", false
	}
	if call.CalleeExpr != nil || len(call.Fun) < 2 {
		return "", false
	}
	last := call.Fun[len(call.Fun)-1]
	if last == nil {
		return "", false
	}
	return last.Value, true
}

// goSourceMethodCallReceiver splits a method call into its receiver
// expression and method name, whether the call carries a callee expression
// or a dotted name path (`f.Func.Name` is the path f, Func, Name).
func goSourceMethodCallReceiver(call *syntax.BashPPCall) (syntax.BashPPExpr, string, bool) {
	if selector, ok := call.CalleeExpr.(*syntax.BashPPSelectorExpr); ok {
		return selector.X, selector.Sel.Value, true
	}
	if call.CalleeExpr != nil || len(call.Fun) < 2 {
		return nil, "", false
	}
	var receiver syntax.BashPPExpr = &syntax.BashPPIdent{Name: call.Fun[0]}
	for _, part := range call.Fun[1 : len(call.Fun)-1] {
		receiver = &syntax.BashPPSelectorExpr{X: receiver, Sel: part}
	}
	return receiver, call.Fun[len(call.Fun)-1].Value, true
}

// goSourceFramesReceiver reports the *runtime.Frames iterator a method call's
// receiver names, when it names one held in a variable.
func (r *Runner) goSourceFramesReceiver(call *syntax.BashPPCall) (*bashPPBridgeValue, string, bool) {
	method, ok := goSourceCallMethodName(call)
	if !ok || method != "Next" {
		return nil, "", false
	}
	receiver, _, ok := goSourceMethodCallReceiver(call)
	if !ok {
		return nil, "", false
	}
	id, ok := receiver.(*syntax.BashPPIdent)
	if !ok {
		return nil, "", false
	}
	native := r.bashPPNativeCellValue(id.Name.Value)
	if native == nil || native.Kind != "frames" {
		return nil, "", false
	}
	return native, method, true
}

// goSourceFramesNext answers (*runtime.Frames).Next: the frame of the
// counter at the cursor and whether another follows. A counter the table
// did not issue is a frame with only its PC, as Go reports unknown code.
func (r *Runner) goSourceFramesNext(call *syntax.BashPPCall, frames *bashPPBridgeValue, method string) ([]bashPPBridgeValue, bool, error) {
	if method != "Next" || len(call.ArgExprs) != 0 {
		return nil, true, fmt.Errorf("gosource: (*runtime.Frames).%s: unsupported call", method)
	}
	cursor, _ := strconv.Atoi(frames.Text)
	if cursor >= len(frames.Elements) {
		return []bashPPBridgeValue{goSourceFrameStruct(0, goSourceStackFrame{}, false), goSourceStackBool(false)}, true, nil
	}
	pc, _ := strconv.ParseUint(frames.Elements[cursor].Text, 10, 64)
	frames.Text = strconv.Itoa(cursor + 1)
	frame, known := r.goSourcePCFrame(pc)
	return []bashPPBridgeValue{goSourceFrameStruct(pc, frame, known), goSourceStackBool(cursor+1 < len(frames.Elements))}, true, nil
}

// goSourceFrameStruct is the runtime.Frame value for a program counter. Its
// Func crosses as a nil *runtime.Func — the dependency has no function at
// an interpreted counter — and the method calls a program makes on it are
// answered from the frame's PC instead; see goSourceFrameFuncReceiver.
func goSourceFrameStruct(pc uint64, frame goSourceStackFrame, known bool) bashPPBridgeValue {
	fields := map[string]bashPPBridgeValue{
		"PC":       goSourceStackUintptr(pc),
		"Func":     {Kind: "nil", Type: "*runtime.Func"},
		"Function": goSourceStackString(""),
		"File":     goSourceStackString(""),
		"Line":     goSourceStackIntValue(0),
		"Entry":    goSourceStackUintptr(0),
	}
	if known {
		fields["Function"] = goSourceStackString(frame.name)
		fields["File"] = goSourceStackString(frame.file)
		fields["Line"] = goSourceStackIntValue(int(frame.line))
		fields["Entry"] = goSourceStackUintptr(goSourceFrameEntry(pc))
	}
	return bashPPBridgeValue{Kind: "struct", Type: "runtime.Frame", Fields: fields}
}

// goSourceFrameFieldFunc reports the program counter behind `<frame>.Func`
// when receiver is that selector on a variable holding a runtime.Frame the
// table issued, so the *runtime.Func methods answer from the table.
func (r *Runner) goSourceFrameFieldFunc(receiver syntax.BashPPExpr) (uint64, bool) {
	selector, ok := receiver.(*syntax.BashPPSelectorExpr)
	if !ok || selector.Sel.Value != "Func" {
		return 0, false
	}
	id, ok := selector.X.(*syntax.BashPPIdent)
	if !ok {
		return 0, false
	}
	native := r.bashPPNativeCellValue(id.Name.Value)
	if native == nil || native.Kind != "struct" || native.Type != "runtime.Frame" {
		return 0, false
	}
	pc, err := strconv.ParseUint(native.Fields["PC"].Text, 10, 64)
	if err != nil {
		return 0, false
	}
	if _, known := r.goSourcePCFrame(pc); !known {
		return 0, false
	}
	return pc, true
}
