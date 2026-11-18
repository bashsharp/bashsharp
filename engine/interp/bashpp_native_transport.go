package interp

import (
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// validateLocalTransport refuses dependency mutation of interpreter-owned
// references. Formatting is read-only; its original methods use authenticated
// receiver identities and execute against the interpreter's own storage.
func validateLocalTransport(req bashPPEvalRequest, q bashPPBridgeRequest) error {
	if q.Op != "call" {
		return nil
	}
	if q.Receiver != nil && q.Receiver.reflectFunction && !reflectedOriginalFunctionUse(q) {
		return fmt.Errorf("gosource: reflected original function only supports synchronous Call and type inspection")
	}
	if q.Receiver != nil && q.Receiver.reflectFunction && q.Receiver.callRefusal != "" && (q.Selector == "Call" || q.Selector == "CallSlice") {
		return fmt.Errorf("%s", q.Receiver.callRefusal)
	}
	// reflect.TypeOf reads only the dynamic type of its operand: it neither
	// retains the value nor invokes any of its methods, so a copied
	// reference-bearing value with pointer methods crosses as a type witness
	// (cmd/compile/internal/types TestSizeof: reflect.TypeOf(Func{}).Size()).
	if bashPPTypeDescriptorResult(req, q) {
		return nil
	}
	alias, name, selected := strings.Cut(q.Selector, ".")
	nativeWriterFormat := selected && req.Imports[alias] == "fmt" && (name == "Fprint" || name == "Fprintln" || name == "Fprintf")
	local := req.localTypePlan()
	functionCallbacks := false
	// identity reports that this value is reachable through an original pointer
	// the dependency can name, so a method callback on it binds to the original
	// interpreter storage rather than to a rebuilt copy. It propagates from a
	// pointer to its pointee and from a struct to its fields, which is exactly
	// the addressable chain `&x.f.g` names in Go.
	var inspect func(bashPPBridgeValue, bool) (bool, bool, error)
	inspect = func(v bashPPBridgeValue, identity bool) (bool, bool, error) {
		if v.Kind == "callback" {
			if v.localRefusal != "" {
				return false, false, fmt.Errorf("%s", v.localRefusal)
			}
			if v.copiedResults && !copiedResultsConsumer(req, q) || v.copiedHandleResults && !copiedHandleResultsConsumer(req, q) {
				return false, false, fmt.Errorf("gosource: original callback signature requires value-semantics parameters and supported results")
			}
			functionCallbacks = true
			return false, false, nil
		}
		if v.Kind == "handle" {
			return false, false, nil
		}
		// A typed-nil pointer of a method-bearing local type is admitted: a
		// mirrored method raised on it runs the original body with a nil
		// receiver, which is exactly how native Go invokes it.
		name := strings.TrimPrefix(v.Type, "main.")
		typ, isLocal := local.declared(name)
		if len(typ.OmittedMethods) > 0 {
			alias, _, _ := strings.Cut(q.Selector, ".")
			for _, method := range typ.OmittedMethods {
				if req.Imports[alias] != "fmt" || method == "Format" || method == "GoString" {
					return false, false, fmt.Errorf("gosource: original method %s.%s is not supported by dependency transport", name, method)
				}
			}
		}
		reference := v.Kind == "pointer" || v.Kind == "slice" || v.Kind == "map"
		identity = identity || v.Origin != 0
		nested := identity && (v.Kind == "pointer" || v.Kind == "struct")
		nestedRef := false
		for _, child := range v.Elements {
			l, ref, err := inspect(child, nested)
			if err != nil {
				return false, false, err
			}
			isLocal = isLocal || l
			nestedRef = nestedRef || ref
		}
		for _, child := range v.Fields {
			l, ref, err := inspect(child, nested)
			if err != nil {
				return false, false, err
			}
			isLocal = isLocal || l
			nestedRef = nestedRef || ref
		}
		for _, entry := range v.Entries {
			l, ref, err := inspect(entry.Value, false)
			if err != nil {
				return false, false, err
			}
			isLocal = isLocal || l
			nestedRef = nestedRef || ref
		}
		// A value-receiver method on copied maps/slices/reference-bearing
		// structs used to be refused outright here. It is now admitted: the
		// callback runs against the rebuilt copy and bashPPNativeCallback
		// compares the shared storage before and after the body, so an effect
		// on a shared referent is reported instead of silently lost. A pointer
		// method still requires original identity, which it already has
		// wherever the value is addressable.
		if len(typ.Methods) > 0 && (reference || nestedRef) && !identity {
			for _, m := range typ.Methods {
				if m.Pointer && dependencyMayInvokeOriginalMethod(req, q, m.Name) {
					return false, false, fmt.Errorf("gosource: pointer-receiver callback on reference-bearing value %s requires original reference identity", v.Type)
				}
			}
		}
		return isLocal, reference || nestedRef, nil
	}
	unsafe := false
	for _, arg := range q.Args {
		local, ref, err := inspect(arg, false)
		if err != nil {
			return err
		}
		unsafe = unsafe || (local && ref) || arg.Kind == "pointer"
	}
	if !unsafe && !functionCallbacks && !requestHasCallbacks(req, q) {
		return nil
	}
	// Plain-data pointer writeback reconciles field mutations the dependency
	// performs through an origin-bearing pointer; it never accounts for the
	// dependency invoking the pointee's ORIGINAL methods. A pointer to a local
	// method-bearing type is a callback interface value (e.g. an image.Image):
	// handing it to an arbitrary dependency lets that dependency retain it and
	// raise its original callbacks later, asynchronously, which this transport
	// cannot service. Such values may only cross to a reviewed synchronous
	// consumer (png.Encode, pic.ShowImage, the reader helpers) below; the
	// writeback admission is restricted to callback-free requests so an
	// unreviewed retainer like dep.Retain(image.Image) is refused here.
	if !functionCallbacks && !requestHasCallbacks(req, q) && nativePointerWritebackAllowed(req, q) {
		return nil
	}
	// A read-only structural emitter (json/xml Marshal, base64/hex encode, the
	// fmt formatters) walks the transported value tree and allocates its own
	// output; it never retains or mutates the interpreter-owned slices nested
	// inside a local struct. An original mirror method may ride along and is
	// executed synchronously on the parked Runner, exactly as it is for the fmt
	// entries in the same set. A general function callback still is not
	// admitted here: those go through synchronousFunctionCallback below.
	if callable := nativeSliceCallable(req, q); nativeSliceReadOnly(callable) && !functionCallbacks {
		return nil
	}
	// testing.Main owns the scheduler while this request remains parked. Its
	// descriptor slices are read-only copies and every original test callback
	// returns before Main does, so neither original storage nor callback
	// lifetime escapes this request.
	if functionCallbacks && nativeSliceCallable(req, q) == "testing.Main" {
		return nil
	}
	// An exclusive slice transfer (bashpp_native_transfer.go) hands the
	// dependency the slices as its own storage of record and, beside them,
	// only handles and scalars it already owns: no copy of interpreter
	// storage crosses for it to mutate or retain. The callbacks the elements
	// carry are retained by the callee, and the session serves them from now
	// on (markRetainedCallbacks at the request).
	if nativeSliceTransferOnly(q) {
		return nil
	}
	if synchronousReaderCallback(req, q) || synchronousImageCallback(req, q) || !functionCallbacks && (synchronousUnwrapCallback(req, q) || synchronousErrorsAsType(req, q)) {
		return nil
	}
	if reflectedMethodValueOf(req, q) || reflectedValueCopy(req, q) || reflectedCopyDerivedCall(q) || reflectedOriginalFunctionValueOf(req, q) || reflectedOriginalFunctionUse(q) {
		return nil
	}
	// A reviewed synchronous methods-driven consumer over origin-bearing
	// references (bashpp_native_transfer.go): the origin pointee is the one
	// native copy, mirrored callbacks bind the original storage and re-decode
	// the pointee on reply, and a native write through the pointee returns on
	// the pointer writeback. Only the template consumer dispatches natively —
	// it additionally proves its parsed tree free of functions and
	// sub-templates before its data crosses.
	if nativeSharedReferenceConsumer(req, q) && q.Receiver != nil && nativeTemplateExecuteProven(req, q) {
		return nil
	}
	if functionCallbacks && !synchronousFunctionCallback(req, q) && !retainedFunctionCallback(req, q) && !resultOwnedFunctionCallback(req, q) {
		return fmt.Errorf("gosource: asynchronous or retained original function callbacks are unsupported for %s", q.Selector)
	}
	if !functionCallbacks && synchronousOriginalMethodCallback(req, q) {
		return nil
	}
	if !unsafe && (synchronousFunctionCallback(req, q) || retainedFunctionCallback(req, q) || resultOwnedFunctionCallback(req, q)) {
		return nil
	}
	if !unsafe && !requestHasCallbacks(req, q) {
		return nil
	}
	// A made function is served by the retained-callback protocol, routed to
	// the calling runner; a request with no callback runner cannot serve it.
	madeByOtherRunner := q.Receiver != nil && req.Bridge.madeFunc(*q.Receiver) && !req.Bridge.madeFuncCallable(*q.Receiver, req.CallbackOwner)
	if !unsafe && !functionCallbacks && !madeByOtherRunner && q.Receiver != nil && q.Receiver.Kind == "handle" && q.Receiver.Callbacks {
		// The dependency already owns the retained function; calling it (or
		// converting its reflect.Value back with Interface) hands over no
		// interpreter storage and registers no new callback.
		if q.Selector == "" && q.Receiver.Function {
			return nil
		}
		if q.Receiver.NativeType == "reflect.Value" && q.Selector == "Interface" && len(q.Args) == 0 {
			return nil
		}
	}
	if q.Receiver != nil && q.Receiver.Callbacks && (q.Selector == "Error" || q.Selector == "String") && len(q.Args) == 0 {
		return nil
	}
	if nativeWriterFormat {
		return nil
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if ok && req.Imports[alias] == "fmt" {
		switch name {
		case "Print", "Println", "Printf", "Sprint", "Sprintln", "Sprintf", "Errorf":
			return nil
		}
	}
	if bashPPMadeFuncUse(req, q) {
		return nil
	}
	if mirroredMethodExpressionCall(req, q) {
		return nil
	}
	return fmt.Errorf("gosource: dependency mutation of interpreter-owned references is unsupported for %s", q.Selector)
}

// dependencyMayInvokeOriginalMethod reports whether the selected dependency
// operation can reach one method while inspecting an argument. Most imported
// calls remain conservative. The fmt family is narrower by Go's own contract:
// it can invoke only the formatting protocols, so an unrelated method such as
// (*Nodes).Append may ride on a copied value without becoming callable.
func dependencyMayInvokeOriginalMethod(req bashPPEvalRequest, q bashPPBridgeRequest, method string) bool {
	alias, _, selected := strings.Cut(q.Selector, ".")
	if selected && req.Imports[alias] == "fmt" {
		return fmtProtocolMethod(method)
	}
	return true
}

// synchronousOriginalMethodCallback admits the general method-callback bridge:
// the dependency may invoke mirrored methods while the request is parked, but
// it must not be a known storage retainer or in-place mutator. If original
// slices crossed by copy, prepareNativeSliceBuffers has either attached the
// post-callback coherence model or left the request to the ordinary refusal.
func synchronousOriginalMethodCallback(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Op != "call" || !requestHasCallbacks(req, q) {
		return false
	}
	if callbackFunctionHandleInRequest(q) {
		return false
	}
	callable := nativeSliceCallable(req, q)
	if callable == "" || nativeSliceRetainsStorage(callable) || nativeValueRetainer(callable) || nativeSliceMutatingIndex(callable) >= 0 || nativeRetainedPointerMutator(callable) {
		return false
	}
	// The retainer sets above review the standard library. A callee outside
	// it — the program's own module or a third-party package — is admitted
	// only once its sources prove it cannot retain or asynchronously invoke
	// the callback-bearing value (dependencyFunctionCallbackLifetimeProof).
	if !bashPPStandardImportPath(bashPPCallablePackage(callable)) && !q.sourceSynchronousMethodCallback {
		return false
	}
	if q.coherence != nil {
		return true
	}
	if q.sliceCallbackSync {
		return true
	}
	if len(q.SliceBuffers) > 0 {
		return false
	}
	return !nestedNativeSliceViewInRequest(q)
}

// nativeValueRetainer reports constructors that keep a copy of their argument
// inside the value they return. reflect.ValueOf wraps the copied value; a
// later request on that Value can mutate the interpreter-owned references
// the copy still shares (a map field, say) without any writeback. The
// reviewed reflect admissions (reflectedMethodValueOf, reflectedValueCopy,
// reflectedOriginalFunctionValueOf) are decided before the general bridge.
func nativeValueRetainer(name string) bool {
	switch name {
	case "reflect.ValueOf":
		return true
	}
	return false
}

// bashPPCallablePackage returns the import path of a callable spelled as
// nativeSliceCallable spells it: "pkg/path.Func", "*pkg/path.Type.Method".
func bashPPCallablePackage(callable string) string {
	name := strings.TrimLeft(callable, "*")
	slash := strings.LastIndexByte(name, '/')
	dot := strings.IndexByte(name[slash+1:], '.')
	if dot < 0 {
		return name
	}
	return name[:slash+1+dot]
}

// bashPPStandardImportPath reports a standard-library import path: by Go's
// own rule, one whose first element has no dot.
func bashPPStandardImportPath(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return first != "" && !strings.Contains(first, ".")
}

func callbackFunctionHandleInRequest(q bashPPBridgeRequest) bool {
	var check func(bashPPBridgeValue) bool
	check = func(v bashPPBridgeValue) bool {
		if v.Kind == "handle" && v.Callbacks && v.Function {
			return true
		}
		for _, child := range v.Elements {
			if check(child) {
				return true
			}
		}
		for _, child := range v.Fields {
			if check(child) {
				return true
			}
		}
		for _, entry := range v.Entries {
			if check(entry.Key) || check(entry.Value) {
				return true
			}
		}
		return false
	}
	if q.Receiver != nil && check(*q.Receiver) {
		return true
	}
	for _, arg := range q.Args {
		if check(arg) {
			return true
		}
	}
	return false
}

func nestedNativeSliceViewInRequest(q bashPPBridgeRequest) bool {
	if q.Receiver != nil && (q.Receiver.sliceView != nil || nestedNativeSliceView(*q.Receiver)) {
		return true
	}
	for _, arg := range q.Args {
		if arg.sliceView != nil || nestedNativeSliceView(arg) {
			return true
		}
	}
	return false
}

// reflectedMethodValueOf is the narrow creation edge for synchronous reflected
// method values. Its origin is minted only from an addressable local cell;
// arbitrary reflect calls and dependency retainers do not satisfy it.
func reflectedMethodValueOf(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Op != "call" || q.Receiver != nil || len(q.Args) != 1 || q.Args[0].Origin == 0 || q.Args[0].Session == "" {
		return false
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	return ok && req.Imports[alias] == "reflect" && name == "ValueOf" && requestHasCallbacks(req, q)
}

// ValueOf may preserve an original function as a reflected handle when the
// only admitted uses are synchronous Call and immutable type inspection. The
// callback server remains parked for Call; a native retainer receives no
// permission merely because the handle exists.
func reflectedOriginalFunctionValueOf(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Op != "call" || q.Receiver != nil || len(q.Args) != 1 || q.Args[0].Kind != "callback" {
		return false
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	return ok && req.Imports[alias] == "reflect" && name == "ValueOf"
}

func reflectedOriginalFunctionUse(q bashPPBridgeRequest) bool {
	if q.Op != "call" || q.Receiver == nil || q.Receiver.Kind != "handle" || !q.Receiver.reflectFunction {
		return false
	}
	switch q.Selector {
	case "Call", "CallSlice":
		return len(q.Args) == 1
	case "Type", "Kind":
		return len(q.Args) == 0
	}
	return false
}

// goSourceReflectedFunctionPointer answers Pointer on a reflected original
// function. The dependency holds only a MakeFunc trampoline, whose code
// address names no program function; Go reports the function's entry, so
// the interpreter reports the entry of the synthetic program counter its
// frame table issues for the function, which runtime.FuncForPC resolves to
// the function's own name and declaration line.
func (r *Runner) goSourceReflectedFunctionPointer(q bashPPBridgeRequest) (bashPPBridgeValue, bool) {
	if q.Op != "call" || q.Selector != "Pointer" || len(q.Args) != 0 || q.Receiver == nil || !q.Receiver.reflectFunction {
		return bashPPBridgeValue{}, false
	}
	fn := q.Receiver.reflectFunc
	if fn == nil || !r.bashPPGoSource {
		return bashPPBridgeValue{}, false
	}
	var name string
	var pos syntax.Pos
	switch {
	case fn.decl != nil:
		name, pos = r.goSourceDeclFrameName(fn.decl), fn.decl.Pos()
	case fn.lit != nil:
		literal, ok := r.goSourceLiteralNames()[fn.lit]
		if !ok {
			return bashPPBridgeValue{}, false
		}
		pos = bashPPCallbackSourcePos(fn)
		if !pos.IsValid() {
			return bashPPBridgeValue{}, false
		}
		name = literal
	default:
		return bashPPBridgeValue{}, false
	}
	file, line := r.goSourceFramePosition(pos)
	pc := r.goSourceFramePC(goSourceStackFrame{name: name, file: file, line: line})
	return goSourceStackUintptr(goSourceFrameEntry(pc)), true
}

// reflectedValueCopy admits reflect.ValueOf over an interpreter value whose
// every write the dependency could perform is either impossible or
// reconciled. reflect.ValueOf copies its operand, exactly as Go does: the
// copy is unaddressable, so no Set* reaches a direct field, and a mirrored
// value-receiver method invoked through the resulting Value runs on a copy
// in Go as well. What such a copy can still reach is referenced storage: a
// pointer is admitted only with an authenticated origin, whose pointee the
// native pointer writeback reconciles on every reply, while a copied slice,
// map or channel, or an original function value, stays refused because a
// native write or retained call through it would not reach the original.
func reflectedValueCopy(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Op != "call" || q.Receiver != nil || len(q.Args) != 1 {
		return false
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if !ok || req.Imports[alias] != "reflect" || name != "ValueOf" {
		return false
	}
	return reflectCopyReconciled(q.Args[0])
}

// reflectedCopyDerivedCall admits a call on a handle the host derived from an
// admitted reflect copy — Elem, Field, Interface, a mirrored method of the
// value Interface returned — whose arguments satisfy the same rule. Every
// value such a call can reach is still the copy or origin-reconciled storage,
// so it neither mutates nor retains interpreter storage the host cannot see.
// The mark is host-only (reflectCopy is never decoded from the wire), and the
// result stays callback-tainted, so a retaining dependency still refuses it.
func reflectedCopyDerivedCall(q bashPPBridgeRequest) bool {
	if q.Op != "call" || q.Receiver == nil || q.Receiver.Kind != "handle" || !q.Receiver.reflectCopy {
		return false
	}
	for _, arg := range q.Args {
		if !reflectCopyReconciled(arg) {
			return false
		}
	}
	return true
}

// reflectedCopyDerived reports a request whose handle results are still the
// admitted reflect copy: the ValueOf itself, an admitted call on a derived
// handle, or a read of one — a member (method value), field or element.
func reflectedCopyDerived(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	switch q.Op {
	case "member", "field", "index":
		return q.Receiver != nil && q.Receiver.Kind == "handle" && q.Receiver.reflectCopy
	}
	return reflectedValueCopy(req, q) || reflectedCopyDerivedCall(q)
}

// bashPPMarkReflectCopy sets the host-only reflect-copy mark on every handle
// a reply carries — a result slice of reflect.Values (Call) nests them — and
// clears it everywhere else, so the mark never survives from the wire.
func bashPPMarkReflectCopy(v *bashPPBridgeValue, derived bool) {
	v.reflectCopy = derived && v.Kind == "handle"
	for i := range v.Elements {
		bashPPMarkReflectCopy(&v.Elements[i], derived)
	}
	for name, field := range v.Fields {
		bashPPMarkReflectCopy(&field, derived)
		v.Fields[name] = field
	}
	for i := range v.Entries {
		bashPPMarkReflectCopy(&v.Entries[i].Key, derived)
		bashPPMarkReflectCopy(&v.Entries[i].Value, derived)
	}
}

// reflectCopyReconciled reports a transported value none of whose storage a
// native write could change unseen: scalars, value aggregates, origin-bearing
// pointers (reconciled by the pointer writeback) and native handles that do
// not carry an original callback from anywhere but an admitted reflect copy.
func reflectCopyReconciled(v bashPPBridgeValue) bool {
	switch v.Kind {
	case "struct", "array", "bool", "int", "uint", "float", "complex", "string", "nil":
	case "handle":
		if v.Callbacks && !v.reflectCopy {
			return false
		}
	case "pointer":
		if v.Origin == 0 {
			return false
		}
	default:
		return false
	}
	for _, child := range v.Elements {
		if !reflectCopyReconciled(child) {
			return false
		}
	}
	for _, child := range v.Fields {
		if !reflectCopyReconciled(child) {
			return false
		}
	}
	return len(v.Entries) == 0
}

// bashPPReflectTypeOnly rewrites original function arguments of reflect.TypeOf
// into bare type descriptors. TypeOf inspects the argument's type and never
// invokes the value, so no callback trampoline is wired and the value is not
// marked callback-bearing; the worker resolves the rendered signature against
// its registered type table and hands reflect a zero value of that type.
func bashPPReflectTypeOnly(req bashPPEvalRequest, q *bashPPBridgeRequest) {
	if q.Op != "call" || q.Receiver != nil {
		return
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if !ok || req.Imports[alias] != "reflect" || name != "TypeOf" {
		return
	}
	s := req.Bridge
	if s == nil {
		return
	}
	for i, arg := range q.Args {
		if arg.Kind != "callback" || arg.Session != s.id {
			continue
		}
		s.mu.Lock()
		fn := s.functions[arg.Handle]
		s.mu.Unlock()
		if fn == nil {
			continue
		}
		if text, ok := bashPPFunctionTypeText(fn); ok {
			q.Args[i] = bashPPBridgeValue{Kind: "nil", Type: text}
		}
	}
}

// ValueOf accepts an interface, so its formal parameter cannot tell the
// worker which function signature to use for an original callback. Supply
// the registered signature from the callback owner without changing the
// callback value or its session identity.
func bashPPReflectFunctionType(req bashPPEvalRequest, q *bashPPBridgeRequest) {
	if !reflectedOriginalFunctionValueOf(req, *q) || req.Bridge == nil {
		return
	}
	arg := &q.Args[0]
	if arg.Session != req.Bridge.id {
		return
	}
	req.Bridge.mu.Lock()
	fn := req.Bridge.functions[arg.Handle]
	req.Bridge.mu.Unlock()
	if fn == nil {
		return
	}
	if text, ok := bashPPFunctionTypeText(fn); ok {
		arg.Type = text
	}
}

// bashPPTypeDescriptorResult reports a call whose results are pure type
// descriptors. reflect.TypeOf retains neither its argument nor the argument's
// mirrored callbacks, so its result handle must not be marked callback-bearing
// — later method reads on the descriptor are plain dependency operations.
func bashPPTypeDescriptorResult(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Op != "call" || q.Receiver != nil {
		return false
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	return ok && req.Imports[alias] == "reflect" && name == "TypeOf"
}

// bashPPDependencyOwnedWriter reports a writer argument the dependency itself
// stores: a native handle, or an original pointer whose pointee is one. The
// worker binds such a pointer to the handle's own storage, so formatted writes
// land in the value later reads observe. Anything else — an original type's
// own Write method, a rebuilt copy — stays refused.
func bashPPDependencyOwnedWriter(args []bashPPBridgeValue) bool {
	if len(args) == 0 {
		return false
	}
	w := args[0]
	if w.Kind == "handle" {
		return true
	}
	return w.Kind == "pointer" && len(w.Elements) == 1 && w.Elements[0].Kind == "handle"
}

// nativePointerWritebackAllowed reports a call whose pointer arguments all
// carry original identity. Every write the dependency performs through such a
// pointer — during this call, or during a later one if the dependency retained
// it — is compared against the value it was given and transported back into
// the original storage, so no native write is silently dropped. A pointer
// without identity is still refused: there would be nothing to write back to.
func nativePointerWritebackAllowed(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	var inspect func(bashPPBridgeValue) (pointers int, valid bool)
	inspect = func(v bashPPBridgeValue) (int, bool) {
		pointers := 0
		if v.Kind == "pointer" {
			if v.Origin == 0 {
				return 0, false
			}
			pointers++
		}
		for _, child := range v.Elements {
			n, ok := inspect(child)
			if !ok {
				return 0, false
			}
			pointers += n
		}
		for _, child := range v.Fields {
			n, ok := inspect(child)
			if !ok {
				return 0, false
			}
			pointers += n
		}
		for _, entry := range v.Entries {
			for _, child := range []bashPPBridgeValue{entry.Key, entry.Value} {
				n, ok := inspect(child)
				if !ok {
					return 0, false
				}
				pointers += n
			}
		}
		return pointers, true
	}
	pointers := 0
	for _, arg := range q.Args {
		n, ok := inspect(arg)
		if !ok {
			return false
		}
		pointers += n
	}
	return pointers > 0
}

// nativePointerReadOnlyRequest identifies dependency operations whose Go
// contracts cannot mutate an origin pointer. Pointer writeback is otherwise
// conservative: without this proof the worker structurally snapshots origins
// the request may mutate or retain. Treating a reflected root as one of those
// on every tiny read turns reflection traversal into repeated whole-graph
// walks.
//
// Keep this list positive. In particular, reflect.Value setters, Call, Recv
// and Send stay on the conservative path. fmt's listed emitters may invoke an
// original Writer, Formatter, Stringer, or error method, but those callbacks
// run against interpreter storage while the request is parked; fmt itself
// never writes through the formatted pointer copies.
func nativePointerReadOnlyRequest(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Op != "call" {
		return false
	}
	if q.Receiver == nil {
		alias, name, ok := strings.Cut(q.Selector, ".")
		if !ok {
			return false
		}
		pkg := req.Imports[alias]
		switch pkg {
		case "reflect":
			switch name {
			case "ValueOf", "TypeOf", "DeepEqual":
				return true
			}
			return false
		case "fmt":
			callable := pkg + "." + name
			return nativeSliceReadOnly(callable)
		default:
			return false
		}
	}
	receiverType := strings.TrimPrefix(q.Receiver.NativeType, "*")
	declaredType := strings.TrimPrefix(q.Receiver.Type, "*")
	if receiverType == "reflect.Type" || declaredType == "reflect.Type" {
		// reflect.Type is immutable; all of its methods inspect type metadata.
		return true
	}
	if receiverType != "reflect.Value" && declaredType != "reflect.Value" {
		return false
	}
	switch q.Selector {
	case "CanAddr", "CanComplex", "CanConvert", "CanFloat", "CanInt", "CanInterface", "CanSet", "CanUint",
		"Cap", "Comparable", "Complex", "Convert", "Elem", "Equal", "Field", "FieldByIndex", "FieldByIndexErr",
		"FieldByName", "Float", "Index", "Int", "Interface", "InterfaceData", "IsNil", "IsValid", "IsZero",
		"Kind", "Len", "MapIndex", "MapKeys", "MapRange", "Method", "MethodByName", "NumField", "NumMethod",
		"OverflowComplex", "OverflowFloat", "OverflowInt", "OverflowUint", "Pointer", "String", "Type", "Uint", "UnsafePointer":
		return true
	}
	return false
}

func nativeRetainedPointerMutator(name string) bool {
	switch name {
	case "flag.Parse", "*flag.FlagSet.Parse":
		return true
	}
	return false
}

func synchronousErrorsAsType(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Receiver != nil {
		return false
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	return ok && req.Imports[alias] == "errors" && name == "AsType" && len(q.Args) == 1
}

// requestCallbackCapable reports a request that must be able to service an
// original callback while it is in flight. That is either a request carrying
// one, or any request on a session that has already been handed a RETAINED
// callback: the dependency may raise that one at any later moment, and the
// request parked at that moment is the only frame able to run it.
func requestCallbackCapable(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	// A native select only transfers channel values; it cannot invoke a
	// retained original callback. Holding the callback gate while it blocks
	// would prevent the task whose callback sends to that channel from
	// entering its request and wake the receiver.
	if q.Op == "channel-select" && !requestHasCallbacks(req, q) {
		return false
	}
	if requestHasCallbacks(req, q) && !callbackInertRequest(req, q) {
		return true
	}
	if reflectValueCall(q) && localMethodsMirrored(req) {
		return true
	}
	if companionTrampolineCall(req, q) {
		return true
	}
	if reflectedHandleOperand(q) {
		return true
	}
	return req.Bridge != nil && req.Bridge.retainedCallbacks()
}

// companionTrampolineCall reports a call of a no-body declaration satisfied by
// an object companion which itself references an original package function.
// The companion may reach that body while this request is parked, so the
// request must own its callbacks exactly as a mirrored method call does.
func companionTrampolineCall(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if len(req.CompanionTrampolines) == 0 || q.Op != "call" || q.Receiver != nil {
		return false
	}
	for _, fn := range req.NativeFuncs {
		if fn.Name == q.Selector {
			return true
		}
	}
	return false
}

// reflectValueCall reports a reflect.Value.Call or CallSlice request. The
// callee inside the Value is opaque to the transport — a Method(i).Func or
// MethodByName result is a plain reflect.Value handle, and its []reflect.Value
// arguments are handles too, so nothing in the request spells a local type.
// When the program mirrors any local method, that callee may be one, and the
// call re-enters the interpreter synchronously while this request is parked:
// the request must own its callbacks, exactly as a bare call of a func(main.M)
// handle does through synchronousFunctionCallback.
func reflectValueCall(q bashPPBridgeRequest) bool {
	if q.Op != "call" || q.Receiver == nil || q.Receiver.Kind != "handle" {
		return false
	}
	if q.Selector != "Call" && q.Selector != "CallSlice" {
		return false
	}
	return strings.TrimPrefix(q.Receiver.NativeType, "*") == "reflect.Value" || strings.TrimPrefix(q.Receiver.Type, "*") == "reflect.Value"
}

// localMethodsMirrored reports whether the helper mirrors any original method
// at all; without one no dependency call can reach an original body.
func localMethodsMirrored(req bashPPEvalRequest) bool {
	return req.localTypePlan().methodsMirrored
}

// Only requests carrying a local interface callback acquire the gate. Native
// blocking synchronization must remain available to other interpreted tasks.
func requestHasCallbacks(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	local := req.localTypePlan()
	for _, v := range q.Args {
		if bridgeValueCarriesCallbacks(local, v) {
			return true
		}
	}
	return q.Receiver != nil && bridgeValueCarriesCallbacks(local, *q.Receiver)
}

// requestFunctionCallbackArgs reports a request handing over an original
// function value among its arguments.
func requestFunctionCallbackArgs(q bashPPBridgeRequest) bool {
	for _, arg := range q.Args {
		if arg.Kind == "callback" {
			return true
		}
	}
	return false
}

// bridgeValueCarriesCallbacks reports a transported value through which the
// dependency can reach an original body: a retained-callback value, or a
// local type whose mirrored methods the dependency may invoke, anywhere in
// the value tree.
func bridgeValueCarriesCallbacks(local *bashPPLocalTypePlan, v bashPPBridgeValue) bool {
	if v.Callbacks || local.localMethodCallbackName(v.Type) {
		return true
	}
	for _, c := range v.Elements {
		if bridgeValueCarriesCallbacks(local, c) {
			return true
		}
	}
	for _, c := range v.Fields {
		if bridgeValueCarriesCallbacks(local, c) {
			return true
		}
	}
	for _, e := range v.Entries {
		if bridgeValueCarriesCallbacks(local, e.Key) || bridgeValueCarriesCallbacks(local, e.Value) {
			return true
		}
	}
	return false
}
