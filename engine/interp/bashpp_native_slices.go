package interp

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A captured slice header retains the original backing storage even if the
// source binding changes before a deferred call. No original expression or
// Runner is captured here.
type bashPPNativeSlice struct {
	view []any
	meta *bashPPCollectionMeta
	typ  syntax.BashPPTypeExpr
}
type bashPPNativeSliceBuffer struct {
	Index  int               `json:"index"`
	Length int               `json:"length"`
	Value  bashPPBridgeValue `json:"value"`
}

type bashPPNativeSliceRefreshKey struct {
	storage          uint64
	offset           int
	length, capacity int
}

// errNativeCopiedSliceCallback is the refusal of a dependency call that would
// keep or change a decoded copy of interpreter storage while original
// callbacks can still run.
var errNativeCopiedSliceCallback = errors.New("gosource: original callback with copied slice references is unsupported")

// nativePoolPutDroppable reports whether q is sync.Pool.Put. A pool may drop
// any item at any time without notification, and the race-enabled runtime
// does so at random, so a Put whose argument the dependency may not retain is
// answered by dropping the item: the program then observes exactly what it
// would after a collection emptied the pool.
func nativePoolPutDroppable(q bashPPBridgeRequest) bool {
	return q.Op == "call" && q.Selector == "Put" && len(q.Args) == 1 &&
		q.Receiver != nil && q.Receiver.Kind == "pointer" && q.Receiver.Type == "*sync.Pool"
}

func nativeSliceCallable(req bashPPEvalRequest, q bashPPBridgeRequest) string {
	if q.Receiver != nil {
		if q.Selector == "" {
			return q.Receiver.Callable
		}
		return q.Receiver.NativeType + "." + q.Selector
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if !ok {
		return ""
	}
	return req.Imports[alias] + "." + name
}
func nativeReaderType(name string) bool {
	switch name {
	case "*strings.Reader", "*bytes.Reader", "*bytes.Buffer", "*bufio.Reader", "*os.File":
		return true
	}
	return false
}
func nativeSliceReadIndex(req bashPPEvalRequest, q bashPPBridgeRequest) int {
	name := nativeSliceCallable(req, q)
	for _, method := range []string{".Read", ".ReadAt"} {
		if owner, ok := strings.CutSuffix(name, method); ok && nativeReaderType(owner) {
			return 0
		}
	}
	if (name == "io.ReadFull" || name == "io.ReadAtLeast") && len(q.Args) > 0 && q.Args[0].Kind == "handle" && nativeReaderType(q.Args[0].NativeType) {
		return 1
	}
	return -1
}
func nativeSliceReadOnly(name string) bool {
	switch name {
	case "fmt.Print", "fmt.Println", "fmt.Printf", "fmt.Sprint", "fmt.Sprintln", "fmt.Sprintf", "fmt.Errorf", "fmt.Fprint", "fmt.Fprintln", "fmt.Fprintf",
		"bytes.Equal", "bytes.Compare", "bytes.Contains", "bytes.Count", "bytes.HasPrefix", "bytes.HasSuffix", "bytes.Index", "bytes.IndexByte", "bytes.IndexAny", "bytes.LastIndex", "bytes.LastIndexByte", "bytes.LastIndexAny", "bytes.Clone",
		"strings.Join", "os.WriteFile", "syscall.Exec",
		// testing.Main reads its descriptor slices and retains their callbacks
		// only for the lifetime of this blocking call. The callback-capable
		// request remains their owner until the scheduler returns.
		"testing.Main",
		// slices.Equal only reads both transported slices to answer a bool; it
		// retains neither. The generic function is not a reflectable dependency
		// symbol, so nativeSliceGenericHelper computes the result interpreter-side.
		"slices.Equal", "maps.Equal",
		"*crypto/internal/fips140/sha256.Digest.Write", "*crypto/sha256.digest.Write", "crypto/sha256.Sum256", "crypto/sha1.Sum", "crypto/md5.Sum",
		"*bytes.Buffer.Write", "*bufio.Writer.Write", "*os.File.Write", "*net.TCPConn.Write", "*net.UnixConn.Write",
		// Byte-slice emitters that read the transported storage and hand back a
		// freshly allocated string/[]byte; the original slice is never retained.
		"*encoding/base64.Encoding.EncodeToString", "encoding/base64.StdEncoding.EncodeToString",
		"encoding/hex.EncodeToString", "encoding/hex.Dump",
		"regexp.Match", "*regexp.Regexp.Match", "*regexp.Regexp.Find", "*regexp.Regexp.FindAll", "*regexp.Regexp.FindIndex", "*regexp.Regexp.FindSubmatch", "*regexp.Regexp.ReplaceAll", "*regexp.Regexp.ReplaceAllFunc",
		"slices.IsSorted",
		// reflect.TypeOf reads only the argument's type: no element storage is
		// retained or written, and no mirrored method is invoked.
		"reflect.TypeOf",
		// reflect.DeepEqual walks both transported values and answers a bool;
		// it retains neither, writes nothing and invokes no method.
		"reflect.DeepEqual",
		// Structural value emitters — the marshalers walk the transported value
		// tree and allocate their own output. Element storage is read only.
		"encoding/json.Marshal", "encoding/json.MarshalIndent", "encoding/json/v2.Marshal",
		"encoding/xml.Marshal", "encoding/xml.MarshalIndent",
		// The structural decoders read the transported input bytes once and
		// build their own result; the input storage is neither retained nor
		// written. Their output travels through the pointer writeback.
		"encoding/json.Unmarshal", "encoding/json/v2.Unmarshal", "encoding/xml.Unmarshal":
		return true
	}
	return false
}

// nativeSliceRetainsStorage reports constructors whose result keeps the
// supplied slice as backing storage after the call returns. The bridge only
// transports a copy, so admitting these calls would silently sever Go's alias
// relationship between the original slice and the returned object.
func nativeSliceRetainsStorage(name string) bool {
	switch name {
	case "bytes.NewBuffer", "bytes.NewReader":
		return true
	}
	return false
}

// nativeSliceMutatingIndex reports the argument that an in-place collection
// operation reorders or rewrites through the transported backing storage, or
// -1 when the callable does not mutate a slice argument. The dependency sorts
// its own decoded copy of the elements; the writeback protocol then copies the
// reordered elements back into the interpreter's original backing array so
// aliases observe the change, exactly as native Go would. The concrete
// sort.Ints/Strings/Float64s sorters cross to the dependency as reflected
// symbols; the generic slices.Sort is not a reflectable symbol, so it shares
// this mutation writeback but has its reordering computed interpreter-side by
// nativeSliceGenericHelper before the request would reach the dependency.
// fmtReachesCallbacks reports whether a fmt formatting walk over the request's
// transported values can invoke an original body: a value carrying retained
// callbacks, or a local type mirroring one of the formatting protocol methods
// fmt looks up. Every local type is consulted, not only the ones the argument
// spellings name — an embedded local type's protocol method is promoted onto
// its outer type in the helper exactly as in Go. A method the mirror omitted
// counts too: fmt then formats the value raw, and refusing keeps that
// divergence from silently printing.
func fmtReachesCallbacks(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	var retained func(bashPPBridgeValue) bool
	retained = func(v bashPPBridgeValue) bool {
		if v.Callbacks {
			return true
		}
		for _, c := range v.Elements {
			if retained(c) {
				return true
			}
		}
		for _, c := range v.Fields {
			if retained(c) {
				return true
			}
		}
		for _, e := range v.Entries {
			if retained(e.Key) || retained(e.Value) {
				return true
			}
		}
		return false
	}
	for _, v := range q.Args {
		if retained(v) {
			return true
		}
	}
	if q.Receiver != nil && retained(*q.Receiver) {
		return true
	}
	return req.localTypePlan().fmtFormatting
}

// fmtProtocolMethod names the methods fmt's verbs look up on an operand:
// fmt.Formatter, fmt.GoStringer, error and fmt.Stringer.
func fmtProtocolMethod(name string) bool {
	switch name {
	case "Format", "GoString", "Error", "String":
		return true
	}
	return false
}

func nativeSliceMutatingIndex(name string) int {
	switch name {
	case "sort.Ints", "sort.Strings", "sort.Float64s", "slices.Sort",
		"slices.Reverse",
		// slices.SortFunc/SortStableFunc reorder in place through the original
		// comparison callback; goSourceSlicesSortFunc computes the ordering
		// interpreter-side and rides this same writeback.
		"slices.SortFunc", "slices.SortStableFunc":
		return 0
	}
	return -1
}

func prepareNativeSliceBuffers(ctx context.Context, req bashPPEvalRequest, q *bashPPBridgeRequest) error {
	if q.Op != "call" {
		return nil
	}
	q.SliceBuffers = nil
	q.sliceTargets = nil
	hasSlice := false
	hasDirectSlice := false
	seenSlices := map[bashPPNativeSliceRefreshKey]bool{}
	var refresh func(*bashPPBridgeValue) error
	refresh = func(v *bashPPBridgeValue) error {
		if capture := v.sliceView; capture != nil {
			hasSlice = true
			key := bashPPNativeSliceRefreshKey{
				storage:  v.Storage,
				offset:   v.Offset,
				length:   v.Length,
				capacity: v.Capacity,
			}
			if key.storage != 0 {
				if seenSlices[key] {
					return nil
				}
				seenSlices[key] = true
			}
			if req.CallbackOwner == nil {
				return fmt.Errorf("gosource: original slice has no request owner")
			}
			current, err := req.CallbackOwner.bashPPBridgeCollection(capture.view, capture.meta, capture.typ)
			if err != nil {
				return err
			}
			// The refreshed cells can carry adopted original handles or lazy
			// reflect values even though the initial argument was materialized.
			// Normalize the new snapshot before it becomes transport data.
			current, _, err = req.CallbackOwner.goSourceMaterializeLocalReflectValue(ctx, req, current)
			if err != nil {
				return err
			}
			*v = current
		}
		for i := range v.Elements {
			if err := refresh(&v.Elements[i]); err != nil {
				return err
			}
		}
		for name, field := range v.Fields {
			if err := refresh(&field); err != nil {
				return err
			}
			v.Fields[name] = field
		}
		for i := range v.Entries {
			if err := refresh(&v.Entries[i].Key); err != nil {
				return err
			}
			if err := refresh(&v.Entries[i].Value); err != nil {
				return err
			}
		}
		return nil
	}
	for i := range q.Args {
		hasDirectSlice = hasDirectSlice || q.Args[i].sliceView != nil
		if err := refresh(&q.Args[i]); err != nil {
			return err
		}
	}
	if !hasSlice {
		return nil
	}
	// reflect.Value.Call reads a slice of dependency-owned Value handles. The
	// slice itself is an interpreter allocation, but neither the callee nor the
	// reflected original function can retain or mutate that argument slice.
	if reflectedOriginalFunctionUse(*q) && (q.Selector == "Call" || q.Selector == "CallSlice") && len(q.Args) == 1 && q.Args[0].Kind == "slice" {
		nativeValues := true
		for _, v := range q.Args[0].Elements {
			if v.Kind != "handle" || v.NativeType != "reflect.Value" {
				nativeValues = false
				break
			}
		}
		if nativeValues {
			return nil
		}
	}
	// The refusal below guards a dependency that would RETAIN or MUTATE a
	// decoded copy of interpreter storage while an original callback runs. Two
	// kinds of callable are outside that hazard and are admitted with their
	// callbacks: one this Runner answers itself, which never sends the storage
	// anywhere, and a read-only emitter, which walks the transported tree once
	// and allocates its own output. The latter is the same footing fmt's
	// formatting entries already stand on, and they are in that set.
	//
	// A direct original slice under fmt is the one read-only case that stays
	// refused: a formatting protocol method the walk invokes could write the
	// interpreter's storage while fmt is still reading its decoded copy, and
	// the copy would then print stale. That hazard needs a callback fmt can
	// actually reach — String, Error, Format or GoString. A local type whose
	// mirrored methods all lie outside that protocol (a generic set type's
	// Equal, say) never re-enters the interpreter from a format walk, so its
	// slices print on the same footing as any other read-only emitter.
	//
	// A callee that may RETAIN the storage past the call is admitted only by
	// an exclusive transfer (bashpp_native_transfer.go): the decoded slice
	// then is the storage of record and the interpreter's binding is rebound
	// to it, so nothing is copied for the callee to hold.
	if callable := nativeSliceCallable(req, *q); requestHasCallbacks(req, *q) &&
		!goSourceInterpretedCallable(callable) &&
		(!nativeSliceReadOnly(callable) || hasDirectSlice && strings.HasPrefix(callable, "fmt.") && fmtReachesCallbacks(req, *q)) {
		if !nativeSliceReadOnly(callable) && nativeSliceTransferable(req, q) {
			return nil
		}
		// A slice reachable only through an origin-bearing pointer handed to a
		// reviewed methods-driven consumer is not a detached copy: the origin
		// pointee is reconciled after every callback and on return
		// (bashpp_native_transfer.go), so the callbacks and the consumer read
		// one storage. validateLocalTransport still rules on the request.
		if hasSlice && !hasDirectSlice && nativeSharedReferenceConsumer(req, *q) {
			return nil
		}
		// ValueOf over an authenticated original pointer creates the worker
		// storage that goSourceLocalReflect refreshes on later uses. A nested
		// slice is part of that origin rather than an untracked direct copy.
		// Keep the admission proof-based: the coherence collector must resolve
		// the argument to that re-readable origin and reject malformed or
		// unauthenticated pointer graphs.
		if hasSlice && !hasDirectSlice && reflectedMethodValueOf(req, *q) && prepareNativeCopyCoherence(req, q) {
			return nil
		}
		// A read-only dependency handed a direct original slice can keep the
		// copy live across synchronous callbacks. Register it as a normal slice
		// buffer; each callback first publishes the dependency's current backing
		// contents to the interpreter and then refreshes that same backing from
		// the interpreter before the dependency resumes. This is especially
		// important for a spread variadic slice: its elements remain aliased to
		// the caller, so a formatting callback may mutate a later argument.
		if nativeSliceReadOnly(callable) && hasDirectSlice {
			if err := prepareNativeSliceReconcile(req, q); err == nil {
				q.sliceCallbackSync = true
				return nil
			}
		}
		// A reviewed synchronous function-callback consumer (for example
		// testing.T.Run) may keep the callback until the request's parked
		// goroutine runs it, but the request does not return until that body is
		// done. Synchronize every direct copied slice before and after each
		// callback, so the dependency's temporary copy and the interpreter's
		// backing array behave as one logical store for the bounded call.
		if synchronousFunctionCallback(req, *q) && hasDirectSlice {
			if err := prepareNativeSliceReconcile(req, q); err == nil {
				q.sliceCallbackSync = true
				return nil
			}
		}
		// A read-only emitter reaching a formatting callback is admitted when
		// every argument's live storage can be re-read: each callback is then
		// followed by a coherence check against the dependency's copy
		// (bashpp_native_coherence.go), and a write the copy cannot observe
		// fails the callback instead of printing stale.
		if nativeSliceReadOnly(callable) && prepareNativeCopyCoherence(req, q) {
			return nil
		}
		return fmt.Errorf("%w (calling %s)", errNativeCopiedSliceCallback, callable)
	}
	if nativeSliceCallable(req, *q) == "*text/template.Template.Execute" {
		// A configured template may call arbitrary registered functions. Permit
		// only inspected function-free trees over primitive string slices.
		if q.Receiver == nil || q.Selector != "Execute" || len(q.Args) != 2 || q.Args[1].Type != "[]string" || q.Args[1].Kind != "slice" {
			return fmt.Errorf("gosource: template slice transport requires direct primitive string data")
		}
		for _, element := range q.Args[1].Elements {
			if element.Kind != "string" || element.Type != "string" {
				return fmt.Errorf("gosource: template slice elements must be primitive strings")
			}
		}
		values, err := req.CallbackOwner.bashPPNativeRequest(req.CallbackOwner.ectx, req, bashPPBridgeRequest{Op: "template-readonly", Receiver: q.Receiver})
		if err != nil {
			return err
		}
		if len(values) != 1 || values[0].Kind != "bool" || values[0].Text != "true" {
			return fmt.Errorf("gosource: template with function or template callbacks cannot receive original slice storage")
		}
		return nil
	}
	name := nativeSliceCallable(req, *q)
	if mut := nativeSliceMutatingIndex(name); mut >= 0 {
		if name == "slices.Reverse" && (mut >= len(q.Args) || q.Args[mut].sliceView == nil) {
			// Dependency-owned and nil slices stay on the normal native path.
			return nil
		}
		return prepareNativeSliceMutation(req, q, mut)
	}
	if nativePointerWritebackAllowed(req, *q) || nativeRetainedPointerMutator(name) {
		return nil
	}
	index := nativeSliceReadIndex(req, *q)
	if index < 0 {
		if nativeSliceRetainsStorage(name) {
			return fmt.Errorf("gosource: native slice retention or mutation is unsupported for %s", name)
		}
		// An interpreter-computed helper that does not mutate reads the
		// transported elements and allocates its own answer.
		if nativeSliceReadOnly(name) || goSourceInterpretedCallable(name) {
			return nil
		}
		return prepareNativeSliceReconcile(req, q)
	}
	if index >= len(q.Args) || q.Args[index].sliceView == nil {
		return fmt.Errorf("gosource: native Read requires a direct original byte slice")
	}
	target := q.Args[index].sliceView
	collection, ok := req.CallbackOwner.bashPPUnderlyingType(target.typ).(*syntax.BashPPCollectionType)
	if !ok || collection.Kind != "slice" {
		return fmt.Errorf("gosource: native Read buffer has no slice identity")
	}
	element := bashPPTypeText(req.CallbackOwner.bashPPUnderlyingType(collection.Element))
	if element != "byte" && element != "uint8" {
		return fmt.Errorf("gosource: native Read requires a byte slice")
	}
	full, err := req.CallbackOwner.bashPPBridgeCollection(target.view[:cap(target.view)], target.meta, target.typ)
	if err != nil {
		return err
	}
	// Capacity outside the visible length may still use the collection layer's
	// lazy nil slots. For byte storage those slots are Go's zero bytes.
	for i := range full.Elements {
		if full.Elements[i].Kind == "nil" {
			full.Elements[i] = bashPPBridgeValue{Kind: "uint", Type: "uint8", Text: "0"}
		}
	}
	q.SliceBuffers = []bashPPNativeSliceBuffer{{Index: index, Length: len(target.view), Value: full}}
	q.sliceTargets = []*bashPPNativeSlice{target}
	q.sliceMutating = []bool{false}
	q.sliceElem = []syntax.BashPPTypeExpr{collection.Element}
	return nil
}

// prepareNativeSliceMutation sets up the transport for a concrete in-place
// mutator (sort.Ints/Strings/Float64s). Only the visible length crosses the
// boundary; the dependency reorders its decoded copy and the writeback copies
// the reordered elements back into the interpreter's original backing array so
// aliasing slices observe the same reordering, as native Go does.
func prepareNativeSliceMutation(req bashPPEvalRequest, q *bashPPBridgeRequest, index int) error {
	if requestHasCallbacks(req, *q) && !goSourceInterpretedCallable(nativeSliceCallable(req, *q)) {
		return fmt.Errorf("gosource: %s cannot mutate a slice carrying original callbacks", nativeSliceCallable(req, *q))
	}
	if index >= len(q.Args) || q.Args[index].sliceView == nil {
		return fmt.Errorf("gosource: %s requires a direct original slice", nativeSliceCallable(req, *q))
	}
	target := q.Args[index].sliceView
	collection, ok := req.CallbackOwner.bashPPUnderlyingType(target.typ).(*syntax.BashPPCollectionType)
	if !ok || collection.Kind != "slice" {
		return fmt.Errorf("gosource: native mutation buffer has no slice identity")
	}
	visible, err := req.CallbackOwner.bashPPBridgeCollection(target.view, target.meta, target.typ)
	if err != nil {
		return err
	}
	if visible.Kind != "slice" {
		return fmt.Errorf("gosource: native mutation buffer is not a slice")
	}
	q.SliceBuffers = []bashPPNativeSliceBuffer{{Index: index, Length: len(target.view), Value: visible}}
	q.sliceTargets = []*bashPPNativeSlice{target}
	q.sliceMutating = []bool{true}
	q.sliceElem = []syntax.BashPPTypeExpr{collection.Element}
	return nil
}

// nestedNativeSliceView reports an original slice view carried inside another
// value — an element, field or entry — where the buffer writeback cannot reach
// its storage.
func nestedNativeSliceView(v bashPPBridgeValue) bool {
	for _, e := range v.Elements {
		if e.sliceView != nil || nestedNativeSliceView(e) {
			return true
		}
	}
	for _, e := range v.Fields {
		if e.sliceView != nil || nestedNativeSliceView(e) {
			return true
		}
	}
	for _, e := range v.Entries {
		if e.Key.sliceView != nil || nestedNativeSliceView(e.Key) {
			return true
		}
		if e.Value.sliceView != nil || nestedNativeSliceView(e.Value) {
			return true
		}
	}
	return false
}

// prepareNativeSliceReconcile transports every direct original slice argument
// as a registered buffer and lets the observed call behaviour decide its
// class: a read-only consumer hands the elements back unchanged and nothing is
// written back, while an in-place mutator's changes are written back over the
// visible length, so aliases observe them as native Go would. Storage the
// writeback cannot reach — a slice view nested inside another value, or one
// whose declared type has no slice identity — keeps the retention refusal.
func prepareNativeSliceReconcile(req bashPPEvalRequest, q *bashPPBridgeRequest) error {
	name := nativeSliceCallable(req, *q)
	refuse := fmt.Errorf("gosource: native slice retention or mutation is unsupported for %s", name)
	if q.Receiver != nil && (q.Receiver.sliceView != nil || nestedNativeSliceView(*q.Receiver)) {
		return refuse
	}
	for i := range q.Args {
		if nestedNativeSliceView(q.Args[i]) {
			return refuse
		}
		target := q.Args[i].sliceView
		if target == nil {
			continue
		}
		collection, ok := req.CallbackOwner.bashPPUnderlyingType(target.typ).(*syntax.BashPPCollectionType)
		if !ok || collection.Kind != "slice" {
			return refuse
		}
		visible, err := req.CallbackOwner.bashPPBridgeCollection(target.view, target.meta, target.typ)
		if err != nil {
			return err
		}
		if visible.Kind != "slice" {
			return refuse
		}
		// Visible length may still hold the collection layer's lazy nil slots
		// (make([]T, n) before assignment). For scalar storage those slots are
		// Go's zero values.
		elem := bashPPTypeText(req.CallbackOwner.bashPPUnderlyingType(collection.Element))
		for j := range visible.Elements {
			if visible.Elements[j].Kind == "nil" {
				if zero, ok := bridgeZeroScalar(elem); ok {
					visible.Elements[j] = zero
				}
			}
		}
		q.SliceBuffers = append(q.SliceBuffers, bashPPNativeSliceBuffer{Index: i, Length: len(target.view), Value: visible})
		q.sliceTargets = append(q.sliceTargets, target)
		q.sliceMutating = append(q.sliceMutating, true)
		q.sliceElem = append(q.sliceElem, collection.Element)
		q.sliceReconcile = append(q.sliceReconcile, true)
	}
	return nil
}

// bridgeZeroScalar is the transported zero value for a scalar element type,
// filling the collection layer's lazy nil slots the way Go zero-initialises
// backing storage. A non-scalar element reports false and keeps its nil slot.
func bridgeZeroScalar(elem string) (bashPPBridgeValue, bool) {
	switch elem {
	case "int", "int8", "int16", "int32", "int64", "rune":
		return bashPPBridgeValue{Kind: "int", Type: elem, Text: "0"}, true
	case "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte":
		return bashPPBridgeValue{Kind: "uint", Type: elem, Text: "0"}, true
	case "float32", "float64":
		return bashPPBridgeValue{Kind: "float", Type: elem, Text: "0"}, true
	case "complex64", "complex128":
		return bashPPBridgeValue{Kind: "complex", Type: elem, Text: "(0+0i)"}, true
	case "string":
		return bashPPBridgeValue{Kind: "string", Type: elem, Text: "", Bytes: []byte{}}, true
	case "bool":
		return bashPPBridgeValue{Kind: "bool", Type: elem, Text: "false"}, true
	}
	return bashPPBridgeValue{}, false
}

// bridgeValueUnchanged reports whether the worker handed a value back exactly
// as sent. Only value content is compared: the worker's snapshot spells types
// through its own reflect view and mints fresh handle ids, so type spellings
// do not participate and a handle only matches its own id.
func bridgeValueUnchanged(a, b bashPPBridgeValue) bool {
	if a.Kind != b.Kind || a.Handle != b.Handle {
		return false
	}
	if a.Kind == "string" {
		if a.stringText() != b.stringText() {
			return false
		}
	} else if a.Text != b.Text {
		return false
	}
	if !bridgeElementsUnchanged(a.Elements, b.Elements) {
		return false
	}
	if len(a.Fields) != len(b.Fields) {
		return false
	}
	for name, av := range a.Fields {
		bv, ok := b.Fields[name]
		if !ok || !bridgeValueUnchanged(av, bv) {
			return false
		}
	}
	if len(a.Entries) != len(b.Entries) {
		return false
	}
	for i := range a.Entries {
		if !bridgeValueUnchanged(a.Entries[i].Key, b.Entries[i].Key) || !bridgeValueUnchanged(a.Entries[i].Value, b.Entries[i].Value) {
			return false
		}
	}
	return true
}
func bridgeElementsUnchanged(sent, got []bashPPBridgeValue) bool {
	if len(sent) != len(got) {
		return false
	}
	for i := range sent {
		if !bridgeValueUnchanged(sent[i], got[i]) {
			return false
		}
	}
	return true
}

func applyNativeSliceBuffers(runner *Runner, q bashPPBridgeRequest, reply bashPPBridgeResponse) error {
	if len(reply.SliceUpdates) == 0 && reply.Error != "" {
		return nil
	} // rejected before invoking the dependency
	if len(reply.SliceUpdates) != len(q.SliceBuffers) {
		return fmt.Errorf("gosource: native slice writeback count mismatch")
	}
	type sliceWriteback struct {
		values   []any
		metas    []*bashPPCollectionMeta
		length   int  // visible length written; the byte path writes cap
		mutating bool // reorder the visible length vs fill up to capacity
	}
	updates := make([]sliceWriteback, len(reply.SliceUpdates))
	for i, wire := range reply.SliceUpdates {
		target := q.sliceTargets[i]
		mutating := i < len(q.sliceMutating) && q.sliceMutating[i]
		want := cap(target.view)
		if mutating {
			// The registered buffer is the operation's visible slice header.
			// A refreshed target may expose more of the same backing store, but
			// a sort must neither require nor overwrite that tail.
			want = q.SliceBuffers[i].Length
		}
		if wire.Index != q.SliceBuffers[i].Index || wire.Length != q.SliceBuffers[i].Length || wire.Value.Kind != "slice" || len(wire.Value.Elements) != want {
			return fmt.Errorf("gosource: invalid native slice writeback shape")
		}
		// A reconciled buffer's class is decided here by what the call actually
		// did: elements handed back unchanged mean a read-only consumer, and
		// the interpreter's storage — including element identity — is left
		// untouched.
		if i < len(q.sliceReconcile) && q.sliceReconcile[i] && bridgeElementsUnchanged(q.SliceBuffers[i].Value.Elements, wire.Value.Elements) {
			continue
		}
		values := make([]any, len(wire.Value.Elements))
		metas := make([]*bashPPCollectionMeta, len(wire.Value.Elements))
		if mutating {
			// Rebuild the reordered elements at the slice's declared element
			// type. Structured metadata (per-element identity) rides along so a
			// mutated element remains a full interpreter value.
			for j, v := range wire.Value.Elements {
				value, meta, err := runner.bashPPBridgeContents(v, q.sliceElem[i])
				if err != nil {
					return fmt.Errorf("gosource: invalid native slice writeback: %w", err)
				}
				values[j], metas[j] = value, bashPPBridgeNestedMeta(meta)
			}
		} else {
			for j, v := range wire.Value.Elements {
				n, err := strconv.ParseUint(v.Text, 10, 8)
				if err != nil || (v.Kind != "uint" && v.Kind != "int") {
					return fmt.Errorf("gosource: invalid byte in native slice writeback")
				}
				values[j] = int(n)
			}
		}
		updates[i] = sliceWriteback{values: values, metas: metas, length: want, mutating: mutating}
	}
	for i, update := range updates {
		target := q.sliceTargets[i]
		copy(target.view[:update.length], update.values)
		if update.mutating && target.meta != nil && len(target.meta.sequence) >= update.length {
			for j := 0; j < update.length; j++ {
				target.meta.sequence[j] = update.metas[j]
			}
		}
	}
	return nil
}

// nativeCallbackSliceBuffers snapshots the interpreter side of every direct
// slice registered on an in-flight callback-capable request. The dependency
// applies these elements to its existing backing array before returning from
// the callback, preserving the header and every alias it has already handed
// to the callee.
func nativeCallbackSliceBuffers(runner *Runner, q bashPPBridgeRequest) ([]bashPPNativeSliceBuffer, error) {
	if !q.sliceCallbackSync {
		return nil, nil
	}
	if runner == nil || len(q.sliceTargets) != len(q.SliceBuffers) {
		return nil, fmt.Errorf("gosource: callback slice ownership was lost")
	}
	buffers := make([]bashPPNativeSliceBuffer, len(q.SliceBuffers))
	for i, target := range q.sliceTargets {
		value, err := runner.bashPPBridgeCollection(target.view, target.meta, target.typ)
		if err != nil {
			return nil, err
		}
		if value.Kind != "slice" || len(value.Elements) != len(target.view) {
			return nil, fmt.Errorf("gosource: callback slice refresh has invalid shape")
		}
		buffers[i] = bashPPNativeSliceBuffer{Index: q.SliceBuffers[i].Index, Length: len(target.view), Value: value}
	}
	return buffers, nil
}

// nativeSliceGenericHelper answers the two generic slices helpers the dependency
// worker cannot invoke: an uninstantiated generic function is not a reflectable
// imported symbol, so slices.Equal/slices.Sort never resolve there. Both operate
// over primitive element values that already crossed the collection transport, so
// the interpreter computes them directly. slices.Equal reads both transported
// slices and answers a bool (the read-only path); slices.Sort reorders the
// visible elements and rides the existing mutation writeback, so every aliasing
// header observes the reordering exactly as the concrete sort.Strings path does.
// handled is false for every other callable, leaving the normal dependency
// dispatch untouched.
func (r *Runner) nativeSliceGenericHelper(ctx context.Context, req bashPPEvalRequest, q *bashPPBridgeRequest) (values []bashPPBridgeValue, handled bool, err error) {
	if q.Op != "call" {
		return nil, false, nil
	}
	if q.Receiver == nil {
		alias, _, ok := strings.Cut(q.Selector, ".")
		if !ok {
			return nil, false, nil
		}
		pkg := req.Imports[alias]
		if pkg != "slices" && pkg != "maps" && pkg != "cmp" {
			return nil, false, nil
		}
	}
	switch nativeSliceCallable(req, *q) {
	case "slices.Collect":
		values, err := r.goSourceSlicesCollect(ctx, req, q.Args)
		return values, true, err
	case "slices.Equal":
		if len(q.Args) != 2 {
			return nil, false, fmt.Errorf("gosource: slices.Equal requires two slices")
		}
		equal, err := nativeSlicePrimitiveEqual(q.Args[0], q.Args[1])
		if err != nil {
			return nil, false, err
		}
		return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: strconv.FormatBool(equal)}}, true, nil
	case "maps.Equal":
		if len(q.Args) != 2 {
			return nil, true, fmt.Errorf("gosource: maps.Equal requires two maps")
		}
		equal, err := nativeMapPrimitiveEqual(q.Args[0], q.Args[1])
		if err != nil {
			return nil, true, err
		}
		return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: strconv.FormatBool(equal)}}, true, nil
	case "slices.Sort":
		if len(q.SliceBuffers) != 1 {
			return nil, false, fmt.Errorf("gosource: slices.Sort requires a direct original slice")
		}
		// slices.Sort always writes its visible elements back in place. Keep
		// that classification with the interpreter-side generic helper: unlike
		// the reflect-dispatched concrete sorters, it never reaches a worker
		// reply that would otherwise establish it.
		q.sliceMutating = []bool{true}
		elements := q.SliceBuffers[0].Value.Elements
		if len(elements) < q.SliceBuffers[0].Length {
			return nil, false, fmt.Errorf("gosource: slices.Sort buffer is shorter than its visible length")
		}
		sorted, err := nativeSliceSorted(elements[:q.SliceBuffers[0].Length])
		if err != nil {
			return nil, false, err
		}
		reply := bashPPBridgeResponse{SliceUpdates: []bashPPNativeSliceBuffer{{
			Index:  q.SliceBuffers[0].Index,
			Length: q.SliceBuffers[0].Length,
			Value:  bashPPBridgeValue{Kind: "slice", Elements: sorted},
		}}}
		if err := applyNativeSliceBuffers(r, *q, reply); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	case "slices.Reverse":
		if len(q.Args) != 1 {
			return nil, true, fmt.Errorf("gosource: slices.Reverse requires one slice")
		}
		if q.Args[0].Kind == "nil" {
			return nil, true, nil
		}
		if q.Args[0].sliceView == nil {
			return nil, false, nil
		}
		shared, ok := r.goSourceSharedSliceOf(q.Args[0])
		if !ok {
			return nil, true, fmt.Errorf("gosource: slices.Reverse requires an original slice")
		}
		for i, j := 0, len(shared.view)-1; i < j; i, j = i+1, j-1 {
			shared.view[i], shared.view[j] = shared.view[j], shared.view[i]
			if shared.meta != nil {
				shared.meta.sequence[i], shared.meta.sequence[j] = shared.meta.sequence[j], shared.meta.sequence[i]
			}
		}
		return nil, true, nil
	case "slices.IsSorted":
		if len(q.Args) != 1 {
			return nil, false, fmt.Errorf("gosource: slices.IsSorted requires one slice")
		}
		elements, err := nativeSliceElements(q.Args[0])
		if err != nil {
			return nil, false, err
		}
		sorted, err := nativeSliceIsSorted(elements)
		if err != nil {
			return nil, false, err
		}
		return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: strconv.FormatBool(sorted)}}, true, nil
	}
	// The generic callback-taking helpers share this interpreter-side dispatch:
	// slices.SortFunc and friends are uninstantiated generic functions too.
	return r.goSourceGenericCallbackHelper(ctx, req, q)
}

func nativeMapPrimitiveEqual(a, b bashPPBridgeValue) (bool, error) {
	if a.Kind == "nil" && b.Kind == "nil" {
		return true, nil
	}
	if a.Kind != "map" || b.Kind != "map" {
		return false, fmt.Errorf("gosource: maps.Equal requires map operands, got %s and %s", a.Kind, b.Kind)
	}
	if len(a.Entries) != len(b.Entries) {
		return false, nil
	}
	for _, left := range a.Entries {
		found := false
		for _, right := range b.Entries {
			keysEqual, err := nativeSliceScalarEqual(left.Key, right.Key)
			if err != nil {
				return false, err
			}
			if !keysEqual {
				continue
			}
			valuesEqual, err := nativeSliceScalarEqual(left.Value, right.Value)
			if err != nil {
				return false, err
			}
			if !valuesEqual {
				return false, nil
			}
			found = true
			break
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

func (r *Runner) goSourceSlicesCollect(ctx context.Context, req bashPPEvalRequest, args []bashPPBridgeValue) ([]bashPPBridgeValue, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("gosource: slices.Collect requires one current iterator callback")
	}
	iterator, err := r.goSourceSlicesCollectIterator(req, args[0])
	if err != nil {
		return nil, err
	}
	yieldType, err := r.goSourceIteratorYield(iterator)
	if err != nil {
		return nil, fmt.Errorf("gosource: slices.Collect: %w", err)
	}
	yieldParams := bashppParams(yieldType.Params)
	if len(yieldParams) != 1 {
		return nil, fmt.Errorf("gosource: slices.Collect iterator must yield one value")
	}
	var collected []bashPPBridgeValue
	yield := &bashPPFunc{
		collectYield: &collected,
		lit:          &syntax.BashPPFuncLit{Params: yieldType.Params, Results: yieldType.Results},
		scope:        r.bashPPScope,
	}
	vr := r.bashPPStoreFunc(yield)
	savedResults, savedCalls := r.bashPPResultCells, r.bashPPCallCells
	defer func() { r.bashPPResultCells, r.bashPPCallCells = savedResults, savedCalls }()
	r.bashPPCallCells = []*bashPPCell{{vr: vr, declType: yieldType}}
	failure := r.bashPPShortFailureSeq
	r.bashPPInvoke(ctx, iterator, []string{vr.Str})
	if r.bashPPPanicking() || r.exit.exiting || r.exit.fatalExit || r.exit.err != nil || r.bashPPShortFailureSeq != failure {
		return nil, errBashPPScalarInterrupted
	}
	if len(collected) == 0 {
		return []bashPPBridgeValue{{Kind: "nil", Type: "[]" + bashPPTypeText(yieldParams[0].typ)}}, nil
	}
	return []bashPPBridgeValue{{
		Kind:     "slice",
		Type:     "[]" + bashPPTypeText(yieldParams[0].typ),
		Length:   len(collected),
		Capacity: len(collected),
		Elements: collected,
	}}, nil
}

func (r *Runner) goSourceSlicesCollectIterator(req bashPPEvalRequest, arg bashPPBridgeValue) (*bashPPFunc, error) {
	if req.Bridge == nil {
		return nil, fmt.Errorf("gosource: slices.Collect requires one current iterator callback")
	}
	if arg.Kind == "callback" && arg.Session == req.Bridge.id {
		req.Bridge.mu.Lock()
		iterator := req.Bridge.functions[arg.Handle]
		req.Bridge.mu.Unlock()
		if iterator != nil {
			return iterator, nil
		}
	}
	if arg.Kind == "handle" {
		if signature, ok := r.bashPPBridgeFuncType(arg); ok {
			native := arg
			native.Callable = "range-iterator"
			return &bashPPFunc{native: &native, lit: &syntax.BashPPFuncLit{Params: signature.Params, Results: signature.Results}}, nil
		}
	}
	return nil, fmt.Errorf("gosource: slices.Collect requires one current iterator callback")
}

func (r *Runner) bashPPBridgeFuncType(value bashPPBridgeValue) (*syntax.BashPPFuncType, bool) {
	for _, text := range []string{value.Type, value.NativeType} {
		if text == "" {
			continue
		}
		typ := syntax.BashPPTypeExprFromText(text)
		if typ == nil {
			continue
		}
		if r != nil {
			typ = r.bashPPUnderlyingType(typ)
		}
		if signature, ok := typ.(*syntax.BashPPFuncType); ok {
			return signature, true
		}
	}
	return nil, false
}

// nativeSliceElements returns the visible element values of a transported
// slice, treating a nil slice as empty so slices.Equal(nil, []T{}) is true as
// native Go reports. Interpreter-owned slices carry Elements through Capacity
// so mutation writeback can preserve aliases, but read-only helpers must stop
// at Length. Older synthetic carriers have no header and keep their historical
// Elements-only meaning.
func nativeSliceElements(v bashPPBridgeValue) ([]bashPPBridgeValue, error) {
	switch v.Kind {
	case "slice":
		hasHeader := v.Storage != 0 || v.Offset != 0 || v.Length != 0 || v.Capacity != 0
		if !hasHeader {
			return v.Elements, nil
		}
		if v.Offset < 0 || v.Length < 0 || v.Capacity < v.Length || v.Capacity != len(v.Elements) {
			return nil, fmt.Errorf("gosource: malformed slice header offset=%d len=%d cap=%d elements=%d", v.Offset, v.Length, v.Capacity, len(v.Elements))
		}
		return v.Elements[:v.Length], nil
	case "nil":
		return nil, nil
	}
	return nil, fmt.Errorf("gosource: slices.Equal requires slice operands, got %s", v.Kind)
}

// nativeSlicePrimitiveEqual compares two transported slices element by element
// over the primitive element kinds. Non-primitive elements are rejected rather
// than silently mishandled, keeping the helper fail-closed.
func nativeSlicePrimitiveEqual(a, b bashPPBridgeValue) (bool, error) {
	left, err := nativeSliceElements(a)
	if err != nil {
		return false, err
	}
	right, err := nativeSliceElements(b)
	if err != nil {
		return false, err
	}
	if len(left) != len(right) {
		return false, nil
	}
	for i := range left {
		equal, err := nativeSliceScalarEqual(left[i], right[i])
		if err != nil {
			return false, err
		}
		if !equal {
			return false, nil
		}
	}
	return true, nil
}

// nativeSliceScalarEqual compares primitive elements with Go's == semantics.
// In particular, NaN never equals itself while signed zero values compare equal.
func nativeSliceScalarEqual(a, b bashPPBridgeValue) (bool, error) {
	if a.Kind != b.Kind {
		return false, fmt.Errorf("gosource: slices.Equal requires uniform element kinds, got %s and %s", a.Kind, b.Kind)
	}
	switch a.Kind {
	case "string":
		return a.stringText() == b.stringText(), nil
	case "bool":
		return a.Text == b.Text, nil
	case "int":
		ai, err := strconv.ParseInt(a.Text, 10, 64)
		if err != nil {
			return false, fmt.Errorf("gosource: invalid integer element %q", a.Text)
		}
		bi, err := strconv.ParseInt(b.Text, 10, 64)
		if err != nil {
			return false, fmt.Errorf("gosource: invalid integer element %q", b.Text)
		}
		return ai == bi, nil
	case "uint":
		au, err := strconv.ParseUint(a.Text, 10, 64)
		if err != nil {
			return false, fmt.Errorf("gosource: invalid unsigned element %q", a.Text)
		}
		bu, err := strconv.ParseUint(b.Text, 10, 64)
		if err != nil {
			return false, fmt.Errorf("gosource: invalid unsigned element %q", b.Text)
		}
		return au == bu, nil
	case "float":
		af, err := strconv.ParseFloat(a.Text, 64)
		if err != nil {
			return false, fmt.Errorf("gosource: invalid float element %q", a.Text)
		}
		bf, err := strconv.ParseFloat(b.Text, 64)
		if err != nil {
			return false, fmt.Errorf("gosource: invalid float element %q", b.Text)
		}
		return af == bf, nil
	}
	return false, fmt.Errorf("gosource: slices.Equal requires primitive elements, got %s", a.Kind)
}

// nativeSliceSorted returns the elements ordered like slices.Sort over an ordered
// primitive element type. The input order is preserved among equal elements; that
// choice is unobservable by value, which is all the mutation writeback restores.
func nativeSliceSorted(elems []bashPPBridgeValue) ([]bashPPBridgeValue, error) {
	out := append([]bashPPBridgeValue(nil), elems...)
	var sortErr error
	sort.SliceStable(out, func(i, j int) bool {
		if sortErr != nil {
			return false
		}
		less, err := nativeSliceElementLess(out[i], out[j])
		if err != nil {
			sortErr = err
			return false
		}
		return less
	})
	if sortErr != nil {
		return nil, sortErr
	}
	return out, nil
}

func nativeSliceIsSorted(elems []bashPPBridgeValue) (bool, error) {
	for i := 1; i < len(elems); i++ {
		less, err := nativeSliceElementLess(elems[i], elems[i-1])
		if err != nil {
			return false, err
		}
		if less {
			return false, nil
		}
	}
	return true, nil
}

// nativeSliceElementLess orders two primitive elements as cmp.Ordered would:
// strings lexically and numbers by value. bool is not ordered and is rejected,
// as slices.Sort's cmp.Ordered constraint requires.
func nativeSliceElementLess(a, b bashPPBridgeValue) (bool, error) {
	if a.Kind != b.Kind {
		return false, fmt.Errorf("gosource: slices.Sort requires uniform element kinds, got %s and %s", a.Kind, b.Kind)
	}
	switch a.Kind {
	case "string":
		return a.stringText() < b.stringText(), nil
	case "int":
		ai, err := strconv.ParseInt(a.Text, 10, 64)
		if err != nil {
			return false, fmt.Errorf("gosource: invalid integer element %q", a.Text)
		}
		bi, err := strconv.ParseInt(b.Text, 10, 64)
		if err != nil {
			return false, fmt.Errorf("gosource: invalid integer element %q", b.Text)
		}
		return ai < bi, nil
	case "uint":
		au, err := strconv.ParseUint(a.Text, 10, 64)
		if err != nil {
			return false, fmt.Errorf("gosource: invalid unsigned element %q", a.Text)
		}
		bu, err := strconv.ParseUint(b.Text, 10, 64)
		if err != nil {
			return false, fmt.Errorf("gosource: invalid unsigned element %q", b.Text)
		}
		return au < bu, nil
	case "float":
		af, err := strconv.ParseFloat(a.Text, 64)
		if err != nil {
			return false, fmt.Errorf("gosource: invalid float element %q", a.Text)
		}
		bf, err := strconv.ParseFloat(b.Text, 64)
		if err != nil {
			return false, fmt.Errorf("gosource: invalid float element %q", b.Text)
		}
		return cmp.Less(af, bf), nil
	}
	return false, fmt.Errorf("gosource: slices.Sort requires ordered primitive elements, got %s", a.Kind)
}
