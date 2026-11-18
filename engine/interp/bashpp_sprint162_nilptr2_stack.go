package interp

import (
	"errors"
	"fmt"
	"go/constant"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 162, lane interp-nilptr-2: runtime stack introspection of
// interpreted frames.
//
// runtime.Caller, runtime.FuncForPC, runtime.Stack and debug.Stack are
// imported functions, and an imported call is answered by the dependency
// process — whose stack holds the worker's own frames and none of the
// program's. A Go program that walks its stack expects to find its own
// functions there, at the line each is executing, and while a deferred call
// runs for a panic it expects the panicking frames too: Go runs the deferred
// calls before it unwinds them.
//
// The interpreter therefore keeps its own frame table and answers these
// calls itself, the way sync/atomic is answered (gosource_atomic.go):
//
//   - every Bash++ frame records the position of the call that entered it
//     (callFrame.callPos), so the caller's line is the line of that call;
//   - a raise snapshots the frames at the fault, top frame at the fault
//     line, and the snapshot stays spliced into the reported stack while a
//     deferred call for the panic is running — until the panic is recovered
//     and the recovering frame returns;
//   - a synthetic program counter names each reported frame, so
//     runtime.FuncForPC(pc).Name() and FileLine resolve through the same
//     table.
//
// Frames the runtime itself would show (runtime.gopanic, runtime.main) are
// not fabricated; only interpreted frames are reported.

// goSourceStackFrame is one frame of the interpreted call stack as Go's
// runtime would report it: the qualified function name, and the file and
// line the frame is executing.
type goSourceStackFrame struct {
	name string
	file string
	line uint
	seq  uint64
}

// goSourceFaultStack is the frame snapshot a raise takes.
type goSourceFaultStack struct {
	// frames are outermost first; the last is the faulting frame at the
	// fault line.
	frames []goSourceStackFrame
	// keepFrame is the sequence number of the deferred frame that recovered
	// the panic. The snapshot stays reported while that frame is on the
	// stack — Go unwinds only when the deferred call returns — and is stale
	// once it has left. Zero while the panic is still active.
	keepFrame uint64
}

// goSourcePCBase is the first synthetic program counter. Real code
// addresses of the dependency never reach the interpreter, so the range is
// unambiguous. Each reported frame owns goSourcePCStride counters from its
// entry; the counter reported for it lies inside, so a program that looks
// up pc-1 — the return address convention runtime.Callers documents — or
// pc itself resolves the same frame, and (*runtime.Func).Entry is below.
const (
	goSourcePCBase   = 0x7f000000
	goSourcePCStride = 16
	goSourcePCOffset = 8
)

// goSourceNextFrameSeq issues the identity a call frame carries, so a frame
// snapshot can tell a frame still on the stack from a new one at the same
// depth. It runs as the frame is pushed, while the caller is still on top,
// and notes on the caller what the new frame tells about it.
func (r *Runner) goSourceNextFrameSeq() uint64 {
	r.goSourceFrameEntering()
	r.goSourceFrameSeq++
	return r.goSourceFrameSeq
}

// goSourceStackFile is the absolute path of the program's source file, as
// runtime.Caller reports it.
func (r *Runner) goSourceStackFile(name string) string {
	if name == "" {
		name = r.filename
	}
	if name == "" || filepath.IsAbs(name) {
		return name
	}
	if r.Dir != "" {
		return filepath.Join(r.Dir, name)
	}
	if abs, err := filepath.Abs(name); err == nil {
		return abs
	}
	return name
}

// goSourceFrameFuncName is the qualified name Go prints for the function
// running in callStack[i]: `main.f`, `main.(*T).M`, `main.T.M`, or for a
// function literal the enclosing frame's name with the `.funcN` suffix Go
// gives its literals, numbered by the literal's position among the
// literals of that frame's function.
func (r *Runner) goSourceFrameFuncName(i int) string {
	frame := r.callStack[i]
	if frame.goSourceName != "" {
		return frame.goSourceName
	}
	fn := frame.bashPPFn
	if fn == nil {
		return "main." + frame.funcName
	}
	if fn.decl != nil {
		return r.goSourceDeclFrameName(fn.decl)
	}
	// A literal the program declares is named from the source index; one
	// the index does not know (a body the runtime instantiated) is numbered
	// from the frames on the stack.
	if name, ok := r.goSourceLiteralNames()[fn.lit]; ok {
		return name
	}
	parent := "main"
	if i > 0 {
		parent = r.goSourceFrameFuncName(i - 1)
	}
	return parent + ".func" + strconv.Itoa(r.goSourceLiteralIndex(fn, i))
}

// goSourceEnterInitializerFrame supplies the implicit package init frame when
// a package variable initializer calls an interpreted function. Flattening
// keeps initializers as positioned top-level declarations, so there is no
// callable declaration to push; the source position and linked package path
// are nevertheless authoritative. Synthetic glue calls are unpositioned and
// therefore do not enter this path.
func (r *Runner) goSourceEnterInitializerFrame() {
	if !r.bashPPGoSource || len(r.callStack) != 0 || r.bashPPGoSourceFile == nil || !r.curStmtPos.IsValid() {
		return
	}
	source, ok := r.bashPPGoSourceFile.SourceAt(r.curStmtPos)
	if !ok || source.PackagePath == "" {
		return
	}
	r.goSourceFrameSeq++
	r.callStack = append(r.callStack, callFrame{
		callPos:      r.curStmtPos,
		seq:          r.goSourceFrameSeq,
		goSourceName: source.PackagePath + ".init",
	})
}

// goSourceLiteralIndex numbers a function literal among the literals of the
// function it runs under, in source order: 1 for the first literal whose
// position precedes or equals this one's, counting only literals seen on
// this stack. Go numbers literals by source order within their enclosing
// declaration; the frames give the nearest approximation the interpreter
// has without a declaration index.
func (r *Runner) goSourceLiteralIndex(fn *bashPPFunc, depth int) int {
	if fn.lit == nil {
		return 1
	}
	index := 1
	for j := 0; j < depth; j++ {
		other := r.callStack[j].bashPPFn
		if other == nil || other.lit == nil || other == fn {
			continue
		}
		otherPos, fnPos := bashPPCallbackSourcePos(other), bashPPCallbackSourcePos(fn)
		if otherPos.IsValid() && fnPos.IsValid() && otherPos.Offset() < fnPos.Offset() {
			index++
		}
	}
	return index
}

// goSourceLiveFrames reports the frames on the stack now, outermost first.
// A frame below the top is at the line of the call that entered the frame
// above it; the top frame is at top.
func (r *Runner) goSourceLiveFrames(top syntax.Pos) []goSourceStackFrame {
	frames := make([]goSourceStackFrame, len(r.callStack))
	for i := range r.callStack {
		pos := top
		if i+1 < len(r.callStack) {
			pos = r.goSourceFrameLinePos(i)
		}
		file, line := r.goSourceFramePosition(pos)
		frames[i] = goSourceStackFrame{name: r.goSourceFrameFuncName(i), file: file, line: line, seq: r.callStack[i].seq}
	}
	return frames
}

// goSourceCaptureFault snapshots the frames at a raise. The raise sites set
// the panic's trace line first; a raise without one is at the statement
// executing.
func (r *Runner) goSourceCaptureFault() {
	if !r.bashPPGoSource || len(r.callStack) == 0 {
		return
	}
	top := r.curStmtPos
	frames := r.goSourceLiveFrames(top)
	fault := &frames[len(frames)-1]
	if r.bashPPPanic.traceLine != 0 && r.bashPPPanic.traceLine != top.Line() {
		// The trace line is the raise site's (a panic call spanning lines,
		// say); the file stays the one governing the statement, which the
		// raise site shares.
		fault.line = r.bashPPPanic.traceLine
		if r.bashPPPanic.traceSource != "" && (r.bashPPGoSourceFile == nil || !top.IsValid()) {
			fault.file = r.goSourceStackFile(r.bashPPPanic.traceSource)
		}
	}
	r.goSourceFault = &goSourceFaultStack{frames: frames}
}

// goSourceFaultRecovered marks the fault snapshot as kept by the frame that
// recovered it: the directly deferred call now on top of the stack.
func (r *Runner) goSourceFaultRecovered() {
	if !r.bashPPGoSource || r.goSourceFault == nil || len(r.callStack) == 0 {
		return
	}
	r.goSourceFault.keepFrame = r.callStack[len(r.callStack)-1].seq
}

// goSourceFaultLive reports whether the fault snapshot still describes
// frames Go would report: the panic is unwinding, or the deferred call that
// recovered it has not returned.
func (r *Runner) goSourceFaultLive() bool {
	fault := r.goSourceFault
	if fault == nil {
		return false
	}
	if r.bashPPPanic.active && fault.keepFrame == 0 {
		return true
	}
	if fault.keepFrame != 0 {
		for _, frame := range r.callStack {
			if frame.seq == fault.keepFrame {
				return true
			}
		}
	}
	r.goSourceFault = nil
	return false
}

// goSourceStackFrames is the stack Go would report from a call at top,
// innermost first: the frames entered since the deferred call for a live
// panic began, runtime.gopanic — which called that deferred call — then
// the snapshot of the panicking frames. The frames both share are reported
// once, at the lines they held when the panic was raised, since Go has not
// unwound them.
func (r *Runner) goSourceStackFrames(top syntax.Pos) []goSourceStackFrame {
	live := r.goSourceLiveFrames(top)
	var ordered []goSourceStackFrame
	if r.goSourceFaultLive() {
		fault := r.goSourceFault.frames
		shared := 0
		for shared < len(live) && shared < len(fault) && live[shared].seq == fault[shared].seq {
			shared++
		}
		for i := len(live) - 1; i >= shared; i-- {
			ordered = append(ordered, live[i])
		}
		if len(live) > shared {
			ordered = append(ordered, goSourceRuntimeFrames.gopanic)
		}
		for i := len(fault) - 1; i >= 0; i-- {
			ordered = append(ordered, fault[i])
		}
		return ordered
	}
	for i := len(live) - 1; i >= 0; i-- {
		ordered = append(ordered, live[i])
	}
	return ordered
}

// goSourceFramePC issues the synthetic program counter naming a reported
// frame. One site — a function at a file and line — keeps one counter, as
// a real program's code address does.
func (r *Runner) goSourceFramePC(frame goSourceStackFrame) uint64 {
	for i, known := range r.goSourcePCs {
		if known.name == frame.name && known.file == frame.file && known.line == frame.line {
			return goSourcePCBase + uint64(i)*goSourcePCStride + goSourcePCOffset
		}
	}
	r.goSourcePCs = append(r.goSourcePCs, frame)
	return goSourcePCBase + uint64(len(r.goSourcePCs)-1)*goSourcePCStride + goSourcePCOffset
}

// goSourcePCFrame resolves a synthetic program counter anywhere inside the
// frame's range.
func (r *Runner) goSourcePCFrame(pc uint64) (goSourceStackFrame, bool) {
	if pc < goSourcePCBase || (pc-goSourcePCBase)/goSourcePCStride >= uint64(len(r.goSourcePCs)) {
		return goSourceStackFrame{}, false
	}
	return r.goSourcePCs[(pc-goSourcePCBase)/goSourcePCStride], true
}

// goSourceFrameEntry is the entry counter of the frame owning pc.
func goSourceFrameEntry(pc uint64) uint64 {
	return pc - (pc-goSourcePCBase)%goSourcePCStride
}

// goSourceStackText renders the frames the way runtime.Stack does.
func (r *Runner) goSourceStackText(frames []goSourceStackFrame) string {
	var b strings.Builder
	b.WriteString("goroutine 1 [running]:\n")
	for _, frame := range frames {
		pc := r.goSourceFramePC(frame)
		name, args := frame.name, "()"
		if name == goSourceRuntimeFrames.gopanic.name {
			// Tracebacks print the runtime's panic entry as Go does, by
			// the name the program called it by.
			name, args = "panic", "({...})"
		}
		fmt.Fprintf(&b, "%s%s\n\t%s:%d +0x%x\n", name, args, frame.file, frame.line, pc-goSourceFrameEntry(pc))
	}
	return b.String()
}

// goSourceStackSelector names the imported function a call reaches, as
// "<package path>.<Name>", for the runtime packages this file answers.
func (r *Runner) goSourceStackSelector(call *syntax.BashPPCall) (string, bool) {
	if !r.bashPPGoSource || call == nil {
		return "", false
	}
	alias, name := "", ""
	if selector, ok := call.CalleeExpr.(*syntax.BashPPSelectorExpr); ok {
		id, ok := selector.X.(*syntax.BashPPIdent)
		if !ok {
			return "", false
		}
		alias, name = id.Name.Value, selector.Sel.Value
	} else if len(call.Fun) == 2 {
		alias, name = call.Fun[0].Value, call.Fun[1].Value
	} else {
		return "", false
	}
	if r.bashPPScope != nil && r.bashPPScope.lookup(alias) != nil {
		return "", false
	}
	path, ok := r.bashPPImports[alias]
	if !ok || (path != "runtime" && path != "runtime/debug") {
		return "", false
	}
	return path + "." + name, true
}

// goSourceFrameFuncValue is the *runtime.Func the interpreter hands out for
// a synthetic program counter.
func goSourceFrameFuncValue(pc uint64) bashPPBridgeValue {
	return bashPPBridgeValue{Kind: "frame-func", Type: "*runtime.Func", Text: strconv.FormatUint(pc, 10)}
}

// goSourceFrameFuncEqual compares synthetic *runtime.Func values, which never
// cross to the dependency. Go's runtime.FuncForPC returns one *Func per
// function, so two are equal exactly when their counters share an entry. A
// synthetic *Func is never nil and never one the dependency's runtime
// issued, so it differs from every other operand.
func goSourceFrameFuncEqual(lv, rv bashPPBridgeValue) (bool, bool) {
	if lv.Kind != "frame-func" && rv.Kind != "frame-func" {
		return false, false
	}
	if lv.Kind != rv.Kind {
		return false, true
	}
	l, lerr := strconv.ParseUint(lv.Text, 10, 64)
	r, rerr := strconv.ParseUint(rv.Text, 10, 64)
	if lerr != nil || rerr != nil {
		return false, false
	}
	return goSourceFrameEntry(l) == goSourceFrameEntry(r), true
}

// goSourceFrameFuncReceiver reports the synthetic *runtime.Func a method
// call's receiver expression denotes, without evaluating anything that is
// not one: a runtime.FuncForPC call, or a variable holding such a value.
func (r *Runner) goSourceFrameFuncReceiver(call *syntax.BashPPCall) (uint64, string, bool) {
	method, ok := goSourceCallMethodName(call)
	if !ok {
		return 0, "", false
	}
	switch method {
	case "Name", "Entry", "FileLine":
	default:
		return 0, "", false
	}
	receiver, _, ok := goSourceMethodCallReceiver(call)
	if !ok {
		return 0, "", false
	}
	for {
		paren, ok := receiver.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		receiver = paren.X
	}
	if pc, ok := r.goSourceFrameFieldFunc(receiver); ok {
		return pc, method, true
	}
	var value bashPPBridgeValue
	switch x := receiver.(type) {
	case *syntax.BashPPCall:
		if name, ok := r.goSourceStackSelector(x); !ok || name != "runtime.FuncForPC" {
			return 0, "", false
		}
		values, claimed, err := r.goSourceRuntimeStackCall(x)
		if !claimed || err != nil || len(values) != 1 {
			return 0, "", false
		}
		value = values[0]
	case *syntax.BashPPIdent:
		native := r.bashPPNativeCellValue(x.Name.Value)
		if native == nil {
			return 0, "", false
		}
		value = *native
	default:
		return 0, "", false
	}
	if value.Kind != "frame-func" {
		return 0, "", false
	}
	pc, err := strconv.ParseUint(value.Text, 10, 64)
	if err != nil {
		return 0, "", false
	}
	return pc, method, true
}

// goSourceRuntimeStackCall answers the stack-introspection calls of runtime
// and runtime/debug over the interpreter's frame table, reporting whether
// it claimed the call. It claims nothing it cannot answer exactly: any
// other function of those packages, and a FuncForPC of a program counter
// the table did not issue, keep the dependency path.
func (r *Runner) goSourceRuntimeStackCall(call *syntax.BashPPCall) ([]bashPPBridgeValue, bool, error) {
	if !r.bashPPGoSource || call == nil || call.Ellipsis.IsValid() || len(call.ArgExprs) != len(call.Args) {
		return nil, false, nil
	}
	if pc, method, ok := r.goSourceFrameFuncReceiver(call); ok {
		return r.goSourceFrameFuncMethod(call, pc, method)
	}
	if frames, method, ok := r.goSourceFramesReceiver(call); ok {
		return r.goSourceFramesNext(call, frames, method)
	}
	name, ok := r.goSourceStackSelector(call)
	if !ok {
		return nil, false, nil
	}
	args := len(call.ArgExprs)
	switch {
	case name == "runtime.Caller" && args == 1:
		skip, err := r.goSourceStackInt(call.ArgExprs[0])
		if err != nil {
			return nil, true, err
		}
		frames := r.goSourceCallerFrames(call.Pos())
		if skip < 0 || skip >= int64(len(frames)) {
			return []bashPPBridgeValue{goSourceStackUintptr(0), goSourceStackString(""), goSourceStackIntValue(0), goSourceStackBool(false)}, true, nil
		}
		frame := frames[skip]
		return []bashPPBridgeValue{goSourceStackUintptr(r.goSourceFramePC(frame)), goSourceStackString(frame.file), goSourceStackIntValue(int(frame.line)), goSourceStackBool(true)}, true, nil
	case name == "runtime.Callers" && args == 2:
		return r.goSourceCallersValue(call)
	case name == "runtime.CallersFrames" && args == 1:
		return r.goSourceFramesValue(call)
	case name == "runtime.FuncForPC" && args == 1:
		pc, err := r.goSourceStackInt(call.ArgExprs[0])
		if err != nil {
			return nil, true, err
		}
		if _, ok := r.goSourcePCFrame(uint64(pc)); !ok {
			return nil, false, nil
		}
		return []bashPPBridgeValue{goSourceFrameFuncValue(uint64(pc))}, true, nil
	case name == "runtime.Stack" && args == 2:
		buffer, err := r.bashPPBridgeExpr(call.ArgExprs[0])
		if err != nil {
			return nil, true, err
		}
		if _, err := r.goSourceStackBool(call.ArgExprs[1]); err != nil {
			return nil, true, err
		}
		text := r.goSourceStackText(r.goSourceStackFrames(call.Pos()))
		n, err := r.goSourceStackWriteBytes(buffer, text)
		if err != nil {
			return nil, true, err
		}
		return []bashPPBridgeValue{goSourceStackIntValue(n)}, true, nil
	case name == "runtime/debug.Stack" && args == 0:
		text := r.goSourceStackText(append([]goSourceStackFrame{goSourceRuntimeFrames.debugStack}, r.goSourceStackFrames(call.Pos())...))
		value, err := r.goSourceStackBytes(text)
		if err != nil {
			return nil, true, err
		}
		return []bashPPBridgeValue{value}, true, nil
	case name == "runtime/debug.PrintStack" && args == 0:
		r.errf("%s", r.goSourceStackText(r.goSourceStackFrames(call.Pos())))
		return nil, true, nil
	}
	return nil, false, nil
}

// goSourceFrameFuncMethod answers Name, Entry and FileLine on a synthetic
// *runtime.Func.
func (r *Runner) goSourceFrameFuncMethod(call *syntax.BashPPCall, pc uint64, method string) ([]bashPPBridgeValue, bool, error) {
	frame, ok := r.goSourcePCFrame(pc)
	if !ok {
		return nil, true, fmt.Errorf("gosource: unknown interpreted program counter %d", pc)
	}
	switch {
	case method == "Name" && len(call.ArgExprs) == 0:
		return []bashPPBridgeValue{goSourceStackString(frame.name)}, true, nil
	case method == "Entry" && len(call.ArgExprs) == 0:
		return []bashPPBridgeValue{goSourceStackUintptr(goSourceFrameEntry(pc))}, true, nil
	case method == "FileLine" && len(call.ArgExprs) == 1:
		at, err := r.goSourceStackInt(call.ArgExprs[0])
		if err != nil {
			return nil, true, err
		}
		if located, ok := r.goSourcePCFrame(uint64(at)); ok {
			frame = located
		}
		return []bashPPBridgeValue{goSourceStackString(frame.file), goSourceStackIntValue(int(frame.line))}, true, nil
	}
	return nil, true, fmt.Errorf("gosource: (*runtime.Func).%s: wrong argument count", method)
}

// goSourceStackWriteBytes writes text into the caller's byte slice, as
// runtime.Stack fills its buffer, and returns the number of bytes written.
func (r *Runner) goSourceStackWriteBytes(buffer bashPPBridgeValue, text string) (int, error) {
	target := buffer.sliceView
	if target == nil {
		return 0, fmt.Errorf("gosource: runtime.Stack requires a direct original byte slice")
	}
	collection, ok := r.bashPPUnderlyingType(target.typ).(*syntax.BashPPCollectionType)
	if !ok || collection.Kind != "slice" {
		return 0, fmt.Errorf("gosource: runtime.Stack buffer has no slice identity")
	}
	if element := bashPPTypeText(r.bashPPUnderlyingType(collection.Element)); element != "byte" && element != "uint8" {
		return 0, fmt.Errorf("gosource: runtime.Stack requires a byte slice")
	}
	n := min(len(text), len(target.view))
	for i := 0; i < n; i++ {
		target.view[i] = int(text[i])
	}
	return n, nil
}

func (r *Runner) goSourceStackInt(expr syntax.BashPPExpr) (int64, error) {
	scalar, err := r.bashPPEvalScalarExpr(expr)
	if err != nil {
		return 0, err
	}
	if scalar.value == nil || scalar.value.Kind() != constant.Int {
		return 0, fmt.Errorf("gosource: %s is not an integer", scalar.value)
	}
	n, ok := constant.Int64Val(scalar.value)
	if !ok {
		return 0, fmt.Errorf("gosource: %s overflows int64", scalar.value)
	}
	return n, nil
}

func (r *Runner) goSourceStackBool(expr syntax.BashPPExpr) (bool, error) {
	scalar, err := r.bashPPEvalScalarExpr(expr)
	if err != nil {
		return false, err
	}
	if scalar.value == nil || scalar.value.Kind() != constant.Bool {
		return false, fmt.Errorf("gosource: %s is not a boolean", scalar.value)
	}
	return constant.BoolVal(scalar.value), nil
}

func goSourceStackString(s string) bashPPBridgeValue {
	return bashPPBridgeValue{Kind: "string", Type: "string", Text: s}
}

func goSourceStackIntValue(n int) bashPPBridgeValue {
	return bashPPBridgeValue{Kind: "int", Type: "int", Text: strconv.Itoa(n)}
}

func goSourceStackUintptr(pc uint64) bashPPBridgeValue {
	return bashPPBridgeValue{Kind: "uint", Type: "uintptr", Text: strconv.FormatUint(pc, 10)}
}

func goSourceStackBool(b bool) bashPPBridgeValue {
	return bashPPBridgeValue{Kind: "bool", Type: "bool", Text: strconv.FormatBool(b)}
}

// goSourceStackBytes is the []byte a stack text is returned as: a slice the
// dependency owns, exactly like every other []byte an imported call
// returns, so len, indexing, slicing and string() read it the same way.
func (r *Runner) goSourceStackBytes(text string) (bashPPBridgeValue, error) {
	elements := make([]bashPPBridgeValue, len(text))
	for i := range text {
		elements[i] = bashPPBridgeValue{Kind: "uint", Type: "uint8", Text: strconv.Itoa(int(text[i]))}
	}
	literal := bashPPBridgeValue{Kind: "slice", Type: "[]uint8", Elements: elements, Length: len(text), Capacity: len(text)}
	return r.bashPPNativeTypeRequest("construct", &syntax.BashPPCollectionType{Kind: "slice", Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "uint8"}}}, literal)
}

// goSourceRuntimeFaultAt is goSourceRuntimeFault for a fault detected while
// expr was being evaluated: the panic is raised at expr's own line, which
// is the line Go's runtime reports — the case of a select, the operand of
// a switch — rather than the line of the statement that contains it.
func (r *Runner) goSourceRuntimeFaultAt(err error, expr syntax.BashPPExpr) error {
	if err == nil || !r.bashPPGoSource || expr == nil || !expr.Pos().IsValid() {
		return r.goSourceRuntimeFault(err)
	}
	var fault *bashPPRuntimeError
	if !errors.As(err, &fault) || r.bashPPPanicHalts() {
		return r.goSourceRuntimeFault(err)
	}
	saved := r.curStmtPos
	r.curStmtPos = expr.Pos()
	defer func() { r.curStmtPos = saved }()
	return r.goSourceRuntimeFault(err)
}
