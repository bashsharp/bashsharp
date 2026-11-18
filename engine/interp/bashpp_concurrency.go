// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

const (
	bashPPChanHandlePrefix = "chan@bashpp:"
	bashPPMaxChanCapacity  = 65536
)

type bashPPChannel struct {
	native  *bashPPBridgeValue
	elem    string
	ch      chan any
	element syntax.BashPPTypeExpr
	mu      sync.Mutex
	changed *sync.Cond
	closing chan struct{}
	closed  bool
	active  int
}

func newBashPPChannel(elem string, capacity int) *bashPPChannel {
	c := &bashPPChannel{elem: elem, ch: make(chan any, capacity), closing: make(chan struct{})}
	c.changed = sync.NewCond(&c.mu)
	return c
}

// newBashPPChannelChecked turns the Go runtime's own refusal of the channel
// storage this interpreter allocates into an error, so the gosource
// allocation path can raise the same recoverable runtime panic the original
// program sees. Only the runtime's makechan refusal message is translated;
// anything else the allocation panics with is not ours to interpret and keeps
// panicking.
func newBashPPChannelChecked(elem string, capacity int) (c *bashPPChannel, err error) {
	defer func() {
		if failure := recover(); failure != nil {
			if message := fmt.Sprint(failure); message == "makechan: size out of range" {
				err = errors.New(message)
				return
			}
			panic(failure)
		}
	}()
	return newBashPPChannel(elem, capacity), nil
}

func (c *bashPPChannel) beginSend() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.active++
	return true
}

func (c *bashPPChannel) endSend() {
	c.mu.Lock()
	c.active--
	if c.active == 0 {
		c.changed.Broadcast()
	}
	c.mu.Unlock()
}

func (c *bashPPChannel) close() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.closed = true
	close(c.closing)
	for c.active != 0 {
		c.changed.Wait()
	}
	close(c.ch)
	return true
}

// bashPPObjectCloneKey identifies one shared payload. A slice's identity is
// its data pointer *and* its length: `s` and `s[:3]` start at the same address,
// so a pointer-only key would hand a task the wrong one of the two.
type bashPPObjectCloneKey struct {
	kind   uint8
	ptr    uintptr
	length int
}

type bashPPObjectCloner struct {
	active  map[bashPPObjectCloneKey]bool
	done    map[bashPPObjectCloneKey]any
	pointer func(*bashPPPointer) *bashPPPointer
	// native scopes imported dependency handles; see bashpp_task.go. The zero
	// value preserves handle identity without a staleness check.
	native bashPPNativeHandleScope
}

func newBashPPObjectCloner() *bashPPObjectCloner {
	return &bashPPObjectCloner{
		active: make(map[bashPPObjectCloneKey]bool),
		done:   make(map[bashPPObjectCloneKey]any),
	}
}

func (c *bashPPObjectCloner) clone(value any) (any, error) {
	switch value := value.(type) {
	case nil, bool, string, float32, float64,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return value, nil
	case *bashPPPointer:
		if c.pointer != nil {
			return c.pointer(value), nil
		}
		return value, nil
	case *bashPPBridgeValue:
		// An imported native value is a reference into the dependency
		// session, not interpreter-owned mutable state; cloning preserves the
		// object it names. See bashpp_task.go.
		return c.cloneNativeHandle(value)
	case map[string]any:
		key := bashPPObjectCloneKey{kind: 1, ptr: reflect.ValueOf(value).Pointer()}
		if c.active[key] {
			return nil, fmt.Errorf("cyclic Bash++ object")
		}
		if done, ok := c.done[key]; ok {
			return done, nil
		}
		entries := bashPPStorageSnapshot(value)
		out := make(map[string]any, len(entries))
		c.active[key] = true
		for name, item := range entries {
			copy, err := c.clone(item)
			if err != nil {
				return nil, err
			}
			out[name] = copy
		}
		delete(c.active, key)
		c.done[key] = out
		return out, nil
	case []any:
		key := bashPPObjectCloneKey{kind: 2, ptr: reflect.ValueOf(value).Pointer(), length: len(value)}
		if c.active[key] {
			return nil, fmt.Errorf("cyclic Bash++ object")
		}
		if done, ok := c.done[key]; ok {
			return done, nil
		}
		out := make([]any, len(value))
		c.active[key] = true
		for i, item := range value {
			copy, err := c.clone(item)
			if err != nil {
				return nil, err
			}
			out[i] = copy
		}
		delete(c.active, key)
		c.done[key] = out
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported mutable Bash++ object type %T", value)
	}
}

func cloneBashPPTaskVariable(vr expand.Variable, objects *bashPPObjectCloner) (expand.Variable, error) {
	vr = cloneBashPPVariable(vr)
	if vr.Kind != expand.Object || vr.Obj == nil {
		return vr, nil
	}
	copy, err := objects.clone(vr.Obj)
	if err != nil {
		return expand.Variable{}, err
	}
	vr.Obj = copy
	return vr, nil
}

func cloneBashPPTaskCells(r *Runner, objects *bashPPObjectCloner, shared map[*bashPPCell]bool) error {
	metadata := make(map[*bashPPCollectionMeta]*bashPPCollectionMeta)
	return r.bashPPWalkCells(func(cell *bashPPCell) error {
		if shared[cell] {
			// A GoSource captured cell is the PARENT's cell, reached here
			// through the child's scope. Copying its payload would rewrite the
			// parent's variable in place, which is the one thing sharing must
			// not do; see gosource_task_capture.go.
			return nil
		}
		if r.bashPPGoSource && r.bashPPGoTask {
			// Go task scopes contain only shared lexical cells and immutable
			// constants. Actual value arguments bypass shell deep cloning.
			return nil
		}
		copy, err := cloneBashPPTaskVariable(cell.vr, objects)
		if err != nil {
			return err
		}
		cell.vr = copy
		if cell.object != nil && cell.object.collection != nil {
			cell.object.collection = bashPPCloneCollectionMeta(cell.object.collection, metadata, nil)
		}
		cell.valueMeta = bashPPCloneCollectionMeta(cell.valueMeta, metadata, nil)
		return nil
	})
}

type bashPPTaskFailure struct {
	ordinal uint64
	code    uint8
	text    string
}

type bashPPTaskState struct {
	ordinal uint64
	ready   chan struct{}
	once    sync.Once
}

// bashPPHandleProvenance is defense-only history for exact handles which are
// still present in the persistent shell environment. It never grants channel
// authority; typed bashPPCell provenance is the sole authority source.
type bashPPHandleProvenance struct {
	mu      sync.RWMutex
	handles map[string]struct{}
}

// One dynamic structured task group belongs to one owning File Run. Unlike a
// sync.WaitGroup, registration remains safe while the owner is joining; only
// reaching zero permanently quiesces the group.
type bashPPConcurrent struct {
	mu      sync.Mutex
	changed *sync.Cond
	ctx     context.Context
	cancel  context.CancelFunc
	// failureCancel interrupts the owning File Run when a child interpreter
	// fails, including when the owner is blocked in a dependency call.
	failureCancel context.CancelFunc
	// fileRunOwner is stamped only by bashPPConcurrency while its Runner is
	// inside Run(*syntax.File). Callback templates retain this group pointer,
	// so the stamp is the capability which lets a verified testing callback
	// recover the otherwise deliberately-uncloned bashPPFileRun bit.
	fileRunOwner bool
	active       int
	quiesced     bool
	failures     []bashPPTaskFailure
	nextTask     uint64
	tasks        map[uint64]*bashPPTaskState
	chans        map[string]*bashPPChannel
	ioMu         sync.Mutex
	logicalMu    sync.Mutex
	observerMu   sync.Mutex
	fifoMu       sync.Mutex
	fifos        map[*os.File]*bashPPFIFOEntry
	// fifoCount mirrors len(fifos). It lets the statement hot path avoid
	// contending on fifoMu merely to discover that no reconciliation is due.
	// Every change is made while fifoMu is held.
	fifoCount atomic.Int32
	// fifoPending holds rendezvous openers that have announced themselves
	// but not yet acquired a descriptor; see bashPPFIFOOpen.
	fifoPending map[*bashPPFIFOEntry]struct{}
	// finalizers holds the program's armed finalizers; see
	// gosource_finalizer.go.
	finalizers *goSourceFinalizers
	mainGoexit bool
	exitCalled bool
	exitStatus int
}

// bashPPLockedWriter serializes one Write call at a time across every task in
// a File-owned group. It deliberately does not hold the lock across a handler
// invocation or process lifetime.
type bashPPLockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w *bashPPLockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

func (c *bashPPConcurrent) writer(w io.Writer) io.Writer {
	if w == nil {
		return nil
	}
	if _, ok := w.(*bashPPProcessWriter); ok {
		return w // private pipe owns its locks; stderr keeps the shared lock
	}
	if locked, ok := w.(*bashPPLockedWriter); ok && locked.mu == &c.ioMu {
		return w
	}
	// pipelineWriter must remain the outermost facade so it can translate
	// platform EPIPE behavior and DefaultExecHandler can still unwrap it.
	if pw, ok := w.(*pipelineWriter); ok {
		inner := c.writer(pw.w)
		if inner == pw.w {
			return pw
		}
		return &pipelineWriter{w: inner, runner: pw.runner}
	}
	// Native descriptors already support concurrent Write and their concrete
	// shape is required for direct os/exec wiring, TTY probing, job control,
	// and kernel SIGPIPE behavior. Do not hide them behind an io.Writer facade.
	if _, ok := w.(*os.File); ok {
		return w
	}
	if _, ok := w.(interface{ Fd() uintptr }); ok {
		return w
	}
	return &bashPPLockedWriter{mu: &c.ioMu, w: w}
}

func (r *Runner) bashPPWriter(w io.Writer) io.Writer {
	if r.bashPPConcurrent == nil {
		return w
	}
	return r.bashPPConcurrent.writer(w)
}

func (r *Runner) bashPPObserve(fn func()) {
	if r.bashPPConcurrent == nil {
		fn()
		return
	}
	r.bashPPConcurrent.observerMu.Lock()
	defer r.bashPPConcurrent.observerMu.Unlock()
	fn()
}

// bashPPLockLogicalOutput keeps a builtin's complete logical record atomic.
// The writer facade protects memory safety per Write; commands such as echo
// intentionally issue separate writes for operands and the newline, so they
// need this outer, distinct mutex as well. A depth counter makes `builtin
// echo`/`command echo` re-entry safe without making the mutex recursive.
func (r *Runner) bashPPLockLogicalOutput(name string) func() {
	if r.bashPPConcurrent == nil || (name != "echo" && name != "printf") {
		return func() {}
	}
	r.bashPPLogicalDepth++
	if r.bashPPLogicalDepth > 1 {
		return func() { r.bashPPLogicalDepth-- }
	}
	mu := &r.bashPPConcurrent.logicalMu
	if w, ok := r.stdout.(*bashPPProcessWriter); ok {
		mu = &w.logicalMu
	}
	mu.Lock()
	return func() {
		r.bashPPLogicalDepth--
		mu.Unlock()
	}
}

var bashPPConcurrencyInitMu sync.Mutex

func newBashPPConcurrent(parent context.Context) *bashPPConcurrent {
	ctx, cancel := context.WithCancel(parent)
	c := &bashPPConcurrent{ctx: ctx, cancel: cancel, chans: make(map[string]*bashPPChannel), tasks: make(map[uint64]*bashPPTaskState)}
	c.changed = sync.NewCond(&c.mu)
	return c
}

func (r *Runner) bashPPConcurrency(ctx context.Context) *bashPPConcurrent {
	bashPPConcurrencyInitMu.Lock()
	defer bashPPConcurrencyInitMu.Unlock()
	if r.bashPPConcurrent == nil {
		r.bashPPConcurrent = newBashPPConcurrent(ctx)
	}
	if r.bashPPRunCancel != nil {
		r.bashPPConcurrent.mu.Lock()
		r.bashPPConcurrent.failureCancel = r.bashPPRunCancel
		r.bashPPConcurrent.mu.Unlock()
	}
	if r.bashPPFileRun {
		r.bashPPConcurrent.mu.Lock()
		if !r.bashPPConcurrent.quiesced && r.bashPPConcurrent.ctx.Err() == nil {
			r.bashPPConcurrent.fileRunOwner = true
		}
		r.bashPPConcurrent.mu.Unlock()
	}
	return r.bashPPConcurrent
}

// ownsActiveFileRun reports whether c is the still-live group minted for an
// owning File Run. Merely attaching a fresh group to a bare Runner is not an
// ownership capability, and cancellation or quiescence revokes it.
func (c *bashPPConcurrent) ownsActiveFileRun() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fileRunOwner && !c.quiesced && c.ctx.Err() == nil
}

func (c *bashPPConcurrent) add() (*bashPPTaskState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.quiesced || c.ctx.Err() != nil {
		return nil, false
	}
	ordinal := c.nextTask
	c.nextTask++
	c.active++
	state := &bashPPTaskState{ordinal: ordinal, ready: make(chan struct{})}
	c.tasks[ordinal] = state
	return state, true
}

func (c *bashPPConcurrent) arm(task *bashPPTaskState) {
	if task == nil {
		return
	}
	task.once.Do(func() { close(task.ready) })
}

func (c *bashPPConcurrent) armed(task *bashPPTaskState) bool {
	if task == nil {
		return true
	}
	select {
	case <-task.ready:
		return true
	default:
		return false
	}
}

func (c *bashPPConcurrent) done(ordinal uint64, f *bashPPTaskFailure) {
	c.mu.Lock()
	state := c.tasks[ordinal]
	var cancelRun context.CancelFunc
	if f != nil {
		c.failures = append(c.failures, *f)
		// Failure is a structured-concurrency cancellation point, not something
		// deferred until the owner reaches EOF. The launch handshake makes this
		// deterministic: the owner cannot pass this task's `go` statement until
		// its first command has either completed or committed to a cancellable
		// block.
		c.cancel()
		cancelRun = c.failureCancel
	}
	delete(c.tasks, ordinal)
	c.active--
	if c.active == 0 {
		c.changed.Broadcast()
	}
	c.mu.Unlock()
	if cancelRun != nil {
		cancelRun()
	}
	// Always release a launcher, including snapshot failures, empty functions,
	// and panics before the first semantic command.
	c.arm(state)
}

func (c *bashPPConcurrent) stopAndJoin() *bashPPTaskFailure {
	c.cancel()
	c.mu.Lock()
	for c.active != 0 {
		c.changed.Wait()
	}
	c.quiesced = true
	result := c.primaryFailureLocked()
	c.mu.Unlock()
	c.closeFIFOs(nil)
	return result
}

func (c *bashPPConcurrent) primaryFailureLocked() *bashPPTaskFailure {
	var result *bashPPTaskFailure
	for i := range c.failures {
		f := &c.failures[i]
		if result == nil || f.ordinal < result.ordinal {
			copy := *f
			result = &copy
		}
	}
	return result
}

// bashPPTaskRunCanceled recognizes the task's cancellation even when a nested
// dependency helper reports its process status before the bridge can preserve
// context.Canceled. This race occurs when the task's final callback wakes main:
// main reaches File EOF and cancels the group while the task is still returning
// through the helper. Native Go abandons that goroutine successfully when main
// returns, so an ExitStatus observed only after group cancellation is not a
// task failure. Live-context statuses and non-status callback errors remain
// genuine failures.
func bashPPTaskRunCanceled(ctx context.Context, child *Runner) bool {
	if child == nil {
		return false
	}
	if child.bashPPTaskCanceled || errors.Is(child.exit.err, context.Canceled) || errors.Is(child.exit.err, context.DeadlineExceeded) {
		return true
	}
	if ctx == nil || ctx.Err() == nil || !child.exit.fatalExit {
		return false
	}
	var status ExitStatus
	if !errors.As(child.exit.err, &status) {
		return false
	}
	child.exit = exitStatus{}
	child.bashPPTaskCanceled = true
	return true
}

func newBashPPChannelCapability() (string, error) {
	var raw [24]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return "", err
	}
	return bashPPChanHandlePrefix + hex.EncodeToString(raw[:]), nil
}

func (r *Runner) bashPPTaskContext(ctx context.Context) context.Context {
	if r.bashPPConcurrent != nil {
		return r.bashPPConcurrent.ctx
	}
	return ctx
}

// bashPPArmBeforeBlock is the single launch-handshake transition for semantic
// operations which may block. It checks cancellation on both sides of arm:
// the first check prevents already-cancelled work, while the recheck closes
// the race where cancellation and launcher release happen together.
func (r *Runner) bashPPArmBeforeBlock(ctx context.Context) bool {
	if err := observeBlocking(ctx); err != nil {
		if r.bashPPGoTask {
			r.bashPPTaskCanceled = true
		}
		r.exit.fatal(err)
		return false
	}
	if !r.bashPPGoTask || r.bashPPConcurrent == nil {
		return true
	}
	taskCtx := r.bashPPTaskContext(ctx)
	if err := taskCtx.Err(); err != nil {
		r.bashPPTaskCanceled = true
		r.exit.fatal(err)
		r.bashPPConcurrent.arm(r.bashPPTaskState)
		return false
	}
	r.bashPPConcurrent.arm(r.bashPPTaskState)
	if err := taskCtx.Err(); err != nil {
		r.bashPPTaskCanceled = true
		r.exit.fatal(err)
		return false
	}
	return true
}

func (r *Runner) bashPPDeadlockIfNoTasks(ctx context.Context) bool {
	if !r.bashPPGoSource || r.bashPPGoTask || r.bashPPConcurrent == nil {
		return false
	}
	if err := r.bashPPTaskContext(ctx).Err(); err != nil {
		return false
	}
	c := r.bashPPConcurrent
	c.mu.Lock()
	deadlocked := c.active == 0
	c.mu.Unlock()
	if !deadlocked {
		return false
	}
	r.errf("fatal error: all goroutines are asleep - deadlock!\n")
	r.exit = exitStatus{code: bashPPPanicStatus}
	return true
}

// bashPPTaskOpen acquires a filesystem descriptor without ever blocking on a
// pathname that races to a FIFO. Task opens deliberately fail closed for
// custom handlers: the OpenHandler contract does not promise cancellation or
// honor O_NONBLOCK, so invoking one could strand the structured File join.
func (r *Runner) bashPPTaskOpen(ctx context.Context, path string, flags int, mode os.FileMode, print, requireRegular bool) (io.ReadWriteCloser, error) {
	// The owner retains virtual stdin; the host pseudo-path can name a
	// different pipe. Custom open handlers retain their existing boundary.
	if !r.inBashPPTask() && path == "/dev/stdin" && flags&3 == os.O_RDONLY && r.stdin != nil {
		return r.open(ctx, path, flags, mode, print)
	}
	groupOpen := !requireRegular && !r.customOpenActive() &&
		(r.bashPPConcurrent != nil || (r.bashPPFileRun && r.Dialect() == syntax.LangBashPP))
	if groupOpen {
		openCtx := ctx
		if r.bashPPConcurrent != nil {
			openCtx = r.bashPPConcurrent.ctx
		}
		if err := openCtx.Err(); err != nil {
			if r.bashPPGoTask {
				r.bashPPTaskCanceled = true
				r.exit.fatal(err)
			}
			return nil, err
		}
		// The registered-peer rendezvous below is for a group with
		// concurrent members. Until the File has had a task, a FIFO open
		// is Bash's blocking open (a Classic script under the Bash++
		// dialect must behave as Classic), published to the group so a
		// later task still finds the descriptor.
		if !r.bashPPFIFORendezvous() {
			return r.bashPPFIFOOpenNative(ctx, path, flags, mode, print)
		}
		file, fifo, err := r.bashPPFIFOOpen(ctx, path, flags)
		if fifo {
			if err != nil && print {
				r.errf("%s%s: %v\n", r.bashErrPrefix(r.curStmtPos), path, err)
			}
			if err != nil {
				if r.bashPPGoTask && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
					r.bashPPTaskCanceled = true
					r.exit.fatal(err)
				}
				return nil, err
			}
			return file, nil
		}
	}
	if !r.inBashPPTask() {
		return r.open(ctx, path, flags, mode, print)
	}
	taskCtx := r.bashPPTaskContext(ctx)
	if err := taskCtx.Err(); err != nil {
		r.bashPPTaskCanceled = true
		r.exit.fatal(err)
		return nil, err
	}
	reportOpenError := func(err error) error {
		if print {
			reason := err
			if pathErr, ok := err.(*os.PathError); ok {
				reason = pathErr.Err
			}
			r.errf("%s%s: %v\n", r.bashErrPrefix(r.curStmtPos), path, reason)
		}
		return err
	}
	if r.customOpenActive() {
		err := fmt.Errorf("custom open handlers are unavailable inside a Bash++ task")
		return nil, reportOpenError(err)
	}
	// DialContext is cancellation-aware. Network pseudo-paths have no
	// descriptor to probe, so this is their exact potential-block boundary.
	if ctx.Value(devNetworkRedirectCtxKey{}) != nil {
		if _, _, ok := devNetworkRedirect(path); ok {
			if !r.bashPPArmBeforeBlock(ctx) {
				return nil, r.bashPPTaskContext(ctx).Err()
			}
			f, err := r.open(ctx, path, flags, mode, print)
			if err != nil {
				return nil, err
			}
			if err := r.bashPPTaskContext(ctx).Err(); err != nil {
				_ = f.Close()
				return nil, err
			}
			return f, nil
		}
	}
	if flags&os.O_CREATE != 0 {
		mode &^= os.FileMode(r.umask)
	}
	// This precheck deterministically prevents already-cancelled tasks from
	// creating or truncating files. Cancellation can still race the syscall;
	// closing that narrow check-to-open window requires the group-wide action
	// commit protocol shared with exec/background launch (tracked separately).
	file, supported, err := bashPPTaskProbeOpen(r.dirFile, r.Dir, path, flags, mode)
	if !supported {
		err := fmt.Errorf("task file opens are unavailable on this platform")
		return nil, reportOpenError(err)
	}
	if err != nil {
		return nil, reportOpenError(err)
	}
	fifo := false
	if err := r.bashPPTaskContext(ctx).Err(); err != nil {
		if file != nil {
			_ = file.Close()
		}
		return nil, err
	}
	if file != nil {
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, reportOpenError(err)
		}
		fifo = info.Mode()&os.ModeNamedPipe != 0
		if requireRegular && !info.Mode().IsRegular() {
			_ = file.Close()
			if fifo {
				err := fmt.Errorf("FIFO input is unavailable inside a Bash++ task")
				return nil, reportOpenError(err)
			}
			err := fmt.Errorf("non-regular input is unavailable inside a Bash++ task")
			return nil, reportOpenError(err)
		}
		if !fifo && !info.Mode().IsRegular() {
			_ = file.Close()
			err := fmt.Errorf("non-regular file is unavailable inside a Bash++ task")
			return nil, reportOpenError(err)
		}
	}
	if fifo {
		if requireRegular {
			_ = file.Close()
			err := fmt.Errorf("FIFO input is unavailable inside a Bash++ task")
			return nil, reportOpenError(err)
		}
		_ = file.Close()
		return nil, reportOpenError(fmt.Errorf("FIFO changed before its registered-peer rendezvous could be acquired"))
	}
	if err := bashPPTaskSourceClearNonblock(file); err != nil {
		_ = file.Close()
		return nil, reportOpenError(err)
	}
	return file, nil
}

func (r *Runner) bashPPChannel(w *syntax.Word) (*bashPPChannel, bool) {
	name := r.literal(w)
	if r.bashPPConcurrent == nil {
		r.errf("bash++: %s is not a channel in this task group\n", name)
		r.exit.code = 2
		return nil, false
	}
	if r.bashPPChanBoundary {
		r.errf("bash++: channel %s cannot cross a shell-copy boundary\n", name)
		r.exit.code = 2
		return nil, false
	}
	c := r.bashPPConcurrent
	cell := r.bashPPScope.lookup(name).view()
	if cell == nil || cell.channel == nil || cell.channelOwner != c {
		r.errf("bash++: %s is not a channel in this task group\n", name)
		r.exit.code = 2
		// Wake any later operation in this malformed structured group; in
		// particular, an invalid send followed by a receive must not strand the
		// owner before it reaches the File join boundary.
		c.cancel()
		return nil, false
	}
	return cell.channel, true
}

func (r *Runner) bashPPMakeChan(ctx context.Context, d *syntax.BashPPShortDecl) {
	if len(d.Lhs) != 1 {
		r.errf("assignment mismatch: make(chan) has one value\n")
		r.exit.code = 2
		return
	}
	if r.bashPPGoSource {
		cell, err := r.goSourceMakeChannelCell(d.MakeChan.ChanType, d.MakeChan.CapacityExpr, d.MakeChan.Capacity)
		if err != nil {
			r.goSourceNativeChannelError(err)
			return
		}
		r.bashPPBindReceivedCell(d.Lhs[0].Value, cell)
		return
	}
	capacity := 0
	elem := d.MakeChan.ChanType.Elem.Value
	if _, ok := r.bashPPTypes[elem]; !r.bashPPGoSource && !ok && !bashPPBuiltinType(elem) {
		r.errf("make(chan): undefined element type: %s\n", elem)
		r.exit.code = 2
		return
	}
	if r.bashPPGoSource && d.MakeChan.CapacityExpr != nil {
		var err error
		capacity, err = r.goSourceChannelCapacity(d.MakeChan.CapacityExpr, d.MakeChan.Capacity)
		if err != nil {
			r.goSourceNativeChannelError(err)
			return
		}
	} else if d.MakeChan.Capacity != nil {
		var err error
		capacity, err = strconv.Atoi(r.literal(d.MakeChan.Capacity))
		if err != nil || capacity < 0 || capacity > bashPPMaxChanCapacity {
			r.errf("make(chan): capacity must be between 0 and %d\n", bashPPMaxChanCapacity)
			r.exit.code = 2
			return
		}
	}
	c := r.bashPPConcurrency(ctx)
	c.mu.Lock()
	h, err := newBashPPChannelCapability()
	if err != nil {
		c.mu.Unlock()
		r.errf("make(chan): cannot allocate capability: %v\n", err)
		r.exit.code = 2
		return
	}
	c.chans[h] = newBashPPChannel(elem, capacity)
	channel := c.chans[h]
	if r.bashPPGoSource {
		channel.element = d.MakeChan.ChanType.Element
	}
	c.mu.Unlock()
	r.bashPPDeclareName(d.Lhs[0].Value, expand.Variable{Set: true, Kind: expand.String, Str: h})
	if cell := r.bashPPScope.lookup(d.Lhs[0].Value); cell != nil {
		cell.channel, cell.channelOwner = channel, c
		if r.bashPPGoSource {
			cell.declType = d.MakeChan.ChanType
		}
	}
}

func (r *Runner) bashPPSend(ctx context.Context, s *syntax.BashPPSend) {
	c, ok := r.bashPPGoSendChannel(s)
	if !ok {
		return
	}
	if c.native != nil {
		r.goSourceNativeSend(ctx, c, s)
		return
	}
	v, ok := r.bashPPGoSendPayload(c, s)
	if !ok {
		return
	}
	if !c.beginSend() {
		r.bashPPClosedSend()
		return
	}
	defer c.endSend()
	taskCtx := r.bashPPTaskContext(ctx)
	if err := taskCtx.Err(); err != nil {
		r.bashPPTaskCanceled = true
		r.exit.code = 1
		return
	}
	select {
	case c.ch <- v:
		return
	case <-c.closing:
		r.bashPPClosedSend()
		return
	case <-taskCtx.Done():
		r.bashPPTaskCanceled = true
		r.exit.code = 1
		return
	default:
	}
	if !r.bashPPArmBeforeBlock(ctx) {
		return
	}
	if r.bashPPDeadlockIfNoTasks(ctx) {
		return
	}
	select {
	case c.ch <- v:
	case <-c.closing:
		r.bashPPClosedSend()
	case <-r.bashPPTaskContext(ctx).Done():
		r.bashPPTaskCanceled = true
		r.exit.code = 1
	}
}

func (r *Runner) bashPPReceive(ctx context.Context, recv *syntax.BashPPReceive, lhs []*syntax.Lit) (string, bool) {
	cell, open := r.bashPPReceiveCell(ctx, recv, lhs)
	if cell == nil {
		return "", false
	}
	return cell.vr.String(), open
}

func (r *Runner) bashPPReceiveCell(ctx context.Context, recv *syntax.BashPPReceive, lhs []*syntax.Lit) (*bashPPCell, bool) {
	if lhs != nil && (len(lhs) == 0 || len(lhs) > 2) {
		r.errf("receive assignment mismatch\n")
		r.exit.code = 2
		return nil, false
	}
	c, ok := r.bashPPGoReceiveChannel(recv)
	if !ok {
		return nil, false
	}
	if c.native != nil {
		return r.goSourceNativeReceive(ctx, c, lhs)
	}
	var v any
	var open bool
	taskCtx := r.bashPPTaskContext(ctx)
	if err := taskCtx.Err(); err != nil {
		r.bashPPTaskCanceled = true
		r.exit.code = 1
		return nil, false
	}
	select {
	case v, open = <-c.ch:
		// An immediately buffered or closed receive is part of the current
		// task's deterministic prefix and must not release a later launch.
	case <-taskCtx.Done():
		r.bashPPTaskCanceled = true
		r.exit.code = 1
		return nil, false
	default:
		if !r.bashPPArmBeforeBlock(ctx) {
			return nil, false
		}
		if r.bashPPDeadlockIfNoTasks(ctx) {
			return nil, false
		}
		select {
		case v, open = <-c.ch:
		case <-taskCtx.Done():
			r.bashPPTaskCanceled = true
			r.exit.code = 1
			return nil, false
		}
	}
	cell := r.bashPPReceivedCell(c, v, open)
	if lhs != nil {
		r.bashPPBindReceivedCell(lhs[0].Value, cell)
		if len(lhs) == 2 {
			r.bashPPDeclareName(lhs[1].Value, expand.Variable{Set: true, Kind: expand.String, Str: strconv.FormatBool(open)})
		}
	}
	return cell, open
}

func (r *Runner) bashPPSetReceivedType(name, elem string) {
	typ, ok := r.bashPPTypes[elem]
	if !ok || r.bashPPScope == nil {
		return
	}
	for typ.alias {
		elem = typ.underlying
		typ, ok = r.bashPPTypes[elem]
		if !ok {
			// An alias of a builtin has that builtin's identity, represented by
			// the empty typeName used for ordinary scalar cells.
			elem = ""
			break
		}
	}
	if cell := r.bashPPScope.lookup(name); cell != nil {
		cell.typeName = elem
	}
}

func (r *Runner) bashPPClose(cl *syntax.BashPPClose) {
	c, ok := r.bashPPChannelOperation(cl.Chan, "close")
	if !ok {
		return
	}
	if !c.close() {
		r.errf("bash++: close of closed channel\n")
		r.exit.code = 2
	}
}

// bashPPTaskSnapshot is the explicit task boundary. subshell(true) clones
// variables, cwd, options, functions/types/imports, traps/signals/jobs and fd
// maps. The maps are task-owned; underlying OS open-file descriptions retain
// their normal shared offsets. Only the group and channel cores are shared.
func (r *Runner) bashPPTaskSnapshot(ordinal uint64, shared map[*bashPPCell]bool) (*Runner, error) {
	// The scope cloner inside subshell is what grants capture identity, so the
	// set has to be visible for exactly that call and no longer.
	saved := r.bashPPGoSourceCapture
	r.bashPPGoSourceCapture = shared
	child := r.subshell(true)
	r.bashPPGoSourceCapture = saved
	child.bashPPGoSourceCapture = nil
	child.bashPPConcurrent, child.bashPPGoTask, child.bashPPChanBoundary = r.bashPPConcurrent, true, false
	child.bashPPFileRun = true
	// A task is not a shell-copy boundary, so BASH_SUBSHELL stays unchanged.
	child.subshellLevel = r.subshellLevel
	// Tasks inherit every trap as a private snapshot, independent of the
	// errtrace/functrace inheritance rules used by actual shell subshells.
	child.trapCallbacks = cloneStringMap(r.trapCallbacks)
	child.inheritedExitTrap = false
	if child.trapCallbacks["ERR"] != "" {
		if child.noOpSetState == nil {
			child.noOpSetState = make(map[string]bool)
		}
		child.noOpSetState["errtrace"] = true
	}
	// Give deterministic tasks independent streams. Sharing PCG is both a data
	// race and schedule-dependent; the launch ordinal is stable by construction.
	if child.deterministic {
		seed := uint64(child.deterministicSeed) ^ (ordinal+1)*0x9e3779b97f4a7c15
		child.deterministicRng = mathrand.NewPCG(seed, seed^0xd1b54a32d192ed03)
	}
	child.randomSeed = r.randomSeed + uint32(ordinal+1)*0x9e37
	if !r.randomSeeded && !r.deterministic {
		var seed [4]byte
		if _, err := cryptorand.Read(seed[:]); err != nil {
			return child, fmt.Errorf("task random seed: %w", err)
		}
		child.randomSeeded = true
		child.randomSeed = uint32(seed[0]) | uint32(seed[1])<<8 | uint32(seed[2])<<16 | uint32(seed[3])<<24
	}
	// Jobs and coprocs are owned by the task that creates them. An owner's
	// existing jobs are not waitable or mutable from a child task.
	child.bgProcs, child.lastBang, child.inheritedBang = nil, nil, nil
	child.doneBgPids = nil
	child.coprocReg, child.coprocFds, child.coprocReapedFds = nil, nil, nil
	child.coprocSeq = 0
	child.asyncList, child.asyncProc, child.jobsReadOnly = false, nil, false
	child.preferredJobID = 0
	child.exit = exitStatus{}
	child.expandRunExit = exitStatus{}
	child.bashPPDeferStack = nil
	child.bashPPReturn = bashPPReturnState{}
	child.bashPPPanic = bashPPPanicState{}
	child.bashPPDeferDepth = 0

	// The ordinary subshell copy aliases *os.File pointers. Tasks instead own
	// dup'd descriptors: aliases in the virtual fd tables stay aliases of one
	// duplicate, while the kernel open description (and therefore offset) is
	// shared with the owner. Closing a task descriptor cannot close the owner.
	dups := make(map[*os.File]*os.File)
	dup := func(f *os.File) (*os.File, error) {
		if f == nil {
			return nil, nil
		}
		if done := dups[f]; done != nil {
			return done, nil
		}
		copy, owned, err := dupPipeFd(f)
		if err != nil {
			return nil, err
		}
		if !owned && copy == f {
			return nil, fmt.Errorf("platform cannot duplicate task descriptor %s", f.Name())
		}
		dups[f] = copy
		if owned {
			child.bashPPTaskFiles = append(child.bashPPTaskFiles, copy)
			if child.bashPPConcurrent != nil {
				child.bashPPConcurrent.cloneFIFO(f, copy, child)
			}
		}
		return copy, nil
	}
	var err error
	if child.stdin, err = dup(r.stdin); err != nil {
		return child, err
	}
	if child.origStdin, err = dup(r.origStdin); err != nil {
		return child, err
	}
	if child.stdout, err = bashPPDupWriter(r.stdout, child, dup); err != nil {
		return child, err
	}
	if child.origStdout, err = bashPPDupWriter(r.origStdout, child, dup); err != nil {
		return child, err
	}
	if child.stderr, err = bashPPDupWriter(r.stderr, child, dup); err != nil {
		return child, err
	}
	if child.origStderr, err = bashPPDupWriter(r.origStderr, child, dup); err != nil {
		return child, err
	}
	for fd, f := range r.fdTable {
		if child.fdTable[fd], err = dup(f); err != nil {
			return child, err
		}
	}
	for fd, w := range r.fdWriteTable {
		if child.fdWriteTable[fd], err = bashPPDupWriter(w, child, dup); err != nil {
			return child, err
		}
	}
	// newOverlayEnviron's background snapshot is shallow for compatibility;
	// Bash++ tasks require deep mutable-value isolation.
	child.writeEnv = &overlayEnviron{}
	objects := newBashPPObjectCloner()
	// The task inherits this runner's dependency session, so its native
	// handles keep naming the same objects; see bashpp_task.go.
	objects.native = bashPPNativeScopeOf(r)
	for name, vr := range r.writeEnv.Each {
		if r.bashPPGoSource {
			// The shell environment is an immutable binding snapshot here; Go
			// variables resolve through the exact typed lexical cells. Never
			// traverse referenced user storage solely for an unused env copy.
			_ = child.writeEnv.Set(name, vr)
			continue
		}
		copy, err := cloneBashPPTaskVariable(vr, objects)
		if err != nil {
			return child, fmt.Errorf("variable %s: %w", name, err)
		}
		_ = child.writeEnv.Set(name, copy)
	}
	if err := cloneBashPPTaskCells(child, objects, shared); err != nil {
		return child, err
	}
	for name, typ := range child.bashPPTypes {
		typ.members = append([]string(nil), typ.members...)
		child.bashPPTypes[name] = typ
	}
	child.fillExpandConfig(r.ectx)
	return child, nil
}

func (r *Runner) closeBashPPTaskResources() {
	r.stopSignalSubscriptions()
	// Background jobs, coprocs, and process substitutions created by this task
	// inherit its group context. Cancel and join them before releasing their fd
	// owners; otherwise a File Run could return while task descendants still
	// use the cloned runner.
	for _, bg := range r.bgProcs {
		if bg != nil && bg.cancel != nil {
			bg.cancel()
		}
	}
	for _, bg := range r.bgProcs {
		if bg == nil {
			continue
		}
		<-bg.done
		if bg.coprocPid != 0 {
			r.reapCoproc(bg)
		}
	}
	r.bgProcs = nil
	if r.bashPPConcurrent != nil {
		r.bashPPConcurrent.closeFIFOs(r)
	}
	for _, file := range r.bashPPTaskFiles {
		_ = file.Close()
	}
	r.bashPPTaskFiles = nil
	r.closeDirFile()
}

func cloneStringMap(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func (r *Runner) bashPPGo(ctx context.Context, g *syntax.BashPPGo) {
	if g.Call == nil {
		return
	}
	if !r.bashPPFileRun {
		r.errf("bash++: go requires an owning File Run\n")
		r.exit.code = 2
		return
	}
	// Resolve the function value first; then evaluate its arguments exactly
	// once in the launching goroutine, before any task is registered.
	call := g.Call
	var shared map[*bashPPCell]bool
	var pin *bashPPGoSourcePin
	var prepared *goSourceTaskArguments
	// A callee with no original body — a builtin, a dependency operation, a
	// native function value — has nothing for the lexical capture analysis to
	// walk; its operands are fixed here and the call runs in the task. See
	// gosource_task_native.go.
	launch, handled := r.goSourcePrepareNativeLaunch(ctx, call)
	if handled {
		if launch == nil || r.exit.code != 0 {
			return
		}
		shared = launch.shared
		if launch.original != nil {
			prepared = launch.original
			pin = prepared.pin
			launch = nil
		}
	} else {
		shared, pin = r.bashPPGoSourceTaskCapture(call)
		if r.exit.code != 0 {
			return
		}
		if r.bashPPGoSource {
			prepared, shared = r.goSourcePrepareTaskArguments(call, pin)
			if prepared == nil || r.exit.code != 0 {
				return
			}
			pin = prepared.pin
		}
	}
	c := r.bashPPConcurrency(ctx)
	state, ok := c.add()
	if !ok {
		// A prior child failure owns the eventual File status. A syntactically
		// later launch observes cancellation and has no side effects.
		return
	}
	ordinal := state.ordinal
	child, err := r.bashPPTaskSnapshot(ordinal, shared)
	if err != nil {
		if child != nil {
			child.closeBashPPTaskResources()
		}
		c.done(ordinal, &bashPPTaskFailure{ordinal: ordinal, code: 2, text: fmt.Sprintf("task snapshot: %v", err)})
		return
	}
	if pin != nil && pin.bound != nil {
		pin = child.goSourceCopyTaskMethodPin(pin, shared)
	}
	child.bashPPTaskState = state
	if c.ctx.Err() != nil {
		child.closeBashPPTaskResources()
		c.done(ordinal, nil)
		return
	}
	go func() {
		var failure *bashPPTaskFailure
		defer func() {
			if x := recover(); x != nil {
				failure = &bashPPTaskFailure{ordinal: ordinal, code: 2, text: fmt.Sprintf("panic: %v", x)}
			}
			// A task is a terminal shell lifetime. Run its private EXIT snapshot
			// once even when group cancellation has already fired; cleanup traps
			// must not be skipped merely because a sibling failed.
			func() {
				defer func() {
					if x := recover(); x != nil && failure == nil {
						failure = &bashPPTaskFailure{ordinal: ordinal, code: 2, text: fmt.Sprintf("EXIT trap panic: %v", x)}
					}
				}()
				child.trapCallback(context.WithoutCancel(c.ctx), child.trapCallbacks["EXIT"], "exit")
			}()
			child.closeBashPPTaskResources()
			c.done(ordinal, failure)
		}()
		if c.ctx.Err() != nil {
			return
		}
		// The callee was resolved once, in the parent, exactly as Go
		// evaluates a function value in the launching goroutine; the child
		// runs that function rather than resolving the name again.
		child.bashPPGoSourcePin = pin
		if child.bashPPGoSource {
			// Implicit expression requests use ectx too, so they must share
			// the launched task's cancellation lifetime with its statements.
			child.fillExpandConfig(c.ctx)
		}
		switch {
		case launch != nil:
			launch.run(c.ctx, child)
		case prepared != nil:
			child.goSourceInvokeTaskArguments(c.ctx, call, prepared)
		default:
			child.bashPPCall(c.ctx, call)
		}
		code := child.exit.code
		canceled := bashPPTaskRunCanceled(c.ctx, child)
		if canceled {
			return
		}
		if code != 0 && child.trapCallbacks["ERR"] != "" {
			child.trapCallback(c.ctx, child.trapCallbacks["ERR"], "error")
		}
		if code != 0 {
			text := fmt.Sprintf("exit status %d", code)
			// A fatal diagnostic recorded by the task — a dependency call the
			// bridge refused — is the reason the task failed; the status
			// alone would hide it.
			if child.exit.fatalExit && child.exit.err != nil && !child.bashPPPanicking() && !errors.Is(child.exit.err, errBashPPScalarInterrupted) {
				text = child.exit.err.Error()
			}
			failure = &bashPPTaskFailure{ordinal: ordinal, code: code, text: text}
		}
	}()
	// Deterministic launch handshake: a nonblocking first command completes
	// before the owner advances, while a known blocking operation announces
	// itself immediately before it blocks. This preserves useful concurrency
	// without allowing a fast failure to race later side effects.
	<-state.ready
}

// bashPPAdoptTaskFailure gives the owner a failed task's status. A failure
// cancels the owning run, so an owner that was blocked at the time — main
// asleep, receiving or locking while a goroutine panics — ends with that
// interruption as its own nonzero status. The status is the failure's echo
// rather than a second failure, and the task's replaces it; a canceled task
// records no failure, so one that is recorded was never caused by a
// cancellation from outside. An owner that is itself panicking keeps its own.
func (r *Runner) bashPPAdoptTaskFailure(ctx context.Context, failure *bashPPTaskFailure) {
	echo := r.exit.code != 0 && !r.bashPPPanicking() &&
		(errors.Is(r.exit.err, context.Canceled) || ctx != nil && ctx.Err() != nil)
	if r.exit.code != 0 && !echo {
		return
	}
	r.exit.code = failure.code
	if echo {
		r.exit.exiting, r.exit.fatalExit, r.exit.err = true, true, ExitStatus(failure.code)
	}
}

func (r *Runner) bashPPWait(ctx context.Context) {
	c := r.bashPPConcurrent
	if r.bashPPGoTask {
		return
	}
	if c == nil {
		r.bashPPPruneIssuedHandles(nil)
		if r.bashPPMainGoexit {
			r.errf("fatal error: no goroutines (main called runtime.Goexit) - deadlock!\n")
			r.exit = exitStatus{code: bashPPPanicStatus, exiting: true}
		}
		return
	}
	if r.bashPPMainGoexit {
		c.mu.Lock()
		c.mainGoexit = true
		c.changed.Broadcast()
		for c.active != 0 && !c.exitCalled {
			c.changed.Wait()
		}
		if c.exitCalled {
			code := c.exitStatus
			c.cancel()
			for c.active != 0 {
				c.changed.Wait()
			}
			c.quiesced = true
			c.mu.Unlock()
			r.bashPPPruneIssuedHandles(c)
			c.closeFIFOs(nil)
			r.bashPPClearChannelRefs(c)
			r.bashPPConcurrent = nil
			r.exit = exitStatus{code: uint8(code), exiting: true}
			if code != 0 {
				r.exit.fatalExit = true
				r.exit.err = ExitStatus(uint8(code))
			}
			return
		}
		c.quiesced = true
		failure := c.primaryFailureLocked()
		c.mu.Unlock()
		r.bashPPPruneIssuedHandles(c)
		c.closeFIFOs(nil)
		r.bashPPClearChannelRefs(c)
		r.bashPPConcurrent = nil
		if failure != nil {
			r.errf("bash++: task failed: %s\n", failure.text)
			r.bashPPAdoptTaskFailure(ctx, failure)
			return
		}
		r.errf("fatal error: no goroutines (main called runtime.Goexit) - deadlock!\n")
		r.exit = exitStatus{code: bashPPPanicStatus, exiting: true}
		return
	}
	// EOF is the structured lifetime boundary. Successful blocked tasks must
	// not keep a File Run alive forever.
	c.cancel()
	c.mu.Lock()
	for c.active != 0 {
		c.changed.Wait()
	}
	c.quiesced = true
	failure := c.primaryFailureLocked()
	c.mu.Unlock()
	r.bashPPPruneIssuedHandles(c)
	c.closeFIFOs(nil)
	r.bashPPClearChannelRefs(c)
	if failure != nil {
		r.errf("bash++: task failed: %s\n", failure.text)
		r.bashPPAdoptTaskFailure(ctx, failure)
	}
	// A completed File Run never lends its quiesced registry to a later Run.
	// Handles left in persistent shell variables consequently become invalid
	// capabilities rather than aliases into stale channel state.
	r.bashPPConcurrent = nil
}

func (r *Runner) bashPPPruneIssuedHandles(c *bashPPConcurrent) {
	p := r.bashPPIssuedHandles
	if p == nil && c == nil {
		return
	}
	if p == nil {
		p = &bashPPHandleProvenance{}
		r.bashPPIssuedHandles = p
	}
	p.mu.Lock()
	candidates := make(map[string]struct{}, len(p.handles))
	for handle := range p.handles {
		candidates[handle] = struct{}{}
	}
	if c != nil {
		c.mu.Lock()
		for handle := range c.chans {
			candidates[handle] = struct{}{}
		}
		c.mu.Unlock()
	}
	retained := make(map[string]struct{})
	for _, vr := range r.bashPPEnv().Each {
		for handle := range candidates {
			if variableContainsString(vr, handle) {
				retained[handle] = struct{}{}
			}
		}
	}
	r.bashPPVisitPersistentCells(func(cell *bashPPCell) {
		for handle := range candidates {
			if variableContainsString(cell.vr, handle) {
				retained[handle] = struct{}{}
			}
		}
	})
	p.handles = retained
	p.mu.Unlock()
}

func variableContainsString(vr expand.Variable, needle string) bool {
	if strings.Contains(vr.Str, needle) {
		return true
	}
	for _, value := range vr.List {
		if strings.Contains(value, needle) {
			return true
		}
	}
	for _, value := range vr.ListMap {
		if strings.Contains(value, needle) {
			return true
		}
	}
	for key, value := range vr.Map {
		if strings.Contains(key, needle) || strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

// Persistent closures retain lexical cells across Files. Revoke the completed
// group's capabilities without disturbing any other captured state.
func (r *Runner) bashPPClearChannelRefs(owner *bashPPConcurrent) {
	r.bashPPVisitPersistentCells(func(cell *bashPPCell) {
		if cell.channelOwner == owner {
			cell.channel, cell.channelOwner = nil, nil
		}
	})
}

func (r *Runner) bashPPWalkCells(fn func(*bashPPCell) error) error {
	seenScopes := make(map[*bashPPScope]bool)
	seenCells := make(map[*bashPPCell]bool)
	visitCell := func(cell *bashPPCell) error {
		if cell == nil || seenCells[cell] {
			return nil
		}
		seenCells[cell] = true
		return fn(cell)
	}
	var visit func(*bashPPScope) error
	visit = func(scope *bashPPScope) error {
		if scope == nil || seenScopes[scope] {
			return nil
		}
		seenScopes[scope] = true
		if err := visit(scope.parent); err != nil {
			return err
		}
		for _, cell := range scope.entries {
			if err := visitCell(cell); err != nil {
				return err
			}
		}
		return nil
	}
	visitFunc := func(f *bashPPFunc) error {
		if f == nil {
			return nil
		}
		if err := visit(f.scope); err != nil {
			return err
		}
		return visitCell(f.receiver)
	}
	if err := visit(r.bashPPScope); err != nil {
		return err
	}
	for _, scope := range r.bashPPFuncScopes {
		if err := visit(scope); err != nil {
			return err
		}
	}
	for _, f := range r.bashPPFuncs {
		if err := visitFunc(f); err != nil {
			return err
		}
	}
	for _, methods := range r.bashPPMethods {
		for _, f := range methods {
			if err := visitFunc(f); err != nil {
				return err
			}
		}
	}
	for _, f := range r.bashPPClosures {
		if err := visitFunc(f); err != nil {
			return err
		}
	}
	for _, deferred := range r.bashPPDeferStack {
		if err := visitFunc(deferred.fn); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) bashPPVisitPersistentCells(fn func(*bashPPCell)) {
	_ = r.bashPPWalkCells(func(cell *bashPPCell) error {
		fn(cell)
		return nil
	})
}

func (r *Runner) bashPPSelect(ctx context.Context, s *syntax.BashPPSelect) {
	var nativeCases []bashPPBridgeValue
	var nativeArms []*syntax.BashPPSelectCase
	hasLocal, liveNative := false, false
	var cases []reflect.SelectCase
	var arms []*syntax.BashPPSelectCase
	var caseElems []*bashPPChannel
	var sends []*bashPPChannel
	var pendingSends []*bashPPChannel
	released := false
	releaseSends := func() {
		if released {
			return
		}
		released = true
		for _, c := range sends {
			c.endSend()
		}
	}
	defer releaseSends()
	closingCases := make(map[int]bool)
	var def *syntax.BashPPSelectCase
	for _, arm := range s.Cases {
		if arm.Default {
			def = arm
			continue
		}
		switch comm := arm.Comm.(type) {
		case *syntax.BashPPReceive:
			c, ok := r.bashPPGoReceiveChannel(comm)
			if !ok {
				return
			}
			if c.native != nil {
				nativeCases = append(nativeCases, bashPPBridgeValue{Kind: "recv", Elements: []bashPPBridgeValue{*c.native}})
				nativeArms = append(nativeArms, arm)
				liveNative = liveNative || !goSourceNativeNilChannel(c.native)
			} else {
				hasLocal = true
			}
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(c.ch)})
			caseElems = append(caseElems, c)
		case *syntax.BashPPShortDecl:
			if comm.Recv == nil {
				r.errf("invalid select receive declaration\n")
				r.exit.code = 2
				return
			}
			if len(comm.Lhs) == 0 || len(comm.Lhs) > 2 {
				r.errf("receive assignment mismatch\n")
				r.exit.code = 2
				return
			}
			c, ok := r.bashPPGoReceiveChannel(comm.Recv)
			if !ok {
				return
			}
			if c.native != nil {
				nativeCases = append(nativeCases, bashPPBridgeValue{Kind: "recv", Elements: []bashPPBridgeValue{*c.native}})
				nativeArms = append(nativeArms, arm)
				liveNative = liveNative || !goSourceNativeNilChannel(c.native)
			} else {
				hasLocal = true
			}
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(c.ch)})
			caseElems = append(caseElems, c)
		case *syntax.BashPPAssign:
			recv := bashPPSelectAssignReceive(comm)
			if recv == nil {
				r.errf("invalid select receive assignment\n")
				r.exit.code = 2
				return
			}
			c, ok := r.bashPPGoReceiveChannel(recv)
			if !ok {
				return
			}
			if c.native != nil {
				nativeCases = append(nativeCases, bashPPBridgeValue{Kind: "recv", Elements: []bashPPBridgeValue{*c.native}})
				nativeArms = append(nativeArms, arm)
				liveNative = liveNative || !goSourceNativeNilChannel(c.native)
			} else {
				hasLocal = true
			}
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(c.ch)})
			caseElems = append(caseElems, c)
		case *syntax.BashPPSend:
			c, ok := r.bashPPGoSendChannel(comm)
			if !ok {
				return
			}
			if c.native != nil {
				value, err := r.goSourceNativeChannelPayload(comm.ValueExpr)
				if err != nil {
					r.bashPPGoSendError(comm.ValueExpr, err)
					return
				}
				nativeCases = append(nativeCases, bashPPBridgeValue{Kind: "send", Elements: []bashPPBridgeValue{*c.native, value}})
				nativeArms = append(nativeArms, arm)
				liveNative = liveNative || !goSourceNativeNilChannel(c.native)
				// The interpreter-owned select below sees this arm disabled,
				// which is what a send on a nil channel is.
				cases = append(cases, reflect.SelectCase{Dir: reflect.SelectSend})
				arms = append(arms, arm)
				caseElems = append(caseElems, nil)
				continue
			}
			hasLocal = true
			v, ok := r.bashPPGoSendPayload(c, comm)
			if !ok {
				return
			}
			if r.bashPPGoSource {
				pendingSends = append(pendingSends, c)
			} else {
				if !c.beginSend() {
					r.errf("bash++: send on closed channel\n")
					r.exit.code = 2
					return
				}
				sends = append(sends, c)
			}
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectSend, Chan: reflect.ValueOf(c.ch), Send: reflect.ValueOf(v)})
			arms = append(arms, arm)
			caseElems = append(caseElems, nil)
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(c.closing)})
			arms = append(arms, nil)
			caseElems = append(caseElems, nil)
			closingCases[len(cases)-1] = true
			continue
		default:
			r.errf("invalid select case\n")
			r.exit.code = 2
			return
		}
		arms = append(arms, arm)
	}
	// Communication on a nil channel never proceeds, so dependency arms whose
	// channels are all nil need no arbitration against interpreter-owned arms:
	// the interpreter's select runs with those arms disabled.
	mixed := len(nativeCases) > 0 && hasLocal && liveNative
	if len(nativeCases) > 0 && !hasLocal {
		r.goSourceNativeSelect(ctx, nativeCases, nativeArms, def)
		return
	}
	// All channel operands and send values have now been evaluated once.
	for _, c := range pendingSends {
		if c.beginSend() {
			sends = append(sends, c)
		} else {
			for i := range cases {
				if cases[i].Dir == reflect.SelectSend && cases[i].Chan.IsValid() && cases[i].Chan.Interface() == c.ch {
					cases[i].Chan = reflect.Value{}
				}
			}
		}
		// A closed send competes with other ready arms, rather than aborting
		// evaluation or forcing selection before the language select chooses.
	}
	taskCtx := r.bashPPTaskContext(ctx)
	if len(cases) == 0 && def == nil {
		if !r.bashPPArmBeforeBlock(ctx) {
			return
		}
		if r.bashPPDeadlockIfNoTasks(ctx) {
			return
		}
		<-taskCtx.Done()
		r.bashPPTaskCanceled = true
		r.exit.code = 1
		return
	}
	if err := taskCtx.Err(); err != nil {
		r.bashPPTaskCanceled = true
		r.exit.code = 1
		return
	}
	if mixed {
		// Dependency arms are disabled (nil) entries in cases; the
		// arbitration commits at most one operation on either side.
		choice, ok := r.bashPPMixedSelect(ctx, cases, nativeCases, def != nil)
		releaseSends()
		if !ok {
			return
		}
		if choice.native >= 0 {
			r.goSourceNativeSelectArm(ctx, nativeArms[choice.native], choice.cell, choice.open)
			return
		}
		if choice.local < 0 {
			r.goSourceNativeSelectArm(ctx, def, nil, false)
			return
		}
		if closingCases[choice.local] {
			r.bashPPClosedSend()
			return
		}
		r.bashPPSelectLocalArm(ctx, arms[choice.local], caseElems[choice.local], choice.v, choice.open)
		return
	}
	ctxIndex := len(cases)
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(taskCtx.Done())})
	probeDefault := len(cases)
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectDefault})
	i, v, open := reflect.Select(cases)
	var arm *syntax.BashPPSelectCase
	if i == probeDefault {
		cases = cases[:probeDefault]
		if def != nil {
			arm = def
			i = -1 // language default has no entry in cases/arms
		} else {
			if !r.bashPPArmBeforeBlock(ctx) {
				return
			}
			if r.bashPPDeadlockIfNoTasks(ctx) {
				return
			}
			i, v, open = reflect.Select(cases)
		}
	}
	releaseSends()
	if arm == nil && i == ctxIndex {
		r.bashPPTaskCanceled = true
		r.exit.code = 1
		return
	}
	if i >= 0 && closingCases[i] {
		r.bashPPClosedSend()
		return
	}
	if arm == nil {
		arm = arms[i]
	}
	var elem *bashPPChannel
	if i >= 0 {
		elem = caseElems[i]
	}
	r.bashPPSelectLocalArm(ctx, arm, elem, v, open)
}

// bashPPSelectLocalArm binds an interpreter-owned select arm's received value
// and runs its body.
func (r *Runner) bashPPSelectLocalArm(ctx context.Context, arm *syntax.BashPPSelectCase, elem *bashPPChannel, v reflect.Value, open bool) {
	leave := r.bashPPPushScope()
	defer leave()
	if decl, yes := arm.Comm.(*syntax.BashPPShortDecl); yes {
		var value any
		if v.IsValid() {
			value = v.Interface()
		}
		r.bashPPBindReceivedCell(decl.Lhs[0].Value, r.bashPPReceivedCell(elem, value, open))
		if len(decl.Lhs) == 2 {
			r.bashPPDeclareName(decl.Lhs[1].Value, expand.Variable{Set: true, Kind: expand.String, Str: strconv.FormatBool(open)})
		}
	} else if assign, yes := arm.Comm.(*syntax.BashPPAssign); yes {
		var value any
		if v.IsValid() {
			value = v.Interface()
		}
		r.bashPPSelectReceiveAssign(assign, r.bashPPReceivedCell(elem, value, open), open)
	}
	r.stmts(r.bashPPTaskContext(ctx), arm.Stmts)
	if r.bashPPBranch == bashPPBranchBreak {
		if r.bashPPBranchEscapesEligible() {
			return
		}
		r.bashPPClearBranch()
	}
}

func (r *Runner) bashPPRange(ctx context.Context, rng *syntax.BashPPRange) {
	if r.bashPPRangeLines(ctx, rng) || r.bashPPRangeForeignIterator(ctx, rng) {
		return
	}
	// A field, index or directional conversion can hold a resident channel.
	// Classify its static type before the collection dispatcher erases its
	// capability into an aggregate value. Evaluate the operand exactly once.
	if r.bashPPGoSource && rng.Expr != nil {
		if typ, ok := r.goSourceStaticExprType(rng.Expr); ok {
			if _, channel := r.bashPPUnderlyingType(typ).(*syntax.BashPPChanType); channel {
				if c, ok := r.goSourceChannelOperand(rng.Expr, rng.Chan, "receive"); ok {
					if c.native != nil {
						r.goSourceRangeNativeChannel(ctx, rng, c.native)
					} else {
						r.bashPPRangeChannel(ctx, rng, c)
					}
				}
				return
			}
		}
	}
	if r.goSourceRangeFunction(ctx, rng) {
		return
	}
	if r.bashPPRangeCollection(ctx, rng) {
		return
	}
	if r.bashPPRangeScalar(ctx, rng) {
		return
	}
	c, ok := r.bashPPChannelOperation(rng.Chan, "receive")
	if !ok {
		return
	}
	r.bashPPRangeChannel(ctx, rng, c)
}

// bashPPRangeChannel consumes an already evaluated channel operand. Keeping
// this separate is important for `range f()`: the call result carries channel
// identity which must not be flattened to its scalar handle and re-evaluated.
func (r *Runner) bashPPRangeChannel(ctx context.Context, rng *syntax.BashPPRange, c *bashPPChannel) {
	if len(rng.Names) > 1 {
		r.errf("BASHPP-ERANGE-TYPE: channel range permits at most one iteration variable\n")
		r.exit = exitStatus{code: 2}
		return
	}
	taskCtx := r.bashPPTaskContext(ctx)
	for {
		var v any
		var open bool
		if err := taskCtx.Err(); err != nil {
			r.bashPPTaskCanceled = true
			r.exit.code = 1
			return
		}
		select {
		case v, open = <-c.ch:
		case <-taskCtx.Done():
			r.bashPPTaskCanceled = true
			r.exit.code = 1
			return
		default:
			if !r.bashPPArmBeforeBlock(ctx) {
				return
			}
			if r.bashPPDeadlockIfNoTasks(ctx) {
				return
			}
			select {
			case v, open = <-c.ch:
			case <-taskCtx.Done():
				r.bashPPTaskCanceled = true
				r.exit.code = 1
				return
			}
		}
		if !open {
			return
		}
		leave := r.bashPPPushScope()
		if len(rng.Names) == 1 {
			r.bashPPBindReceivedCell(rng.Names[0].Value, r.bashPPReceivedCell(c, v, true))
		}
		r.cmd(taskCtx, rng.Body)
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
		case bashPPBranchFallthrough:
			panic("validated fallthrough escaped to Bash++ range")
		case bashPPBranchGoto:
			return
		}
	}
}

func (r *Runner) bashPPHasRuntimeHandle(value string) bool {
	c := r.bashPPConcurrent
	if c != nil {
		c.mu.Lock()
		for handle := range c.chans {
			if strings.Contains(value, handle) {
				c.mu.Unlock()
				return true
			}
		}
		c.mu.Unlock()
	}
	if p := r.bashPPIssuedHandles; p != nil {
		p.mu.RLock()
		defer p.mu.RUnlock()
		for handle := range p.handles {
			if strings.Contains(value, handle) {
				return true
			}
		}
	}
	return false
}

func (r *Runner) bashPPVariableHasRuntimeHandle(vr expand.Variable) bool {
	if r.bashPPHasRuntimeHandle(vr.Str) {
		return true
	}
	for _, value := range vr.List {
		if r.bashPPHasRuntimeHandle(value) {
			return true
		}
	}
	for _, value := range vr.ListMap {
		if r.bashPPHasRuntimeHandle(value) {
			return true
		}
	}
	for key, value := range vr.Map {
		if r.bashPPHasRuntimeHandle(key) || r.bashPPHasRuntimeHandle(value) {
			return true
		}
	}
	return false
}
