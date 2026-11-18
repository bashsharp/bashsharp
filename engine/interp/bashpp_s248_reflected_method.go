package interp

// Sprint: #248; Story: #702; Story-ID: f330582c10c8
//
// Interpreter-owned reflected method values.
//
// reflect.ValueOf(p).MethodByName(name).Interface() on a pointer p to an
// original (interpreter-declared) type yields, in Go, a func value that calls
// the original method on p. The dependency helper cannot run that body: every
// step used to cross the bridge — ValueOf registered p's pointee as a new
// helper origin, MethodByName and Interface each minted a helper handle, and
// the call went back to the helper only to be served as a callback into the
// interpreter under the session-wide callback gate. The helper's origin table
// only grows, and every later request re-snapshots every registered pointee
// (the pointer writeback), so a loop building one such value per iteration was
// quadratic and serialized across goroutines (fixedbugs/issue27695.go).
//
// Here the chain stays with the interpreter that owns the receiver:
//
//   - reflect.ValueOf(p), p an original pointer to a non-generic original
//     type that declares methods, answers a lazy reflect.Value: a handle
//     descriptor that carries the receiver pointer instead of a helper id.
//   - Method(i)/MethodByName(name) on it select an exported original method
//     in the complete method set and answer a lazy method value.
//   - Interface() on that answers any(bound method): the same interpreter
//     closure a Go method value p.M is, bound to p's own storage, so the call
//     runs on the calling goroutine's Runner with receiver identity and
//     writes exactly as a direct method call — nothing crosses the bridge and
//     no Runner is borrowed.
//
// Everything else is materialised on use: any request that carries a lazy
// descriptor first replays the chain against the helper (ValueOf from a fresh
// encoding of the pointer, then the selection) and caches the resulting
// helper handle on the descriptor, so every other reflect operation behaves
// exactly as before. A selection this file does not own — an unexported,
// promoted or value-receiver method, an out-of-range index, a missing name —
// is also answered by the helper after materialisation. An interface result
// sent to a dependency is materialised the same way, to the helper's own
// reflected func that Interface() answered before. The session refuses a descriptor that reaches it
// unmaterialised (see [bashPPNativeSession.request]).

import (
	"context"
	"fmt"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceLocalReflect is the host-only payload of a lazy reflect.Value.
// Copies of one descriptor share it, so they materialise to one helper handle.
type goSourceLocalReflect struct {
	ptr      *bashPPPointer
	origin   uint64 // the session origin ptr was transported under
	typeName string
	// valueOf is the reflect.ValueOf request the descriptor stands for, kept
	// for replay: its selector spells the program's own import alias and its
	// source position attributes a helper panic. The pointer argument is
	// re-encoded at replay, so the helper sees the pointee as it is then.
	valueOf bashPPBridgeRequest
	// operand marks a lazily answered reflect.ValueOf of a plain value
	// (bashpp_s275_lazy_valueof.go): valueOf already carries that operand,
	// encoded when ValueOf was called, and replays as is.
	operand bool
	// parent and selection describe a method value selected from parent.
	parent    *goSourceLocalReflect
	selection bashPPBridgeRequest
	method    string
	// iface marks the Interface() result of a method value; its interpreter
	// value is the bridge value's localCell, and this descriptor only
	// replays the chain when the value is handed to the helper.
	iface bool

	mu     sync.Mutex
	handle *bashPPBridgeValue
}

// goSourceLocalReflectReplayKey marks the context of a ValueOf replay with the
// origin being replayed, so the replay is not itself answered lazily.
type goSourceLocalReflectReplayKey struct{}

// goSourceLocalReflectHandles numbers lazy descriptors for interpreter-side
// identity (map keys, equality). The range is disjoint from helper handle ids,
// which count up from one; the helper never sees these numbers.
var goSourceLocalReflectHandles atomic.Uint64

func goSourceLocalReflectHandle() uint64 {
	return 1<<62 | goSourceLocalReflectHandles.Add(1)
}

// goSourceReflectTrace, when set (tests only), observes every request this
// file forwards to the helper, keyed by selector, and every step it answers
// locally, keyed by "local:"+selector.
var goSourceReflectTrace func(step string)

// goSourceLocalReflectRequest answers the interpreter-owned steps of the
// chain, or rewrites q so that every lazy descriptor it carries is a real
// helper value. handled reports that values/err are the complete answer.
func (r *Runner) goSourceLocalReflectRequest(ctx context.Context, req bashPPEvalRequest, q *bashPPBridgeRequest) (values []bashPPBridgeValue, handled bool, err error) {
	if !r.bashPPGoSource || req.Bridge == nil {
		return nil, false, nil
	}
	if value, ok := r.goSourceLazyValueOf(ctx, req, *q); ok {
		goSourceReflectTraceStep("local:ValueOf")
		return []bashPPBridgeValue{value}, true, nil
	}
	if value, ok := r.goSourceLocalReflectValueOf(ctx, req, *q); ok {
		goSourceReflectTraceStep("local:ValueOf")
		return []bashPPBridgeValue{value}, true, nil
	}
	if q.Op == "call" && q.Receiver != nil && q.Receiver.localReflect != nil {
		lr := q.Receiver.localReflect
		switch {
		case lr.iface:
		case !lr.operand && lr.method == "" && (q.Selector == "MethodByName" || q.Selector == "Method"):
			if method, ok := r.goSourceLocalReflectSelect(lr, *q); ok {
				goSourceReflectTraceStep("local:" + q.Selector)
				value := *q.Receiver
				value.Handle = goSourceLocalReflectHandle()
				value.localReflect = &goSourceLocalReflect{ptr: lr.ptr, origin: lr.origin, typeName: lr.typeName, parent: lr, selection: bashPPBridgeRequest{Op: q.Op, Selector: q.Selector, Args: q.Args, SourceFile: q.SourceFile, SourceLine: q.SourceLine}, method: method}
				return []bashPPBridgeValue{value}, true, nil
			}
		case !lr.operand && lr.method == "" && goSourceReflectNavStep(*q):
			// Selection is eager, as it is in Go: bad indexes and field numbers
			// panic here, and an Index result remains bound to the backing array
			// selected now even if a containing slice header is later replaced.
			// The descriptor retains that one native handle; later uses refresh
			// only the original pointer storage, never replay this selection.
			selected := &goSourceLocalReflect{
				ptr: lr.ptr, origin: lr.origin, typeName: lr.typeName,
				parent:    lr,
				selection: bashPPBridgeRequest{Op: q.Op, Selector: q.Selector, Args: q.Args, SourceFile: q.SourceFile, SourceLine: q.SourceLine},
			}
			value, err := r.goSourceLocalReflectMaterialize(ctx, req, selected)
			if err != nil {
				return nil, true, err
			}
			value.localReflect = selected
			goSourceReflectTraceStep(q.Selector)
			return []bashPPBridgeValue{value}, true, nil
		case lr.method != "" && q.Selector == "Interface" && len(q.Args) == 0:
			value, err := r.goSourceLocalReflectInterface(ctx, req, lr, *q)
			if err != nil {
				return nil, true, err
			}
			goSourceReflectTraceStep("local:Interface")
			return []bashPPBridgeValue{value}, true, nil
		}
		if err := r.goSourceRefuseDetachedReflectWrite(lr, *q); err != nil {
			return nil, true, err
		}
	}
	if err := r.goSourceMaterializeLocalReflect(ctx, req, q); err != nil {
		return nil, true, err
	}
	goSourceReflectTraceStep(q.Selector)
	return nil, false, nil
}

// goSourceReflectNavStep reports the reflect.Value operations whose result is
// a selected Value. They must execute once, at the source operation, rather
// than becoming a replayable recipe: selection validates eagerly and may bind
// to storage whose containing header changes later.
func goSourceReflectNavStep(q bashPPBridgeRequest) bool {
	if q.Spread {
		return false
	}
	switch q.Selector {
	case "Elem":
		return len(q.Args) == 0
	case "Field", "Index":
		return len(q.Args) == 1 && q.Args[0].Kind == "int" && q.Args[0].localReflect == nil
	case "FieldByName":
		return len(q.Args) == 1 && q.Args[0].Kind == "string" && q.Args[0].localReflect == nil
	}
	return false
}

func goSourceReflectWrite(q bashPPBridgeRequest) bool {
	return q.Op == "call" && (strings.HasPrefix(q.Selector, "Set") || q.Selector == "Clear" || q.Selector == "Grow")
}

// A selected slice element can outlive replacement of its containing header.
// Reads remain well-defined because the worker retains the old backing array.
// A write can cross back only while some path from the original pointer still
// names that backing storage; otherwise refusing is safer than reporting a
// successful mutation of the worker's detached copy.
func (r *Runner) goSourceRefuseDetachedReflectWrite(lr *goSourceLocalReflect, q bashPPBridgeRequest) error {
	if !goSourceReflectWrite(q) || lr == nil || lr.ptr == nil {
		return nil
	}
	lr.mu.Lock()
	var storage uint64
	if lr.handle != nil {
		storage = lr.handle.Storage
	}
	lr.mu.Unlock()
	if storage == 0 {
		return nil
	}
	root, err := r.bashPPBridgePointerValue(lr.ptr)
	if err != nil {
		return err
	}
	if bridgeValueHasStorage(root, storage) {
		return nil
	}
	return fmt.Errorf("gosource: reflected write targets slice storage no longer reachable from its original pointer")
}

func bridgeValueHasStorage(v bashPPBridgeValue, storage uint64) bool {
	if v.Storage == storage {
		return true
	}
	for _, child := range v.Elements {
		if bridgeValueHasStorage(child, storage) {
			return true
		}
	}
	for _, child := range v.Fields {
		if bridgeValueHasStorage(child, storage) {
			return true
		}
	}
	for _, entry := range v.Entries {
		if bridgeValueHasStorage(entry.Key, storage) || bridgeValueHasStorage(entry.Value, storage) {
			return true
		}
	}
	return false
}

func goSourceReflectTraceStep(step string) {
	if trace := goSourceReflectTrace; trace != nil {
		trace(step)
	}
}

// goSourceLocalReflectValueOf admits reflect.ValueOf of an original pointer
// whose pointee is a plain (non-alias, non-generic) original named type with
// declared methods. The pointer must be one the session minted an origin for.
func (r *Runner) goSourceLocalReflectValueOf(ctx context.Context, req bashPPEvalRequest, q bashPPBridgeRequest) (bashPPBridgeValue, bool) {
	if q.Op != "call" || q.Receiver != nil || q.Spread || len(q.Args) != 1 {
		return bashPPBridgeValue{}, false
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if !ok || name != "ValueOf" || req.Imports[alias] != "reflect" {
		return bashPPBridgeValue{}, false
	}
	arg := q.Args[0]
	s := req.Bridge
	if arg.Kind != "pointer" || arg.Origin == 0 || arg.Session != s.id || arg.Interface != "" || len(arg.Elements) != 1 {
		return bashPPBridgeValue{}, false
	}
	// A replay of this very ValueOf must reach the helper.
	if replaying, _ := ctx.Value(goSourceLocalReflectReplayKey{}).(uint64); replaying == arg.Origin {
		return bashPPBridgeValue{}, false
	}
	typeName := strings.TrimPrefix(arg.Type, "*")
	if strings.HasPrefix(typeName, "*") {
		return bashPPBridgeValue{}, false
	}
	typeName = bashPPLocalTypeName(typeName)
	plain := false
	for _, local := range req.localTypePlan().named(typeName) {
		if !local.Alias && local.WireType == "" && local.PublicType == "" && local.GenericDecl == "" {
			plain = true
			break
		}
	}
	if !plain || len(r.goSourceLocalReflectMethodNames(typeName)) == 0 {
		return bashPPBridgeValue{}, false
	}
	ptr, _ := s.originLookup(arg.Origin)
	if ptr == nil || ptr.target == nil {
		return bashPPBridgeValue{}, false
	}
	if cell := bashPPPointerCell(ptr); cell.typeName != typeName {
		return bashPPBridgeValue{}, false
	}
	replay := q
	replay.Args = nil
	return bashPPBridgeValue{
		Kind:         "handle",
		Type:         "reflect.Value",
		NativeType:   "reflect.Value",
		Origin:       arg.Origin,
		Session:      s.id,
		Handle:       goSourceLocalReflectHandle(),
		localReflect: &goSourceLocalReflect{ptr: ptr, origin: arg.Origin, typeName: typeName, valueOf: replay},
	}, true
}

// goSourceLocalReflectMethodNames returns the exported, unambiguous original
// method set of *typeName in reflect's lexical order. Resolving every known
// original name through the ordinary selector walk preserves shadowing and
// ambiguity; no method is admitted merely because its spelling matches.
func (r *Runner) goSourceLocalReflectMethodNames(typeName string) []string {
	root := &syntax.BashPPPointerType{Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: typeName}}}
	seen := make(map[string]bool)
	var methods []string
	for _, declared := range r.bashPPMethods {
		for name := range declared {
			if seen[name] || !token.IsExported(name) {
				continue
			}
			seen[name] = true
			sel := r.bashPPResolveSelection(root, name, true, false)
			if !sel.ambiguous && sel.method != nil {
				methods = append(methods, name)
			}
		}
	}
	slices.Sort(methods)
	return methods
}

// goSourceLocalReflectSelect resolves Method(i) or MethodByName(name) against
// that full method set. The saved name is resolved again when the descriptor
// is called, retaining the exact embedded field path on the live outer value.
func (r *Runner) goSourceLocalReflectSelect(lr *goSourceLocalReflect, q bashPPBridgeRequest) (string, bool) {
	if len(q.Args) != 1 || q.Spread {
		return "", false
	}
	var method string
	switch q.Selector {
	case "MethodByName":
		if q.Args[0].Kind != "string" {
			return "", false
		}
		method = q.Args[0].Text
	case "Method":
		if q.Args[0].Kind != "int" {
			return "", false
		}
		index, err := strconv.Atoi(q.Args[0].Text)
		if err != nil {
			return "", false
		}
		exported := r.goSourceLocalReflectMethodNames(lr.typeName)
		if index < 0 || index >= len(exported) {
			return "", false
		}
		method = exported[index]
	default:
		return "", false
	}
	root := &syntax.BashPPPointerType{Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: lr.typeName}}}
	sel := r.bashPPResolveSelection(root, method, true, false)
	if !token.IsExported(method) || sel.ambiguous || sel.method == nil ||
		(!sel.method.decl.Receiver.Pointer && len(sel.edges) == 0) {
		return "", false
	}
	return method, true
}

// goSourceLocalReflectTemplates caches, per session, the helper's answer to
// Interface() on a method value of one original type and method: the func
// handle's type spellings and type token. A later Interface() on the same
// selection answers a descriptor with exactly that shape — so assertions and
// type switches see what they always saw — without crossing. The first one
// per key is materialised for real and supplies the template.
var goSourceLocalReflectTemplates sync.Map // goSourceLocalReflectKey -> bashPPBridgeValue

type goSourceLocalReflectKey struct{ session, typeName, method string }

// goSourceLocalReflectInterface answers Interface() on a lazy method value.
func (r *Runner) goSourceLocalReflectInterface(ctx context.Context, req bashPPEvalRequest, lr *goSourceLocalReflect, q bashPPBridgeRequest) (bashPPBridgeValue, error) {
	iface := &goSourceLocalReflect{ptr: lr.ptr, origin: lr.origin, typeName: lr.typeName, parent: lr, selection: bashPPBridgeRequest{Op: q.Op, Selector: q.Selector, SourceFile: q.SourceFile, SourceLine: q.SourceLine}, method: lr.method, iface: true}
	key := goSourceLocalReflectKey{session: req.Bridge.id, typeName: lr.typeName, method: lr.method}
	template, ok := goSourceLocalReflectTemplates.Load(key)
	if !ok {
		value, err := r.goSourceLocalReflectMaterialize(ctx, req, iface)
		if err != nil {
			return bashPPBridgeValue{}, err
		}
		if value.Kind != "handle" || !value.Function || value.Session != req.Bridge.id {
			// Not the reflected func this file knows how to call; hand the
			// helper's answer back unchanged.
			return value, nil
		}
		template, _ = goSourceLocalReflectTemplates.LoadOrStore(key, value)
	}
	value := template.(bashPPBridgeValue)
	value.Origin = lr.origin
	value.Handle = goSourceLocalReflectHandle()
	value.localReflect = iface
	return value, nil
}

// goSourceLocalReflectFunc is the interpreted method a reflected func
// descriptor calls: the original method bound to the receiver pointer's own
// storage, as the method value p.M is.
func (r *Runner) goSourceLocalReflectFunc(value *bashPPBridgeValue) (*bashPPFunc, bool) {
	if value == nil || value.localReflect == nil || !value.localReflect.iface || r.bashPPTools.bridge == nil || value.Session != r.bashPPTools.bridge.id {
		return nil, false
	}
	lr := value.localReflect
	root := &syntax.BashPPPointerType{Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: lr.typeName}}}
	sel := r.bashPPResolveSelection(root, lr.method, true, false)
	if sel.ambiguous || sel.method == nil {
		return nil, false
	}
	return r.bashPPBindPromotedMethod(bashPPPointerCell(lr.ptr), lr.method, sel, true)
}

// goSourceLocalReflectCall answers a call whose callee names a reflected func
// descriptor: the original method runs as an ordinary interpreted call on
// this Runner. Its results are interpreter cells; each crosses as the value
// the interpreter would send for it, carrying the cell itself for the
// native-result binders (goSourceNativeValueCell, bashPPBindNativeValue).
func (r *Runner) goSourceLocalReflectCall(call *syntax.BashPPCall) ([]bashPPBridgeValue, bool, error) {
	if !r.bashPPGoSource || call == nil || call.CalleeExpr != nil || len(call.Fun) != 1 {
		return nil, false, nil
	}
	fn, ok := r.goSourceLocalReflectFunc(r.bashPPNativeCellValue(call.Fun[0].Value))
	if !ok {
		return nil, false, nil
	}
	goSourceReflectTraceStep("local:call")
	cells, err := r.goSourceCallResultCells(call, fn)
	if err != nil {
		return nil, true, err
	}
	values := make([]bashPPBridgeValue, len(cells))
	for i, cell := range cells {
		value, err := r.bashPPBridgeCell(cell)
		if err != nil {
			return nil, true, err
		}
		value.localCell = cell
		values[i] = value
	}
	return values, true, nil
}

// goSourceLocalReflectCell is the interpreter value a call result stands for.
// Each read gets its own copy, as each read of a native result does.
func goSourceLocalReflectCell(cell *bashPPCell) *bashPPCell {
	return bashPPCopyAssignmentCell(cell)
}

// goSourceBindLocalReflectCell declares name from a call result cell.
func (r *Runner) goSourceBindLocalReflectCell(name string, cell *bashPPCell) {
	value := goSourceLocalReflectCell(cell)
	r.bashPPDeclareName(name, value.vr)
	if target := r.bashPPScope.lookup(name); target != nil {
		target.publish(value)
	}
}

// goSourceMaterializeLocalReflect rewrites every lazy descriptor q carries
// into the helper value it stands for. Values are copied on write: the
// caller's cells keep their descriptors.
func (r *Runner) goSourceMaterializeLocalReflect(ctx context.Context, req bashPPEvalRequest, q *bashPPBridgeRequest) error {
	if q.Receiver != nil {
		value, changed, err := r.goSourceMaterializeLocalReflectValue(ctx, req, *q.Receiver)
		if err != nil {
			return err
		}
		if changed {
			q.Receiver = &value
		}
	}
	var args []bashPPBridgeValue
	for i, arg := range q.Args {
		value, changed, err := r.goSourceMaterializeLocalReflectValue(ctx, req, arg)
		if err != nil {
			return err
		}
		if changed {
			if args == nil {
				args = slices.Clone(q.Args)
			}
			args[i] = value
		}
	}
	if args != nil {
		q.Args = args
	}
	return nil
}

func (r *Runner) goSourceMaterializeLocalReflectValue(ctx context.Context, req bashPPEvalRequest, v bashPPBridgeValue) (bashPPBridgeValue, bool, error) {
	if v.localReflect != nil {
		value, err := r.goSourceLocalReflectMaterialize(ctx, req, v.localReflect)
		return value, true, err
	}
	if v.localCell != nil {
		// A call result already carries the interpreter's own encoding of
		// its cell; only the host-side cell stays behind.
		v.localCell = nil
		return v, true, nil
	}
	var elements []bashPPBridgeValue
	for i, child := range v.Elements {
		value, changed, err := r.goSourceMaterializeLocalReflectValue(ctx, req, child)
		if err != nil {
			return v, false, err
		}
		if changed {
			if elements == nil {
				elements = slices.Clone(v.Elements)
			}
			elements[i] = value
		}
	}
	var fields map[string]bashPPBridgeValue
	for name, child := range v.Fields {
		value, changed, err := r.goSourceMaterializeLocalReflectValue(ctx, req, child)
		if err != nil {
			return v, false, err
		}
		if changed {
			if fields == nil {
				fields = make(map[string]bashPPBridgeValue, len(v.Fields))
				for k, f := range v.Fields {
					fields[k] = f
				}
			}
			fields[name] = value
		}
	}
	var entries []bashPPBridgeEntry
	for i, entry := range v.Entries {
		key, keyChanged, err := r.goSourceMaterializeLocalReflectValue(ctx, req, entry.Key)
		if err != nil {
			return v, false, err
		}
		value, valueChanged, err := r.goSourceMaterializeLocalReflectValue(ctx, req, entry.Value)
		if err != nil {
			return v, false, err
		}
		if keyChanged || valueChanged {
			if entries == nil {
				entries = slices.Clone(v.Entries)
			}
			entries[i] = bashPPBridgeEntry{Key: key, Value: value}
		}
	}
	if elements == nil && fields == nil && entries == nil {
		return v, false, nil
	}
	if elements != nil {
		v.Elements = elements
	}
	if fields != nil {
		v.Fields = fields
	}
	if entries != nil {
		v.Entries = entries
	}
	return v, true, nil
}

// goSourceLocalReflectMaterialize replays the descriptor's chain against the
// helper once and caches the helper handle it produced.
func (r *Runner) goSourceLocalReflectMaterialize(ctx context.Context, req bashPPEvalRequest, lr *goSourceLocalReflect) (bashPPBridgeValue, error) {
	lr.mu.Lock()
	if lr.handle != nil {
		value := *lr.handle
		lr.mu.Unlock()
		if !lr.operand && lr.ptr != nil {
			if err := r.goSourceLocalReflectRefresh(ctx, req, lr); err != nil {
				return bashPPBridgeValue{}, err
			}
		}
		return value, nil
	}
	lr.mu.Unlock()
	q := lr.selection
	if lr.operand {
		q = lr.valueOf
		ctx = context.WithValue(ctx, goSourceLazyValueOfReplayKey{}, true)
	} else if lr.parent == nil {
		arg, err := r.bashPPBridgePointerValue(lr.ptr)
		if err != nil {
			return bashPPBridgeValue{}, err
		}
		q = lr.valueOf
		q.Args = []bashPPBridgeValue{arg}
		ctx = context.WithValue(ctx, goSourceLocalReflectReplayKey{}, arg.Origin)
	} else {
		recv, err := r.goSourceLocalReflectMaterialize(ctx, req, lr.parent)
		if err != nil {
			return bashPPBridgeValue{}, err
		}
		q.Receiver = &recv
	}
	values, err := r.bashPPNativeRequest(ctx, req, q)
	if err != nil {
		return bashPPBridgeValue{}, err
	}
	if len(values) != 1 {
		return bashPPBridgeValue{}, fmt.Errorf("gosource: reflected value replay returned %d values", len(values))
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.handle == nil {
		value := values[0]
		lr.handle = &value
	}
	return *lr.handle, nil
}

// goSourceLocalReflectRefresh updates the worker's persistent storage for an
// original pointer without recreating a reflect.Value or replaying any
// selection or Call. Stable slice-storage ids make an in-place element update
// visible through retained Index values, while a replacement header binds the
// root to new backing and leaves earlier Index selections on the old backing.
func (r *Runner) goSourceLocalReflectRefresh(ctx context.Context, req bashPPEvalRequest, lr *goSourceLocalReflect) error {
	arg, err := r.bashPPBridgePointerValue(lr.ptr)
	if err != nil {
		return err
	}
	if arg.Origin == 0 || arg.Session != req.Bridge.id {
		return fmt.Errorf("gosource: reflected original pointer identity expired")
	}
	_, err = req.Bridge.request(ctx, req, bashPPBridgeRequest{
		Op: "origin-refresh", Args: []bashPPBridgeValue{arg},
	})
	if err != nil {
		return err
	}
	lr.mu.Lock()
	storage := uint64(0)
	if lr.handle != nil {
		storage = lr.handle.Storage
	}
	lr.mu.Unlock()
	if storage == 0 || bridgeValueHasStorage(arg, storage) {
		return nil
	}
	// The selected backing may have become detached from the containing slice
	// header while remaining live through another interpreter alias. Refresh
	// exactly that retained backing; never replay the selection or another
	// pointer origin.
	req.Bridge.mu.Lock()
	kept := req.Bridge.sliceOriginKeep[storage]
	var retainedView bashPPNativeSlice
	if kept != nil {
		retainedView = *kept
		// The worker persists this storage past the refresh, so its
		// region stays pinned.
		req.Bridge.pinSliceRegionLocked(storage)
	}
	req.Bridge.mu.Unlock()
	if kept == nil {
		return fmt.Errorf("gosource: reflected slice storage identity expired")
	}
	retained, err := r.bashPPBridgeCollection(retainedView.view, retainedView.meta, retainedView.typ)
	if err != nil {
		return err
	}
	_, err = req.Bridge.request(ctx, req, bashPPBridgeRequest{
		Op: "storage-refresh", Args: []bashPPBridgeValue{retained},
	})
	return err
}
