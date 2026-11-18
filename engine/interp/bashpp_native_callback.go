package interp

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
//
// The interpreter side of a dependency callback. A materialised local type's
// reviewed method is a generated stub in the helper whose whole body
// asks for this: the original body is never compiled, it is executed here by
// the interpreter that owns it.

import (
	"context"
	"errors"
	"fmt"
	"go/constant"
	"strconv"
	"strings"
	"sync"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPTestingCallbackFrames owns independent interpreter frames which native
// package testing may suspend at T.Parallel. The native M.Run request is the
// bounded lifetime root: it cannot return until all test, subtest and cleanup
// callbacks have returned, and the host joins the same frames before accepting
// its reply.
type bashPPTestingCallbackFrames struct {
	sync.WaitGroup
	mu     sync.Mutex
	status *exitStatus
	frames []*Runner
	close  sync.Once
}

func (g *bashPPTestingCallbackFrames) add(r *Runner) {
	g.mu.Lock()
	g.frames = append(g.frames, r)
	g.Add(1)
	g.mu.Unlock()
}

func (g *bashPPTestingCallbackFrames) finish(r *Runner) {
	if r == nil {
		return
	}
	g.mu.Lock()
	if g.status == nil && (r.exit.exiting || r.exit.fatalExit || r.exit.err != nil) {
		status := r.exit
		g.status = &status
	}
	for i, frame := range g.frames {
		if frame == r {
			last := len(g.frames) - 1
			g.frames[i] = g.frames[last]
			g.frames[last] = nil
			g.frames = g.frames[:last]
			break
		}
	}
	g.mu.Unlock()
	r.closeDirFile()
}

func (g *bashPPTestingCallbackFrames) adopt(r *Runner) error {
	g.Wait()
	g.mu.Lock()
	status := g.status
	g.mu.Unlock()
	if status == nil {
		return nil
	}
	if r == nil {
		return errors.New("gosource: testing callback lifetime has no owning runner")
	}
	r.exit = *status
	if status.exiting {
		return &bashPPNativeExit{status: int(status.code)}
	}
	return status.err
}

func (g *bashPPTestingCallbackFrames) closeFrames() {
	g.Wait()
	g.close.Do(func() {
		g.mu.Lock()
		frames := append([]*Runner(nil), g.frames...)
		g.mu.Unlock()
		for _, frame := range frames {
			frame.closeDirFile()
		}
	})
}

func (registered *bashPPCallbackFunction) bashPPTestingCallbackFrame(group *bashPPTestingCallbackFrames) (*Runner, *bashPPFunc, error) {
	if registered == nil || registered.template == nil || registered.templateFn == nil {
		return nil, nil, fmt.Errorf("callback has no registered execution template")
	}
	template := registered.template
	concurrent := template.bashPPConcurrent
	if !concurrent.ownsActiveFileRun() {
		return nil, nil, fmt.Errorf("callback has no active owning File Run")
	}
	child := template.subshellWithBashPPCapture(true, registered.capture)
	child.bashPPGoSourceCapture = nil
	// A testing callback is another Go execution frame in the registering
	// task group, not a shell-copy boundary. It owns its stacks and scopes but
	// retains the group's channels and cancellation capability. The template
	// deliberately did not clone bashPPFileRun; restore it only after the exact
	// registration-time group proved that its owning File Run is still active.
	child.bashPPConcurrent, child.bashPPGoTask, child.bashPPChanBoundary = concurrent, true, false
	child.bashPPFileRun = true
	child.bashPPTools.callbackDepth = 0
	child.bashPPTools.routedDepth = 0
	child.bashPPTools.callbackDescendant = true
	child.bashPPTools.testingCallbackFrames = group
	child.exit = exitStatus{}
	child.lastExit = exitStatus{}
	cloner := newBashPPClonerFor(child)
	// Registration recorded both true lexical captures and the authenticated
	// source package's global cells. Reuse that immutable identity set: cloning
	// the template's root here would either lose package mutation or retain
	// unrelated frame and foreign-package storage.
	cloner.shared = registered.capture
	cloner.goSourceTask = true
	cloner.exactCapture = true
	return child, registered.templateFn.cloned(cloner), nil
}

func nativeTestingCallbackBarrier(q bashPPBridgeRequest) (testingM bool, ok bool) {
	if q.Op != "call" || q.Selector != "Run" || q.Receiver == nil {
		return false, false
	}
	switch q.Receiver.NativeType {
	case "testing.M", "*testing.M":
		return true, true
	case "testing.T", "*testing.T":
		return false, true
	}
	return false, false
}

func (s *bashPPNativeSession) testingCallbackFrame(group *bashPPTestingCallbackFrames, q bashPPBridgeResponse) (*Runner, *bashPPFunc, bool, error) {
	if group == nil || q.Receiver == nil || q.Receiver.Kind != "callback" {
		return nil, nil, false, nil
	}
	registered := s.callbackFunction(q.Receiver.Handle)
	// Sessions and focused tests predating execution templates may populate
	// only functions. Preserve their serialized callback path.
	if registered == nil {
		return nil, nil, false, nil
	}
	if registered.templateErr != "" {
		return nil, nil, false, errors.New(registered.templateErr)
	}
	if registered.template == nil {
		return nil, nil, false, nil
	}
	frame, fn, err := registered.bashPPTestingCallbackFrame(group)
	if err != nil {
		return nil, nil, false, err
	}
	group.add(frame)
	return frame, fn, true, nil
}

func (s *bashPPNativeSession) answerMailboxCallback(mailbox *bashPPCallbackMailbox, slot int, answer bashPPBridgeRequest) {
	s.mu.Lock()
	alive, conn := s.mailbox == mailbox, s.conn
	s.mu.Unlock()
	if !alive {
		return
	}
	if mailbox.answer(slot, answer) {
		// The waiter spins first, so a short body is answered with no
		// socket write at all; only a parked waiter needs the wake.
		if conn != nil && mailbox.workerParked(slot) {
			_ = s.sendMessage(func() any { return bashPPBridgeRequest{ID: answer.ID, Op: "callback-mailbox-wake"} })
		}
		return
	}
	// The original body has already run. Send its exact result once through
	// the authenticated socket, then wake its mailbox waiter with a marker.
	var sendErr error
	if conn == nil {
		sendErr = errors.New("gosource: callback connection closed")
	} else {
		sendErr = s.sendMessage(func() any { return answer })
	}
	marker := bashPPBridgeRequest{ID: answer.ID, Op: "callback-mailbox-overflow"}
	if sendErr != nil {
		marker.Error = sendErr.Error()
	}
	mailbox.answer(slot, marker)
}

// enterCallbacks serializes callback-capable outer requests. Nested imports from
// the active callback may reenter; an unrelated Runner never borrows its state.
func (s *bashPPNativeSession) enterCallbacks(ctx context.Context, req bashPPEvalRequest, q bashPPBridgeRequest) (func(), chan bashPPBridgeResponse, error) {
	if !requestCallbackCapable(req, q) {
		return func() {}, nil, nil
	}
	s.mu.Lock()
	if s.callbackGate == nil {
		s.callbackGate = make(chan struct{}, 1)
		s.callbackGate <- struct{}{}
	}
	gate := s.callbackGate
	nested := req.CallbackDepth > 0 && req.CallbackOwner == s.callbackOwner
	foreign := req.CallbackDepth > 0 && req.CallbackOwner != s.callbackOwner
	s.mu.Unlock()
	if foreign {
		return nil, nil, fmt.Errorf("gosource: concurrent task cannot reenter an active original method callback")
	}
	if !nested {
		select {
		case <-gate:
		default:
			// Waiting for another task's callback frame is a blocking
			// operation: announce it, so the goroutine that launched this
			// task — possibly the one that will unblock that frame — is not
			// held at the launch handshake meanwhile.
			if owner := req.CallbackOwner; owner != nil && !owner.bashPPArmBeforeBlock(ctx) {
				return nil, nil, errBashPPScalarInterrupted
			}
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
	}
	s.mu.Lock()
	previous, owner := s.activeCallbacks, s.callbackOwner
	callbacks := make(chan bashPPBridgeResponse, 1)
	s.activeCallbacks, s.callbackOwner = callbacks, req.CallbackOwner
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.activeCallbacks, s.callbackOwner = previous, owner
		s.mu.Unlock()
		if !nested {
			gate <- struct{}{}
		}
	}, callbacks, nil
}

// serveCallback executes synchronously in the parked request's goroutine. The
// socket reader stays free, and nested imports can service their own callbacks.
//
// coherence, when the parked request copied original storage for a read-only
// emitter, is checked after the body and after the receiver reconciliation
// is computed: a stale copy fails this callback before the dependency reads
// any further.
func (s *bashPPNativeSession) callbackAnswer(ctx context.Context, owner *Runner, q bashPPBridgeResponse, outer *bashPPBridgeRequest, frameFn *bashPPFunc) (answer bashPPBridgeRequest) {
	s.adoptHandleResponse(&q)
	defer func() { s.retainHandleReply(&answer) }()
	answer = bashPPBridgeRequest{ID: q.ID, Op: "callback-reply"}
	var coherence *goSourceCopyCoherence
	if outer != nil {
		coherence = outer.coherence
	}
	if q.Receiver != nil && q.Receiver.Kind == "callback" {
		if s.originalCallbackFunction(q.Receiver.Handle) == nil {
			answer.Error = "gosource: original callback handle expired"
			return answer
		}
	}
	if owner != nil && outer != nil && outer.sliceCallbackSync {
		for i := range q.SliceUpdates {
			s.bashPPAuthenticateCallbackValue(&q.SliceUpdates[i].Value)
		}
		if err := applyNativeSliceBuffers(owner, *outer, bashPPBridgeResponse{SliceUpdates: q.SliceUpdates}); err != nil {
			answer.Error = err.Error()
			if !owner.exit.exiting {
				owner.exit.fatal(err)
				s.recordCallbackRefusal(err)
			}
		}
	}
	if answer.Error != "" {
		// The dependency's copy could not be reconciled, so no original body
		// may run against a different view of the backing storage.
	} else if owner == nil || q.Receiver == nil {
		answer.Error = "gosource: callback has no original owner or receiver"
	} else {
		// A protocol callback runs whether its receiver is a pointer or a value.
		// A pointer with an authenticated origin has its pointee reconciled after
		// the body (below); a value receiver owns only a decoded copy of itself
		// and reaches no live pointee, but a write through that copy's reference
		// storage is caught by the copied-receiver digest in bashPPNativeCallback.
		// Either way, when the parked request copied original storage for a
		// read-only emitter, the post-callback coherence check (afterCallback)
		// re-reads every copied source — including a slice nested inside a value
		// receiver's own struct fields — and fails the callback if the body wrote
		// storage the dependency still holds a stale copy of. So a value receiver
		// needs no up-front refusal beyond that general check.
		//
		// The dependency may have written to its pipes before asking for this
		// callback. Wait for those bytes only if the interpreted body writes to
		// a caller stream. The outer request still drains on its final reply.
		// Wrapping the original streams too covers process substitutions, which
		// inherit them instead of the current stdout/stderr pair.
		restoreOutput := s.lazyCallbackOutputBarrier(owner)
		defer restoreOutput()
		s.bashPPAuthenticateCallbackValue(q.Receiver)
		owner.bashPPTools.callbackDepth++
		var values []bashPPBridgeValue
		var err error
		if q.Receiver.Kind == "callback" {
			values, err = owner.bashPPNativeFunctionCallback(ctx, q.Receiver.Handle, q.Receiver.Elements, frameFn)
		} else {
			values, err = owner.bashPPNativeCallback(ctx, q.Selector, *q.Receiver)
		}
		owner.bashPPTools.callbackDepth--
		if err != nil {
			// Interruption is an internal unwind token after the shared program
			// outcome is already known. Never serialize its text as an ordinary
			// callback failure and destroy that identity across the bridge.
			if !errors.Is(err, errBashPPScalarInterrupted) {
				answer.Error = err.Error()
			}
			if answer.Error != "" && !owner.exit.exiting {
				owner.exit.fatal(err)
				s.recordCallbackRefusal(err)
			}
		} else {
			answer.Values = s.bashPPWireLazyValueOf(values)
		}
		if q.Receiver.Origin != 0 {
			ptr, _ := s.originLookup(q.Receiver.Origin)
			if ptr != nil {
				v, err := owner.bashPPBridgePointerValue(ptr)
				if err != nil {
					answer.Error = err.Error()
				} else {
					answer.Receiver = &v
				}
			}
		}
		if coherence != nil && answer.Error == "" {
			if err := coherence.afterCallback(owner, answer.Receiver); err != nil {
				answer.Error, answer.Values = err.Error(), nil
				if !owner.exit.exiting {
					owner.exit.fatal(err)
					s.recordCallbackRefusal(err)
				}
			}
		}
	}
	if owner != nil && outer != nil && answer.Error == "" {
		if outer.sliceCallbackSync {
			buffers, err := nativeCallbackSliceBuffers(owner, *outer)
			if err != nil {
				answer.Error, answer.Values = err.Error(), nil
				if !owner.exit.exiting {
					owner.exit.fatal(err)
					s.recordCallbackRefusal(err)
				}
			} else {
				answer.SliceBuffers = buffers
			}
		} else if len(q.SliceUpdates) > 0 {
			// Every helper callback raised inside a call with decoded slice
			// arguments owns a callback slice frame, even when admission did not
			// request interpreter-side reconciliation. The reply must still match
			// that frame exactly. Echo its authenticated snapshot: it is a no-op
			// refresh of the helper's own backing arrays, not a copied-slice write.
			answer.SliceBuffers = append([]bashPPNativeSliceBuffer(nil), q.SliceUpdates...)
			for i := range answer.SliceBuffers {
				buffer := &answer.SliceBuffers[i]
				if buffer.Length < 0 || buffer.Length > len(buffer.Value.Elements) {
					answer.Error, answer.Values, answer.SliceBuffers = "gosource: invalid callback slice snapshot", nil, nil
					break
				}
				// callbackSliceSnapshot publishes the full-capacity backing array;
				// applyCallbackSliceReply decodes a value whose length must match
				// the live argument header. Preserve that header in the no-op echo.
				buffer.Value.Elements = buffer.Value.Elements[:buffer.Length]
				buffer.Value.Length = buffer.Length
				buffer.Value.Capacity = buffer.Length
			}
		}
	}
	return answer
}

func (s *bashPPNativeSession) recordCallbackRefusal(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.callbackRefusal == nil {
		s.callbackRefusal = err
	}
}

func (s *bashPPNativeSession) serveCallback(ctx context.Context, owner *Runner, q bashPPBridgeResponse, outer *bashPPBridgeRequest, frameFn *bashPPFunc) {
	s.adoptHandleResponse(&q)
	answer := s.callbackAnswer(ctx, owner, q, outer, frameFn)
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		_ = s.sendMessage(func() any { return answer })
	}
}

// bashPPLocalTypeName maps a dependency-reported type identity back to the
// original program's own type name. The helper is package main, exactly as the
// original program is, so a materialised local type reports itself as
// "main.Vertex" — the same identity Go's %T prints.
func bashPPLocalTypeName(identity string) string {
	return strings.TrimPrefix(identity, "main.")
}

// bashPPNativeCallback executes one original method body for the dependency.
// Reviewed signatures include String/Error, synchronous Read, and image.Image.
// Arguments and results preserve their declared types across the callback.
func (r *Runner) bashPPNativeCallback(ctx context.Context, selector string, recv bashPPBridgeValue) (values []bashPPBridgeValue, err error) {
	defer func() {
		if failure := recover(); failure != nil {
			values = nil
			err = fmt.Errorf("gosource: original callback interpreter failure: %v", failure)
		}
	}()
	if name, ok := strings.CutPrefix(selector, bashPPCompanionSelectorPrefix); ok {
		return r.bashPPNativeCompanionCallback(ctx, name, recv.CallArgs)
	}
	declaringType, method, ok := strings.Cut(selector, ".")
	if !ok {
		return nil, fmt.Errorf("gosource: malformed original method selector %q", selector)
	}
	declaringType = bashPPLocalTypeName(declaringType)
	var copied *bashPPCell
	fn := r.bashPPMethods[declaringType][method]
	if fn == nil {
		return nil, fmt.Errorf("gosource: original type %s has no method %s", declaringType, method)
	}
	named := r.bashPPSprint162CallbackType(recv, declaringType)
	var cell *bashPPCell
	var promoted bashPPSelection
	if recv.Origin != 0 {
		session := r.bashPPTools.bridge
		ptr, _ := session.originLookup(recv.Origin)
		if ptr == nil {
			return nil, fmt.Errorf("gosource: callback receiver identity expired")
		}
		cell = bashPPPointerCell(ptr)
		// A generated helper method is declared on the embedded type, so its
		// callback selector names that declaration even when reflect invoked it
		// as a promoted method of an outer value. The authenticated origin is
		// authoritative for the actual receiver. Resolve from its complete type
		// and require the selected method to be the exact declaration reported
		// by the helper; this retains the field path and refuses ambiguity or a
		// same-named outer method instead of falling back by name.
		promoted = r.bashPPResolveSelection(cell.declType, method, true, false)
		if promoted.ambiguous || promoted.method != fn {
			return nil, fmt.Errorf("gosource: original method %s does not belong to authenticated receiver %s", selector, bashPPTypeText(cell.declType))
		}
	} else if recv.Kind == "nil" {
		// A typed-nil pointer receiver has no storage to bind. The original
		// body runs with a nil receiver, exactly as native Go invokes it — a
		// body that checks its receiver observes nil, one that dereferences
		// it panics as the original would.
		cell = &bashPPCell{pointer: true, nilPointer: true, typeName: declaringType, declType: &syntax.BashPPPointerType{Element: named}}
	} else {
		if fn.decl.Receiver.Pointer {
			return nil, fmt.Errorf("gosource: pointer callback requires original receiver identity")
		}
		value, meta, err := r.bashPPBridgeContents(recv, named)
		if err != nil {
			return nil, fmt.Errorf("gosource: %s receiver: %w", selector, err)
		}
		cell = &bashPPCell{declType: named, typeName: declaringType}
		bashPPStoreCellValue(cell, value, meta)
		// The receiver is a copy of storage the caller still shares. A write
		// through one of its references would be invisible to the caller here,
		// so it is detected after the body rather than dropped.
		copied = cell
	}
	before := r.goSourceCopiedReceiverDigest(copied)

	// The callback borrows the runner while its caller is parked inside the
	// imported call, so the caller's in-flight argument state is restored
	// rather than consumed by this nested invocation.
	savedResults, savedChannels := r.bashPPResultCells, r.bashPPCallChannels
	savedInterfaces, savedCells := r.bashPPCallInterfaces, r.bashPPCallCells
	savedSpread := r.bashPPCallSpread
	savedExit, savedPanic, failure := r.exit, r.bashPPPanic, r.bashPPShortFailureSeq
	savedCtx := r.ectx
	r.ectx = ctx
	defer func() {
		r.bashPPResultCells, r.bashPPCallChannels = savedResults, savedChannels
		r.bashPPCallInterfaces, r.bashPPCallCells = savedInterfaces, savedCells
		r.bashPPCallSpread = savedSpread
		r.ectx = savedCtx
	}()
	r.bashPPCallChannels, r.bashPPCallInterfaces, r.bashPPCallCells, r.bashPPCallSpread = nil, nil, nil, false

	// A pointer receiver binds to the original interpreter storage.
	var bound *bashPPFunc
	if len(promoted.edges) > 0 {
		bound, ok = r.bashPPBindPromotedMethod(cell, method, promoted, true)
	} else {
		bound, ok = r.bashPPBindMethodReceiver(cell, method, true, copied != nil)
	}
	if !ok {
		return nil, fmt.Errorf("gosource: cannot bind original method %s", selector)
	}
	previousReturnDepth := r.bashPPCallbackReturnDepth
	r.bashPPCallbackReturnDepth = r.bashPPFuncActive + 1
	defer func() { r.bashPPCallbackReturnDepth = previousReturnDepth }()
	var arguments []string
	var readerBuffer []any
	if method == "Read" {
		if (len(recv.CallArgs) != 1 && len(recv.CallArgs) != 2) || recv.CallArgs[0].Kind != "handle" || (recv.CallArgs[0].Type != "[]uint8" && recv.CallArgs[0].Type != "[]byte") {
			return nil, fmt.Errorf("gosource: original Read requires an authenticated native byte buffer")
		}
		argument := goSourceNativeValueCell(recv.CallArgs[0])
		argument.declType = bound.params()[0].FieldTypeExpr
		argument.typeName = bashPPTypeText(argument.declType)
		if len(recv.CallArgs) == 2 && r.bashPPReaderLocalBufferAllowed(bound) {
			snapshot := recv.CallArgs[1]
			if snapshot.Kind != "reader-buffer" || snapshot.ReaderLength < 0 || snapshot.ReaderLength > len(snapshot.ReaderBuffer) {
				return nil, fmt.Errorf("gosource: invalid Read buffer snapshot")
			}
			readerBuffer = make([]any, len(snapshot.ReaderBuffer))
			for i, b := range snapshot.ReaderBuffer {
				readerBuffer[i] = int(b)
			}
			bashPPStoreCellValue(argument, readerBuffer[:snapshot.ReaderLength], &bashPPCollectionMeta{kind: "slice", typ: argument.declType, sequence: make([]*bashPPCollectionMeta, snapshot.ReaderLength, len(readerBuffer))})
		}

		r.bashPPCallCells = []*bashPPCell{argument}
		arguments = []string{""}
	} else {
		// A generalised mirrored signature — image.Image's At(x, y int) — sends
		// its arguments with the call. Each binds at the original parameter's
		// own declared type, so the body sees exactly what it declared.
		params := bashppParams(bound.params())
		if len(recv.CallArgs) != len(params) {
			return nil, fmt.Errorf("gosource: original %s got %d argument(s), want %d", selector, len(recv.CallArgs), len(params))
		}
		cells := make([]*bashPPCell, len(params))
		for i, arg := range recv.CallArgs {
			cell := r.goSourceNativeValueCell(arg)
			cell.declType = params[i].typ
			if params[i].variadic {
				// A generated variadic mirror receives its trailing arguments as
				// Go's one []T parameter. Bind that slice with spread semantics so
				// the original frame observes the same nil/header/backing identity
				// and each transported element keeps its own value metadata.
				cell.declType = &syntax.BashPPCollectionType{Kind: "slice", Element: params[i].typ}
				r.bashPPCallSpread = true
			}
			cell.typeName = bashPPTypeText(cell.declType)
			cells[i] = cell
			text := ""
			if scalar, err := arg.scalar(); err == nil {
				text = bashPPScalarString(scalar.value)
			}
			arguments = append(arguments, text)
		}
		r.bashPPCallCells = cells
	}
	r.bashPPResultCells = nil
	entry := len(r.callStack)
	results := r.bashPPInvoke(ctx, bound, arguments)
	if r.bashPPGoexiting() {
		r.bashPPPanic, r.exit = savedPanic, savedExit
		// The body ended by runtime.Goexit: the goroutine which raised
		// this callback ends too. See bashpp_goexit.go.
		return []bashPPBridgeValue{{Kind: bashPPGoexitKind}}, nil
	}
	if r.bashPPCallbackRaised(entry) && !r.exit.exiting {
		payload, goexit := r.bashPPPanic.value(), r.bashPPGoexiting()
		r.bashPPPanic, r.exit = savedPanic, savedExit
		if goexit {
			// The body ended by runtime.Goexit: the goroutine which raised
			// this callback ends too. See bashpp_goexit.go.
			return []bashPPBridgeValue{{Kind: bashPPGoexitKind}}, nil
		}
		return []bashPPBridgeValue{bashPPBridgePanicString(payload)}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.exit.err != nil {
		return nil, r.exit.err
	}
	if r.exit.exiting || r.exit.fatalExit || r.exit.code != 0 || r.bashPPShortFailureSeq != failure {
		return nil, fmt.Errorf("gosource: original %s failed (status %d)", selector, r.exit.code)
	}
	if copied != nil && r.goSourceCopiedReceiverDigest(copied) != before {
		return nil, fmt.Errorf("gosource: original %s wrote through reference storage of a copied receiver; that effect cannot cross back", selector)
	}
	// Error and String answer with the one string the original body returns.
	// Every other mirrored signature — Read, and the image.Image method set —
	// crosses back as typed values, so a native handle stays an authenticated
	// handle rather than being flattened into text.
	if want := bashppParams(bound.results()); !bashPPStringResult(want) {
		if len(r.bashPPResultCells) != len(want) {
			return nil, fmt.Errorf("gosource: original %s returned %d values, want %d", selector, len(r.bashPPResultCells), len(want))
		}
		for _, cell := range r.bashPPResultCells {
			value, err := r.bashPPBridgeCell(cell)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		if readerBuffer != nil {
			data := make([]byte, len(readerBuffer))
			for i, item := range readerBuffer {
				n, ok := item.(int)
				if !ok || n < 0 || n > 255 {
					return nil, fmt.Errorf("gosource: invalid Read byte writeback")
				}
				data[i] = byte(n)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			values = append(values, bashPPBridgeValue{Kind: "reader-buffer", ReaderBuffer: data})
		}
		return values, nil
	}
	if len(results) != 1 {
		return nil, fmt.Errorf("gosource: original %s returned %d values, want 1", selector, len(results))
	}
	return []bashPPBridgeValue{bashPPBridgeString(results[0])}, nil
}

// bashPPStringResult reports the fixed String/Error protocol shape: exactly one
// result, of type string. Those mirrors answer with text; everything else the
// helper mirrors answers with typed values.
func bashPPStringResult(results []bashPPParam) bool {
	return len(results) == 1 && bashPPTypeText(results[0].typ) == "string"
}

// bashPPBridgeContents rebuilds an interpreter value at its original declared
// type. Native fields retain their authenticated session handles; their private
// SDK storage is never reflected into an interpreter-owned imitation.
func (r *Runner) bashPPBridgeContents(v bashPPBridgeValue, typ syntax.BashPPTypeExpr) (any, *bashPPCollectionMeta, error) {
	_, interfaceType := r.bashPPInterfaceType(typ)
	if interfaceType {
		if bashPPBridgeValueNilInterface(v) {
			return "", &bashPPCollectionMeta{kind: "interface", typ: typ, interfaceValue: &bashPPInterfaceValue{nilIface: true}}, nil
		}
		dynamic := bashPPBridgeValueDynamicType(v)
		if _, ok := r.bashPPInterfaceType(dynamic); ok {
			return nil, nil, fmt.Errorf("dynamic type %s of an interface value is not materialised", v.Type)
		}
		if v.Kind == "nil" {
			if _, _, err := r.goSourceNativeAssignedValue(v, typ); err != nil {
				return nil, nil, err
			}
			// The native receiver contains a nonnil interface whose dynamic
			// value is typed nil. Keep that type; no handle needs rebuilding.
			payload := goSourceNativeValueCell(v)
			payload.declType = dynamic
			iv := &bashPPInterfaceValue{cell: payload, dynamic: dynamic}
			return payload.vr.Obj, &bashPPCollectionMeta{kind: "interface", typ: typ, interfaceValue: iv}, nil
		}
		inner, innerMeta, err := r.bashPPBridgeContents(v, dynamic)
		if err != nil {
			return nil, nil, err
		}
		payload := &bashPPCell{declType: dynamic}
		if named, ok := dynamic.(*syntax.BashPPNamedType); ok && named.Name != nil {
			payload.typeName = named.Name.Value
		}
		innerMeta = bashPPBridgeNestedMeta(innerMeta)
		bashPPStoreCellValue(payload, inner, innerMeta)
		iv := &bashPPInterfaceValue{cell: payload, dynamic: dynamic}
		return inner, &bashPPCollectionMeta{kind: "interface", typ: typ, interfaceValue: iv}, nil
	}
	if v.Kind == "handle" {
		if r.bashPPTools.bridge == nil || v.Session != r.bashPPTools.bridge.id {
			return nil, nil, fmt.Errorf("gosource: native callback field belongs to another dependency session")
		}
		copy := v
		return &copy, &bashPPCollectionMeta{kind: "native", typ: typ}, nil
	}
	if pointer, ok := r.bashPPPointerType(typ); ok {
		if v.Kind == "nil" {
			return nil, bashPPPointerMeta(typ), nil
		}
		// A pointee the dependency sends under an origin of this session IS
		// that origin's pointer — whether spelled as a back-reference (no
		// pointee: a cycle) or flattened with its origin — so identity
		// survives a writeback; a back-reference to no known origin fails
		// closed. See bashpp_sprint165_runtime2_cycle.go.
		if ptr, resolved, err := r.bashPPBridgeOriginPointer(v); err != nil {
			return nil, nil, err
		} else if resolved {
			return ptr, bashPPPointerMeta(typ), nil
		}
		// The dependency sends the pointee by shape, so the original body
		// reads the value it would have read through the pointer. The
		// dependency keeps ownership of the address itself.
		return r.bashPPBridgeContents(v, pointer.Element)
	}
	if v.Kind == "nil" {
		value, meta := r.bashPPZeroValue(typ)
		return value, meta, nil
	}
	switch shape := r.bashPPUnderlyingType(typ).(type) {
	case *syntax.BashPPStructType:
		if v.Kind != "struct" {
			return nil, nil, fmt.Errorf("%s is a struct but the dependency sent %s", bashPPTypeText(typ), v.Kind)
		}
		out := map[string]any{}
		meta := &bashPPCollectionMeta{kind: "struct", typ: typ, mapping: map[string]*bashPPCollectionMeta{}}
		for _, field := range bashPPFlatFields(shape.Fields) {
			item, ok := v.Fields[field.name]
			if !ok {
				return nil, nil, fmt.Errorf("field %s did not cross the dependency boundary", field.name)
			}
			value, child, err := r.bashPPBridgeContents(item, field.typ)
			if err != nil {
				return nil, nil, err
			}
			out[field.name], meta.mapping[field.name] = value, bashPPBridgeNestedMeta(child)
		}
		return out, meta, nil
	case *syntax.BashPPCollectionType:
		if shape.Kind == "map" {
			if v.Kind != "map" {
				return nil, nil, fmt.Errorf("%s is a map but the dependency sent %s", bashPPTypeText(typ), v.Kind)
			}
			out := map[string]any{}
			meta := &bashPPCollectionMeta{kind: "map", typ: typ, mapping: map[string]*bashPPCollectionMeta{}, mapKeys: map[bashPPMapKey]*bashPPMapEntry{}}
			for _, item := range v.Entries {
				key, keyMeta, err := r.bashPPBridgeContents(item.Key, shape.Key)
				if err != nil {
					return nil, nil, err
				}
				value, child, err := r.bashPPBridgeContents(item.Value, shape.Element)
				if err != nil {
					return nil, nil, err
				}
				if _, err := r.bashPPSprint165MapStore(out, meta, key, bashPPBridgeNestedMeta(keyMeta), shape.Key, value, bashPPBridgeNestedMeta(child)); err != nil {
					return nil, nil, err
				}
			}
			return out, meta, nil
		}
		if v.Kind == "bytes" {
			// Compact byte array (see bashPPByteArrayBytes): rebuild the
			// same per-byte storage the structural spelling decodes to,
			// element by element through the normal path.
			if shape.Kind != "array" && shape.Kind != "slice" {
				return nil, nil, fmt.Errorf("%s is a sequence but the dependency sent %s", bashPPTypeText(typ), v.Kind)
			}
			if !bashPPByteElementType(r, shape.Element) {
				return nil, nil, fmt.Errorf("%s is not a byte sequence but the dependency sent bytes", bashPPTypeText(typ))
			}
			out := make([]any, 0, len(v.Bytes))
			meta := &bashPPCollectionMeta{kind: shape.Kind, typ: typ}
			for _, b := range v.Bytes {
				item, child, err := r.bashPPBridgeContents(bashPPBridgeValue{Kind: "uint", Type: "uint8", Text: strconv.Itoa(int(b))}, shape.Element)
				if err != nil {
					return nil, nil, err
				}
				out = append(out, item)
				meta.sequence = append(meta.sequence, bashPPBridgeNestedMeta(child))
			}
			return out, meta, nil
		}
		if v.Kind != "slice" && v.Kind != "array" {
			return nil, nil, fmt.Errorf("%s is a sequence but the dependency sent %s", bashPPTypeText(typ), v.Kind)
		}
		out := make([]any, 0, len(v.Elements))
		meta := &bashPPCollectionMeta{kind: shape.Kind, typ: typ}
		for _, item := range v.Elements {
			value, child, err := r.bashPPBridgeContents(item, shape.Element)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, value)
			meta.sequence = append(meta.sequence, bashPPBridgeNestedMeta(child))
		}
		return out, meta, nil
	}
	value, _, err := bashPPBridgeScalarValue(v)
	if err != nil {
		return nil, nil, err
	}
	if bashPPBridgeCarrierText(v, value) {
		// The payload is the value's spelling, not a value of typ, so it
		// travels the way every other collection carrier text does: bare, for
		// the declared destination to reconstruct on read.
		return value, nil, nil
	}
	return value, &bashPPCollectionMeta{kind: "scalar", typ: typ}, nil
}

// bashPPBridgeCarrierText reports whether the interpreter representation of a
// transported scalar is that value's exact Go spelling rather than a value of
// its declared type. Complex numbers have no interpreter scalar of their own —
// collections stay JSON-shaped, so the lossless text is the carrier and the
// destination type rebuilds the tagged scalar on read (see
// bashPPSprint162ComplexCollectionText). Such a payload must not claim scalar
// collection metadata: the metadata would assert that the text IS a value of
// that type, and scalar consumers would then compare, print and convert the
// spelling instead of the number.
func bashPPBridgeCarrierText(v bashPPBridgeValue, value any) bool {
	if _, text := value.(string); !text {
		return false
	}
	return v.Kind != "string"
}

// bashPPBridgeNestedMeta drops the scalar tag bashPPBridgeContents puts on a
// scalar result: the tag keeps a top-level callback result's declared type
// (sh 36121727), but inside a struct, map, sequence or interface payload a
// scalar is stored bare, as every reader of a collection expects. A tagged
// element made a range variable over a written-back slice (sort.Strings)
// bind as an object.
func bashPPBridgeNestedMeta(meta *bashPPCollectionMeta) *bashPPCollectionMeta {
	if bashPPScalarComparableMeta(meta) {
		return nil
	}
	return meta
}

func bashPPBridgeDynamicType(name string) syntax.BashPPTypeExpr {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "*") {
		return &syntax.BashPPPointerType{Element: bashPPBridgeDynamicType(name[1:])}
	}
	switch name {
	case "interface {}", "interface{}":
		return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "any"}}
	}
	if strings.HasPrefix(name, "[]") {
		return &syntax.BashPPCollectionType{
			Kind:    "slice",
			Element: bashPPBridgeDynamicType(name[2:]),
		}
	}
	if strings.HasPrefix(name, "[") {
		if end := strings.IndexByte(name, ']'); end > 1 {
			if _, err := strconv.ParseUint(name[1:end], 10, 63); err == nil {
				return &syntax.BashPPCollectionType{
					Kind:    "array",
					Length:  &syntax.Lit{Value: name[1:end]},
					Element: bashPPBridgeDynamicType(name[end+1:]),
				}
			}
		}
	}
	if strings.HasPrefix(name, "map[") {
		end := strings.IndexByte(name, ']')
		if end > len("map[") {
			return &syntax.BashPPCollectionType{
				Kind:    "map",
				Key:     bashPPBridgeDynamicType(name[len("map["):end]),
				Element: bashPPBridgeDynamicType(name[end+1:]),
			}
		}
	}
	return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: bashPPLocalTypeName(name)}}
}

func bashPPBridgeValueDynamicType(v bashPPBridgeValue) syntax.BashPPTypeExpr {
	name := v.Type
	if (v.Kind == "handle" || v.Kind == "nil") && v.NativeType != "" {
		name = v.NativeType
	}
	return bashPPBridgeDynamicType(name)
}

func bashPPBridgeValueNilInterface(v bashPPBridgeValue) bool {
	if v.Kind == "nil" && v.NativeType != "" && v.NativeType != v.Interface {
		return false
	}
	return v.Kind == "nil" && (v.Type == "" || v.Type == v.Interface)
}

// bashPPBridgeScalarValue converts one transported scalar into the interpreter
// representation the collection layer stores.
func bashPPBridgeScalarValue(v bashPPBridgeValue) (any, *bashPPCollectionMeta, error) {
	switch v.Kind {
	case "string":
		return v.stringText(), nil, nil
	case "bool":
		return v.Text == "true", nil, nil
	case "int", "uint":
		n, err := strconv.Atoi(v.Text)
		if err != nil {
			return nil, nil, fmt.Errorf("integer %q from the dependency: %w", v.Text, err)
		}
		return n, nil, nil
	case "float":
		// A NaN crosses the bridge as its IEEE bits so the payload survives;
		// collections store that carrier as the float's text.
		if _, nan := bashPPNaNBitsScalar(v.Text, ""); nan {
			return v.Text, nil, nil
		}
		n, err := strconv.ParseFloat(v.Text, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("float %q from the dependency: %w", v.Text, err)
		}
		return n, nil, nil
	case "complex":
		// Collections use the exact Go spelling as their complex carrier. The
		// declared destination type reconstructs the scalar on read, including
		// defined complex types and non-finite runtime values.
		if z, special := bashPPNonFiniteComplexText(v.Text); special {
			return strconv.FormatComplex(z, 'g', -1, 128), nil, nil
		}
		if bashPPParseComplex(v.Text).Kind() != constant.Complex {
			return nil, nil, fmt.Errorf("complex %q from the dependency is invalid", v.Text)
		}
		return v.Text, nil, nil
	}
	return nil, nil, fmt.Errorf("%s (%s) has no interpreter representation", v.Kind, v.Type)
}

// Only values arriving on this session's authenticated callback connection get
// its identity. Stored handles keep that identity through nested field reads,
// and the normal request validator rejects them after Reset or in another runner.
func (s *bashPPNativeSession) bashPPAuthenticateCallbackValue(v *bashPPBridgeValue) {
	s.adoptHandleValue(v)
	if v.Kind == "handle" || v.Kind == "callback" || v.Origin != 0 {
		v.Session = s.id
		s.rememberNativeHandleType(*v)
	}
	for i := range v.CallArgs {
		s.bashPPAuthenticateCallbackValue(&v.CallArgs[i])
	}
	for i := range v.Elements {
		s.bashPPAuthenticateCallbackValue(&v.Elements[i])
	}
	for name, field := range v.Fields {
		s.bashPPAuthenticateCallbackValue(&field)
		v.Fields[name] = field
	}
	for i := range v.Entries {
		s.bashPPAuthenticateCallbackValue(&v.Entries[i].Key)
		s.bashPPAuthenticateCallbackValue(&v.Entries[i].Value)
	}
}

// bashPPNativeCompanionCallback runs the original package function an object
// companion called. The trampoline in the helper only carries the call; the
// body executes here, over this runner's own package state.
func (r *Runner) bashPPNativeCompanionCallback(ctx context.Context, name string, args []bashPPBridgeValue) ([]bashPPBridgeValue, error) {
	fn := r.bashPPFuncs[name]
	if fn == nil {
		return nil, fmt.Errorf("gosource: assembly companion target %s is not an original function", name)
	}
	return r.bashPPRunCallbackFunc(ctx, fn, args)
}
