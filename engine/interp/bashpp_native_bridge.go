package interp

// Sprint: #118; Story: #50; Story-ID: cf81e4868348
// The persistent child runs imported dependency operations only. Its source is
// a fixed protocol template plus a Go-type-checked export table, never a tested
// program body or expression. There is no second source-language interpreter.
import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	mrand "math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed bashpp_native_worker.go.txt
var bashPPNativeWorker string

type bashPPBridgeValue struct {
	handleOwner  *bashPPHandleOwner    // shared by every interpreter copy; never serialized
	Lease        uint64                `json:"lease,omitempty"`
	residentSync *goSourceResidentSync // host-only; never serialized
	// deferredNativeComposite is return-boundary provenance minted only while
	// evaluating the mirrored method frame's own return expression. The worker
	// reconstructs the value at the declared reflect result type; the marker is
	// host-only and is never trusted from the wire.
	deferredNativeComposite bool
	ReaderBuffer            []byte              `json:"reader_buffer,omitempty"`
	ReaderLength            int                 `json:"reader_length,omitempty"`
	CallArgs                []bashPPBridgeValue `json:"call_args,omitempty"`
	sliceView               *bashPPNativeSlice  // host-only original backing view
	// localReflect and localCell are host-only interpreter-owned reflected
	// values (bashpp_s248_reflected_method.go); neither ever crosses.
	localReflect *goSourceLocalReflect
	localCell    *bashPPCell
	// A pointer argument keeps its storage live while the request is being
	// assembled, before openOriginRequest can claim its transport origin.
	localOrigin *bashPPPointer

	// reflectCopy marks a handle derived from an admitted reflect.ValueOf
	// copy (reflectedValueCopy). Host-only: it is set on replies by the
	// session itself and never trusted from the wire.
	reflectCopy bool
	// reflectFunction marks a reflect.Value created from an original function.
	// Only synchronous Call and type inspection may use this callback-bearing
	// handle; it is never accepted as an arbitrary dependency argument.
	reflectFunction bool
	// reflectFunc is the original function a reflectFunction handle wraps,
	// so Pointer can name it in the interpreter's frame table. Host-only.
	reflectFunc *bashPPFunc
	// callRefusal, on an original callback registered for reflect.ValueOf,
	// is the diagnostic its signature earns: the reflected handle may be
	// inspected, but Call refuses it. Host-only.
	callRefusal string
	// localRefusal, on an original callback handed to an interpreter-answered
	// generic slices helper, is the diagnostic its signature earns anywhere
	// else: the helper binds its parameters from live storage, but the callback
	// never crosses to the dependency. Host-only.
	localRefusal string

	// Callable is derived by the interpreter from authenticated native type or
	// import metadata; the dependency worker cannot set callback policy itself.
	Callable     string `json:"-"`
	NativeType   string `json:"native_type,omitempty"`
	NativeTypeID uint64 `json:"native_type_id,omitempty"`
	// LocalWriter authenticates standard dependency writers whose Write can
	// run against the Runner's matching sink without a bridge round trip.
	LocalWriter string `json:"local_writer,omitempty"`
	Callbacks   bool   `json:"callbacks,omitempty"`
	Function    bool   `json:"function,omitempty"`
	Origin      uint64 `json:"origin,omitempty"`
	// Storage identifies interpreter-owned slice backing storage within this
	// session. Offset, Length and Capacity describe one view into that backing.
	// The worker uses them only while refreshing authenticated storage; they
	// preserve overlapping views and capacity-only elements without turning
	// ordinary ValueOf(value) copies into live views.
	Storage   uint64                       `json:"storage,omitempty"`
	Offset    int                          `json:"offset,omitempty"`
	Length    int                          `json:"length,omitempty"`
	Capacity  int                          `json:"capacity,omitempty"`
	Interface string                       `json:"interface,omitempty"`
	Signature string                       `json:"signature,omitempty"`
	Session   string                       `json:"session,omitempty"`
	Kind      string                       `json:"kind"`
	Type      string                       `json:"type,omitempty"`
	Text      string                       `json:"text,omitempty"`
	Bytes     []byte                       `json:"bytes,omitempty"`
	Handle    uint64                       `json:"handle,omitempty"`
	Elements  []bashPPBridgeValue          `json:"elements,omitempty"`
	Fields    map[string]bashPPBridgeValue `json:"fields,omitempty"`
	Entries   []bashPPBridgeEntry          `json:"entries,omitempty"`
	// Within places a pointer to an array element (or to a subobject of one)
	// inside the dependency-side storage of the enclosing array; see
	// bashpp_s374_element_address.go. Only the interpreter sends it.
	Within *bashPPBridgeWithin `json:"within,omitempty"`

	// NilChannel marks a dependency channel handle whose value is nil. A nil
	// channel never communicates, so a select arm on it needs no arbitration.
	NilChannel bool `json:"nil_channel,omitempty"`
	// copiedResults marks an original callback whose declared results include
	// a slice of dependency handles, rebuilt on the dependency side on return.
	// Host-only; only a non-retaining result consumer may carry it.
	copiedResults bool
	// copiedHandleResults marks the subset of copiedResults whose result is a
	// slice of dependency handles; see copiedHandleResultsConsumer.
	copiedHandleResults bool
	// newCallback is local request ownership metadata. It never crosses JSON:
	// a synchronous request releases a callback registered solely for that
	// call once the dependency has returned.
	newCallback bool
}

func bashPPBridgeString(text string) bashPPBridgeValue {
	return bashPPBridgeValue{Kind: "string", Type: "string", Text: text, Bytes: []byte(text)}
}

func bashPPBridgePanicString(text string) bashPPBridgeValue {
	value := bashPPBridgeString(text)
	value.Kind = "panic"
	return value
}

func (v bashPPBridgeValue) stringText() string {
	if (v.Kind == "string" || v.Kind == "panic" && v.Type == "string") && v.Bytes != nil {
		return string(v.Bytes)
	}
	return v.Text
}

type bashPPBridgeEntry struct {
	Key   bashPPBridgeValue `json:"key"`
	Value bashPPBridgeValue `json:"value"`
}
type bashPPBridgeRequest struct {
	AckHandles     bool                      `json:"ack_handles,omitempty"`
	Releases       []uint64                  `json:"releases,omitempty"`
	OriginReleases []uint64                  `json:"origin_releases,omitempty"`
	SliceBuffers   []bashPPNativeSliceBuffer `json:"slice_buffers,omitempty"`
	sliceTargets   []*bashPPNativeSlice
	// sliceMutating[i] distinguishes a full in-place mutation writeback (sort)
	// from the byte-read writeback: a read fills a caller buffer up to its
	// capacity, while a mutation reorders exactly the visible length. Host-only.
	sliceMutating []bool
	// sliceElem[i] is the declared element type of sliceTargets[i], used to
	// rebuild the writeback elements as interpreter values. Host-only.
	sliceElem []syntax.BashPPTypeExpr
	// sliceReconcile[i] marks a buffer whose class is decided by the observed
	// call behaviour: elements returned unchanged are a read-only consumer and
	// nothing is written back; changed elements are an in-place mutation and
	// are written back over the visible length. Host-only.
	sliceReconcile []bool
	// Transfers lists the argument indexes whose original slice is handed to
	// the dependency as the storage of record rather than as a copy; the
	// worker answers each with a handle on that same slice (Transferred) and
	// argCells[i] is the interpreter binding the transfer rebinds to it. See
	// bashpp_native_transfer.go. argCells, transferProof and sourceProgram are host-only.
	Transfers     []int `json:"transfers,omitempty"`
	argCells      []*bashPPCell
	transferProof []bool
	// coherence, when set, re-reads the storage a read-only request copied
	// after every callback it serves. See bashpp_native_coherence.go.
	coherence *goSourceCopyCoherence
	// sliceCallbackSync marks read-only dependency calls whose direct original
	// slices are synchronized in both directions at each callback boundary.
	// The worker and interpreter therefore keep one logical backing store even
	// though the transport itself necessarily decoded a copy.
	sliceCallbackSync bool
	// sourceSynchronousCallback is set only after the selected dependency's
	// immutable Go sources prove that every callback stays on this call's
	// stack. It is host-only and deliberately absent from the wire protocol.
	sourceSynchronousCallback bool
	// sourceSynchronousMethodCallback is the same proof for a request whose
	// only callbacks are local values with mirrored methods: it admits a
	// callee outside the standard library to the general method-callback
	// bridge (synchronousOriginalMethodCallback) and nothing else.
	sourceSynchronousMethodCallback bool
	sourceProgram                   bool   // the call site is in the program package itself
	ID                              uint64 `json:"id"`
	Op                              string `json:"op"`
	PanicOnFault                    bool   `json:"panic_on_fault,omitempty"`
	// PointerReadOnly says this operation cannot mutate any origin pointer in
	// the dependency. The worker can then omit its global pointer writeback
	// scan; the bit is derived here from authenticated call metadata.
	PointerReadOnly bool   `json:"pointer_read_only,omitempty"`
	Selector        string `json:"selector"`
	// AccessPackage is the interpreter-authenticated package identity of the
	// source performing a member read. The worker still compares it with the
	// field's declaring package before exposing unexported storage.
	AccessPackage string `json:"access_package,omitempty"`
	// Instance is the type-argument suffix of an instantiated imported
	// generic function; the helper resolves Selector+Instance.
	Instance string              `json:"instance,omitempty"`
	Receiver *bashPPBridgeValue  `json:"receiver,omitempty"`
	Args     []bashPPBridgeValue `json:"args,omitempty"`
	Spread   bool                `json:"spread,omitempty"`
	// FormatOnly redirects fmt.Fprint* to its fmt.Sprint* counterpart in the
	// worker. The writer is interpreter-owned, so only the formatted bytes
	// return across the bridge and its Write body runs locally.
	FormatOnly string `json:"format_only,omitempty"`
	SourceFile string `json:"source_file,omitempty"`
	SourceLine int    `json:"source_line,omitempty"`
	LogPrint   string `json:"log_print,omitempty"`
	// Values and Error answer a callback the dependency raised; they are set
	// only when Op is "callback-reply". Sprint #118 Story #54 (c3a60493cde9).
	Values []bashPPBridgeValue `json:"values,omitempty"`
	Error  string              `json:"error,omitempty"`
	// Route asks the helper to name this request as the Parent of every
	// callback its dispatching goroutine raises; see routedCallbackRequest.
	Route bool `json:"route,omitempty"`
}
type bashPPBridgeResponse struct {
	SliceUpdates []bashPPNativeSliceBuffer `json:"slice_updates,omitempty"`
	// Transferred answers each requested transfer with a handle on the
	// decoded slice the dependency received, in Transfers order.
	Transferred []bashPPNativeSliceBuffer `json:"transferred,omitempty"`
	PtrUpdates  []bashPPBridgeValue       `json:"ptr_updates,omitempty"`

	Panic *bashPPBridgeValue `json:"panic,omitempty"`
	// PanicAddr carries runtime.Error values produced by SetPanicOnFault. It
	// is meaningful only when Error is a worker-recovered native dependency
	// panic.
	PanicAddr *uint64 `json:"panic_addr,omitempty"`
	ID        uint64  `json:"id"`
	// Op, Selector and Receiver are set only when the dependency is asking the
	// interpreter to run an original method body it must not compile itself.
	Op       string              `json:"op,omitempty"`
	Selector string              `json:"selector,omitempty"`
	Receiver *bashPPBridgeValue  `json:"receiver,omitempty"`
	Values   []bashPPBridgeValue `json:"values,omitempty"`
	Error    string              `json:"error,omitempty"`
	// Parent names the routed request whose dispatch raised this callback.
	Parent uint64 `json:"parent,omitempty"`
	// OutputBarrierStdout and OutputBarrierStderr are per-stream worker-side
	// marker sequences written before this reply. A zero sequence for a
	// drained stream means the marker write failed and requires the
	// host-written fallback barrier.
	OutputBarrierStdout uint64 `json:"output_barrier_stdout,omitempty"`
	OutputBarrierStderr uint64 `json:"output_barrier_stderr,omitempty"`
}

func bashPPNativeSelectDefaultReply(q bashPPBridgeRequest, reply bashPPBridgeResponse) bool {
	return q.Op == "channel-select" && q.Selector == "probe" && reply.Error == "" &&
		reply.Panic == nil && len(reply.Values) == 3 && reply.Values[0].Kind == "int" &&
		reply.Values[0].Text == "-1"
}

func bashPPNativeNoOutputReply(req bashPPEvalRequest, q bashPPBridgeRequest, reply bashPPBridgeResponse) bool {
	if reply.Error != "" || reply.Panic != nil {
		return false
	}
	if bashPPNativeSelectDefaultReply(q, reply) {
		return true
	}
	if q.Op != "call" {
		return false
	}
	if q.Receiver != nil {
		return q.Receiver.Type == "time.Duration" && q.Selector == "Round"
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	return ok && req.Imports[alias] == "time" && name == "Since"
}

type bashPPNativeSession struct {
	callbackLeaseReplies map[uint64]bashPPBridgeRequest
	handleReleaseMu      sync.Mutex
	handleState          *bashPPHandleState
	fastRandMu           sync.Mutex
	fastRand             *mrand.Rand
	fastRandProof        map[string]bool
	fastRandProofSeen    map[string]bool
	// Type facts are authenticated on this connection; no native values are cached.
	handleTypes         map[uint64]uint64
	localWriters        map[uint64]string
	typeFacts           map[bashPPNativeTypeFactKey]bashPPBridgeValue // protected by mu
	interfaceAdmissions map[goSourceNativeAdmissionKey]bool
	callbackProofs      map[string]bool // immutable source proofs, protected by mu
	callbackProofSeen   map[string]bool // distinguishes a proved refusal from absent
	functions           map[uint64]*bashPPFunc
	functionOwners      map[uint64]*bashPPCallbackFunction
	functionNext        uint64
	callbackGate        chan struct{}
	activeCallbacks     chan bashPPBridgeResponse
	callbackOwner       *Runner
	// routes are the callback channels of routed requests, by request ID.
	// Protected by mu.
	routes map[uint64]chan bashPPBridgeResponse
	// valueOfReplies are admitted reflect.ValueOf replies by operand shape,
	// the templates of lazily answered ones. Protected by mu.
	valueOfReplies map[string]bashPPBridgeValue
	mailbox        *bashPPCallbackMailbox
	// callbackRefusal is the first diagnostic from an interpreter-refused
	// dependency callback. The dependency exits nonzero after the refusal, so
	// exit-status adoption must preserve this original cause.
	callbackRefusal error
	// retained records that this session was handed an original callback it
	// keeps past the handing-over call. Every later request then parks as a
	// callback server; see requestCallbackCapable.
	retained bool
	// madeFuncs are the handles of functions reflect.MakeFunc built over an
	// original implementation, and of their Interface() views; see
	// bashPPMadeFuncUse.
	madeFuncs map[uint64]*Runner
	origins   map[uint64]*bashPPOriginRecord
	// originOpen holds the origins named by requests awaiting their reply,
	// with the strong pointer that keeps each cell alive while its request
	// is in flight. originReleaseQueue holds swept dead origins until the
	// next request carries them to the worker. Both protected by mu.
	originOpen         map[uint64]*bashPPOriginHold
	originReleaseQueue []uint64
	// originSweepNext is the table size at which the next unforced sweep is
	// worth a collection: twice what the last sweep left alive. Without it a
	// table of live origins above the cap would collect on every transport.
	originSweepNext int
	// Native uintptr results whose address is a transported original pointer.
	// Protected by mu; retaining the pointer also keeps its interpreter cell live.
	addressOrigins  map[uint64]*bashPPPointer
	originIndex     map[bashPPOriginKey]uint64 // protected by mu; see bashPPTransportOrigin
	originIndexed   int                        // len(origins) the index covers
	originNext      uint64
	sliceOriginKeep map[uint64]*bashPPNativeSlice // widest registered view, kept live
	sliceOriginNext uint64
	// slicePinned marks regions the worker may reference past the call
	// that introduced them — slices nested in a pointer pointee, refreshed
	// storage, transferred storage — so eviction never drops them. Every
	// other region is transient and leaves once the table exceeds its bound.
	// slicePinDepth counts active pointer-pointee walks on this session; a
	// slice registered while it is nonzero is nested in a transported
	// pointer and decodes into worker-persisted storage. Both protected by
	// mu.
	slicePinned   map[uint64]bool
	slicePinDepth int
	// sliceRegionRoot indexes sliceOriginKeep by backing interval so a
	// transported view finds its tightest containing region without
	// scanning every registered view; see bashPPTransportSliceOrigin.
	// sliceRegionCount is len(sliceOriginKeep) the index covers; a
	// mismatch rebuilds it, as for originIndex. Both protected by mu.
	sliceRegionRoot  *bashPPSliceRegion
	sliceRegionCount int
	start            sync.Mutex
	write            sync.Mutex
	outbox           bashPPBridgeOutbox
	// encoder is created with conn and used only with write held, by the
	// close message. Every other message goes through the outbox
	// (bashpp_native_group_write.go).
	encoder             *json.Encoder
	verifiedPlan        *bashPPNativeRequestPlan // protected by mu
	mu                  sync.Mutex
	next                atomic.Uint64
	conn                net.Conn
	cmd                 *exec.Cmd
	pending             map[uint64]chan bashPPBridgeResponse
	done                chan struct{}
	waitErr             error
	forwardedSignal     int   // protected by mu; last parent signal delivered to cmd
	closeCancellation   error // protected by mu; set only by the closer of conn
	processCancellation error // protected by mu; command context requested kill
	closeOnce           sync.Once
	cleanup             func()
	reexecMu            sync.Mutex
	reexecDir           string
	reexecLauncher      string
	stopSignals         func()
	drains              []*bashPPNativeOutputDrain
	imports             string
	locals              string
	embeds              string
	companions          string
	cgo                 string
	instances           string
	genericTypes        string
	selectors           string
	id                  string
}

// bashPPBridgeEncoder writes the private worker protocol, which is JSON over a
// socket and never embedded in HTML. Disabling HTML escaping avoids expanding
// '<', '>' and '&' in the bridge's many string fields while preserving JSON
// string semantics for the worker decoder.
func bashPPBridgeEncoder(w io.Writer) *json.Encoder {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder
}

func (r *Runner) closeGoSourceBridge() {
	r.bashPPTools.nativeTypes = nil
	r.bashPPTools.requestEnv = nil
	r.bashPPTools.buildEnv = nil
	r.bashPPTools.runtimeEnv = nil
	if session := r.bashPPTools.bridge; session != nil {
		session.close()
		r.bashPPTools.bridge = nil
	}
}
func (s *bashPPNativeSession) close() { s.closeCanceled(nil) }

func (s *bashPPNativeSession) closeCanceled(cause error) {
	s.closeOnce.Do(func() {
		defer func() { s.handleReleaseMu.Lock(); s.callbackLeaseReplies = nil; s.handleReleaseMu.Unlock() }()
		if s.conn != nil {
			s.write.Lock()
			processExited := false
			select {
			case <-s.done:
				processExited = true
			default:
			}
			encoder := s.encoder
			if encoder == nil {
				encoder = bashPPBridgeEncoder(s.conn)
			}
			_ = encoder.Encode(bashPPBridgeRequest{Op: "close"})
			s.mu.Lock()
			s.verifiedPlan = nil
			if err := s.conn.Close(); err == nil && cause != nil && !processExited {
				s.closeCancellation = cause
			}
			s.mu.Unlock()
			s.write.Unlock()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			select {
			case <-s.done:
			case <-time.After(time.Second):
				bashPPNativeKill(s.cmd)
				<-s.done
			}
		}
		if s.stopSignals != nil {
			s.stopSignals()
		}
		s.closeDrains()
		s.mu.Lock()
		mailbox := s.mailbox
		s.mailbox = nil
		s.mu.Unlock()
		mailbox.close()
		s.closeCallbackFunctionTemplates()
		if s.cmd != nil && s.cmd.Process != nil {
			// The child is gone by now, so the copiers can reach EOF; waiting
			// here makes the program's final output visible before the session
			// reports closed.
			s.waitDrains()
		}
		if s.cleanup != nil {
			s.cleanup()
		}
		if s.reexecDir != "" {
			_ = os.RemoveAll(s.reexecDir)
		}
	})
}
func bridgeImportIdentity(imports map[string]string) string {
	data, _ := json.Marshal(imports)
	return string(data)
}
func (s *bashPPNativeSession) begin(ctx context.Context, req bashPPEvalRequest) error {
	s.mu.Lock()
	verified := s.conn != nil && s.verifiedPlan != nil && req.identityPlan == s.verifiedPlan && req.identityPlan.matchesRequest(req)
	s.mu.Unlock()
	if verified {
		return nil
	}
	s.start.Lock()
	defer s.start.Unlock()
	if s.conn != nil {
		// The fingerprints this compares against are derived once per program
		// revision, so refusing namespace drift costs the same on a program of
		// any size; see bashpp_s281_request_plan.go.
		identity := req.sessionIdentity()
		if s.imports != identity.imports {
			return errors.New("gosource: imports changed after native dependency initialization")
		}
		if s.locals != identity.locals {
			return errors.New("gosource: local types changed after native dependency initialization")
		}
		if s.embeds != identity.embeds {
			return errors.New("gosource: embed declarations changed after native dependency initialization")
		}
		if s.companions != identity.companions {
			return errors.New("gosource: native companions changed after native dependency initialization")
		}
		if s.cgo != identity.cgo {
			return errors.New("gosource: cgo packages changed after native dependency initialization")
		}
		if s.instances != identity.instances {
			return errors.New("gosource: imported instantiations changed after native dependency initialization")
		}
		if s.genericTypes != identity.generics {
			return errors.New("gosource: generic bridge types changed after native dependency initialization")
		}
		if s.selectors != identity.selectors {
			return errors.New("gosource: bridge selectors changed after native dependency initialization")
		}
		s.mu.Lock()
		s.verifiedPlan = req.identityPlan
		s.mu.Unlock()
		return nil
	}
	source, err := bashPPNativeSource(ctx, bashPPModuleRequest(req))
	if err != nil {
		return err
	}
	listener, bridgeNetwork, cleanupControl, err := bashPPNativeControlListener()
	if err != nil {
		return err
	}
	defer listener.Close()
	defer cleanupControl()
	secret := make([]byte, 24)
	if _, err = rand.Read(secret); err != nil {
		return err
	}
	auth := hex.EncodeToString(secret)
	s.id = auth[:16]
	stdoutMarker := []byte(auth + "-stdout")
	stderrMarker := []byte(auth + "-stderr")
	mailbox, mailboxErr := newBashPPCallbackMailbox()
	if mailboxErr == nil {
		s.mu.Lock()
		s.mailbox = mailbox
		s.mu.Unlock()
	}
	mailboxStarted := false
	defer func() {
		if !mailboxStarted && mailbox != nil {
			s.mu.Lock()
			if s.mailbox == mailbox {
				s.mailbox = nil
			}
			s.mu.Unlock()
			mailbox.close()
		}
	}()
	mailboxPath := ""
	if mailbox != nil {
		mailboxPath = mailbox.path
	}
	mailboxImports, mailboxSource := bashPPMailboxWorkerSource(mailbox != nil)
	source = strings.Replace(source, "//CALLBACKMAILBOXIMPORTS", mailboxImports, 1)
	source = strings.Replace(source, "//CALLBACKMAILBOX", mailboxSource, 1)
	stdoutDrain := req.Stdout != nil
	if _, isFile := req.Stdout.(*os.File); isFile {
		stdoutDrain = false
	}
	stderrDrain := req.Stderr != nil
	if _, isFile := req.Stderr.(*os.File); isFile {
		stderrDrain = false
	}
	sharedDrain := stdoutDrain && stderrDrain && bashPPSameWriter(req.Stderr, req.Stdout)
	linkValues := map[string]string{
		"bridgeNetwork":       bridgeNetwork,
		"bridgeAddress":       listener.Addr().String(),
		"callbackMailboxPath": mailboxPath,
		"workerBootstrap":     auth,
	}
	if stdoutDrain {
		linkValues["bridgeStdoutBarrier"] = "1"
	}
	if stderrDrain && !sharedDrain {
		linkValues["bridgeStderrBarrier"] = "1"
	}
	scratchEnv := req.RuntimeEnv
	if scratchEnv == nil {
		scratchEnv = req.Env
	}
	sourceDir := bashPPModuleRequest(req).Dir
	policy := bashPPScratchIsolated
	if len(req.EmbedDecls) > 0 || len(req.CompanionFiles) > 0 {
		sourceDir = req.SourceDir
		policy = bashPPScratchSourceRoot
	} else if len(req.MappedCompanions) > 0 {
		// Put the command helper inside the first mapped package's visibility
		// tree. A package such as cmd/compile/internal/ssa may be imported only
		// from there; the helper remains a named command file while the package
		// directory itself sees only overlaid package declarations.
		sourceDir = req.MappedCompanions[0].SourceDir
		policy = bashPPScratchIsolated
	}
	file, err := bashPPImportTempSource(sourceDir, "bashpp-session-*.go", scratchEnv, policy)
	if err != nil {
		return err
	}
	cleanup := file.cleanup
	s.cleanup = cleanup
	if _, err = file.WriteString(source); err != nil {
		file.Close()
		cleanup()
		return err
	}
	if err = file.Close(); err != nil {
		cleanup()
		return err
	}
	authPath := filepath.Join(filepath.Dir(file.Name()), "bridge-auth")
	if err = os.WriteFile(authPath, []byte(auth), 0600); err != nil {
		cleanup()
		return err
	}
	linkValues["bridgeAuthPath"] = authPath
	buildLDFlags := bashPPWorkerBuildLDFlags(linkValues)
	binary := file.Name() + ".bin"
	buildEnv := setEnvString(req.internalBuildEnv(), "CGO_ENABLED", "0")
	if len(req.CgoPackages) > 0 {
		buildEnv = setEnvString(req.internalBuildEnv(), "CGO_ENABLED", "1")
	}
	// The importcfg link routes below bypass cmd/go, so the original build's
	// default GODEBUG must be replayed into the worker by hand; the go build
	// routes compute it themselves. See bashPPDefaultGODEBUG.
	defaultGODEBUG := bashPPDefaultGODEBUG(ctx, req)
	if len(req.MappedCompanions) > 0 {
		if err = bashPPOverlayMappedCompanions(ctx, req, file, buildEnv); err != nil {
			cleanup()
			return err
		}
	}
	if len(req.CgoPackages) > 0 {
		// cmd/cgo changes into the logical source directory. Give the overlay
		// files unique names in the existing module directory so that directory
		// exists, while all bytes and build artifacts remain in private scratch.
		cgoMain := filepath.Join(bashPPModuleRequest(req).Dir, filepath.Base(file.buildPath))
		if _, statErr := os.Lstat(cgoMain); !os.IsNotExist(statErr) {
			cleanup()
			return fmt.Errorf("gosource: cgo helper overlay would mask an existing source path")
		}
		if err = file.remap(cgoMain); err != nil {
			cleanup()
			return err
		}
		wrappers, wrapperErr := bashPPCgoWrapperSources(ctx, req)
		if wrapperErr != nil {
			cleanup()
			return wrapperErr
		}
		paths := []string{file.buildPath}
		for i, wrapper := range wrappers {
			path, addErr := file.addSource(fmt.Sprintf("bashpp-cgo-%d.go", i), wrapper)
			if addErr != nil {
				cleanup()
				return addErr
			}
			paths = append(paths, path)
		}
		args := append([]string{"build", "-p", "2", "-ldflags=" + buildLDFlags, "-overlay=" + file.overlay, "-o", binary}, paths...)
		build := exec.CommandContext(ctx, req.internalBuildGo(), args...)
		build.Dir, build.Env = bashPPModuleRequest(req).Dir, buildEnv
		var diagnostics bytes.Buffer
		build.Stdout, build.Stderr = &diagnostics, &diagnostics
		if err = build.Run(); err != nil {
			cleanup()
			return fmt.Errorf("gosource: build cgo dependency bridge: %w: %s", err, diagnostics.String())
		}
	} else if len(req.CompanionFiles) > 0 {
		if req.SourceFile == "" {
			cleanup()
			return fmt.Errorf("gosource: native companions require an original source file")
		}
		// The companion build reads the original package directory, since that
		// is where its assembly lives; every original Go file in it is overlaid
		// away first, so the compiler sees only the generated helper.
		stubs, err := bashPPCompanionRootStubs(req.SourceDir, req.RootFiles)
		if err != nil {
			cleanup()
			return err
		}
		if err = file.overlayRoot(stubs); err != nil {
			cleanup()
			return err
		}
		if err = bashPPCompanionOverlayComplete(file.sourceDir, file.replace); err != nil {
			cleanup()
			return err
		}
		// The build runs in the directory the overlay is keyed by, and says so
		// in its own environment: cmd/go matches overlay paths against the
		// working directory it resolves, and it resolves that one through
		// $PWD. A key spelled differently is not an error, it is simply not a
		// key -- and the build would then quietly compile the original
		// package. Hence both the explicit PWD and the check below.
		companionEnv := setEnvString(buildEnv, "PWD", file.sourceDir)
		if err = bashPPCompanionBuildIsolated(ctx, req.internalBuildGo(), file, companionEnv); err != nil {
			cleanup()
			return err
		}
		build := exec.CommandContext(ctx, req.internalBuildGo(), "build", "-p", "2", "-ldflags="+buildLDFlags, "-overlay="+file.overlay, "-o", binary, ".")
		build.Dir, build.Env = file.sourceDir, companionEnv
		var diagnostics bytes.Buffer
		build.Stdout, build.Stderr = &diagnostics, &diagnostics
		if err = build.Run(); err != nil {
			cleanup()
			return fmt.Errorf("gosource: build dependency bridge: %w: %s", err, diagnostics.String())
		}
	} else if len(req.MappedCompanions) > 0 {
		// The worker imports a mapped companion package (possibly internal, e.g.
		// cmd/compile/internal/ssa) whose overlaid form beside its assembly is
		// what the native side must link. `go build` of the worker would re-apply
		// cmd/go's DIRECTORY internal rule to EVERY worker import from the
		// companion's directory, refusing an internal package the identity rule
		// already admitted for this program (testing/internal/testdeps under the
		// authenticated test-main fact sits in a different tree entirely). The
		// importcfg route compiles the worker directly and lists its dependency
		// closure by path, so the companion is exported from the overlay while
		// the admitted internal imports are never re-decided. See D8 and
		// bashPPBuildWorkerImportcfg.
		if err = bashPPBuildWorkerImportcfg(ctx, req.internalBuildGo(), file.sourceDir, setEnvString(buildEnv, "PWD", file.sourceDir), file.overlay, file.work, file.Name(), binary, linkValues, defaultGODEBUG); err != nil {
			cleanup()
			return err
		}
	} else if policy == bashPPScratchSourceRoot {
		// go:embed patterns resolve against the worker's logical location;
		// only cmd/go's overlay gives the worker one inside the source root.
		build := exec.CommandContext(ctx, req.internalBuildGo(), "build", "-p", "2", "-ldflags="+buildLDFlags, "-overlay="+file.overlay, "-o", binary, file.buildPath)
		build.Dir, build.Env = bashPPModuleRequest(req).Dir, buildEnv
		var diagnostics bytes.Buffer
		build.Stdout, build.Stderr = &diagnostics, &diagnostics
		if err = build.Run(); err != nil {
			cleanup()
			return fmt.Errorf("gosource: build dependency bridge: %w: %s", err, diagnostics.String())
		}
	} else if err = bashPPBuildWorkerImportcfg(ctx, req.internalBuildGo(), bashPPModuleRequest(req).Dir, buildEnv, "", filepath.Dir(file.Name()), file.Name(), binary, linkValues, defaultGODEBUG); err != nil {
		// The importcfg route: the worker's imports were decided at the
		// check (identity-keyed, D8); cmd/go's directory rule does not
		// re-decide them. See bashpp_sprint165_runtime2_worker_build.go.
		cleanup()
		return err
	}
	cmd := exec.CommandContext(ctx, binary)
	// Bootstrap data is compiled into this ephemeral dependency-only helper.
	// It must not pollute os.Args or os.Environ, including dependency init.
	cmd.Args = append([]string(nil), req.Argv...)
	if req.FreshReexec && len(cmd.Args) != 0 {
		// The generated worker is the process actually running the interpreted
		// package test. Advertising it as argv[0] gives testing a real test
		// executable without pretending that the replay launcher is the program
		// under test. Replays retain the launcher's invoked identity instead.
		cmd.Args[0] = binary
	}
	cmd.Dir, cmd.Env = req.Dir, append(append([]string(nil), req.RuntimeEnv...), "BASHPP_WORKER_BOOTSTRAP="+auth)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = req.Stdin, req.Stdout, req.Stderr
	// A non-file writer receives child output through a pipe this session owns,
	// so each answered request can drain its output before the interpreter's
	// next direct write; see bashpp_native_output.go. One writer given for both
	// streams shares one pipe, exactly as os/exec would share one descriptor.
	if stdoutDrain {
		drain, err := newBashPPNativeOutputDrainWithWorker(req.Stdout, stdoutMarker, 1)
		if err != nil {
			cleanup()
			return err
		}
		s.drains = append(s.drains, drain)
		cmd.Stdout = drain.write
	}
	if stderrDrain {
		if sharedDrain {
			cmd.Stderr = cmd.Stdout
		} else {
			drain, err := newBashPPNativeOutputDrainWithWorker(req.Stderr, stderrMarker, 2)
			if err != nil {
				s.closeDrains()
				cleanup()
				return err
			}
			s.drains = append(s.drains, drain)
			cmd.Stderr = drain.write
		}
	}
	bashPPNativeProcessGroup(cmd)
	cmd.Cancel = func() error {
		s.mu.Lock()
		s.processCancellation = ctx.Err()
		s.mu.Unlock()
		bashPPNativeKill(cmd)
		return nil
	}
	if err = startExecCmdWithinBudget(cmd); err != nil {
		s.closeDrains()
		cleanup()
		return err
	}
	// The helper owns the native standard-library state of the interpreted Go
	// program. Keep it in an isolated process group for cleanup, but proxy the
	// program process's catchable signals as an exec replacement would.
	s.stopSignals = forwardBashPPNativeSignalsWithReport(cmd.Process.Pid, func(sig int) {
		s.mu.Lock()
		s.forwardedSignal = sig
		s.mu.Unlock()
	})
	s.cmd = cmd
	s.done = make(chan struct{})
	s.pending = make(map[uint64]chan bashPPBridgeResponse)
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		s.waitErr = err
		conn := s.conn
		s.mu.Unlock()
		close(s.done)
		// The child's descriptors are gone; retiring the host write ends lets
		// each copier drain to EOF and flush the program's final output.
		s.closeDrains()
		_ = listener.Close()
		if conn != nil {
			// Serialize local close with writes and cancellation provenance.
			s.write.Lock()
			_ = conn.Close()
			s.write.Unlock()
		}
	}()
	if tcp, ok := listener.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Now().Add(10 * time.Second))
	}
	conn, err := listener.Accept()
	if err != nil {
		s.close()
		return fmt.Errorf("gosource: bridge handshake: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	decoder := json.NewDecoder(conn)
	var got string
	if err = decoder.Decode(&got); err != nil || got != auth {
		conn.Close()
		s.close()
		return errors.New("gosource: unauthenticated dependency bridge")
	}
	// The process has loaded its executable and authenticated its connection.
	// Removing build inputs now also prevents abrupt host termination from
	// leaving helper effects in the caller's TMPDIR. Close retries cleanup on
	// systems which cannot unlink an executable while it is running.
	cleanup()
	_ = conn.SetDeadline(time.Time{})
	bashPPBridgeSocketBuffers(conn)
	s.mu.Lock()
	s.conn = conn
	s.encoder = bashPPBridgeEncoder(conn)
	s.verifiedPlan = req.identityPlan
	s.mu.Unlock()
	identity := req.sessionIdentity()
	s.imports, s.locals, s.embeds = identity.imports, identity.locals, identity.embeds
	s.companions, s.cgo = identity.companions, identity.cgo
	s.instances, s.genericTypes, s.selectors = identity.instances, identity.generics, identity.selectors
	go func() {
		for {
			var reply bashPPBridgeResponse
			if err := decoder.Decode(&reply); err != nil {
				return
			}
			if reply.Op == "handle-ack" {
				s.ackHandleReply(reply.ID)
				continue
			}
			if reply.Op == "callback-mailbox-wake" {
				mailbox.notify()
				continue
			}
			s.adoptHandleResponse(&reply)
			if reply.Op == "callback" {
				// The waiting request executes its own callback; this reader
				// remains available for nested dependency replies.
				s.mu.Lock()
				callbacks := s.activeCallbacks
				if routed := s.routes[reply.Parent]; reply.Parent != 0 && routed != nil {
					callbacks = routed
				}
				s.mu.Unlock()
				if callbacks != nil {
					select {
					case callbacks <- reply:
					case <-s.done:
						return
					}
				} else {
					s.serveCallback(ctx, nil, reply, nil, nil)
				}
				continue
			}
			s.mu.Lock()
			wait := s.pending[reply.ID]
			s.mu.Unlock()
			if wait != nil {
				wait <- reply
			}
		}
	}()
	mailboxStarted = true
	return nil
}

func (s *bashPPNativeSession) checkBridgeValue(v bashPPBridgeValue) error {
	if v.localReflect != nil || v.localCell != nil {
		return errors.New("gosource: interpreter-owned reflected value reached the dependency unmaterialized")
	}
	if (v.Kind == "handle" || v.Kind == "callback" || v.Origin != 0) && v.Session != s.id {
		return errors.New("gosource: native handle belongs to another dependency session")
	}
	for _, e := range v.Elements {
		if err := s.checkBridgeValue(e); err != nil {
			return err
		}
	}
	for _, e := range v.Fields {
		if err := s.checkBridgeValue(e); err != nil {
			return err
		}
	}
	for _, e := range v.Entries {
		if err := s.checkBridgeValue(e.Key); err != nil {
			return err
		}
		if err := s.checkBridgeValue(e.Value); err != nil {
			return err
		}
	}
	return nil
}

func (s *bashPPNativeSession) request(ctx context.Context, req bashPPEvalRequest, q bashPPBridgeRequest) ([]bashPPBridgeValue, error) {
	defer runtime.KeepAlive(&q)
	if q.Receiver != nil && residentSyncValue(*q.Receiver) != nil {
		return nil, fmt.Errorf("resident sync cannot enter native transport")
	}
	for _, arg := range q.Args {
		if residentSyncValue(arg) != nil {
			return nil, fmt.Errorf("resident sync cannot enter native transport")
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A synchronous consumer cannot retain a callback after its reply. Drop
	// registrations created solely while preparing this request at that exact
	// ownership boundary; retained and result-owned consumers keep their
	// session registrations until their documented later lifetime ends.
	if synchronousFunctionCallback(req, q) {
		handles := bashPPNewCallbackHandles(q)
		defer s.releaseCallbackFunctions(handles)
	}
	// The package sort ordering entry points run over the interpreter's own
	// storage when they carry original callbacks, so the callbacks and the
	// algorithm share one backing array (bashpp_native_shared_order.go).
	if req.CallbackOwner != nil {
		if values, handled, err := req.CallbackOwner.goSourceSharedOrdering(ctx, req, &q); handled || err != nil {
			return values, err
		}
		// container/heap likewise runs over the original receiver
		// (bashpp_native_shared_heap.go).
		if values, handled, err := req.CallbackOwner.goSourceSharedHeap(ctx, req, &q); handled || err != nil {
			return values, err
		}
	}
	if requestHasCallbacks(req, q) && !synchronousFunctionCallback(req, q) {
		proven := dependencyFunctionCallbackLifetimeProof(ctx, req, q)
		if requestFunctionCallbackArgs(q) {
			q.sourceSynchronousCallback = proven
		} else {
			q.sourceSynchronousMethodCallback = proven
		}
	}
	if err := prepareNativeSliceBuffers(ctx, req, &q); err != nil {
		if errors.Is(err, errNativeCopiedSliceCallback) && nativePoolPutDroppable(q) {
			return nil, nil
		}
		return nil, err
	}
	// A generic slices helper (slices.Equal/slices.Sort) is not a reflectable
	// dependency symbol, so it is answered interpreter-side over the values that
	// already crossed the collection transport, before the dependency dispatch.
	if req.CallbackOwner != nil {
		if values, handled, err := req.CallbackOwner.nativeSliceGenericHelper(ctx, req, &q); handled || err != nil {
			return values, err
		}
	}
	bashPPReflectTypeOnly(req, &q)
	bashPPReflectFunctionType(req, &q)
	if err := validateLocalTransport(req, q); err != nil {
		return nil, err
	}
	q.PanicOnFault = req.PanicOnFault
	testingM, testingBarrier := nativeTestingCallbackBarrier(q)
	var testingFrames *bashPPTestingCallbackFrames
	if req.CallbackOwner != nil {
		testingFrames = req.CallbackOwner.bashPPTools.testingCallbackFrames
	}
	if testingM && testingFrames == nil {
		testingFrames = new(bashPPTestingCallbackFrames)
		defer testingFrames.closeFrames()
	}
	// A transfer hands the dependency slices whose elements carry original
	// callbacks; the callee may keep them past this call exactly as a
	// registration API does, so the session serves callbacks from now on.
	if retainedFunctionCallback(req, q) || len(q.Transfers) > 0 && requestHasCallbacks(req, q) {
		s.markRetainedCallbacks()
	}
	routed := routedCallbackRequest(req, q)
	var callbacks chan bashPPBridgeResponse
	var err error
	if routed {
		q.Route = true
		callbacks = make(chan bashPPBridgeResponse, 1)
	} else {
		var release func()
		release, callbacks, err = s.enterCallbacks(ctx, req, q)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	if err := s.begin(ctx, req); err != nil {
		return nil, err
	}
	s.mu.Lock()
	requestMailbox := s.mailbox
	s.mu.Unlock()
	if requestMailbox != nil && requestMailbox.retain() {
		defer requestMailbox.release()
	} else {
		requestMailbox = nil
	}
	for _, arg := range q.Args {
		if err := s.checkBridgeValue(arg); err != nil {
			return nil, err
		}
	}
	if q.Receiver != nil {
		if err := s.checkBridgeValue(*q.Receiver); err != nil {
			return nil, err
		}
	}
	q.ID = s.next.Add(1)
	wait := make(chan bashPPBridgeResponse, 1)
	s.mu.Lock()
	s.pending[q.ID] = wait
	if routed {
		if s.routes == nil {
			s.routes = map[uint64]chan bashPPBridgeResponse{}
		}
		s.routes[q.ID] = callbacks
	}
	s.mu.Unlock()
	var originSet map[uint64]bool
	defer func() {
		s.mu.Lock()
		delete(s.pending, q.ID)
		delete(s.routes, q.ID)
		s.mu.Unlock()
		s.closeOriginRequest(originSet)
	}()
	if os.Getenv("S374_ORIGIN_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "s374send: op=%s sel=%s nargs=%d\n", q.Op, q.Selector, len(q.Args))
	}
	err = s.sendMessage(func() any {
		q.Releases = s.takeHandleReleases()
		originSet = collectBridgeOrigins(&q)
		q.OriginReleases = s.openOriginRequest(originSet)
		return q
	})
	if err != nil {
		// The worker never saw this batch; re-queue it for the next
		// request rather than dropping the releases with the failed send.
		if len(q.OriginReleases) > 0 {
			s.mu.Lock()
			s.originReleaseQueue = append(q.OriginReleases, s.originReleaseQueue...)
			s.mu.Unlock()
		}
		return nil, s.closedWriteError(ctx, err)
	}
	var mailboxIdle bashPPMailboxIdle
	var mailboxLastServe time.Time
	// This parked request owns its callback channel for the loop's whole
	// lifetime: an unrelated top-level request waits on the callback gate
	// before it can install its own channel, and a nested request runs on
	// this same goroutine and restores the previous owner before this loop
	// resumes. Re-reading the owner under the session mutex on every
	// iteration — including every mailbox spin — only contends that lock
	// across parallel requests, so read it once. Mailbox liveness itself
	// is still checked per served callback below.
	s.mu.Lock()
	mailboxActive := requestMailbox != nil && callbacks != nil && s.activeCallbacks == callbacks
	s.mu.Unlock()
	for {
		if mailboxActive {
			if slot, callback, ok := requestMailbox.take(); ok {
				now := time.Now()
				mailboxIdle.reset(now, mailboxLastServe)
				mailboxLastServe = now
				callbackFrames := testingFrames
				if !testingBarrier {
					callbackFrames = nil
				}
				frame, frameFn, scheduled, frameErr := s.testingCallbackFrame(callbackFrames, callback)
				if frameErr != nil {
					answer := bashPPBridgeRequest{ID: callback.ID, Op: "callback-reply", Error: fmt.Sprintf("gosource: testing callback frame: %v", frameErr)}
					s.answerMailboxCallback(requestMailbox, slot, answer)
					continue
				}
				if scheduled {
					go func() {
						defer testingFrames.Done()
						defer testingFrames.finish(frame)
						answer := s.callbackAnswer(ctx, frame, callback, &q, frameFn)
						s.answerMailboxCallback(requestMailbox, slot, answer)
					}()
					continue
				}
				answer := s.callbackAnswer(ctx, req.CallbackOwner, callback, &q, nil)
				s.mu.Lock()
				mailboxAlive, waitErr := s.mailbox == requestMailbox, s.waitErr
				s.mu.Unlock()
				if !mailboxAlive {
					if canceled := s.canceledTermination(ctx, waitErr); canceled != nil {
						return nil, canceled
					}
					return nil, s.programExitError(waitErr)
				}
				s.answerMailboxCallback(requestMailbox, slot, answer)
				if owner := req.CallbackOwner; owner != nil {
					// A nested os.Exit has already made this the program's process
					// outcome. Keep that identity through the enclosing request.
					if owner.exit.exiting {
						return nil, &bashPPNativeExit{status: int(owner.exit.code)}
					}
					if owner.exit.err != nil {
						return nil, owner.exit.err
					}
				}
				continue
			}
		}
		// Poll only while this request owns an active mailbox. Ordinary
		// requests block on their channels, and each event is consumed once.
		var callback bashPPBridgeResponse
		var reply bashPPBridgeResponse
		var event uint8
		if mailboxActive {
			select {
			case callback = <-callbacks:
				event = 1
			case reply = <-wait:
				event = 2
			case <-ctx.Done():
				event = 3
			case <-s.done:
				event = 4
			default:
				if mailboxIdle.spin() || !requestMailbox.park() {
					continue
				}
				select {
				case callback = <-callbacks:
					event = 1
				case reply = <-wait:
					event = 2
				case <-ctx.Done():
					event = 3
				case <-s.done:
					event = 4
				case <-requestMailbox.wake:
				}
				requestMailbox.unpark()
				if event == 0 {
					mailboxIdle.reset(time.Now(), mailboxLastServe)
					continue
				}
			}
		} else {
			mailboxIdle.reset(time.Now(), mailboxLastServe)
			select {
			case callback = <-callbacks:
				event = 1
			case reply = <-wait:
				event = 2
			case <-ctx.Done():
				event = 3
			case <-s.done:
				event = 4
			}
		}
		mailboxIdle.reset(time.Now(), mailboxLastServe)
		switch event {
		case 1:
			// The callback installs a write barrier on the interpreter's
			// streams. If it writes directly, earlier child output lands first;
			// a callback with no direct output needs no pipe round trip.
			if testingBarrier && testingFrames != nil {
				frame, frameFn, scheduled, frameErr := s.testingCallbackFrame(testingFrames, callback)
				if frameErr != nil {
					s.serveCallback(ctx, nil, callback, &q, nil)
					return nil, fmt.Errorf("gosource: testing callback frame: %w", frameErr)
				}
				if !scheduled {
					if routed {
						req.CallbackOwner.bashPPTools.routedDepth++
					}
					s.serveCallback(ctx, req.CallbackOwner, callback, &q, nil)
					if routed {
						req.CallbackOwner.bashPPTools.routedDepth--
					}
					break
				}
				go func() {
					defer testingFrames.Done()
					defer testingFrames.finish(frame)
					if routed {
						frame.bashPPTools.routedDepth++
					}
					s.serveCallback(ctx, frame, callback, &q, frameFn)
				}()
			} else {
				if routed {
					req.CallbackOwner.bashPPTools.routedDepth++
				}
				s.serveCallback(ctx, req.CallbackOwner, callback, &q, nil)
				if routed {
					req.CallbackOwner.bashPPTools.routedDepth--
				}
			}
			if owner := req.CallbackOwner; owner != nil {
				if owner.exit.err != nil {
					return nil, owner.exit.err
				}
				if owner.exit.exiting {
					return nil, &bashPPNativeExit{status: int(owner.exit.code)}
				}
			}
		case 2:
			if testingM && testingFrames != nil {
				if err := testingFrames.adopt(req.CallbackOwner); err != nil {
					return nil, err
				}
			}
			// The reply crossed the control channel after the dependency's own
			// writes; the barrier keeps the next interpreted statement behind them.
			// A probed select that chose default did not communicate with a
			// sender. Pure time.Since and time.Duration.Round calls cannot
			// write output either. Earlier requests already drained their own
			// output, so these replies need no pipe barrier. This matters in
			// polling loops with short sleeps.
			if !bashPPNativeNoOutputReply(req, q, reply) {
				s.awaitOutputBarriers(reply)
			}
			// A written-back element may nest native values this session
			// still owns — a reflect.Type inside a reflect.StructField, a
			// reflect.Value the worker re-minted a handle for. They arrived
			// on its own authenticated connection, so they carry its
			// identity, exactly as a pointer writeback's pointee does; a
			// handle from any other session still fails closed downstream.
			for i := range reply.SliceUpdates {
				s.bashPPAuthenticateCallbackValue(&reply.SliceUpdates[i].Value)
			}
			if err := applyNativeSliceBuffers(req.CallbackOwner, q, reply); err != nil {
				return nil, err
			}
			// The worker reports only pointees whose structural snapshot changed,
			// so applying every reported update preserves retained-pointer writes
			// without replaying stale native copies over interpreter state.
			if os.Getenv("S374_ORIGIN_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "s374reply: op=%s sel=%s ptrupdates=%d values=%d err=%q\n", q.Op, q.Selector, len(reply.PtrUpdates), len(reply.Values), reply.Error)
			}
			if err := s.applyNativePointerUpdates(req, reply); err != nil {
				return nil, err
			}
			if reply.Panic != nil && reply.Panic.Kind == bashPPGoexitKind {
				return nil, &bashPPCallbackGoexit{}
			}
			if reply.Panic != nil {
				return nil, &bashPPCallbackPanic{value: reply.Panic.stringText()}
			}
			if reply.Error != "" {
				s.mu.Lock()
				forwarded := s.forwardedSignal > 0
				s.mu.Unlock()
				// A callback may already have serialized the private unwind token
				// before the signal closes the helper. Reattach its identity only
				// when this session actually forwarded that program signal.
				if forwarded && strings.HasSuffix(reply.Error, errBashPPScalarInterrupted.Error()) {
					return nil, errBashPPScalarInterrupted
				}
				if message, ok := goSourceNativeRuntimePanic(errors.New(reply.Error)); ok && reply.PanicAddr != nil {
					return nil, &bashPPNativeFaultPanic{text: message, addr: *reply.PanicAddr}
				}
				return nil, errors.New(reply.Error)
			}
			// The dependency now holds each transferred slice; rebind the
			// interpreter's supplying variable to that same storage.
			if err := s.applyNativeSliceTransfers(q, reply); err != nil {
				return nil, err
			}
			derivedCopy := reflectedCopyDerived(req, q)
			for i := range reply.Values {
				if v := &reply.Values[i]; v.Kind == "uint" && v.Type == "uintptr" && v.Origin != 0 && v.Session == s.id {
					if address, err := strconv.ParseUint(v.Text, 10, 64); err == nil {
						if ptr, ok := s.originLookup(v.Origin); ok && ptr != nil {
							s.mu.Lock()
							if s.addressOrigins == nil {
								s.addressOrigins = make(map[uint64]*bashPPPointer)
							}
							s.addressOrigins[address] = ptr
							s.mu.Unlock()
							// An integer address escapes to native code, where
							// it can be retained and reforged invisibly: the
							// origin takes the conservative path.
							s.pinTransportOrigin(v.Origin)
						}
					}
				}
				bashPPMarkReflectCopy(&reply.Values[i], derivedCopy)
				if reply.Values[i].Kind == "handle" {
					if reflectedOriginalFunctionValueOf(req, q) {
						reply.Values[i].reflectFunction = true
						reply.Values[i].callRefusal = q.Args[0].callRefusal
						if q.Args[0].Session == s.id {
							s.mu.Lock()
							reply.Values[i].reflectFunc = s.functions[q.Args[0].Handle]
							s.mu.Unlock()
						}
					}
					if q.Receiver != nil && q.Receiver.reflectFunction && q.Selector == "Type" {
						reply.Values[i].Callbacks = false
					}
					reply.Values[i].Session = s.id
					s.rememberNativeHandleType(reply.Values[i])
					if reply.Values[i].Origin != 0 && reply.Values[i].Function {
						reply.Values[i].Callbacks = true
					}
					s.rememberMadeFunc(req, q, reply.Values[i])
					// Only a result of a request that actually CARRIED an
					// original callback may retain one. A request merely
					// parked as a callback server for the session — every
					// request once a retained handler is registered — hands
					// its callbacks to nothing and marks nothing.
					if requestHasCallbacks(req, q) && !synchronousFunctionCallback(req, q) && !bashPPTypeDescriptorResult(req, q) && !reflectedMethodValueOf(req, q) && !(q.Receiver != nil && q.Receiver.reflectFunction && q.Selector == "Type") {
						reply.Values[i].Callbacks = true
					}
				}
			}
			if owner := req.CallbackOwner; owner != nil && bashPPSetPanicOnFaultRequest(req, q) {
				owner.bashPPTools.panicOnFault = q.Args[0].Text == "true"
			}
			return reply.Values, nil
		case 3:
			s.closeCanceled(ctx.Err())
			return nil, ctx.Err()
		case 4:
			s.waitDrains()
			s.mu.Lock()
			err := s.waitErr
			s.mu.Unlock()
			if canceled := s.canceledTermination(ctx, err); canceled != nil {
				return nil, canceled
			}
			return nil, s.programExitError(err)
		}
	}
}

func (s *bashPPNativeSession) programExitError(err error) error {
	if err == nil {
		// A clean exit is the original program terminating itself, for example
		// os.Exit(0) or a successful syscall.Exec replacement.
		return &bashPPNativeExit{}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if exit.ExitCode() >= 0 {
			code := exit.ExitCode()
			forwarded := false
			if runtime.GOOS == "windows" && code == 0xC000013A {
				s.mu.Lock()
				forwarded = s.forwardedSignal > 0
				s.mu.Unlock()
			}
			return &bashPPNativeExit{status: code, err: err, forwarded: forwarded}
		}
		s.mu.Lock()
		forwardedSignal := s.forwardedSignal
		s.mu.Unlock()
		if forwardedSignal > 0 {
			return &bashPPNativeExit{status: 128 + forwardedSignal, err: err, forwarded: true}
		}
	}
	return fmt.Errorf("gosource: dependency process exited: %w", err)
}
func bashPPNativeSource(ctx context.Context, req bashPPEvalRequest) (string, error) {
	// Import package export data through the same reviewed SDK and module context.
	// go doc -json is not an API and is deliberately not used.
	lookup := func(path string) (io.ReadCloser, error) {
		cmd := exec.CommandContext(ctx, req.internalBuildGo(), "list", "-export", "-f", "{{.Export}}", path)
		cmd.Dir, cmd.Env = req.Dir, req.internalBuildEnv()
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("gosource: export %s: %w", path, err)
		}
		return os.Open(strings.TrimSpace(string(out)))
	}
	imp := importer.ForCompiler(token.NewFileSet(), "gc", lookup)
	paths := map[string][]string{}
	for alias, path := range req.Imports {
		paths[path] = append(paths[path], alias)
	}
	typeImports, err := bashPPNativeSyntheticTypeImports(ctx, req)
	if err != nil {
		return "", err
	}
	typeImportPaths := map[string]bool{}
	for alias, path := range typeImports {
		paths[path] = append(paths[path], alias)
		typeImportPaths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	var imports, symbols, typeEntries strings.Builder
	var companionSymbols, unmappedFrames, forcing strings.Builder
	importAliases := map[string]string{}
	materialised := map[string]bool{}
	for _, local := range req.LocalTypes {
		materialised[local.Name] = true
	}
	usedAliases := make(map[string]bool, len(req.Imports)+len(req.MappedCompanions))
	for alias := range req.Imports {
		usedAliases[alias] = true
	}
	for i, pkg := range req.MappedCompanions {
		alias := fmt.Sprintf("bppcompanion%d", i)
		for usedAliases[alias] {
			alias += "_"
		}
		usedAliases[alias] = true
		fmt.Fprintf(&imports, "%s %q\n", alias, pkg.Path)
		for _, fn := range pkg.Funcs {
			if !syntax.BashPPValidIdent(fn.RuntimeName) {
				return "", fmt.Errorf("gosource: invalid mapped companion runtime function %q", fn.RuntimeName)
			}
			fmt.Fprintf(&symbols, "%q: reflect.ValueOf(%s.%s()[%q]),\n", fn.RuntimeName, alias, pkg.Hook, fn.Name)
			fmt.Fprintf(&companionSymbols, "%q: true,\n", fn.RuntimeName)
		}
	}
	// Every package's helper alias is settled before any symbol is
	// emitted: an instantiation registered under one package names types
	// of others (`errors.AsType[*fs.PathError]`), whichever order the
	// packages come in.
	// referenced restricts the exported func/var/const entries to the selectors
	// the program actually reaches (bashPPReferencedSelectors). An empty set
	// means the caller could not prove the program's reach, so nothing is
	// filtered and every export is registered as before. keepSymbol is asked
	// per emitted key; a dot-imported package contributes bare-name symbols the
	// selector walk does not collect, so those are never filtered.
	referenced := make(map[string]bool, len(req.Selectors))
	for _, sel := range req.Selectors {
		referenced[sel] = true
	}
	filterSymbols := len(referenced) > 0
	keepSymbol := func(key, name string) bool {
		if !filterSymbols || strings.HasPrefix(key, ".:") {
			return true
		}
		return referenced[key+"."+name]
	}
	blankOnly := map[string]bool{}
	for i, path := range ordered {
		if path == "C" {
			continue
		}
		blankOnly[path] = true
		for _, alias := range paths[path] {
			if !strings.HasPrefix(alias, "_:") {
				blankOnly[path] = false
			}
		}
		if blankOnly[path] {
			continue
		}
		for _, original := range paths[path] {
			importAliases[original] = fmt.Sprintf("bpppkg%d", i)
		}
	}
	for i, path := range ordered {
		if path == "C" {
			continue
		}
		if blankOnly[path] {
			fmt.Fprintf(&imports, "_ %q\n", path)
			continue
		}
		pkg, err := imp.Import(path)
		if err != nil {
			return "", err
		}
		alias := fmt.Sprintf("bpppkg%d", i)
		fmt.Fprintf(&imports, "%s %q\n", alias, path)
		used := typeImportPaths[path] || bashPPGenericTypesUsePackage(req.GenericTypes, paths[path], path)
		for _, name := range pkg.Scope().Names() {
			obj := pkg.Scope().Lookup(name)
			if !obj.Exported() {
				continue
			}
			keyNames := append([]string(nil), paths[path]...)
			switch obj := obj.(type) {
			case *types.TypeName:
				if alias, ok := obj.Type().(*types.Alias); ok && alias.TypeParams().Len() > 0 {
					continue
				}
				if iface, ok := obj.Type().Underlying().(*types.Interface); ok && !iface.IsMethodSet() {
					continue
				}
				if named, ok := obj.Type().(*types.Named); ok && named.TypeParams().Len() > 0 {
					continue
				}
				// A package bound under its own name (`import "strings"`
				// from Go source) is keyed once: alias and path coincide.
				keyed := map[string]bool{}
				for _, key := range append(keyNames, path) {
					if strings.HasPrefix(key, "_:") || strings.HasPrefix(key, ".:") || keyed[key] {
						continue
					}
					keyed[key] = true
					// TypeOf((*T)(nil)).Elem() is TypeFor's own definition,
					// spelled without a type argument: a type only the
					// compiler knows to be unallocatable (a struct embedding
					// internal/runtime/sys.NotInHeap) cannot instantiate
					// TypeFor but is an ordinary pointee.
					fmt.Fprintf(&typeEntries, "%q: reflect.TypeOf((*%s.%s)(nil)).Elem(),\n", key+"."+name, alias, name)
				}
				used = true
			case *types.Func:
				if sig, ok := obj.Type().(*types.Signature); ok && sig.TypeParams().Len() > 0 {
					// A generic function is a value only once instantiated:
					// the instantiations the program reaches are registered
					// under their instantiated spelling.
					emitted, err := bashPPImportedInstanceSymbols(&symbols, req.Instances, keyNames, name, alias, sig.TypeParams().Len(), importAliases, materialised)
					if err != nil {
						return "", err
					}
					used = used || emitted
					continue
				}
				if !bashPPNativeFuncValueAvailable(path, name) {
					continue
				}
				for _, key := range keyNames {
					if strings.HasPrefix(key, "_:") || !keepSymbol(key, name) {
						continue
					}
					symbol := key + "." + name
					if strings.HasPrefix(key, ".:") {
						symbol = name
					}
					fmt.Fprintf(&symbols, "%q: reflect.ValueOf(%s.%s),\n", symbol, alias, name)
					if bashPPForcesCollection(path, name) {
						fmt.Fprintf(&forcing, "%q: true,\n", symbol)
					}
					used = true
				}
			case *types.Var:
				for _, key := range keyNames {
					if strings.HasPrefix(key, "_:") || !keepSymbol(key, name) {
						continue
					}
					symbol := key + "." + name
					if strings.HasPrefix(key, ".:") {
						symbol = name
					}
					fmt.Fprintf(&symbols, "%q: reflect.ValueOf(&%s.%s).Elem(),\n", symbol, alias, name)
					used = true
				}
			case *types.Const:
				expression := alias + "." + name
				if basic, ok := obj.Type().(*types.Basic); ok && basic.Info()&types.IsUntyped != 0 {
					switch obj.Val().Kind() {
					case constant.Int:
						if _, ok := constant.Int64Val(obj.Val()); ok {
							expression = "int64(" + expression + ")"
						} else if _, ok := constant.Uint64Val(obj.Val()); ok {
							expression = "uint64(" + expression + ")"
						} else {
							continue
						}
					case constant.Complex:
						continue
					}
				}
				for _, key := range keyNames {
					if strings.HasPrefix(key, "_:") || !keepSymbol(key, name) {
						continue
					}
					symbol := key + "." + name
					if strings.HasPrefix(key, ".:") {
						symbol = name
					}
					fmt.Fprintf(&symbols, "%q: reflect.ValueOf(%s),\n", symbol, expression)
					used = true
				}
			}
		}
		if !used {
			fmt.Fprintf(&symbols, "// blank dependency %s\n", path)
			text := imports.String()
			imports.Reset()
			imports.WriteString(strings.Replace(text, alias+" ", "_ ", 1))
		}
	}
	localTypeEntries := bashPPNativeLocalTypeEntries(req.LocalTypes)
	for _, typ := range req.GenericTypes {
		if localTypeEntries[typ] || !bashPPNativeGenericTypeDeclaredInWorker(typ) {
			continue
		}
		mapped, err := bashPPNativeTypeImports(typ, importAliases)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&typeEntries, "%q: reflect.TypeFor[%s](),\n", typ, mapped)
	}
	var embeds strings.Builder
	for i, embed := range req.EmbedDecls {
		typ, err := bashPPNativeTypeImports(embed.Type, importAliases)
		if err != nil {
			return "", err
		}
		name := fmt.Sprintf("__bashpp_embed_%d", i)
		for _, directive := range embed.Directives {
			fmt.Fprintf(&embeds, "//%s\n", directive)
		}
		fmt.Fprintf(&embeds, "var %s %s\n", name, typ)
		fmt.Fprintf(&symbols, "%q: reflect.ValueOf(&%s).Elem(),\n", bashPPEmbedSymbolPrefix+embed.Name, name)
	}
	// Original locally declared types become real declarations here, so the
	// dependency sees the original field names, field types and defined type
	// name. Only the declaration crosses over: a mirrored String/Error body is
	// a fixed callback into the interpreter, never compiled original code.
	var locals strings.Builder
	localTypes := append([]bashPPLocalType(nil), req.LocalTypes...)
	for i := range localTypes {
		mapped, err := bashPPNativeTypeImports(localTypes[i].Decl, importAliases)
		if err != nil {
			return "", err
		}
		localTypes[i].Decl = mapped
		if localTypes[i].GenericDecl != "" {
			mapped, err = bashPPNativeDeclImports(localTypes[i].GenericDecl, importAliases)
			if err != nil {
				return "", err
			}
			localTypes[i].GenericDecl = mapped
		}
		if localTypes[i].PublicType != "" {
			mapped, err = bashPPNativeTypeImports(localTypes[i].PublicType, importAliases)
			if err != nil {
				return "", err
			}
			localTypes[i].PublicType = mapped
		}
		// Mirrored signatures name imported types the same way the original
		// program did; the helper knows them only by its own generated aliases.
		methods := append([]bashPPLocalMethod(nil), localTypes[i].Methods...)
		rewrite := func(list []string) ([]string, error) {
			out := make([]string, len(list))
			for k, text := range list {
				mapped, err := bashPPNativeTypeImports(text, importAliases)
				if err != nil {
					return nil, err
				}
				out[k] = mapped
			}
			return out, nil
		}
		for j := range methods {
			// Copy rather than rewrite in place: the caller's descriptors are
			// the session identity key and must keep the original spellings.
			if methods[j].Params, err = rewrite(methods[j].Params); err != nil {
				return "", err
			}
			if methods[j].Results, err = rewrite(methods[j].Results); err != nil {
				return "", err
			}
		}
		localTypes[i].Methods = methods
		generic := append([]bashPPLocalMethod(nil), localTypes[i].GenericMethods...)
		for j := range generic {
			if generic[j].Params, err = rewrite(generic[j].Params); err != nil {
				return "", err
			}
			if generic[j].Results, err = rewrite(generic[j].Results); err != nil {
				return "", err
			}
		}
		localTypes[i].GenericMethods = generic
	}
	for _, local := range localTypes {
		locals.WriteString(bashPPLocalTypeGo(local))
		// Aliases inherit their target's identity; registering an alias here would
		// overwrite the metadata of that target's exact reflect.Type.
		if id := local.Identity; id != nil && !local.Alias {
			fmt.Fprintf(&locals, "func init(){originalTypeIdentities[reflect.TypeFor[%s]()] = originalTypeIdentity{Name:%q, PackageName:%q, PackagePath:%q}}\n", local.Name, id.Name, id.PackageName, id.PackagePath)
		}

		// Both spellings resolve: the original program's own name, and the
		// package-qualified identity Go's %T prints for it.
		fmt.Fprintf(&typeEntries, "%q: reflect.TypeFor[%s](),\n%q: reflect.TypeFor[%s](),\n", local.Name, local.Name, "main."+local.Name, local.Name)
		if local.WireType != "" {
			fmt.Fprintf(&typeEntries, "%q: reflect.TypeFor[%s](),\n", local.WireType, local.Name)
		}
	}
	for _, fn := range req.NativeFuncs {
		if !syntax.BashPPValidIdent(fn.Name) {
			return "", fmt.Errorf("gosource: invalid native companion function %q", fn.Name)
		}
		fmt.Fprintf(&companionSymbols, "%q: true,\n", fn.Name)
		params, results, err := bashPPNativeFuncSignatureImports(fn.Params, fn.Results, importAliases)
		if err != nil {
			return "", err
		}
		if fn.Linkname != "" {
			fmt.Fprintf(&locals, "//go:linkname %s %s\n", fn.Name, fn.Linkname)
		}
		if results == "" {
			fmt.Fprintf(&locals, "func %s(%s)\n", fn.Name, params)
		} else {
			fmt.Fprintf(&locals, "func %s(%s)(%s)\n", fn.Name, params, results)
		}
		fmt.Fprintf(&symbols, "%q: reflect.ValueOf(%s),\n", fn.Name, fn.Name)
	}
	// A companion object may call a package function whose body is interpreted.
	// The trampoline is what makes the companion link; it compiles no original
	// statement, it only carries the call back to the interpreter that owns it.
	for _, fn := range req.CompanionTrampolines {
		if !syntax.BashPPValidIdent(fn.Name) {
			return "", fmt.Errorf("gosource: invalid assembly companion trampoline %q", fn.Name)
		}
		params, err := bashPPNativeTypeTexts(fn.Params, importAliases)
		if err != nil {
			return "", err
		}
		results, err := bashPPNativeTypeTexts(fn.Results, importAliases)
		if err != nil {
			return "", err
		}
		locals.WriteString(bashPPCompanionTrampolineGo(fn.Name, params, results))
	}
	for pi, pkg := range req.CgoPackages {
		for _, symbol := range pkg.Symbols {
			if symbol.Kind != "func" {
				return "", fmt.Errorf("gosource: cgo package %q selector C.%s has unsupported kind %q", pkg.Path, symbol.Name, symbol.Kind)
			}
			fmt.Fprintf(&symbols, "%q: reflect.ValueOf(__bashpp_cgo_%d_%s),\n", pkg.Alias+"."+symbol.Name, pi, symbol.Name)
		}
	}
	// The frames the runtime has no pointer map for. Their names are reported
	// back in the refusal a forced collection gets while one of them is live.
	for _, name := range req.CompanionUnmappedFrames {
		fmt.Fprintf(&unmappedFrames, "%q,\n", name)
	}
	codecs, err := bashPPLocalCodecsGo(localTypes)
	if err != nil {
		return "", err
	}
	locals.WriteString(codecs)
	source := strings.Replace(bashPPNativeWorker, "//IMPORTS", imports.String(), 1)
	source = strings.Replace(source, "//ROUTESLOTS", bashPPRouteSlotWorkerSource(), 1)
	source = strings.Replace(source, "//EMBEDS", embeds.String(), 1)
	source = strings.Replace(source, "//SYMBOLS", symbols.String(), 1)
	source = strings.Replace(source, "//TYPES", typeEntries.String(), 1)
	source = strings.Replace(source, "//LOCALTYPES", locals.String(), 1)
	source = strings.Replace(source, "//COMPANIONSYMBOLS", companionSymbols.String(), 1)
	source = strings.Replace(source, "//UNMAPPEDFRAMES", unmappedFrames.String(), 1)
	source = strings.Replace(source, "//FORCESGC", forcing.String(), 1)
	return source, nil
}

// bashPPNativeLocalTypeEntries returns every spelling represented by a local
// helper declaration. It remains useful to callers that compare the registry
// universe with materialised declarations.
func bashPPNativeLocalTypeEntries(locals []bashPPLocalType) map[string]bool {
	entries := make(map[string]bool, len(locals)*2)
	for _, local := range locals {
		entries[local.Name] = true
		if local.WireType == "" {
			continue
		}
		entries[local.WireType] = true
		if typ := syntax.BashPPTypeExprFromText(local.WireType); typ != nil {
			entries[bashPPBridgeTypeText(typ)] = true
		}
	}
	return entries
}

// bashPPNativeGenericTypeDeclaredInWorker reports whether a generic registry
// candidate names a declaration available to the generated worker. A local
// generic declaration is never copied wholesale: only the concrete instances
// in LocalTypes are emitted under helper names and registered through WireType.
// In contrast, a package-qualified constructor is available from the worker's
// generated import and needs a direct reflect registration.
func bashPPNativeGenericTypeDeclaredInWorker(text string) bool {
	named, ok := syntax.BashPPTypeExprFromText(text).(*syntax.BashPPNamedType)
	return ok && named.Name != nil && strings.Contains(named.Name.Value, ".")
}

// bashPPNativeSyntheticTypeImports finds package-qualified names that appear
// only in generated helper type positions. These can come from export-data
// type strings, for example strings.SplitSeq's iter.Seq result: the original
// program imported strings, not iter, but generated signatures still need a
// real helper import for the named result type.
func bashPPNativeSyntheticTypeImports(ctx context.Context, req bashPPEvalRequest) (map[string]string, error) {
	known := map[string]bool{}
	for alias := range req.Imports {
		known[strings.TrimPrefix(strings.TrimPrefix(alias, "_:"), ".:")] = true
	}
	needed := map[string]bool{}
	addExpr := func(text string) error {
		if text == "" {
			return nil
		}
		expr, err := parser.ParseExpr(text)
		if err != nil {
			return err
		}
		ast.Inspect(expr, func(node ast.Node) bool {
			if sel, ok := node.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && !known[id.Name] {
					needed[id.Name] = true
				}
			}
			return true
		})
		return nil
	}
	addDecl := func(decl string) error {
		if decl == "" {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), "generated.go", "package main\n"+decl, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if sel, ok := node.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && !known[id.Name] {
					needed[id.Name] = true
				}
			}
			return true
		})
		return nil
	}
	addSignature := func(params, results string) error {
		source := "func _(" + params + ")"
		if results != "" {
			source += "(" + results + ")"
		}
		return addDecl(source)
	}
	for _, typ := range req.GenericTypes {
		if err := addExpr(typ); err != nil {
			return nil, err
		}
	}
	for _, embed := range req.EmbedDecls {
		if err := addExpr(embed.Type); err != nil {
			return nil, err
		}
	}
	for _, local := range req.LocalTypes {
		for _, text := range []string{local.Decl, local.PublicType} {
			if err := addExpr(text); err != nil {
				return nil, err
			}
		}
		if err := addDecl(local.GenericDecl); err != nil {
			return nil, err
		}
		for _, method := range append(append([]bashPPLocalMethod(nil), local.Methods...), local.GenericMethods...) {
			if err := addExpr(method.Receiver); err != nil {
				return nil, err
			}
			for _, text := range append(append([]string(nil), method.Params...), method.Results...) {
				if err := addExpr(text); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, fn := range req.NativeFuncs {
		if err := addSignature(fn.Params, fn.Results); err != nil {
			return nil, err
		}
	}
	for _, fn := range req.CompanionTrampolines {
		for _, text := range append(append([]string(nil), fn.Params...), fn.Results...) {
			if err := addExpr(text); err != nil {
				return nil, err
			}
		}
	}
	if len(needed) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	names := make([]string, 0, len(needed))
	for name := range needed {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path, err := bashPPNativeResolveTypeImport(ctx, req, name)
		if err != nil {
			return nil, err
		}
		out[name] = path
	}
	return out, nil
}

func bashPPNativeResolveTypeImport(ctx context.Context, req bashPPEvalRequest, name string) (string, error) {
	if req.internalBuildGo() == "" {
		return "", fmt.Errorf("gosource: generated type %s needs a package import but no Go toolchain is configured", name)
	}
	cmd := exec.CommandContext(ctx, req.internalBuildGo(), "list", "-f", "{{.Name}}\n{{.ImportPath}}", name)
	cmd.Dir, cmd.Env = req.Dir, req.internalBuildEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gosource: resolve generated type import %s: %w", name, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || lines[0] != name || lines[1] == "" {
		return "", fmt.Errorf("gosource: generated type import %s resolved to %q", name, strings.TrimSpace(string(out)))
	}
	return lines[1], nil
}

// bashPPNativeFuncValueAvailable reports exported dependency functions which
// can exist as ordinary Go function values in the pinned toolchain. FuncPCABI0
// and FuncPCABIInternal are Go 1.27.1 compiler intrinsics with declarations but
// no linker bodies. A direct compiler-recognised call is valid, but wrapping
// either in reflect.ValueOf asks the linker for FuncPCABI0·f/ABIInternal·f and
// then for the deliberately nonexistent underlying symbol.
func bashPPNativeFuncValueAvailable(path, name string) bool {
	if path != "internal/abi" {
		return true
	}
	switch name {
	case "FuncPCABI0", "FuncPCABIInternal":
		return false
	}
	return true
}

func bashPPEmbedIdentity(decls []bashPPEmbedDecl) string {
	data, _ := json.Marshal(decls)
	return string(data)
}

func bashPPGenericTypeIdentity(types []string) string {
	data, _ := json.Marshal(types)
	return string(data)
}

func bashPPGenericTypesUsePackage(types, keys []string, path string) bool {
	for _, typ := range types {
		for _, key := range keys {
			if strings.HasPrefix(key, "_:") {
				continue
			}
			if strings.Contains(typ, key+".") {
				return true
			}
		}
		if strings.Contains(typ, path+".") {
			return true
		}
	}
	return false
}

func bashPPNativeCompanionIdentity(files []string, funcs []bashPPNativeFuncDecl, mapped []bashPPMappedCompanion, trampolines []bashPPCompanionTrampoline, unmapped []string) string {
	data, _ := json.Marshal(struct {
		Files          []string
		Funcs          []bashPPNativeFuncDecl
		Mapped         []bashPPMappedCompanion
		Trampolines    []bashPPCompanionTrampoline
		UnmappedFrames []string
	}{files, funcs, mapped, trampolines, unmapped})
	return string(data)
}

// bashPPNativeTypeTexts maps declared type spellings into the helper's own
// import aliases, one entry per declared value.
func bashPPNativeTypeTexts(texts []string, aliases map[string]string) ([]string, error) {
	out := make([]string, len(texts))
	for i, text := range texts {
		mapped, err := bashPPNativeTypeImports(text, aliases)
		if err != nil {
			return nil, err
		}
		out[i] = mapped
	}
	return out, nil

}

func bashPPCgoIdentity(packages []syntax.CgoPackage) string {
	data, _ := json.Marshal(packages)
	return string(data)
}

func bashPPNativeFuncSignatureImports(params, results string, aliases map[string]string) (string, string, error) {
	source := "func _(" + params + ")"
	if results != "" {
		source += "(" + results + ")"
	}
	decl, err := bashPPNativeDeclImports(source, aliases)
	if err != nil {
		return "", "", err
	}
	decl = strings.TrimSpace(decl)
	open := strings.IndexByte(decl, '(')
	if open < 0 {
		return "", "", fmt.Errorf("gosource: malformed native companion signature %q", decl)
	}
	close := strings.IndexByte(decl[open+1:], ')')
	if close < 0 {
		return "", "", fmt.Errorf("gosource: malformed native companion signature %q", decl)
	}
	close += open + 1
	params = decl[open+1 : close]
	results = strings.TrimSpace(decl[close+1:])
	if strings.HasPrefix(results, "(") && strings.HasSuffix(results, ")") {
		results = strings.TrimSuffix(strings.TrimPrefix(results, "("), ")")
	}
	return params, results, nil
}

func bashPPBridgeLiteral(text string) (bashPPBridgeValue, error) {
	if text == "nil" {
		return bashPPBridgeValue{Kind: "nil"}, nil
	}
	if str, err := strconv.Unquote(text); err == nil {
		value := bashPPBridgeString(str)
		value.Type = ""
		return value, nil
	}
	if text == "true" || text == "false" {
		return bashPPBridgeValue{Kind: "bool", Text: text}, nil
	}
	for _, kind := range []token.Token{token.INT, token.FLOAT} {
		v := constant.MakeFromLiteral(text, kind, 0)
		if v.Kind() == constant.Unknown {
			continue
		}
		if kind == token.INT {
			return bashPPBridgeValue{Kind: "int", Text: v.ExactString()}, nil
		}
		number, _ := constant.Float64Val(v)
		return bashPPBridgeValue{Kind: "float", Text: strconv.FormatFloat(number, 'g', -1, 64)}, nil
	}
	return bashPPBridgeValue{}, fmt.Errorf("gosource: bridge accepts evaluated values, not argument source %q", text)
}
func (s *bashPPNativeSession) legacy(ctx context.Context, req bashPPEvalRequest) ([]bashPPBridgeValue, error) {
	args := make([]bashPPBridgeValue, len(req.Args))
	for i, text := range req.Args {
		v, err := bashPPBridgeLiteral(text)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	return s.request(ctx, req, bashPPBridgeRequest{Op: "call", Selector: strings.Join(req.Selector, "."), Args: args})
}

// GoSourceModuleDir binds dependency resolution to the original source module
// while Dir remains the executing program's cwd. The option is inert for shell
// source; callers should set it when Go source and runtime assets are separated.
func GoSourceModuleDir(dir string) RunnerOption {
	return func(r *Runner) error {
		absolute, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("gosource: module context is not a directory: %s", dir)
		}
		r.bashPPTools.moduleDir = absolute
		return nil
	}
}
func bashPPModuleRequest(req bashPPEvalRequest) bashPPEvalRequest {
	if req.ModuleDir != "" {
		req.Dir = req.ModuleDir
	}
	return req
}
func (r *Runner) bashPPStartGoSourceBridge(ctx context.Context) error {
	if !r.bashPPGoSource || len(r.bashPPImports) == 0 {
		return nil
	}
	req, err := r.bashPPEvalRequest()
	if err != nil {
		return err
	}
	if len(r.bashPPTools.reexecPlan) != 0 {
		// The worker snapshots Argv when it starts. Create the launcher before
		// that snapshot so os.Args[0], testing's test-binary identity, and the
		// os.Executable interception all name the same executable. The launcher
		// still only re-enters this interpreted program; it is not a native
		// fallback for it.
		if _, err := req.Bridge.goSourceReexecLauncher(ctx, req, r.bashPPTools.reexecPlan, r.tempDir); err != nil {
			return err
		}
		req, err = r.bashPPEvalRequest()
		if err != nil {
			return err
		}
	}
	return req.Bridge.begin(ctx, req)
}
func (r *Runner) bashPPBridgeRegisterScalarTypes(ctx context.Context, req bashPPEvalRequest, path, alias string) error {
	if alias == "_" {
		return nil
	}
	req = bashPPModuleRequest(req)
	lookup := func(path string) (io.ReadCloser, error) {
		cmd := exec.CommandContext(ctx, req.internalBuildGo(), "list", "-export", "-f", "{{.Export}}", path)
		cmd.Dir, cmd.Env = req.Dir, req.internalBuildEnv()
		out, err := cmd.Output()
		if err != nil {
			return nil, err
		}
		return os.Open(strings.TrimSpace(string(out)))
	}
	pkg, err := importer.ForCompiler(token.NewFileSet(), "gc", lookup).Import(path)
	if err != nil {
		return err
	}
	r.bashPPUnshareTypes()
	if r.bashPPTypes == nil {
		r.bashPPTypes = map[string]bashPPType{}
	}
	// Copy on write: public subshells and interpreted tasks may share the
	// previous immutable export metadata without sharing import mutations.
	nativeTypes := make(map[string]types.Type, len(r.bashPPTools.nativeTypes)+pkg.Scope().Len())
	for name, typ := range r.bashPPTools.nativeTypes {
		nativeTypes[name] = typ
	}
	for _, name := range pkg.Scope().Names() {
		if object, ok := pkg.Scope().Lookup(name).(*types.TypeName); ok && object.Exported() {
			nativeTypes[path+"."+name] = object.Type()
		}
	}
	r.bashPPTools.nativeTypes = nativeTypes
	for _, name := range pkg.Scope().Names() {
		object, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || !object.Exported() {
			continue
		}
		basic, ok := object.Type().Underlying().(*types.Basic)
		if !ok {
			continue
		}
		key := alias + "." + name
		if alias == "." {
			key = name
		}
		canonical := path + "." + name
		if named, ok := types.Unalias(object.Type()).(*types.Named); ok && named.Obj().Pkg() != nil {
			canonical = named.Obj().Pkg().Path() + "." + named.Obj().Name()
		}
		r.bashPPTypes[canonical] = bashPPType{underlying: basic.Name(), typeExpr: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: basic.Name()}}}
		for _, binding := range []string{key, path + "." + name} {
			if binding != canonical {
				r.bashPPTypes[binding] = bashPPType{underlying: basic.Name(), alias: true, typeExpr: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: canonical}}}
			}
		}
		r.bashPPInvalidateSelectionCache()
	}
	return nil
}

// A sibling may have passed its initial context check before EOF cancellation
// closed the shared connection. Only that causally identified local close is
// cancellation; ordinary network failures and live-context requests stay errors,
// except a live request that lost the connection to a forwarded program signal
// (bashpp_s248_forwarded_close.go).
func (s *bashPPNativeSession) closedWriteError(ctx context.Context, err error) error {
	if !errors.Is(err, net.ErrClosed) {
		return err
	}
	if ctx.Err() == nil {
		return s.forwardedDeathWriteError(err)
	}
	s.mu.Lock()
	canceled := s.closeCancellation != nil || s.processCanceledLocked(s.waitErr)
	s.mu.Unlock()
	if canceled {
		return ctx.Err()
	}
	return err
}

// A signaled dependency process is cancellation only when its command context
// requested that kill. An ordinary exit status remains the program's own exit.
func (s *bashPPNativeSession) processCanceledLocked(err error) bool {
	var exit *exec.ExitError
	return s.processCancellation != nil && errors.As(err, &exit) && exit.ExitCode() < 0
}
func (s *bashPPNativeSession) canceledTermination(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		return nil
	}
	// A close response can terminate the helper before the closer records
	// its provenance. Wait for that close transaction before inspecting it.
	s.write.Lock()
	defer s.write.Unlock()
	s.mu.Lock()
	canceled := s.processCanceledLocked(err)
	if s.closeCancellation != nil {
		var exit *exec.ExitError
		canceled = canceled || err == nil || errors.As(err, &exit) && exit.ExitCode() < 0
	}
	s.mu.Unlock()
	if canceled {
		return ctx.Err()
	}
	return nil
}

func (s *bashPPNativeSession) markRetainedCallbacks() {
	s.mu.Lock()
	s.retained = true
	s.mu.Unlock()
}

func (s *bashPPNativeSession) retainedCallbacks() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retained
}

type bashPPNativeFaultPanic struct {
	text string
	addr uint64
}

func (p *bashPPNativeFaultPanic) Error() string { return p.text }

func bashPPSetPanicOnFaultRequest(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Op != "call" || len(q.Args) != 1 || q.Args[0].Kind != "bool" {
		return false
	}
	pkg, name, ok := strings.Cut(q.Selector, ".")
	return ok && name == "SetPanicOnFault" && req.Imports[pkg] == "runtime/debug"
}

// bashPPForcesCollection reports an imported function that starts a collection
// cycle whatever the pacing says. While an unmapped companion frame is live the
// helper holds collection off, and these are the calls that would defeat that;
// the helper serialises them against that window rather than crashing in it.
func bashPPForcesCollection(path, name string) bool {
	switch path {
	case "runtime":
		return name == "GC"
	case "runtime/debug":
		return name == "FreeOSMemory" || name == "SetGCPercent" || name == "SetMemoryLimit"
	}
	return false
}
