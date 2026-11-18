// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

// Package interp implements an interpreter to execute shell programs
// parsed by the [syntax] package as either [syntax.LangBash]
// or [syntax.LangPOSIX], behaving like Bash as a result.
//
// The interpreter currently aims to behave like a non-interactive shell,
// which is how most shells run scripts, and is more useful to machines.
// In the future, it may gain an option to behave like an interactive shell.
package interp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	mathrand "math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/qiangli/bash/pathconv"
	"golang.org/x/term"
)

const BashyInheritedFdsEnv = "BASHY_INHERITED_FDS"

// BashyInheritedHandlesEnv is the Windows counterpart of
// [BashyInheritedFdsEnv]. Windows cannot pass numbered descriptors through
// exec.Cmd.ExtraFiles, so when a bashy runs this same binary it duplicates
// every open fd >= 3 as an inheritable handle and announces the mapping as
// <fd>:<r|w|rw>:0x<handle>, comma-separated (e.g. "3:r:0x1a4,10:w:0x1b8").
// A Runner adopts the entries from its environment at startup, so a CLI
// needs no extra wiring: `read -u 3`, `cat <&3` and `echo >&10` then reach
// the parent's files. Like [BashyHardIgnoreEnv] it is an internal channel:
// filtered from the environment of every child and unset from the shell's
// own scope. It is ignored off Windows.
const BashyInheritedHandlesEnv = "BASHY_INHERITED_HANDLES"

// BashyHardIgnoreEnv carries, across an exec of our own shell binary, the set
// of signals (comma-separated bash names) that the parent shell had set to
// SIG_IGN via `trap ” SIG`. The child treats them as ignored-on-entry, i.e.
// hard-ignored: a `trap` on them is a silent no-op and they list as
// `trap -- ” SIG`, matching bash's SIG_HARD_IGNORE handling (trap.c). It is an
// internal channel -- filtered out of the environment passed to grandchildren
// and unset from the child's own variable scope so scripts never observe it.
const BashyHardIgnoreEnv = "BASHY_HARD_IGNORE"

// A Runner interprets shell programs. It can be reused, but it is not safe for
// concurrent use. Use [New] to build a new Runner. In particular, callers must
// serialize Run and Subshell calls on one Runner; only shell-created copies
// (such as asynchronous lists and Bash++ go tasks) are isolated internally.
//
// Note that writes to Stdout and Stderr may be concurrent if background
// commands are used. If you plan on using an [io.Writer] implementation that
// isn't safe for concurrent use, consider a workaround like hiding writes
// behind a mutex.
//
// Runner's exported fields are meant to be configured via [RunnerOption];
// once a Runner has been created, the fields should be treated as read-only.
type Runner struct {
	// ownedExecPaths are verified identities of Bashy-owned child binaries.
	// Only these may consume the private argv handoff protocol.
	ownedExecPaths []string
	// Env specifies the initial environment for the interpreter, which must
	// not be nil. It can only be set via [Env].
	//
	// If it includes a TMPDIR variable describing an absolute directory,
	// it is used as the directory in which to create temporary files needed
	// for the interpreter's use, such as named pipes for process substitutions.
	// Otherwise, [os.TempDir] is used.
	Env expand.Environ

	// goSourceEnvironment is the explicitly configured original Go process env.
	goSourceEnvironment []string

	// writeEnv overlays [Runner.Env] so that we can write environment variables
	// as an overlay.
	writeEnv expand.WriteEnviron

	// Dir specifies the working directory of the command, which must be an
	// absolute path. It can only be set via [Dir].
	Dir string

	// dirFile retains the directory inode represented by Dir. An external
	// command inherits a shell's open cwd even if the pathname is later renamed,
	// made unsearchable, or grows beyond the kernel's pathname limit.
	dirFile *os.File

	// tempDir is either $TMPDIR from [Runner.Env], or [os.TempDir].
	tempDir string

	// Params are the current shell parameters, e.g. from running a shell
	// file or calling a function. Accessible via the $@/$* family of vars.
	// It can only be set via [Params].
	Params []string

	// Separate maps - note that bash allows a name to be both a var and a
	// func simultaneously.
	// Vars is mostly superseded by Env at this point.
	// TODO(v4): remove these

	Vars  map[string]expand.Variable
	Funcs map[string]*syntax.Stmt
	// Agentic shell declarations retain their body identity alongside the
	// public function table, which embedders may replace directly.
	bashPPAgenticFuncs map[string]*syntax.Stmt
	bashPPAgentic      bool
	// bashPPDecoratedFuncs mirrors bashPPAgenticFuncs for decorated shell
	// functions; typed functions carry their decorators on the declaration.
	// bashPPNativeDecorators and bashPPAdvice are the [Decorators] and
	// [Advice] options; bashPPDecoratorChains maps a script-visible Call to
	// the invocation it belongs to, and bashPPDecoratorStack names the
	// decorators executing right now, for cycle detection.
	bashPPDecoratedFuncs   map[string]*bashPPDecorated
	bashPPNativeDecorators map[string]DecoratorFunc
	bashPPAdvice           AdviceFunc
	bashPPDecoratorChains  map[*bashPPCell]*bashPPDecoratorChain
	bashPPDecoratorStack   []string
	bashPPDecoratorFailSeq uint64
	bashPPPredeclaredCall  bool

	// bashPPScope is the innermost Bash++ lexical block, or nil when the
	// runner is not in the bash++ dialect. Nil is the fast path every other
	// dialect takes: the read/write hooks in vars.go are one nil check.
	// bashPPGoSource selects ordinary Go package scope semantics for gosource trees.
	bashPPGoSource bool
	// bashPPScalarReceiveMemo holds cells consumed while a scalar comparison is
	// being attempted. If that attempt falls back to scalar evaluation, the
	// receive expression consumes its saved cell rather than reading the channel
	// again. It is scoped by bashPPEvalScalarBinary and is never shared between
	// evaluations or runners.
	bashPPScalarReceiveMemo map[*syntax.BashPPUnaryExpr]*bashPPCell
	// goSourceUnsafeOpaqueIDs assigns runner-owned words for
	// uintptr(unsafe.Pointer(p)). It is shared with Go-source task snapshots
	// so shared captured cells keep one observable pointer identity without
	// exposing host heap addresses.
	goSourceUnsafeOpaqueIDs *goSourceUnsafeOpaqueIDs
	// bashPPConvertHashQY is runner-local compiler policy. It is selected from
	// the runner's own GOFLAGS snapshot for a Go-source run, never process-global
	// state, so concurrent runners may faithfully use different conversion
	// implementations.
	bashPPConvertHashQY bool
	bashPPGoSourceDecls map[string]bool
	// bashPPGoSourcePending holds the package-level type names installed by
	// the pre-registration pass but not yet reached by their own statement.
	bashPPGoSourcePending    map[string]bool
	bashPPGoSourceFile       *syntax.File
	goSourceFieldTrackTarget string
	goSourceFieldTrackReport string
	goSourceTesting          *GoSourceTestingSession
	bashPPScope              *bashPPScope
	// bashPPFuncScopes records, per function name, the lexical environment
	// visible where the function was defined. It is preserved across
	// [Runner.Reset] for the same reason Funcs is: a function that survives a
	// reset without its closure would resolve its free identifiers against
	// whatever happens to be in scope at the call, which is precisely the
	// dynamic binding this scope chain exists to avoid.
	bashPPFuncScopes map[string]*bashPPScope
	// bashPPImports is the import namespace for this runner session. The
	// evaluator is deliberately package-private and replaceable in tests and
	// alternate toolchain adapters without exposing an evaluator ABI.
	bashPPImports                   map[string]string
	bashPPImportedInterfacePackages map[*syntax.BashPPInterfaceType]string
	bashPPTools                     bashPPToolchain

	// bashPPFuncs holds the Go-form (typed) functions declared with `func`,
	// each paired with the lexical scope captured where it was defined so the
	// body's free identifiers close over their definition site, not the call
	// site. It is preserved across [Runner.Reset] alongside Funcs for the same
	// reason bashPPFuncScopes is.
	bashPPFuncs map[string]*bashPPFunc
	// bashPPHoistedDecls are the function declarations a runner fence
	// evaluated at prepare; their statements are skipped when reached in
	// order, since a Bash++ function is declared once per session.
	bashPPHoistedDecls map[*syntax.BashPPFuncDecl]bool
	// Foreign modules are private runtime records. They never enter shell
	// variables or the environment.
	bashPPForeignFuncs   map[string]*bashPPFunc
	bashPPForeignModules []*polyglot.Module
	bashPPForeignImports map[string]*polyglot.Module
	// bashPPSeededImports are direct imports an embedder handed the runner
	// through [ForeignImports]: a lowered program's own import bindings, so a
	// shell region it runs resolves `alias.fn` to the program's worker. They
	// outlive every file run (a file's own imports shadow them) and the
	// runner never closes them.
	bashPPSeededImports map[string]*polyglot.Module
	// bashPPTypes and bashPPMethods are the runner-local named-type namespace.
	// They persist with the session and are cloned for subshell isolation.
	bashPPTypes   map[string]bashPPType
	bashPPMethods map[string]map[string]*bashPPFunc
	// bashPPShared marks the program tables a native callback frame still
	// holds by reference instead of owning. They are copy-on-write; see
	// [bashPPSharedTables].
	bashPPShared bashPPSharedTables
	// bashPPSelectionCache memoizes breadth-first Go selector resolution
	// ([Runner.bashPPResolveSelectionIn]). The lookup is a pure function of
	// the runner's static type and method tables and the selector inputs, but
	// it reallocates edge slices, ancestor maps and struct-field views on
	// every field or method access — the dominant cost of an interpreted
	// struct-heavy program. It is keyed by the canonical identity of the type
	// selected on, not by the type node the evaluator handed in: those nodes
	// are synthesized per evaluation for a root reached through a value, so a
	// node key made the map grow with the number of accesses rather than with
	// the program. Because an entry now outlives the node, every mutation of
	// the type or method tables drops the map
	// ([Runner.bashPPInvalidateSelectionCache]). The cache is deliberately
	// NOT copied into a subshell or Reset clone: those rebuild bashPPMethods
	// into fresh *bashPPFunc objects, so a shared entry would hand back a
	// stale method, and a nil map is lazily rebuilt against the clone's own
	// tables. It is only ever touched by its owning Runner's goroutine.
	bashPPSelectionCache map[bashPPSelectionCacheKey]bashPPSelection
	// bashPPNativeFuncNames memoizes the set of unqualified names that resolve
	// to a native (bodyless) companion function or a mapped-companion runtime
	// symbol for bashPPNativeFuncNamesFile. bashPPBridgeHandles consults it to
	// classify every single-identifier call; recomputing it rescanned every
	// program statement — and could os.ReadDir the source directory — on each
	// such call, a per-call cost that grows with program size. It is keyed by
	// the source File so it self-invalidates when Run swaps bashPPGoSourceFile,
	// and is not copied into a subshell or Reset clone (a nil set is rebuilt).
	bashPPNativeFuncNames     map[string]bool
	bashPPNativeFuncNamesFile *syntax.File
	// bashPPTypeParamArgs binds the type parameter names of the generic
	// function or method whose body is executing RIGHT NOW to their type
	// arguments. It is frame-scoped rather than stacked: entering any frame
	// replaces it with that callee's own arguments — nil when the callee is
	// not generic — so a callee never resolves its caller's `T`, and two
	// instantiations of one method cannot see each other's binding. See
	// bashpp_generic_body.go.
	bashPPTypeParamArgs map[string]syntax.BashPPTypeExpr
	// bashPPClosures is the registry a function-literal value refers to. A
	// closure cannot be stored in a shell variable directly — variables hold
	// strings, and a subshell copies them as bytes — so the variable holds a
	// handle into this slice and the closure itself stays with the runner,
	// where [bashPPCloner] can copy it alongside the scopes it captured. It is
	// preserved across [Runner.Reset] for the same reason bashPPFuncs is.
	bashPPClosures []*bashPPFunc
	// bashPPGoSourceCapture is the set of cells the task currently being
	// snapshotted captures lexically, in GoSource mode only. It is set for the
	// duration of one [Runner.bashPPTaskSnapshot] and is read by the scope
	// cloner, which aliases these cells instead of copying them; see
	// gosource_task_capture.go. Nil everywhere else, which is what keeps the
	// classic Bash++ deep-copy snapshot unchanged.
	bashPPGoSourceCapture map[*bashPPCell]bool
	// bashPPGoSourcePin is the function value a launched GoSource task already
	// resolved in its parent. It keeps the task on that exact function and
	// keeps a computed callee from being evaluated a second time; see
	// gosource_task_capture.go. Nil everywhere else.
	bashPPGoSourcePin *bashPPGoSourcePin
	// bashPPTransportPath is the set of pointer origins on the bridge
	// transport walk in progress, nil between walks; a pointer met again on
	// its own path crosses as a back-reference. See
	// bashpp_sprint165_runtime2_cycle.go.
	bashPPTransportPath map[uint64]bool
	// bashPPGoSourceSharableCells memoizes, per cell, whether GoSource task
	// capture may grant it identity. The answer is taken while this runner is
	// still the cell's sole owner and never revisited; see
	// [Runner.bashPPGoSourceSharable]. A task inherits its own CLONE of the
	// map, never the map itself, so the record of what is already shared
	// reaches nested launches without two goroutines writing one map.
	bashPPGoSourceSharableCells map[*bashPPCell]bool
	// bashPPDeferStack is the LIFO stack of deferred calls awaiting the return
	// of the func invocations currently on the call stack. Each invocation
	// remembers the stack length it entered at and runs everything pushed above
	// that mark, in reverse, as it unwinds.
	bashPPDeferStack []bashPPDeferred
	// bashPPRangeDefer, when non-nil, is the sink that a defer statement executed
	// directly inside a range-over-function body appends to instead of the live
	// stack. Go binds such a defer to the ENCLOSING function, not to the yield
	// callback or the iterator: the iterator sits on top of the enclosing frame
	// on [Runner.bashPPDeferStack], so leaving it there would run — or discard —
	// the cleanup when the iterator returns rather than when the enclosing
	// function does. The buffer is held on the range's yield state and spliced
	// onto the enclosing frame's region once the iterator has finished; see
	// [Runner.goSourceRangeFunction]. A frame entered for an ordinary call made
	// from the body clears this so that callee's own defers are unaffected.
	bashPPRangeDefer *[]bashPPDeferred
	// bashPPReturn carries a Go-form return across the body's statement loop:
	// active is set by a [syntax.BashPPReturn] and consumed by the invoker.
	bashPPReturn bashPPReturnState
	// bashPPFuncActive is non-zero while a Go-form function body is running;
	// it lets ordinary assignment dispatch give bare identifier expressions
	// their Go meaning without changing shell assignments elsewhere.
	bashPPFuncActive int
	// bashPPCallbackReturnDepth is the exact Go-form function depth of the
	// mirrored method invocation currently being serviced by this runner. It is
	// invocation state, not toolchain/session state: subshells and Go tasks must
	// start with zero so a callback marker cannot authorize an unrelated return
	// in a cloned runner.
	bashPPCallbackReturnDepth int
	// bashPPShadowedTypes records the type declarations executed inside Go-form
	// function bodies that shadow a registry entry, so the frame that leaves
	// restores what it shadowed; see [Runner.bashPPShadowLocalType].
	bashPPShadowedTypes []bashPPShadowedType
	// bashPPSourcePackages caches, per linked Go source file (keyed by its
	// base offset), the linked-package tag its declarations carry; see
	// goSourcePackageAt in gosource_struct_identity.go.
	bashPPSourcePackages     map[uint]string
	bashPPSourcePackagesFile *syntax.File
	// bashPPLocalTypes indexes the function-local type declarations of the
	// linked Go source, built on first use; see
	// bashpp_sprint162_type_scope.go.
	bashPPLocalTypes *goSourceLocalTypeIndex
	// bashPPShortTxn makes every := result producer use the same atomic
	// current-scope commit rules. Transactions nest across function calls, so a
	// declaration evaluated by an RHS cannot be mistaken for the caller's LHS.
	bashPPShortTxn *bashPPShortDeclTxn
	// goSourceLivenessFirst/Len/Plan memoize this runner's last
	// goSourceLivenessFor answer, keyed exactly as the shared cache is.
	goSourceLivenessFirst *syntax.Stmt
	goSourceLivenessLen   int
	goSourceLivenessPlan  *goSourceLivenessPlan
	// bashPPRangeKeyReuse memoizes, per range statement, whether its body can
	// retain the key variable; see bashPPRangeKeyReusable. It belongs to one
	// runner and is never copied to another.
	bashPPRangeKeyReuse map[*syntax.BashPPRange]bool
	// bashPPShortFailureSeq lets a function invocation distinguish an ordinary
	// non-zero body status from a diagnosed short-declaration failure which
	// must not be cleared while settling declared results.
	bashPPShortFailureSeq uint64
	// bashPPRecoverSeq counts the times `recover` has reported "nothing to
	// recover" through the exit status. It is the provenance a hosted test
	// callback needs: only a status this counter stamped may be reduced, and
	// only when the counter moved while that one callback ran.
	bashPPRecoverSeq uint64
	// bashPPPanic is the panic currently unwinding this shell, if any. It is
	// deliberately NOT copied into a subshell: a panic is scoped to the shell
	// that raised it, exactly as a Go panic is scoped to its goroutine.
	bashPPPanic bashPPPanicState
	// bashPPDeferDepth is the call-stack depth a DIRECTLY deferred invocation
	// runs at, or zero when no deferred call is running. It is the whole of
	// Go's "recover was not called directly by a deferred function" rule: a
	// recover succeeds only where len(callStack) equals it.
	bashPPDeferDepth int
	// bashPPConcurrent is intentionally runner-session local.  It is shared
	// only by Bash++ go tasks, never by shell copies such as subshells.
	bashPPConcurrent    *bashPPConcurrent
	bashPPIssuedHandles *bashPPHandleProvenance
	// bashPPProcs registers live start(...) handles; shared with subshells.
	bashPPProcs          *bashPPProcessTable
	bashPPCallChannels   []*bashPPChannel
	bashPPCallInterfaces []*bashPPInterfaceValue
	bashPPCallCells      []*bashPPCell
	// bashPPCallSpread records that the pending call's last argument was
	// written `xs...`. Go gives that spelling a different meaning from passing
	// the elements one by one — the callee receives the caller's own slice
	// rather than a fresh one — and only the call site knows which was written.
	bashPPCallSpread bool
	// bashPPResultCells is the authoritative value transport for the most
	// recent typed function invocation. The parallel string results remain the
	// shell-facing representation; these cells retain pointer, channel,
	// closure, interface, collection, and declared-type provenance.
	bashPPResultCells  []*bashPPCell
	bashPPGoTask       bool
	bashPPMainGoexit   bool
	bashPPHostedTask   bool
	bashPPHostedDecls  map[string]BashPPDeclaration
	bashPPDeclRefused  bool
	bashPPChanBoundary bool
	bashPPFileRun      bool
	bashPPRunCancel    context.CancelFunc
	bashPPTaskFiles    []*os.File
	bashPPTaskState    *bashPPTaskState
	bashPPTaskCanceled bool
	// bashPPForwardedDeath is the number of a parent-delivered signal this
	// process proxied to the interpreted Go program's dependency process and
	// that killed it. The program left no handler for it, so once the
	// interpreter has unwound (bridge closed, scratch removed) Run reproduces
	// the same death on this host instead of returning 128+sig as a normal
	// exit; the signal's sender addressed this PID, and only the proxy
	// subscription kept its default action from running here already.
	bashPPForwardedDeath int
	bashPPTaskFailed     bool
	bashPPTaskFailCode   uint8
	bashPPLogicalDepth   int
	bashPPCustomOpen     bool

	// funcSources records the script name active when a function was
	// defined. Bash reports runtime diagnostics in a function body against
	// the definition source, not necessarily the caller's source.
	funcSources map[string]string

	alias map[string]alias

	// callHandler is a function allowing to replace a simple command's
	// arguments. It may be nil.
	callHandler CallHandlerFunc

	// execHandler is responsible for executing programs. It must not be nil.
	execHandler ExecHandlerFunc
	// A bare default handler always launches an OS process for a literal call.
	bareExecHandler bool

	// execReplacement is shared with in-process subshells so an asynchronous
	// `kill $$` can address a foreground command standing in for `exec`.
	execReplacement *execReplacementState

	// execMiddlewares grows with calls to [ExecHandlers],
	// and is used to construct execHandler when Reset is first called.
	// The slice is needed to preserve the relative order of middlewares.
	execMiddlewares []func(ExecHandlerFunc) ExecHandlerFunc

	// openHandler is a function responsible for opening files. It must not be nil.
	openHandler OpenHandlerFunc
	// dryRunOpenHandler overrides openHandler only while dryrun is on.
	dryRunOpenHandler OpenHandlerFunc

	// readDirHandler is a function responsible for reading directories during
	// glob expansion. It must be non-nil.
	readDirHandler ReadDirHandlerFunc2

	// statHandler is a function responsible for getting file stat. It must be non-nil.
	statHandler StatHandlerFunc

	stdin            *os.File // e.g. the read end of a pipe
	stdinTTYFallback bool
	stdinDevTTY      bool
	stdout           io.Writer
	stderr           io.Writer
	// pipelineOutput marks a runner executing the in-process left side of a
	// pipeline. Builtin writes on that side need thread-local SIGPIPE handling:
	// unlike an external child, the simulated subshell shares our process and
	// must not leak its writer signal into the enclosing shell's traps.
	pipelineOutput bool
	// pipelineWriteBroken is set by pipelineWriter when an in-process
	// pipeline child sees EPIPE on stdout or on stderr under |&.
	pipelineWriteBroken atomic.Bool

	ecfg *expand.Config
	ectx context.Context // just so that Runner.Subshell can use it again

	// dialect is the shell language variant the runner implements, and the
	// interp-side half of the dialect seam. The zero value is
	// [syntax.LangBash]; only [syntax.LangBashPP] changes any behaviour.
	// See [Lang]. It is normally fixed at construction, but `set -o bashpp` /
	// `set +o bashpp` toggle it live between [syntax.LangBash] and
	// [syntax.LangBashPP]; origDialect snapshots the construction-time value so
	// Reset restores it.
	dialect          syntax.LangVariant
	origDialect      syntax.LangVariant
	hideBashPPOption bool

	// didReset remembers whether the runner has ever been reset. This is
	// used so that Reset is automatically called when running any program
	// or node for the first time on a Runner.
	didReset bool

	usedNew bool

	filename            string // only if Node was a File, or set for incremental runs
	incrementalFilename string
	interactiveShell    bool

	// cmdlineNoExec records that noexec was requested as a command-line
	// flag (`bash -n`) rather than via `set -n`. Bash honours that form
	// even in an interactive shell, and no `set +n` can clear it — see
	// [Runner.stop].
	cmdlineNoExec bool
	mirrorUmask   bool
	commandString bool
	standardInput bool

	// curStmtPos is the position of the currently executing top-level
	// statement, updated at the top of stmtSync. Error sites that have
	// no other pos to hand (setVar/readonly, builtin-internal failures
	// reached via paths that don't carry a Pos) use it to drive
	// [Runner.bashErrPrefix] so the `<file>: line N:` prefix lands.
	curStmtPos syntax.Pos
	curStmtEnd syntax.Pos

	// discardNextStmt keeps one following top-level statement skipped
	// for bash arithmetic-expansion errors that abort the rest of a
	// physical line rather than only the current simple command.
	discardNextStmt bool

	// discardRestOfLine, when non-zero, names a source line whose
	// remaining top-level statements bash skips. A standalone assignment
	// to a readonly variable (`RO=z; echo skipped`) aborts the rest of
	// its physical line but not subsequent lines, so the runner skips
	// every following top-level statement sharing this line number.
	discardRestOfLine uint

	// enclosingSubshellEnd is set while executing statements inside a
	// foreground subshell. Bash 5.3 reports some fatal declaration errors
	// at the closing ")" rather than at the inner declaration.
	enclosingSubshellEnd syntax.Pos

	// setVarFromBuiltin is non-empty while a declare-family builtin
	// (declare/typeset/local/readonly/export) is parsing a string
	// argument like `readonly -a 'name=value'` and assigning into the
	// variable space. When set, [Runner.setVar]'s error message uses
	// the builtin's name (e.g. "readonly:") instead of the enclosing
	// function name. Bash 5.3 attributes array-conversion-from-string
	// errors to the builtin and syntax-level assignment errors to the
	// enclosing function.
	setVarFromBuiltin string

	// setVarStringParsed is true while a declare-family builtin is
	// processing a string-form arg (`readonly 'name=value'`), even
	// when no array flag is in use. Bash 5.3 suppresses the function-
	// name attribution for these — the error gets no extra prefix
	// beyond `<file>: line N: <var>:`.
	setVarStringParsed bool

	// setVarArrayLiteral is true while a declare-family builtin is
	// processing a syntax-level array-literal assignment
	// (`readonly -a a=(1 2 3)`). Bash 5.3 attributes a failure here
	// to the enclosing function; scalar syntax-level assignments
	// (`readonly r='(5)'`) get no extra prefix.
	setVarArrayLiteral bool

	// declAssignContext is true while a declare-family clause is
	// processing its assignments. Some bash semantics differ between
	// declare-context (`declare a=v`, `readonly a=v`) and an inline
	// prefix-assignment (`a=v cmd`), notably preserving an existing
	// variable's array kind on scalar assignment.
	declAssignContext bool

	// exportedFuncs tracks function names marked for export via
	// `export -f <name>`. When the runner spawns a child process,
	// each entry becomes a `BASH_FUNC_<name>%%=() { … }` env var
	// so the child can re-import the function on startup. Bash's
	// way of propagating shell functions across `exec`.
	exportedFuncs map[string]bool

	// readonlyFuncs tracks function names marked read-only via
	// `readonly -f <name>`. Subsequent redefinition or unset of
	// the function is rejected, and `declare -fr` / `readonly -f`
	// lists only these names.
	readonlyFuncs map[string]bool

	// funcTrace tracks function names given the trace attribute via
	// `declare -ft <name>` (or `declare -tf`). A traced function
	// inherits the DEBUG and RETURN traps even without the global
	// `set -T` (functrace) option — see [Runner.shouldFireDebugTrap].
	funcTrace map[string]bool

	// inlineRestoreFrames tracks active command-prefix assignment restore
	// scopes. A special builtin nested inside a function can mark the exact
	// enclosing frame whose restore must be skipped when its assignment
	// leaks outward.
	inlineRestoreFrames []*inlineRestoreFrame

	// tempEnv records variable names bound by inline assignments for
	// the in-flight call (`a=4 foo`). Bash lets `local a` (no value)
	// inside the callee inherit the temporary-environment value even
	// without shopt localvar_inherit; setVar consults this set to
	// keep that inherit while plain exported globals stay uninherited.
	// Saved/restored around each call so nested calls see the outer
	// binding (the temp env spans the whole call stack).
	tempEnv map[string]bool

	// argv0 is bash's $0 / $BASH_ARGV0 — initialized from filename
	// but separately settable by user code. Error-message prefixes
	// continue to use filename so they stay stable across user
	// reassignments of BASH_ARGV0.
	argv0 string

	// origArgv0 preserves an explicitly configured argv0 (see [WithArgv0])
	// across [Runner.Reset], which otherwise zeroes the live argv0.
	origArgv0 string

	// >0 to break or continue out of N enclosing loops
	breakEnclosing, contnEnclosing int
	// bashPPBranch carries one validated branch through nested typed blocks.
	// bashPPBranchDepth counts eligible enclosing control statements.
	bashPPBranch      bashPPBranchKind
	bashPPBranchDepth int
	// bashPPGotoLabel names the label a pending goto is unwinding towards.
	bashPPGotoLabel string

	inLoop        bool
	inFunc        bool
	inSource      bool
	inTimeClause  bool         // suppress inner `time` keyword's output
	timing        *timingScope // CPU accumulator for the outermost `time` clause
	handlingTrap  bool         // whether we're currently in a trap callback
	trapEntryExit exitStatus   // exit status upon entering the current trap
	xtraceLevel   int          // xtrace indirection depth (trap handlers add one)

	// track if a sourced script set positional parameters
	sourceSetParams bool

	// noErrExit prevents failing commands from triggering [optErrExit],
	// such as the condition in a [syntax.IfClause].
	noErrExit bool

	// The current and last exit statuses. They can only be different if
	// the interpreter is in the middle of running a statement. In that
	// scenario, 'exit' is the status for the current statement being run,
	// and 'lastExit' corresponds to the previous statement that was run.
	exit     exitStatus
	lastExit exitStatus

	// lastSignaled preserves the typed termination of the most recent Run.
	// Runner.Run still returns the historical ExitStatus value so callers
	// comparing errors directly do not break.
	lastSignaled    SignaledStatus
	lastWasSignaled bool
	// An owner's default signal controls incremental EXIT cleanup; an
	// ordinary foreground child's signaled status does not.
	lastWasOwnerSignaled bool

	lastExpandExit     exitStatus // used to surface exit statuses while expanding fields
	lastExpandCmdSubst bool       // whether lastExpandExit came from a command substitution
	expandRunExit      exitStatus // expansion failures which affect Run but not $?

	// outErr records the most recent error from r.out/r.outf (a builtin's
	// standard-output write). Bash fails an output builtin whose stdout write
	// hits a closed fd (`echo >&-`) with a non-zero exit; the builtin
	// dispatcher inspects this after running each builtin.
	outErr error

	// stdinClosed marks fd 0 as explicitly closed (`cmd <&-`). r.stdin is
	// nil in that case (as for "no stdin"), but a closed fd must make a
	// spawned command's read fail with EBADF rather than read an empty
	// /dev/null, so the exec handler synthesizes a genuinely closed
	// descriptor. Saved/restored around each statement's redirects.
	stdinClosed bool

	// lastArithErr captures the most recent error from r.arithm so
	// callers (notably the C-style for-loop) can detect arithmetic
	// failures and abort, matching bash 5.3 — a runtime arith error
	// inside `for ((init; cond; post))` terminates the loop. The
	// runner clears it before each arithm call site that cares.
	lastArithErr error

	// bashSource holds the original script bytes for bash-compatible
	// diagnostics that must preserve source spelling rather than printer
	// output, notably arithmetic errors.
	bashSource []byte

	// stdinSource tracks source bytes that originally came from fd 0.
	// bashy buffers stdin before parsing; reads from sourced scripts and
	// child commands must still consume the same logical input stream.
	stdinSourceActive     bool
	stdinSourceOffset     int
	stdinSourceBaseOffset int
	// stdinScript reports that the bytes in bashSource were read FROM fd 0 —
	// `bash < script`. Only then does the stdin-script quirk apply, so it
	// cannot be inferred from an unnamed *syntax.File: an embedder that parses
	// an unnamed script while handing the runner an unrelated stdin pipe (an
	// editor transcript, message lines) has a stdin that is data, not source.
	// Set with [WithStdinScript].
	stdinScript bool
	// stdinRedirected is set while a command's fd 0 is bound to an explicit
	// input redirect (`<`, `<<`, `<<<`). It suppresses the stdin-source reader
	// above, so a heredoc/here-string/file redirect wins over the
	// "stdin-script commands consume subsequent lines" feature — matching bash,
	// where `cat <<E` reads the heredoc body, not later script lines. Saved and
	// restored around each statement's redirects like r.stdin.
	stdinRedirected bool
	// asyncStdinExplicit is true only while fd 0 comes from syntax enclosing
	// the command being evaluated (a redirect, heredoc, or pipeline). Unlike
	// stdinRedirected it does not persist after `exec <file`: POSIX still gives
	// a later asynchronous list /dev/null unless that list's syntax supplies
	// stdin of its own.
	asyncStdinExplicit bool
	// origStdinAsyncCopy records that StdIO adapted an arbitrary Reader through a
	// copier goroutine. Its readiness and cancellation are not cooperative,
	// so Bash++ tasks fail closed when that original stdin is active.
	origStdinAsyncCopy bool

	// aliasLineOverride is non-zero while expanding a multi-stmt
	// alias body. bashErrPrefix prefers it over the AST stmt's own
	// Pos().Line() so runtime errors from inside an alias body
	// (`command not found`, etc.) report the call site's line in
	// the source script rather than the body-relative line in the
	// alias-body parse.
	aliasLineOverride int
	aliasReparseDepth int

	// aliasBase and aliasDefOverride implement bash's alias-definition
	// *timing*: an alias is expanded only for input read after the line
	// that defined it (commands on the same parse unit / same line as the
	// definition are not affected). The runner walks a pre-parsed AST
	// rather than reading line-by-line, so timing is reconstructed from
	// effective line numbers:
	//
	//   defLine(alias) = aliasDefOverride>0 ? aliasDefOverride : aliasBase+pos.Line()
	//   useLine(cmd)   = aliasBase + cmd.Pos().Line()
	//   expand iff useLine > defLine
	//
	// aliasBase shifts a freshly re-parsed unit (eval/source bodies,
	// successive interactive input reads) past everything read before it,
	// so its tokens see all already-defined aliases while still honoring
	// internal line ordering. aliasDefOverride pins aliases *defined*
	// inside such a re-parsed unit to the outer call's line, matching
	// bash: `eval 'alias g=...'` makes g visible on the next outer line
	// but not on the same one.
	aliasBase        int
	aliasDefOverride int

	// funsubLineOffset is applied to bash-style diagnostic line
	// numbers while executing a multi-line `${ ...; }` funsub body.
	// Bash 5.3 reports runtime command diagnostics one line later
	// than the physical AST position in that form.
	funsubLineOffset int

	// assignNamerefName, when non-empty, overrides the variable name
	// used in a readonly-variable diagnostic from an array or
	// indexed-element assignment made through a nameref. Bash is
	// asymmetric here: a scalar nameref assignment (`r=5`) reports the
	// resolved target name, but an array append or element store
	// (`r+=(4)`, `r[0]=9`) reports the nameref name as written. It is
	// set narrowly around the assignment and cleared immediately after.
	assignNamerefName string

	// evalLineOffset is added to bash-style diagnostic line numbers
	// while executing the body of an `eval` (parsed from a string).
	// Bash keeps eval'd code anchored to the absolute script line
	// where it physically sits, so an `eval` on line N reports a
	// runtime error in its (line-1-based) body at N+line-1. It is
	// reset to zero across a function-body call, since a function
	// invoked from eval reports its own definition line, not the
	// eval call's. See the eval builtin and the function dispatch.
	// evalExec is non-zero whenever such a body is running, which also
	// redirects a few builtin-attributed diagnostics (array-conversion)
	// to `eval`.
	evalLineOffset int
	evalExec       int

	// bgProcs holds all background shells spawned by this runner.
	// Their PIDs are 1-indexed, from 1 to len(bgProcs), with a "g" prefix
	// to distinguish them from real PIDs on the host operating system.
	//
	// Note that each shell only tracks its direct children;
	// subshells do not share nor inherit the background PIDs they can wait for.
	bgProcs []*bgProc

	// lastBang is the most recently started asynchronous job. Unlike bgProcs,
	// it is deliberately retained after wait reaps that job: Bash keeps $!
	// unchanged until another asynchronous job is started.
	lastBang *bgProc

	// inheritedBang is the caller's most recent background job, visible
	// only through $! in subshells. It is intentionally not waitable by
	// this runner; bash lets a subshell expand $! from its parent but
	// still reports "not a child of this shell" for wait $!.
	inheritedBang *bgProc

	// jobsReadOnly is set for command substitutions that may display the
	// parent's job table via `jobs` but may not manipulate those jobs with
	// `fg` or `bg`.
	jobsReadOnly bool

	// preferredJobID records a job Bash has explicitly made current, such
	// as a stopped job continued with `kill -CONT %N` or `bg %N`.
	preferredJobID int

	// doneBgPids keeps exit statuses for completed background jobs after
	// they leave bgProcs, so a later `wait <pid>` can still report the
	// saved status like bash's bgpids table.
	doneBgPids map[int64]exitStatus

	// coprocSeq counts coprocs started by this runner; it seeds the
	// synthetic, unique [bgProc.coprocPid] reported in `<NAME>_PID`.
	coprocSeq int64

	// coprocFds maps a live coproc pipe fd to the array variable element
	// it backs, so that closing the fd (e.g. `exec {fd}<&${COPROC[0]}-`)
	// rewrites that element to "-1", the way bash marks a closed coproc
	// descriptor. Entries are removed when the fd is closed or reaped.
	coprocFds map[int]coprocFdRef

	// coprocReg resolves a coproc's synthetic `<NAME>_PID` back to its
	// bgProc. It is shared by pointer with subshells (unlike bgProcs,
	// which subshells do not inherit) so that `kill $COPROC_PID` works
	// from a background subshell — bash's coproc pid is a real OS pid that
	// any process can signal, and this is how we reach the real child of a
	// coprocess run as a goroutine. Lazily created by the first coproc.
	coprocReg *coprocReg

	// coprocReapedFds records coproc pipe fds released by reapCoproc during
	// the current statement. stmtSync snapshots/restores fdTable around a
	// redirected statement; reaping a coprocess (e.g. via `wait $PID >f`)
	// closes its fds, and that removal must survive the restore rather than
	// being undone. Reset at the start of each stmtSync.
	coprocReapedFds map[int]bool

	opts runnerOpts

	origDir          string
	origParams       []string
	origOpts         runnerOpts
	origNoOpSetState map[string]bool
	origStdin        *os.File
	origStdout       io.Writer
	origStderr       io.Writer

	// Most scripts don't use pushd/popd, so make space for the initial PWD
	// without requiring an extra allocation.
	dirStack     []string
	dirBootstrap [1]string

	// driveCwd records the current directory per Windows drive letter
	// (uppercase), so `cd D:` returns to where the shell last was on D:.
	// Nil until the first cd onto a drive path; unused off Windows.
	driveCwd map[byte]string

	optState getopts

	// redirScopes tracks nested statement cleanup. An exec redirection inside
	// a function must persist through enclosing function-call statements, but
	// must stop at an enclosing command which has its own temporary redirects.
	redirScopes []redirScope

	// lateRedirs holds a simple command's redirections when they must be
	// applied AFTER its words are expanded rather than before — the POSIX
	// order (expand words, then perform redirections). stmtSync stashes them
	// here for a CallExpr whose arguments contain a command substitution, and
	// the CallExpr handler in cmd() applies them once the fields are expanded
	// (so e.g. `echo $(cat f) > f` reads f before the redirect truncates it).
	// lateRedirClosers collects the opened closers for stmtSync to close.
	lateRedirs       []*syntax.Redirect
	lateRedirClosers []io.Closer

	// trapCallbacks maps signal/pseudo-signal names to trap handler code.
	// Supported keys: EXIT, ERR, DEBUG, RETURN, and signal names like INT, TERM, etc.
	trapCallbacks map[string]string
	// asyncDefaultIgnored records INT/QUIT ignores injected by an
	// asynchronous-list default. asyncDefaultReset records an explicit
	// `trap -` which changed that inherited ignore back to default.
	asyncDefaultIgnored map[string]bool
	asyncDefaultReset   map[string]bool

	// inheritedExitTrap is set in subshells where EXIT is visible to
	// `trap -p`, but reset for execution until the subshell sets it.
	inheritedExitTrap bool

	// asyncList marks a runner executing a POSIX asynchronous list (`cmd &`).
	// Such runners get SIGINT/SIGQUIT ignored by default while job control is
	// disabled; external commands launched from them must inherit that state.
	asyncList bool
	// asyncProc is the job record owning this asynchronous runner. It is
	// inherited by foreground subshells nested inside the async list.
	asyncProc *bgProc

	// callStack tracks function call frames for caller/BASH_SOURCE/BASH_LINENO/FUNCNAME.
	callStack []callFrame
	// goSourceFrameSeq, goSourceFault and goSourcePCs back Go's runtime
	// stack introspection of interpreted frames; see
	// bashpp_sprint162_nilptr2_stack.go.
	goSourceFrameSeq uint64
	goSourceFault    *goSourceFaultStack
	goSourcePCs      []goSourceStackFrame
	// goSourceLiteralNameIndex names the program's function literals as Go
	// names their frames; see bashpp_sprint165_frames_names.go.
	goSourceLiteralNameIndex map[*syntax.BashPPFuncLit]string
	// goSourceReflectingFunction is set while the operand of reflect.ValueOf
	// is bridged; see bashPPBridgeFunction.
	goSourceReflectingFunction bool
	// goSourceLocalCallbackArg is set while the callback operand of an
	// interpreter-answered generic slices helper is bridged; see
	// bashPPBridgeFunction and gosource_callback_bridge.go.
	goSourceLocalCallbackArg bool

	// exitTrapCallStack preserves the function stack for an EXIT trap
	// triggered by `exit` from inside a function.
	exitTrapCallStack []callFrame

	// localOptStack tracks function frames that used `local -`,
	// which asks bash to restore shell option state on return.
	localOptStack []localOptFrame

	// cmdHashTable caches resolved command paths and lookup counts for the hash builtin.
	cmdHashTable map[string]cmdHashEntry

	// disabledBuiltins tracks builtins disabled via "enable -n".
	disabledBuiltins map[string]bool

	// dynamicBuiltins tracks fake dynamically-loaded builtins accepted
	// via "enable -f" for Bash compatibility tests.
	dynamicBuiltins map[string]bool

	// completionSpecs stores programmable-completion specs registered
	// via the complete builtin.
	completionSpecs map[string]completionSpec

	// noOpSetState tracks the on/off state of accept-and-ignore
	// `set -o` options (history, monitor, privileged, etc.). The
	// runner doesn't otherwise act on these flags, but it must
	// remember the value so `set -o` listings echo back what the
	// script most recently asserted.
	noOpSetState map[string]bool

	// settingIgnoreEOFOption is true while set/shopt is synchronizing
	// IGNOREEOF from the ignoreeof option, not from a user assignment.
	settingIgnoreEOFOption bool

	// bgPidCallback, when non-nil, is invoked with the OS PID of every
	// real process this runner spawns from a backgrounded statement
	// (`foo &`). Set via [WithBgPidCallback]. Outpost uses this to
	// publish detached PIDs to its job-control registry — the in-shell
	// `fg`/`bg`/`jobs` builtins are unimplemented because subshells
	// here are goroutines, not OS processes.
	bgPidCallback func(pid int)

	// jobCarrier, when non-nil, supplies one kernel-visible carrier
	// process per background job, whose real PID becomes the job's
	// identity in `$!`/`jobs`/`wait`. Set via [WithJobCarrier]; nil
	// runners keep the opaque "g<N>" handles.
	jobCarrier JobCarrier

	// sigReset, when non-nil, is asked to restore a real OS default
	// signal disposition (SIG_DFL) whenever `trap - SIG` resets a signal
	// the shell had made non-default. Set via [WithSignalResetter]; nil
	// runners never mutate the host process's global signal dispositions
	// toward a terminating default, which is the safe embedded default.
	sigReset SignalResetter

	// pipeStatus tracks exit codes from the last pipeline for PIPESTATUS.
	pipeStatus []string

	// promptExpand is called by ${var@P} to expand prompt escape sequences.
	// If nil, a default basic expansion is used.
	promptExpand func(string) string

	// startTime records when the shell was created, for the SECONDS variable.
	startTime time.Time

	// subshellLevel tracks the nesting depth of subshells, for BASH_SUBSHELL.
	subshellLevel int

	// umask is this Runner's virtual file-creation mask. It is applied at
	// [Runner.open] when O_CREATE is set in the flags, never via
	// [syscall.Umask] (which would clobber other Runners in the same
	// process). A custom [OpenHandler] that bypasses [Runner.open]
	// won't see the mask applied.
	umask int

	// loginShell, when true, allows the `logout` builtin to exit the shell.
	// Set via [WithLoginShell]. Bash refuses `logout` from a non-login
	// shell with "not login shell: use 'exit'".
	loginShell bool

	// bashCompatErrors, when true, prefixes builtin error messages with
	// `<filename>: line <N>:` and uses bash's argument-first/no-quote
	// wording. Set via [WithBashCompatErrors] by [cmd/bashy] so the bash
	// 5.3 test suite output matches. Default off so library callers
	// (and the legacy TestRunnerRun tests) keep the old "<name>: <msg>
	// <arg>" wording without the line prefix.
	bashCompatErrors bool
	// execProcessGroups, when true, starts every external command that job
	// control does not already place as the leader of its own process group,
	// and a cancelled context signals that whole group. Set via
	// [ExecProcessGroups] by embedders without a terminal (an agent harness).
	execProcessGroups bool
	// strictPosix, when true, applies strict POSIX semantics where bash
	// deliberately deviates from the standard even in --posix mode: an
	// assignment error on any command kills a non-interactive shell,
	// functions cannot override special builtins, regular builtins
	// require a PATH hit, and `return` in a trap defaults to the
	// pre-trap exit status. Set via [WithStrictPosix] by hosts invoked
	// as `sh` (bashy's argv[0]=sh path). Default off: plain bash and
	// `bash --posix` fidelity is unchanged.
	strictPosix bool
	// dryRun is the bashy-only `set -o dryrun` option's state, readable by
	// handlers via [HandlerContext.DryRun]. dryRunOpt gates whether the option
	// is recognized at all — only hosts that pass [EnableDryRunOption] (i.e.
	// bashy, and not under --posix) accept `set -o dryrun`; everywhere else
	// (the pure bash drop-in, gosh) it errors like any unknown option.
	dryRun    bool
	dryRunOpt bool
	// orig* snapshot the construction-time dry-run state so Reset (which rebuilds
	// the Runner) restores it — dryRunOpt persists; dryRun returns to its initial.
	origDryRun    bool
	origDryRunOpt bool
	// handlingDebugTrap prevents DEBUG traps from recursively firing
	// for commands inside the DEBUG trap itself while still allowing
	// DEBUG to run inside other trap handlers such as RETURN.
	handlingDebugTrap bool

	// auditHandler, when non-nil, is invoked just before the runner
	// hands a simple command off to execHandler. It receives an
	// [AuditEvent] describing the command, its arguments, and the
	// source position. Used by agentic harnesses to log/observe what
	// the shell is about to run.
	auditHandler func(AuditEvent)
	auditLog     io.Writer

	// commandResolver, when non-nil, is the embedder's rung between the
	// shell's own names and PATH for the introspection builtins — see
	// [CommandResolver].
	commandResolver CommandResolverFunc
	servedInProcess func(name string) bool

	// structuredErrorHandler, when non-nil, is invoked for known
	// user-facing diagnostics as they are emitted to stderr. It is an
	// additive observability hook; stderr output and exit semantics are
	// unchanged when it is set.
	structuredErrorHandler func(ErrorEvent)

	// deterministic toggles deterministic-mode behaviour: a seeded
	// PRNG for $RANDOM, frozen $SECONDS / $EPOCHSECONDS, and stable
	// $$ when [deterministicSeed] is set. Embedders enable it via
	// [WithDeterministic] for reproducible agent runs.
	deterministic     bool
	deterministicSeed int64
	deterministicRng  *mathrand.PCG

	// randomSeeded tracks whether $RANDOM has been explicitly assigned.
	// Once set, reads follow bash's own Park-Miller generator so
	// `RANDOM=42; echo $RANDOM $RANDOM` is reproducible.
	randomSeeded bool
	randomSeed   uint32

	// fdTable holds non-stdio file descriptors keyed by OS fd number.
	// 0/1/2 stay in stdin/stdout/stderr; everything else (coproc pipe
	// ends, future `exec N<file` targets) lives here. Lookups happen
	// when a script uses `<&N` / `>&N` with N >= 3. The map is shared
	// with subshells (fds are inherited in bash), but mutations in a
	// subshell do not leak back to the parent because the map itself
	// is cloned by [Runner.subshell].
	fdTable map[int]*os.File
	// fdReadTable marks fdTable entries that are valid input sources.
	fdReadTable map[int]bool

	// fdWriteTable holds write-only descriptors whose target is not a
	// real file, such as stdout captured by command substitution. Those
	// cannot be represented in fdTable but bash still allows `exec 4>&1`
	// and later `echo >&4` within the same shell/subshell.
	fdWriteTable map[int]io.Writer
	// fdClosedTable records inherited non-stdio descriptors explicitly
	// closed by the shell. This keeps lazy inherited-fd lookup from
	// resurrecting a descriptor the script has logically closed.
	fdClosedTable map[int]bool

	// stmtTraceOutput snapshots xtrace output before a simple command's
	// own redirections are applied. Bash traces `cmd 2>file` to the
	// shell's current xtrace sink, not to file.
	stmtTraceOutput io.Writer

	// redirMoveCloseFds tracks source fds closed by N>&M- or N<&M- while
	// applying a statement's redirects. Bash keeps those source closes
	// after the statement's redirection frame is restored.
	redirMoveCloseFds map[int]bool

	inheritedFds map[int]bool

	// inheritedHandles maps an inherited fd to the Windows handle a parent
	// bashy handed us via [BashyInheritedHandlesEnv]; nil elsewhere. Read
	// only after startup, so clones share it.
	inheritedHandles map[int]inheritedHandle

	// ulimitOverride records pseudo-set values from `ulimit -X N`
	// so the next `ulimit -X` read returns them. We don't actually
	// call setrlimit (would affect the whole process and need
	// permissions); the map is purely cosmetic.
	ulimitOverride map[string]string

	// testIntErr is set non-empty when an integer test operator
	// (`-eq`, `-lt`, …) saw a non-integer operand; the test
	// builtin uses this to mirror bash's "integer expected" error
	// and exit 2 after binTest returns. Cleared on next test
	// builtin entry.
	testIntErr string

	// testArithErr captures an arithmetic-syntax error encountered
	// while parsing a [[ ]] arithmetic operand (`[[ 7 -eq 4+ ]]`).
	// It is non-empty alongside the offending operand in
	// [Runner.testIntErr]; TestClause prints the bash 5.3
	// arithmetic-syntax-error wording instead of "integer expected"
	// when this is set, and the exit status is 1, not 2.
	testArithErr string

	// Signal-trap delivery (see signal.go). Lazily initialized the first
	// time `trap` registers a handler for a real OS signal. Per-signal
	// goroutines fed by signal.Notify record receipt-time callbacks in
	// pendingSig; the command-execution loop drains them at statement
	// boundaries and runs the corresponding trap handlers, letting their control flow
	// (return/exit/break/continue) unwind normally. These are deliberately
	// NOT inherited by subshells: a subshell that self-signals must send a
	// real OS signal so the parent's Notify catches it.
	sigMu              sync.Mutex
	pendingSig         map[string]int                // signal name -> pending count, guarded by sigMu
	pendingSigCallback map[string][]string           // callback fixed at signal receipt, guarded by sigMu
	groupSignalReceipt map[string]chan struct{}      // broadcast receipt for a group kill which also signals this shell
	sigNotify          map[string]os.Signal          // signal name -> os.Signal under signal.Notify
	sigNotifyCh        map[string]signalSubscription // per-signal notification channel and lifetime
	standaloneDefaults map[string]bool               // runtime faults relayed to their native default action
	sigIgnored         map[string]bool               // signal name -> set to real SIG_IGN via `trap '' SIG`
	sigIgnoredPreReset map[string]bool               // TP714: signal was ignored before enableSignalTrap; needs restoreExecSignal before Notify
	sigIgnoredRestore  map[string]signalDisposition  // disposition saved before runtime-signal SIG_IGN
	sigWake            chan struct{}                 // wakes a blocked wait when a signal arrives
	hasPendingSig      atomic.Bool                   // fast-path: any pending signal?

	// sigParent links a foreground subshell back to the runner it runs
	// inline within (nil for the top-level shell and for background/async
	// subshells, which run in their own goroutine). A foreground subshell's
	// $$ is the parent shell's PID, so a self-directed `kill -SIG $$` whose
	// trap the parent owns must be delivered synchronously into the parent's
	// pending queue rather than via a racy OS signal — see
	// [Runner.owningSignalRunner] and the kill builtin.
	sigParent *Runner

	// chldTrapActive mirrors whether a non-empty SIGCHLD trap is currently
	// installed. Read from background-job goroutines and the exec handler to
	// decide whether to queue a CHLD trap per reaped child, so it's an atomic
	// rather than a guarded map read.
	chldTrapActive atomic.Bool

	// startupIgnored is the set of signals that were SIG_IGN when this shell
	// process started — inherited from a parent shell that ran `trap '' SIG`
	// before exec'ing us (carried in via BashyHardIgnoreEnv). Bash flags these
	// as SIG_HARD_IGNORE: a `trap` that targets them is a silent no-op and they
	// list as `trap -- '' SIG`. Computed once at first Reset and shared (read
	// only) with subshell clones.
	startupIgnored map[string]bool
	parentPID      int // shell-entry PPID, including a validated native-child bridge

	// inSignalTrap and friends implement POSIX interp 1602: a `return`
	// with no argument executed directly in a signal-trap action yields
	// the $? in effect when the trap was invoked (signalTrapExit), not the
	// trap's own last command. A `return` inside a function *called* from
	// the action uses normal semantics — distinguished by comparing the
	// call-stack depth against signalTrapDepth (the depth at trap entry).
	inSignalTrap    bool
	signalTrapDepth int
	signalTrapExit  uint8
}

// exitStatus holds the state of the shell after running one command.
// Beyond the exit status code, it also holds whether the shell should return or exit,
// as well as any Go error values that should be given back to the user.
//
// TODO(v4): consider replacing ExitStatus with a struct like this,
// so that an [ExecHandlerFunc] can e.g. mimic `exit 0` or fatal errors
// with specific exit codes.
type exitStatus struct {
	// code is the exit status code.
	// When code is zero, err must be nil.
	code uint8

	// TODO: consider an enum, as only one of these should be set at a time
	returning     bool // whether the current function `return`ed
	exiting       bool // whether the current shell is exiting
	fatalExit     bool // whether the current shell is exiting due to a fatal error; err below must not be nil
	noNegate      bool // whether a surrounding `!` must not invert this status
	errexitExempt bool // whether this failure inherited an errexit exemption

	// recoverSeq stamps a status that `recover` itself reported, carrying the
	// value [Runner.bashPPRecoverSeq] held when it did. It is provenance, not
	// a status: a zero stamp, or a stamp older than the runner's counter,
	// means this status came from somewhere else and must be taken at face
	// value. clear() drops it, because a cleared status is no longer the
	// answer recover gave.
	recoverSeq uint64

	// discarding qualifies exiting: a variable-assignment error in a
	// non-interactive non-POSIX shell aborts the current top-level
	// command (bash's DISCARD longjmp) rather than the whole shell.
	// It unwinds via the exiting machinery and is converted back to a
	// plain non-zero status at the end of [Runner.Run]; whenever
	// exiting is cleared (subshell boundaries), discarding must be
	// cleared with it.
	discarding bool

	// err holds the error information for a non-zero exit status code or fatal error.
	// Used so that running a single statement with a custom handler
	// which returns a non-fatal Go error, such as a Go error wrapping [NewExitStatus],
	// can be returned by [Runner.Run] without being lost entirely.
	err error
}

// clear sets the exit status code and error to zero, as long as the exit status
// was not set by `return`, `exit`, or a fatal error.
func (e *exitStatus) clear() {
	if e.returning || e.exiting || e.fatalExit {
		return
	}
	e.code = 0
	e.err = nil
	e.recoverSeq = 0
}

func (e *exitStatus) ok() bool { return e.code == 0 }

// oneIf sets the exit status code to 1 if b is true.
// Note that it assumes the exit status hasn't been set yet,
// meaning that [exitStatus.code] and [exitStatus.err] are zero values.
func (e *exitStatus) oneIf(b bool) {
	if b {
		e.code = 1
	}
}

func (e *exitStatus) fatal(err error) {
	// Scalar interruption is an internal unwind token after a Go panic, exit,
	// cancellation or forwarded signal has already recorded the real outcome.
	// It must never escape as the File's diagnostic, regardless of which
	// expression boundary observes it first.
	if e.fatalExit || err == nil || errors.Is(err, errBashPPScalarInterrupted) {
		return
	}
	e.exiting = true
	e.fatalExit = true
	e.err = err
	if e.code == 0 {
		e.code = 1
	}
}

func (e *exitStatus) fromHandlerError(err error) {
	if err == nil {
		return
	}
	var exit errBuiltinExitStatus
	var es ExitStatus
	if errors.As(err, &exit) {
		*e = exitStatus(exit)
	} else if errors.As(err, &es) {
		e.err = err
		e.code = uint8(es)
	} else {
		e.fatal(err) // handler's custom fatal error
	}
}

// callFrame records a function call for the call stack.
type callFrame struct {
	line         uint
	source       string
	callerSource string
	funcName     string
	args         []string
	// callPos, bashPPFn and seq describe a Bash++ frame for Go's stack
	// introspection: the position of the call that entered it, the
	// function running in it, and an identity that survives the frame's
	// depth being reused; see bashpp_sprint162_nilptr2_stack.go.
	callPos  syntax.Pos
	bashPPFn *bashPPFunc
	seq      uint64
	// frameScope is the typed scope this frame's parameters and named results
	// are declared in — the block the function body itself runs in. Source-level
	// liveness uses it to tell a named result rebound by `:=` (which must stay
	// live until the frame returns) apart from a fresh local that merely shadows
	// a result's name inside a nested block; see goSourceReleaseDead.
	frameScope *bashPPScope
	// goSourceName is set only on a synthetic package-initializer frame.
	// Ordinary frames derive their identity from bashPPFn and source metadata.
	goSourceName string
	// lastCallPos is the position of the last call this frame made, and
	// deferPos the position the frame is returning from once its deferred
	// calls run; see bashpp_sprint165_frames_defer.go.
	lastCallPos syntax.Pos
	deferPos    syntax.Pos
	// bodyLine is the line of the function body's opening token.
	// Bash's parser stamps for/select commands inside a function with
	// this line rather than their own, and runtime diagnostics like
	// "not a valid identifier" surface it.
	bodyLine uint
	// debugTrace reports whether the DEBUG/RETURN traps fire inside
	// this function frame. It is true when the global functrace
	// option is set or the function carries the trace attribute
	// (`declare -ft`), and is also turned on if a `trap ... DEBUG`
	// command runs within the frame (which activates the trap for the
	// remainder of that function, as bash does).
	debugTrace bool
}

type bgProc struct {
	// closed when the background process finishes,
	// after which point the result fields below are set.
	done chan struct{}

	exit *exitStatus

	cmd string

	cancel context.CancelFunc
	// A completed shell file releases an external async job to outlive Reset.
	detachOnExit bool
	detached     atomic.Bool
	// launchYield is non-nil for an external command whose start the parent
	// awaits at `&`; see Runner.yieldAsyncLaunch.
	launchYield     chan struct{}
	launchYieldOnce sync.Once

	killedSignal atomic.Int32

	// execReplacing records that this asynchronous shell reached `exec` and
	// execResult stores the replacement child's terminal status plus one (so
	// zero means no result). A pure-Go subshell cannot execve over its shared
	// host process, but wait must still report the replacement child's actual
	// status if it handles a signal delivered through the carrier.
	execReplacing atomic.Bool
	execResult    atomic.Int32

	state atomic.Int32

	ignoreNextStop atomic.Int32

	ignoreNextContinue atomic.Int32

	// stopSignal is the SIG-prefixed name (e.g. "SIGTSTP", "SIGSTOP") of
	// the signal that last stopped this job. Recorded so `jobs` in POSIX
	// mode can print "Stopped(SIGTSTP)" matching bash's printable_job_status.
	// Empty until the job is stopped, and stays empty on platforms without a
	// job-control wait path. Guarded by stopSignalMu because the unix
	// job-control waiter writes it from the bg goroutine while `jobs` reads it.
	stopSignalMu sync.Mutex
	stopSignal   string

	// jobID is the stable job-table number reported by `jobs`, `%N` job
	// specs and `$!`'s "g<N>" sentinel. It is assigned the lowest free
	// slot when a `&` background job is created and never changes while
	// the job lives, so removing a finished job does not renumber the
	// survivors (mirroring bash's job-table slots). Zero for coproc and
	// process-substitution bgProcs, which are not listed as jobs.
	jobID int

	// pid is the OS PID of the last real external process this
	// backgrounded statement spawned. Zero until set, and stays zero for
	// pure-builtin or pure-goroutine subshells (e.g. `(true) &`,
	// `(echo hi) &`). Updated atomically because the exec handler
	// (DefaultExecHandler / runDetachedExec) writes it from the
	// background goroutine while the parent shell reads it via `$!`.
	pid atomic.Int64

	// pidReady is closed once the bg goroutine has either set pid via a
	// real exec.Start, or finished running entirely without ever doing
	// so. `$!` waits on this channel so it can return a real PID instead
	// of the "g<N>" sentinel whenever one is actually available — the
	// usual `PID=$!; kill $PID` idiom relies on this.
	pidReady chan struct{}

	// finishBeforeFileReturn marks fast output builtins whose goroutine must
	// finish before a top-level File run returns. A standalone Go shell exits
	// immediately after Run, so otherwise `echo ... &` can be discarded before
	// its redirected output is written. Blocking builtins and external jobs
	// deliberately remain detached.
	finishBeforeFileReturn bool

	// publishPidToBang controls whether the first real exec PID should be
	// exposed through $!. Simple external-command backgrounds want that;
	// compound shell jobs keep the synthetic g<N> handle so signals target
	// the whole in-process job, not one inner child.
	publishPidToBang bool

	// pids records every OS PID published by this background statement.
	// Compound commands can exec more than once after `$!` has already
	// captured the first pid; wait must still map that pid to this job.
	pidsMu sync.Mutex
	pids   []int64

	// pidCallback is the runner's WithBgPidCallback hook copied in at
	// spawn time (so publishBgPid doesn't need to reach for the Runner).
	// nil means "no embedder cares". Invoked synchronously from
	// publishBgPid with the OS PID — embedders that want async fan-out
	// should hand off to a goroutine themselves.
	pidCallback func(pid int)

	// carrier is the kernel-visible stand-in process that gives this job
	// a real OS PID (see [WithJobCarrier]). Its PID remains the job identity
	// even when the goroutine starts an external child: signals must enter
	// through the carrier so the job can relay them and preserve wait status.
	// Nil when no carrier was configured or starting one failed. Set before
	// the job goroutine starts and never reassigned.
	carrier CarrierProcess
	// carrierSignalRunner is the runner whose signal disposition represents
	// the externally visible job. It normally points at the asynchronous-list
	// runner; a pipeline publishes its last component instead, matching the
	// process denoted by `$!` in bash.
	// Root parenthesized compound executes in its own internal runner; nested
	// subshells must not replace this job-wide signal owner.
	carrierRootSubshell *syntax.Subshell
	carrierSignalRunner atomic.Pointer[Runner]
	// carrierPipelineSignalOwner restricts runner publication to jobs whose
	// top-level asynchronous statement is itself a pipeline. The CAS prevents
	// nested pipelines inside its last component from replacing that owner.
	carrierPipelineSignalOwner bool
	carrierSignalPublished     atomic.Bool
	// carrierResetSignal records a carrier signal whose last pipeline runner
	// had explicitly reset an asynchronous-list INT/QUIT ignore.
	carrierResetSignal atomic.Int32

	// carrierReaped is set (before Terminate) by reapCarrier once the
	// job itself has finished, so the carrier watcher goroutine can tell
	// a shell-initiated teardown from an external kill that must be
	// relayed to the job as 128+signal.
	carrierReaped atomic.Bool

	// carrierDone is closed by the carrier watcher goroutine once
	// [CarrierProcess.Wait] has returned — i.e. the carrier process has
	// exited AND been reaped by the OS. reapCarrier blocks on it after
	// Terminate so that when the job goroutine seals the exit status (and
	// `wait` unblocks), the carrier's kernel PID is truly gone: a racing
	// `kill -0 $!` sees ESRCH rather than a lingering zombie. Nil unless a
	// carrier was attached; created in attachCarrier alongside carrier.
	carrierDone chan struct{}

	// jobControl records whether monitor mode (`set -m`) was active when
	// this job was backgrounded. `fg`/`bg` refuse a job that was not
	// started under job control, even if monitor mode is later enabled.
	jobControl bool
	// pgrp is the kernel process-group identity of an external job. Record it
	// while the child is alive so jobs/fg/bg retain the ID after its leader
	// exits. Zero means no proven OS process group (for example a synthetic
	// goroutine or a carrier-backed pure builtin).
	pgrp atomic.Int64
	// streamPgrp identifies a reserved foreign-stream job even without monitor mode.
	streamPgrp atomic.Int64
	// pgrpFixed is set before the job goroutine starts when its carrier
	// advertises a stable process group. External children join that group
	// and must not replace it with their own process-group identity.
	pgrpFixed bool

	// coprocReadonly names a readonly variable that a `coproc` failed to
	// bind its fd array to. Bash defers unsetting the coproc variable
	// until the coprocess is reaped, so reaping a readonly one reports
	// `<var>: cannot unset: readonly variable` (see Runner.reapCoproc).
	// Empty for non-coproc background jobs and writable coproc variables.
	coprocReadonly string

	// coprocReadonlyPos is the position of the `coproc` clause, used to
	// drive the `cannot unset` diagnostic's line number at reap time.
	coprocReadonlyPos syntax.Pos

	// coprocPidVar names the `<NAME>_PID` variable a `coproc` is
	// responsible for. Bash's coproc_unsetvars unbinds it (without
	// following namerefs and ignoring the readonly attribute) when the
	// coprocess is reaped, so reaping force-removes it here. Empty for
	// non-coproc background jobs and for coprocs whose name was invalid.
	coprocPidVar string

	// coprocPid is the synthetic, runner-unique integer reported in the
	// `<NAME>_PID` variable so scripts can `wait $COPROC_PID` /
	// `kill $COPROC_PID`. Real bash hands out the coprocess subshell's OS
	// PID; this runner runs the coprocess as a goroutine with no PID of
	// its own, so we mint a stable integer here and resolve it back to
	// this bgProc in wait/kill. Zero for non-coproc background jobs.
	coprocPid int64

	// coprocReadFd / coprocWriteFd are the logical fd numbers bound to the
	// coproc's `${NAME[0]}` / `${NAME[1]}` array. They are released from
	// the runner's fd tables when the coprocess is reaped so a later
	// coproc reuses the same high fds, matching bash. Zero when unset.
	coprocReadFd  int
	coprocWriteFd int

	// coprocReadFile / coprocWriteFile are the parent-side pipe ends to
	// close on reap (mirroring bash closing c_rfd / c_wfd in
	// coproc_dispose). nil for non-coproc background jobs.
	coprocReadFile  *os.File
	coprocWriteFile *os.File
}

// execReplacementState is shared by a shell and all of its in-process
// subshells. When a top-level exec must be proxied because live background
// jobs cannot survive execve, pid is the external command which now owns the
// shell's semantic process identity.
type execReplacementState struct {
	current atomic.Pointer[execReplacementAttempt]
	mu      sync.Mutex
	owner   *Runner
	pending *asyncOwnerSignal
	run     *asyncSignalRun
}

type execReplacementAttempt struct {
	ready    chan struct{}
	pid      atomic.Int64
	observer atomic.Pointer[bgProc]
}

type jobState int32

const (
	jobRunning jobState = iota
	jobStopped
	jobDead
)

func (bg *bgProc) setState(state jobState) {
	bg.state.Store(int32(state))
}

func (bg *bgProc) jobState() jobState {
	return jobState(bg.state.Load())
}

// setStopSignal records the SIG-prefixed name of the signal that stopped
// this job, for POSIX-mode `jobs` reporting.
func (bg *bgProc) setStopSignal(name string) {
	bg.stopSignalMu.Lock()
	bg.stopSignal = name
	bg.stopSignalMu.Unlock()
}

// getStopSignal returns the SIG-prefixed name of the signal that last
// stopped this job, or "" if it has never been stopped.
func (bg *bgProc) getStopSignal() string {
	bg.stopSignalMu.Lock()
	defer bg.stopSignalMu.Unlock()
	return bg.stopSignal
}

// coprocFdRef identifies which coproc array element a live pipe fd
// backs, so closing the fd can rewrite that element to "-1".
type coprocFdRef struct {
	varName string
	idx     int
}

// coprocReg maps a coproc's synthetic `<NAME>_PID` to its bgProc so that
// `wait`/`kill` can resolve it — including from a background subshell,
// which shares this registry by pointer. Safe for concurrent use.
type coprocReg struct {
	mu    sync.Mutex
	byPid map[int64]*bgProc
}

func (c *coprocReg) add(pid int64, bg *bgProc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byPid == nil {
		c.byPid = make(map[int64]*bgProc)
	}
	c.byPid[pid] = bg
}

func (c *coprocReg) lookup(pid int64) *bgProc {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byPid[pid]
}

func (c *coprocReg) remove(pid int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byPid, pid)
}

// bgProcCtxKey is the context-value key under which the bg goroutine
// stashes a pointer to its own bgProc. The default exec handler and the
// nohup/setsid builtins read this back so they can publish the OS PID
// of the process they just spawned. Nil means "not running in a
// backgrounded subshell" — exec handlers in that case skip the publish
// path entirely.
type bgProcCtxKey struct{}

// bgNonPrimaryPidCtxKey marks background pipeline elements whose child PIDs
// belong to the job for wait/cleanup purposes, but must not replace $!.
type bgNonPrimaryPidCtxKey struct{}

// pipelineExecCtxKey keeps foreground terminal handoff out of pipelines until
// the interpreter can create and manage one kernel process group per pipeline.
type pipelineExecCtxKey struct{}

// publishBgSignalRunner selects the runner whose traps classify a signal
// delivered through the job carrier. Only the primary (last) pipeline
// component may replace the asynchronous-list runner; left-side components
// execute concurrently but do not determine the pipeline's disposition.
func publishBgSignalRunner(ctx context.Context, r *Runner) {
	bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc)
	if bg == nil || !bg.carrierPipelineSignalOwner {
		return
	}
	if nonPrimary, _ := ctx.Value(bgNonPrimaryPidCtxKey{}).(bool); nonPrimary {
		return
	}
	if !bg.carrierSignalPublished.CompareAndSwap(false, true) {
		return
	}
	bg.carrierSignalRunner.Store(r)
}

// publishBgPid is what exec handlers call after a successful
// exec.Start. Sets the running goroutine's bgProc.pid and records the
// published pid for later `wait $!` resolution. Safe no-op when not in a
// backgrounded context.
func publishBgPid(ctx context.Context, pid int) {
	bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc)
	if bg == nil {
		return
	}
	pid64 := int64(pid)
	bg.pidsMu.Lock()
	bg.pids = append(bg.pids, pid64)
	bg.pidsMu.Unlock()
	nonPrimary, _ := ctx.Value(bgNonPrimaryPidCtxKey{}).(bool)
	recordBackgroundProcessGroup(bg, pid, nonPrimary)
	if nonPrimary || !bg.publishPidToBang {
		return
	}
	bg.pid.Store(pid64)
	select {
	case <-bg.pidReady:
		// already closed by an earlier exec or by goroutine exit
	default:
		close(bg.pidReady)
	}
	// With a carrier configured the callback already published that stable
	// job identity from attachCarrier. Replacing `$!` for a simple external
	// command must not turn the documented once-per-job callback into two.
	if bg.pidCallback != nil && bg.carrier == nil {
		bg.pidCallback(pid)
	}
}

// releaseBgPid tells the background job machinery that this execution
// path will not produce a real OS PID (e.g. it is a builtin or function).
// It closes pidReady so that `$!` does not block waiting for one.
func releaseBgPid(ctx context.Context) {
	bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc)
	if bg == nil {
		return
	}
	if nonPrimary, _ := ctx.Value(bgNonPrimaryPidCtxKey{}).(bool); nonPrimary || !bg.publishPidToBang {
		return
	}
	select {
	case <-bg.pidReady:
	default:
		close(bg.pidReady)
	}
}

type alias struct {
	// text is the literal alias body exactly as supplied (the part
	// after `=`, including any trailing whitespace). Bash stores and
	// displays alias bodies verbatim, so this is what `alias`/`alias -p`,
	// `type`, and ${BASH_ALIASES[...]} render — args/file/raw below are
	// the re-parsed forms used only at expansion time.
	text string
	// raw is set for bodies Bash accepts as text even though they are
	// not standalone parseable shell words.
	raw string
	// args is set for the common "simple call" alias case
	// (`alias l='ls -la'`); inlined into the surrounding CallExpr's
	// arg list at expansion time.
	args []*syntax.Word
	// file is set for aliases whose body parses to multiple
	// statements or non-CallExpr commands (e.g. with embedded
	// newlines: `alias foo=$'echo a\necho b'`). At expansion time the
	// surrounding command is replaced by running the parsed
	// statements; args is nil in this case.
	file  *syntax.File
	blank bool
	// defLine is the effective input line at which this alias became
	// defined (see [Runner.aliasDefLine]). Bash expands an alias only on
	// input read *after* the line that defined it, so a use is expanded
	// only when its effective line is strictly greater than defLine. Zero
	// means "no position recorded" (e.g. an alias installed on a
	// programmatically-built AST), in which case the timing gate is
	// skipped and the alias always expands.
	defLine int
}

// aliasDefLine returns the effective line to record for an alias defined
// at AST line astLine, honoring the current alias-timing scope. See the
// aliasBase / aliasDefOverride fields on [Runner].
func (r *Runner) aliasDefLine(astLine int) int {
	if r.aliasDefOverride > 0 {
		return r.aliasDefOverride
	}
	return r.aliasBase + astLine
}

// aliasUseLine returns the effective line of a command at AST line astLine
// for alias-timing comparisons.
func (r *Runner) aliasUseLine(astLine int) int {
	return r.aliasBase + astLine
}

// withAliasReparse runs fn while the alias-timing scope is set for a
// freshly re-parsed unit (eval/source body) whose tokens were read just
// after the outer command at effective line outerLine. Inside fn, uses see
// every alias defined up to outerLine and aliases defined are pinned to
// outerLine for outer visibility. The scope is restored afterward.
func (r *Runner) withAliasReparse(outerLine int, fn func()) {
	prevBase, prevOverride := r.aliasBase, r.aliasDefOverride
	r.aliasBase = outerLine
	r.aliasDefOverride = outerLine
	defer func() {
		r.aliasBase, r.aliasDefOverride = prevBase, prevOverride
	}()
	fn()
}

func (r *Runner) trapAliasReparseLine() int {
	line := r.aliasUseLine(int(r.curStmtPos.Line()))
	for _, als := range r.alias {
		if als.defLine >= line {
			line = als.defLine + 1
		}
	}
	if line < 1 {
		line = 1
	}
	return line
}

// AdvanceAliasInput marks the start of a newly-read unit of interactive
// input spanning lineCount lines. Interactive front-ends parse each input
// chunk independently (line numbers restart at 1), so this advances the
// alias-timing base past the previous chunk, letting aliases defined on an
// earlier prompt expand on later ones while a definition and use typed on
// the same line still do not expand. Safe to call between input reads.
func (r *Runner) AdvanceAliasInput(lineCount int) {
	if lineCount < 1 {
		lineCount = 1
	}
	r.aliasBase += lineCount
}

// ConsumedSourceOffset reports how far into the original bash source the
// runner has consumed input while executing parsed statements.
func (r *Runner) ConsumedSourceOffset() int {
	return r.stdinSourceOffset
}

// RunAliasExpandedSourceLine expands aliases on one physical bash source line
// and runs it if the expanded line parses successfully. It is used by callers
// that parse incrementally and need bash's POSIX command-substitution rule:
// aliases are expanded while parsing $(...), so an alias body can provide the
// closing ')'.
func (r *Runner) RunAliasExpandedSourceLine(ctx context.Context, line int) bool {
	if line <= 0 || !r.opts[optExpandAliases] || len(r.bashSource) == 0 {
		return false
	}
	pos := syntax.NewPos(uint(r.sourceLineEndOffset(uint(line-1))), uint(line), 1)
	if file, ok := r.aliasReparsePhysicalLine(pos, r.aliasUseLine(line)); ok {
		// This is an execution boundary between incremental Run calls.
		// Use Run's current-context and signal lifecycle, while a Block
		// avoids treating each recovered line as a file's EXIT boundary.
		prevOverride := r.aliasLineOverride
		r.aliasLineOverride = line
		r.aliasReparseDepth++
		r.withAliasReparse(r.aliasUseLine(line), func() {
			_ = r.Run(ctx, &syntax.Block{Lbrace: pos, Rbrace: file.End(), Stmts: file.Stmts})
		})
		r.aliasReparseDepth--
		r.aliasLineOverride = prevOverride
		return true
	}
	return false
}

// New creates a new Runner, applying a number of options. If applying any of
// the options results in an error, it is returned.
//
// Any unset options fall back to their defaults. For example, not supplying the
// environment falls back to the process's environment, and not supplying the
// standard output writer means that the output will be discarded.
func New(opts ...RunnerOption) (*Runner, error) {
	r := &Runner{
		usedNew:         true,
		execReplacement: new(execReplacementState),
		openHandler:     DefaultOpenHandler(),
		readDirHandler:  DefaultReadDirHandler2(),
		statHandler:     DefaultStatHandler(),
	}
	// Shared by every Runner copied from this one, so the instrument counts
	// one program's native request path; see bashpp_s281_request_plan.go.
	r.bashPPTools.requestScans = new(atomic.Int64)
	r.execReplacement.owner = r
	r.dirStack = r.dirBootstrap[:0]
	// turn "on" the default Bash options
	for i, opt := range bashOptsTable {
		r.opts[len(posixOptsTable)+i] = opt.defaultState
	}

	for _, opt := range opts {
		if err := opt(r); err != nil {
			return nil, err
		}
	}

	// Set the default fallbacks, if necessary.
	if r.Env == nil {
		Env(nil)(r)
	}
	if r.Dir == "" {
		if err := Dir("")(r); err != nil {
			return nil, err
		}
	}
	if r.stdout == nil || r.stderr == nil {
		StdIO(r.stdin, r.stdout, r.stderr)(r)
	}
	if !r.deterministic {
		if value := r.Env.Get("BASHY_DETERMINISTIC").String(); value != "" {
			seed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				seed = 0
			}
			WithDeterministic(seed)(r)
		}
	}
	r.importExportedFuncs()
	if r.auditLog == nil {
		if path := r.Env.Get("BASHY_AUDIT_LOG").String(); path != "" {
			// 0600: an audit log records what was executed and must not be
			// readable or writable by other users (it can carry command lines and
			// argument values). A world-writable audit trail cannot be trusted as
			// evidence — cf. NIST 800-53 AU-9, protection of audit information.
			f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, err
			}
			r.auditLog = f
		}
	}
	return r, nil
}

func (r *Runner) importExportedFuncs() {
	r.Env.Each(func(name string, vr expand.Variable) bool {
		if !vr.IsSet() || vr.Kind != expand.String {
			return true
		}
		fname, ok := strings.CutPrefix(name, "BASH_FUNC_")
		if !ok {
			return true
		}
		fname, ok = strings.CutSuffix(fname, "%%")
		if !ok || !validExportedFuncName(fname) || !validBashFuncName(fname) {
			return true
		}
		body := vr.Str
		if !strings.HasPrefix(body, "()") {
			return true
		}
		src := fname + " " + body
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
		if err != nil || len(file.Stmts) != 1 {
			return true
		}
		fd, ok := file.Stmts[0].Cmd.(*syntax.FuncDecl)
		if !ok || fd.Name.Value != fname {
			return true
		}
		if r.Funcs == nil {
			r.Funcs = make(map[string]*syntax.Stmt)
		}
		r.Funcs[fname] = fd.Body
		if r.funcSources == nil {
			r.funcSources = make(map[string]string)
		}
		r.funcSources[fname] = r.filename
		return true
	})
}

// RunnerOption can be passed to [New] to alter a [Runner]'s behaviour.
// It can also be applied directly on an existing Runner,
// such as interp.Params("-e")(runner).
// Note that options cannot be applied once Run or Reset have been called.
type RunnerOption func(*Runner) error

// OwnedExecutablePaths enables the large-argument handoff only for the given
// installed Bashy executables. The launch path is compared by file identity,
// so an unrelated executable with the same basename is never opted in.
func OwnedExecutablePaths(paths ...string) RunnerOption {
	return func(r *Runner) error {
		r.ownedExecPaths = append([]string(nil), paths...)
		return nil
	}
}

// TODO: enforce the rule above via didReset.

// ForeignImports binds direct Python imports the embedder already owns, by
// alias, for the Bash# dialect: `alias.fn(...)` calls and `alias.fn args`
// commands in the source the runner executes resolve to these workers. It is
// the seam through which a lowered program shares its own import bindings
// with the shell regions it runs, so a command alias reaches the same worker
// (and module state) as the program's typed calls. Imports declared by an
// executed file shadow a seeded alias while that file runs. The runner does
// not close seeded modules; their owner does.
func ForeignImports(modules map[string]*polyglot.Module) RunnerOption {
	return func(r *Runner) error {
		if r.bashPPSeededImports == nil {
			r.bashPPSeededImports = map[string]*polyglot.Module{}
		}
		for alias, module := range modules {
			if module != nil {
				r.bashPPSeededImports[alias] = module
			}
		}
		return nil
	}
}

// Env sets the interpreter's environment. If nil, a copy of the current
// process's environment is used.
func Env(env expand.Environ) RunnerOption {
	return func(r *Runner) error {
		if env == nil {
			env = expand.ListEnviron(os.Environ()...)
		}
		r.Env = env
		return nil
	}
}

// Dir sets the interpreter's working directory. If empty, the process's current
// directory is used. The resulting directory is Runner-local: interpreting cd
// updates this Runner and does not call [os.Chdir] or change the process-global
// working directory.
func Dir(path string) RunnerOption {
	return func(r *Runner) error {
		if path == "" {
			path, err := currentWorkingDir()
			if err != nil {
				return fmt.Errorf("could not get current dir: %w", err)
			}
			r.Dir = path
			return nil
		}
		path, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("could not get absolute dir: %w", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("could not stat: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", path)
		}
		r.Dir = path
		return nil
	}
}

// PromptExpand sets a function to expand prompt escape sequences for ${var@P}.
// If not set, a basic default expansion is used.
func PromptExpand(fn func(string) string) RunnerOption {
	return func(r *Runner) error {
		r.promptExpand = fn
		return nil
	}
}

// Interactive configures the interpreter to behave like an interactive shell,
// akin to Bash. Currently, this only enables the expansion of aliases,
// but later on it should also change other behavior.
// MirrorUmask makes the `umask` builtin also set the process-wide umask via
// [syscall.Umask], so external commands (e.g. `mkdir`) honour the shell's
// umask. This is OFF by default because the library supports many Runners per
// process and the process umask is global; a standalone shell binary running
// ONE Runner per process (e.g. the bashy drop-in) enables it for POSIX
// fidelity. Subshells do not propagate it, so a goroutine subshell can't
// clobber the parent's process umask.
func MirrorUmask(enabled bool) RunnerOption {
	return func(r *Runner) error {
		r.mirrorUmask = enabled
		return nil
	}
}

func Interactive(enabled bool) RunnerOption {
	return func(r *Runner) error {
		r.interactiveShell = enabled
		r.opts[optExpandAliases] = enabled
		return nil
	}
}

// initModeSetOptionDefaults applies the option defaults that depend on
// whether the shell is interactive. GNU bash initializes history, history
// expansion, and line editing only for interactive shells; job control stays
// off until the host successfully enables it for a controlling terminal.
// Preserve any explicit Params setting applied before the first Reset.
func (r *Runner) initModeSetOptionDefaults() {
	if r.noOpSetState == nil {
		r.noOpSetState = make(map[string]bool)
	}
	for _, name := range [...]string{"emacs", "histexpand", "history"} {
		if _, explicit := r.noOpSetState[name]; !explicit {
			r.noOpSetState[name] = r.interactiveShell
		}
	}
	if _, explicit := r.noOpSetState["monitor"]; !explicit {
		r.noOpSetState["monitor"] = false
	}
}

// CommandLineNoExec marks noexec as having been requested by a `-n`
// command-line flag rather than by `set -n`. Bash honours that form even
// in an interactive shell, and no later `set +n` can clear it. Callers
// should still set the [optNoExec] option so `$-` and `set -o` report it.
func CommandLineNoExec(enabled bool) RunnerOption {
	return func(r *Runner) error {
		r.cmdlineNoExec = enabled
		return nil
	}
}

// CommandString marks the runner as executing a command supplied via
// Bash's `-c` mode. This affects dynamic shell state such as `$-`.
func CommandString(enabled bool) RunnerOption {
	return func(r *Runner) error {
		r.commandString = enabled
		return nil
	}
}

// StandardInput marks the runner as executing commands read from standard
// input. This affects dynamic shell state such as `$-`.
func StandardInput(enabled bool) RunnerOption {
	return func(r *Runner) error {
		r.standardInput = enabled
		return nil
	}
}

// Params populates the shell options and parameters. For example, Params("-e",
// "--", "foo") will set the "-e" option and the parameters ["foo"], and
// Params("+e") will unset the "-e" option and leave the parameters untouched.
//
// This is similar to what the interpreter's "set" builtin does.
func Params(args ...string) RunnerOption {
	return func(r *Runner) error {
		fp := flagParser{remaining: args}
		for fp.more() {
			flag := fp.flag()
			if flag == "-" {
				// POSIX: a lone `-` ends option processing and, as a
				// special case, turns off the -x and -v options. Remaining
				// args become positional parameters (unchanged if none).
				r.opts[optXTrace] = false
				if r.noOpSetState == nil {
					r.noOpSetState = make(map[string]bool)
				}
				r.noOpSetState["verbose"] = false
				if args := fp.args(); len(args) > 0 {
					r.Params = args
				}
				return nil
			}
			if flag == "+" {
				// A lone `+` ends option processing like `-` (without the
				// legacy -v/-x reset): remaining args become positional
				// parameters, and with none the parameters are unchanged.
				// Without this, flag[1] below panics on the one-char "+"
				// (`set + -` crashed with index out of range).
				if args := fp.args(); len(args) > 0 {
					r.Params = args
				}
				return nil
			}
			enable := flag[0] == '-'
			if flag[1] != 'o' {
				if flag[1] == 'O' {
					value := fp.value()
					opt, supported := r.bashOptByName(value)
					if opt == nil || !supported {
						return fmt.Errorf("invalid option: %q", value)
					}
					*opt = enable
					continue
				}
				if flag[1] == 'B' {
					if r.noOpSetState == nil {
						r.noOpSetState = make(map[string]bool)
					}
					r.noOpSetState["braceexpand"] = enable
					continue
				}
				if flag == "+r" {
					return fmt.Errorf("+r: invalid option")
				}
				opt := r.posixOptByFlag(flag[1])
				if opt == nil {
					// Accept-and-ignore single-letter options
					// that bash supports but we don't model. The
					// on/off state is still tracked (in
					// noOpSetState) so it surfaces in $-.
					if name, ok := noOpSetFlagToName[flag[1]]; ok {
						if r.noOpSetState == nil {
							r.noOpSetState = make(map[string]bool)
						}
						r.noOpSetState[name] = enable
						continue
					}
					return fmt.Errorf("invalid option: %q", flag)
				}
				*opt = enable
				if opt == &r.opts[optRestricted] && enable {
					r.markRestrictedVarsReadonly()
				}
				continue
			}
			value := fp.value()
			if value == "deterministic" {
				r.deterministic = enable
				if enable {
					r.deterministicRng = mathrand.NewPCG(uint64(r.deterministicSeed), uint64(r.deterministicSeed)^0x9E3779B97F4A7C15)
				}
				continue
			}
			if value == "bashpp" || value == "bashsharp" {
				// Live dialect toggle (`set -o bashsharp`; `bashpp` is the
				// Bash++-era alias, kept one minor release — the listed name
				// stays `bashpp` this release). It changes only interp-side
				// behaviour (object-valued variables); the grammar is
				// unchanged, so no reparse or dispatch is implied.
				// Orthogonal to `set -o posix`.
				r.setBashPPMode(enable)
				continue
			}
			if value == "" && enable {
				// `set -o` (no name): bash lists all options
				// including no-op aliases (history, hashall,
				// emacs, etc.) sorted alphabetically. Build the
				// combined list and emit.
				type oentry struct {
					name    string
					enabled bool
				}
				var list []oentry
				for i, opt := range &posixOptsTable {
					if opt.name == "restricted" {
						continue
					}
					list = append(list, oentry{opt.name, r.opts[i]})
				}
				for n, on := range noOpSetOptions {
					if v, ok := r.noOpSetState[n]; ok {
						on = v
					}
					list = append(list, oentry{n, on})
				}
				// The bashpp dialect toggle is a bashy extension kept out
				// of posixOptsTable (so it stays out of SHELLOPTS), but it
				// is reported here so its live state is observable.
				if r.bashPPEnabled() && !r.hideBashPPOption {
					list = append(list, oentry{"bashpp", true})
				}
				sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
				for _, e := range list {
					r.printOptLine(e.name, e.enabled, true)
				}
				continue
			}
			if value == "" && !enable {
				// `set +o` (no name): same set, reusable form.
				type oentry struct {
					name    string
					enabled bool
				}
				var list []oentry
				for i, opt := range &posixOptsTable {
					if opt.name == "restricted" {
						continue
					}
					list = append(list, oentry{opt.name, r.opts[i]})
				}
				for n, on := range noOpSetOptions {
					if v, ok := r.noOpSetState[n]; ok {
						on = v
					}
					list = append(list, oentry{n, on})
				}
				// See the `set -o` branch above: the bashpp toggle is
				// reported in the reusable form too.
				if r.bashPPEnabled() && !r.hideBashPPOption {
					list = append(list, oentry{"bashpp", true})
				}
				sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
				for _, e := range list {
					setFlag := "+o"
					if e.enabled {
						setFlag = "-o"
					}
					r.outf("set %s %s\n", setFlag, e.name)
				}
				continue
			}
			opt := r.posixOptByName(value)
			if opt == nil {
				if _, ok := noOpSetOptions[value]; ok {
					// accept-and-ignore: remember the toggle so
					// subsequent `set -o` listings echo back what
					// the script asserted, but don't otherwise act
					// on the flag.
					if r.noOpSetState == nil {
						r.noOpSetState = make(map[string]bool)
					}
					r.noOpSetState[value] = enable
					if value == "ignoreeof" {
						r.setIgnoreEOFOption(enable)
					}
					continue
				}
				return fmt.Errorf("invalid option: %q", value)
			}
			if value == "restricted" && !enable {
				return fmt.Errorf("restricted: invalid option name")
			}
			*opt = enable
			if value == "restricted" && enable {
				r.markRestrictedVarsReadonly()
			}
			if value == "posix" {
				r.setPosixMode(enable)
			}
		}
		if args := fp.args(); args != nil {
			// If "--" wasn't given and there were zero arguments,
			// we don't want to override the current parameters.
			r.Params = args

			// Record whether a sourced script sets the parameters.
			if r.inSource {
				r.sourceSetParams = true
			}
		}
		return nil
	}
}

// CallHandler sets the call handler. See [CallHandlerFunc] for more info.
func CallHandler(f CallHandlerFunc) RunnerOption {
	return func(r *Runner) error {
		r.callHandler = f
		return nil
	}
}

// ExecHandler sets one command execution handler,
// which replaces [DefaultExecHandler](2 * time.Second).
//
// Deprecated: use [ExecHandlers] instead, which allows chaining handlers more easily
// like middleware functions.
func ExecHandler(f ExecHandlerFunc) RunnerOption {
	return func(r *Runner) error {
		r.execHandler = f
		return nil
	}
}

// ExecHandlers appends middlewares to handle command execution.
// The middlewares are chained from first to last, and the first is called by the runner.
// Each middleware is expected to call the "next" middleware at most once.
//
// For example, a middleware may implement only some commands.
// For those commands, it can run its logic and avoid calling "next".
// For any other commands, it can call "next" with the original parameters.
//
// Another common example is a middleware which always calls "next",
// but runs custom logic either before or after that call.
// For instance, a middleware could change the arguments to the "next" call,
// or it could print log lines before or after the call to "next".
//
// The last exec handler is always [DefaultExecHandler](2 * time.Second).
func ExecHandlers(middlewares ...func(next ExecHandlerFunc) ExecHandlerFunc) RunnerOption {
	return func(r *Runner) error {
		r.execMiddlewares = append(r.execMiddlewares, middlewares...)
		return nil
	}
}

// TODO: consider porting the middleware API in [ExecHandlers] to [OpenHandler],
// [ReadDirHandler2], and [StatHandler].

// TODO(v4): now that [ExecHandlers] allows calling a next handler with changed
// arguments, one of the two advantages of [CallHandler] is gone. The other is the
// ability to work with builtins; if we make [ExecHandlers] work with builtins, we
// could join both APIs.

// OpenHandler sets file open handler. See [OpenHandlerFunc] for more info.
func OpenHandler(f OpenHandlerFunc) RunnerOption {
	return func(r *Runner) error {
		r.openHandler = f
		r.bashPPCustomOpen = true
		return nil
	}
}

// DryRunOpenHandler overrides file opens only while the option enabled by
// [EnableDryRunOption] is on. Runtime `set -o dryrun` / `set +o dryrun` selects
// the override dynamically, including in shell copies. Normal execution keeps
// its existing open handler and native cooperative task/FIFO behavior.
//
// The override is still a custom [OpenHandlerFunc]: it is not invoked inside
// Bash++ tasks, whose opens require the native cancellation-aware path. Passing
// nil removes the override. This option does not enable the dryrun option.
func DryRunOpenHandler(f OpenHandlerFunc) RunnerOption {
	return func(r *Runner) error {
		r.dryRunOpenHandler = f
		return nil
	}
}

// EnableDryRunOption turns on the non-POSIX `set -o dryrun` shell option,
// initialized to enabled. When on, `set -o dryrun` / `set +o dryrun` toggle the
// runner's dry-run flag, which exec/open handlers read via [HandlerContext.DryRun]
// to print-and-skip instead of executing. Hosts that do not pass this option
// (the pure bash drop-in, gosh, anything under --posix) reject `set -o dryrun`
// as an unknown option, exactly like Bash — so bash conformance is unaffected.
// The option is deliberately kept out of `set -o` listings and SHELLOPTS.
func EnableDryRunOption(enabled bool) RunnerOption {
	return func(r *Runner) error {
		r.dryRunOpt = true
		r.dryRun = enabled
		return nil
	}
}

// WithDisabledBuiltins marks the named builtins as disabled at runner
// construction, exactly as `enable -n <name>` would at runtime: they fall
// through to PATH lookup and `type`/`command -v` report them as external (or
// not-found). This is the programmatic form of `enable -n` and is how a strict
// drop-in suppresses fork-only builtins it adds for embedders. For example the
// pure `bash` binary disables the fork's `nohup`/`setsid` so they resolve to
// the real external commands like bash 5.3 (and, where absent — e.g. setsid on
// macOS — report "not found", matching bash on that platform), while the
// AgentOS / matrix-shell embedders keep them as builtins for in-process detach.
func WithDisabledBuiltins(names ...string) RunnerOption {
	return func(r *Runner) error {
		if len(names) == 0 {
			return nil
		}
		if r.disabledBuiltins == nil {
			r.disabledBuiltins = make(map[string]bool)
		}
		for _, n := range names {
			r.disabledBuiltins[n] = true
		}
		return nil
	}
}

// ReadDirHandler sets the read directory handler. See [ReadDirHandlerFunc] for more info.
//
// Deprecated: use [ReadDirHandler2].
func ReadDirHandler(f ReadDirHandlerFunc) RunnerOption {
	return func(r *Runner) error {
		r.readDirHandler = func(ctx context.Context, path string) ([]fs.DirEntry, error) {
			infos, err := f(ctx, path)
			if err != nil {
				return nil, err
			}
			entries := make([]fs.DirEntry, len(infos))
			for i, info := range infos {
				entries[i] = fs.FileInfoToDirEntry(info)
			}
			return entries, nil
		}
		return nil
	}
}

// ReadDirHandler2 sets the read directory handler. See [ReadDirHandlerFunc2] for more info.
func ReadDirHandler2(f ReadDirHandlerFunc2) RunnerOption {
	return func(r *Runner) error {
		r.readDirHandler = f
		return nil
	}
}

// StatHandler sets the stat handler. See [StatHandlerFunc] for more info.
func StatHandler(f StatHandlerFunc) RunnerOption {
	return func(r *Runner) error {
		r.statHandler = f
		return nil
	}
}

func stdinFile(r io.Reader) (*os.File, error) {
	switch r := r.(type) {
	case *os.File:
		return r, nil
	case fifoWriteFile:
		// A <> redirect on a FIFO is wrapped so Close can refresh the FIFO
		// node's timestamps. Keep using the same O_RDWR descriptor for stdin;
		// copying the wrapper through an os.Pipe both loses its write side and
		// leaves a goroutine blocked forever on the FIFO.
		return r.File, nil
	case nil:
		return nil, nil
	default:
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		go func() {
			io.Copy(pw, r)
			pw.Close()
		}()
		return pr, nil
	}
}

// StdIO configures an interpreter's standard input, standard output, and
// standard error. If out or err are nil, they default to a writer that discards
// the output.
//
// Note that providing a non-nil standard input other than [*os.File] will require
// an [os.Pipe] and spawning a goroutine to copy into it,
// as an [os.File] is the only way to share a reader with subprocesses.
// This may cause the interpreter to consume the entire reader.
// See [os/exec.Cmd.Stdin].
//
// When providing an [*os.File] as standard input, consider using an [os.Pipe]
// as it has the best chance to support cancellable reads via [os.File.SetReadDeadline],
// so that cancelling the runner's context can stop a blocked standard input read.
func StdIO(in io.Reader, out, err io.Writer) RunnerOption {
	return func(r *Runner) error {
		_, stdinIsFile := in.(*os.File)
		r.origStdinAsyncCopy = in != nil && !stdinIsFile
		stdin, _err := stdinFile(in)
		if _err != nil {
			return _err
		}
		r.stdin = stdin
		if out == nil {
			out = io.Discard
		}
		r.stdout = out
		if err == nil {
			err = io.Discard
		}
		r.stderr = err
		return nil
	}
}

// WithBgPidCallback registers a callback invoked with the OS PID of each
// real external process this runner spawns from a backgrounded statement
// (`foo &`). Fires from inside [publishBgPid] right after the PID is
// stored on the bgProc. Not invoked for pure-builtin or pure-goroutine
// backgrounded subshells (e.g. `(true) &`), because there is no kernel
// PID to report.
//
// The callback runs synchronously on the background goroutine. Keep it
// cheap; hand off to your own goroutine if you need to do non-trivial
// work (file I/O, network, etc.).
//
// Use case: an embedder (e.g. the outpost agent) that wants to surface
// detached jobs to an external job-control CLI. The in-shell `fg`/`bg`/
// `jobs` builtins are unimplemented because subshells in this
// interpreter are goroutines, not OS processes — pushing the job
// registry out to a process that can use real OS primitives is the
// supported path.
func WithBgPidCallback(fn func(pid int)) RunnerOption {
	return func(r *Runner) error {
		r.bgPidCallback = fn
		return nil
	}
}

// WithLoginShell marks this runner as a login shell. Without it, the
// `logout` builtin refuses with bash's "not login shell" error. cmd/bashy's
// interactive mode and embedders that own the session lifetime (e.g.
// outpost's SSH attach) should opt in.
func WithLoginShell(login bool) RunnerOption {
	return func(r *Runner) error {
		r.loginShell = login
		return nil
	}
}

// WithBashCompatErrors switches builtin error messages to bash 5.3
// wording. With this flag set, builtins prefix user-fault errors with
// `<filename>: line <N>:` (using [Runner]'s filename or "bashy" when
// running -c), put the offending argument before the message, and drop
// Go's `%q` quoting. cmd/bashy enables this so the upstream bash test
// suite output matches; library callers using [interp.New] directly
// see the legacy mvdan/sh wording by default.
func WithBashCompatErrors(on bool) RunnerOption {
	return func(r *Runner) error {
		r.bashCompatErrors = on
		return nil
	}
}

// ExecProcessGroups makes each external command the leader of its own
// process group (unless job control already placed it in one) and, when the
// command's context is cancelled — a deadline, a cancel — delivers the
// cancel signal and the follow-up kill to that whole group rather than to the
// direct child alone. Without it a grandchild the command left behind (a
// server a test script started) survives the cancellation and keeps the
// command's output pipe open, so waiting for the command never returns.
//
// It is meant for embedders with no controlling terminal, such as an agent
// harness enforcing a per-command wall time: a separate group does not
// receive terminal-generated signals. No effect on Windows.
func ExecProcessGroups(on bool) RunnerOption {
	return func(r *Runner) error {
		r.execProcessGroups = on
		return nil
	}
}

// WithStrictPosix enables strict POSIX semantics in the areas where bash
// deviates from the standard even under --posix (see the strictPosix
// field). Hosts that present themselves as a POSIX `sh` (e.g. bashy when
// argv[0] is "sh") opt in; it implies nothing about parsing — combine
// with [WithPosixMode] for `sh` behavior.
//
// POSIX leaves echo operands containing backslashes implementation-defined.
// Strict mode selects the historical System V behavior, which is also the XSI
// behavior, by enabling xpg_echo. This remains an ordinary shell option so an
// embedding host or script can subsequently change it.
func WithStrictPosix(on bool) RunnerOption {
	return func(r *Runner) error {
		r.strictPosix = on
		if xpgEcho, supported := r.bashOptByName("xpg_echo"); supported {
			*xpgEcho = on
		}
		return nil
	}
}

// WithBashSource provides the original script bytes for bash-compatible
// diagnostics that need source-preserved snippets. The runner copies src so
// callers may reuse their buffer after applying the option.
func WithBashSource(src []byte) RunnerOption {
	return func(r *Runner) error {
		r.bashSource = append(r.bashSource[:0], src...)
		return nil
	}
}

// WithStdinScript declares that the bytes given to [WithBashSource] were read
// from the runner's stdin — the `bash < script` case. Bash keeps ONE logical
// input stream there, so a command that reads fd 0 consumes the script's own
// unread tail:
//
//	$ bash < input-line.sh          # input-line.sh runs `bash ./sub` ...
//	before calling ./sub
//	line read by ./sub was `this line for input-line.sh'
//
// This cannot be inferred from an unnamed [*syntax.File]: an embedder may parse
// an unnamed script while the runner's stdin carries unrelated data. Leave it
// off unless fd 0 really is where the script came from.
func WithStdinScript(on bool) RunnerOption {
	return func(r *Runner) error {
		r.stdinScript = on
		return nil
	}
}

// WithIncrementalFilename sets the filename used for bash-compatible
// diagnostics when [Runner.Run] is called incrementally with a [*syntax.Stmt]
// or [syntax.Command] instead of a full [*syntax.File].
func WithIncrementalFilename(name string) RunnerOption {
	return func(r *Runner) error {
		r.incrementalFilename = name
		return nil
	}
}

// WithArgv0 sets the shell's $0 (and initial $BASH_ARGV0) explicitly,
// independent of the filename used for error-message prefixes. A standalone
// shell uses this for the `-s` / stdin / interactive invocation forms, where
// bash reports $0 as the name the shell was invoked with (argv[0]) even though
// no script file is being run. The value survives [Runner.Reset]; user code can
// still override it at runtime by assigning to $BASH_ARGV0.
func WithArgv0(name string) RunnerOption {
	return func(r *Runner) error {
		r.argv0 = name
		r.origArgv0 = name
		return nil
	}
}

func WithInheritedFds(fds []int) RunnerOption {
	return func(r *Runner) error {
		if len(fds) == 0 {
			r.inheritedFds = nil
			return nil
		}
		r.inheritedFds = make(map[int]bool, len(fds))
		for _, fd := range fds {
			if fd >= 3 {
				r.inheritedFds[fd] = true
			}
		}
		return nil
	}
}

// AuditEvent is delivered to the [WithAuditHandler] callback just
// before the runner dispatches a simple command to either a shell
// builtin or [ExecHandlerFunc].
type AuditEvent struct {
	// Kind is "builtin", "exec", or "island" (a Bash# island function run
	// as a command word through its worker; see [ForeignImports]).
	Kind string
	// Args is the resolved command and its arguments, post expansion.
	Args []string
	// Pos is the source position of the command in the script.
	Pos syntax.Pos
	// When records when the command was observed. In deterministic
	// mode, this is the runner's frozen start time.
	When time.Time
	// Filename is [Runner.filename] (the parsed script's name) or
	// empty if the runner was driven by -c / a Node value.
	Filename string
	// CallStackHash is a SHA-256 digest of the active shell function
	// stack at the observation point.
	CallStackHash string
	// EnvDigest is a SHA-256 digest of the current shell variable
	// environment.
	EnvDigest string
	// IsBuiltin is true if Args[0] is a shell builtin. Embedders that
	// want a record of every "command" — builtin or not — typically
	// keep these; harnesses that only care about external launches
	// can filter them out.
	IsBuiltin bool
}

// WithAuditHandler registers a callback invoked once per simple
// command immediately before [ExecHandlerFunc] runs. Use it to
// build a record of what the shell is about to execute (replay
// logs, capability checking, observability). The callback runs
// synchronously; return quickly. Returning is non-fatal — there
// is no way to veto the command from the audit callback. Use a
// custom [ExecHandlerFunc] for that.
func WithAuditHandler(fn func(AuditEvent)) RunnerOption {
	return func(r *Runner) error {
		r.auditHandler = fn
		return nil
	}
}

// ResolvedCommand is what a [CommandResolverFunc] reports for a name the
// embedder dispatches itself.
type ResolvedCommand struct {
	// Kind is what `type -t` prints. Bash's vocabulary is closed (alias,
	// keyword, function, builtin, file); an empty Kind reports "file" — the
	// closest of the five to a program the shell hands off rather than
	// runs itself, and the answer scripts that switch on `type -t` expect
	// for something that is neither a builtin nor a function.
	Kind string
	// Desc is the `type NAME` / `command -V NAME` line. Empty reports
	// "NAME is NAME".
	Desc string
	// Path is what `command -v NAME` and `type -p NAME` print. Empty means
	// there is no file the shell could exec directly: `command -v` then
	// prints the bare name, as it does for a builtin or function, and
	// `type -p` prints nothing.
	Path string
	// Schema, when non-nil, describes the registered command's accepted
	// arguments. The runner validates and binds it before handing the
	// command to ExecHandler, so a registered script body is not entered
	// on bad input. A nil schema preserves the pre-schema resolver
	// contract exactly.
	Schema *CommandSchema
}

// CommandSchema describes the argument surface of a registered command.
// It is intentionally small: positionals, named flags, scalar conversion,
// defaults and enum validation. Embedders own persistence; the runner owns
// the one bind/validate-input step before command execution.
type CommandSchema struct {
	Positionals []CommandParameter
	Flags       []CommandFlag
}

// CommandParameter describes one positional argument.
type CommandParameter struct {
	Name     string
	Type     string
	Required bool
	Default  string
	Enum     []string
}

// CommandFlag describes one named argument. Name is used as --name, and
// Shorthand, when non-empty, is used as -x. Bool flags may be supplied without
// a value, which binds them to true.
type CommandFlag struct {
	Name      string
	Shorthand string
	Type      string
	Required  bool
	Default   string
	Enum      []string
}

// CommandResolverFunc answers "does the embedder run this name itself, and
// as what" for one word that is not a keyword, alias, function or enabled
// builtin. ok=false falls through to the hash table and PATH.
type CommandResolverFunc func(name string) (ResolvedCommand, bool)

// CommandResolver registers the embedder's rung in command LOOKUP, so the
// introspection builtins — `type`, `command -v`, `command -V` — see the
// same ladder the dispatch path runs. An [ExecHandlerFunc] middleware that
// intercepts a name before PATH (an in-process applet, a registered
// command) is invisible to those builtins by default: the name runs, yet
// `command -v NAME` says not found, so a script that probes before
// calling concludes the command is absent. The resolver closes that gap
// without the embedder defining a shell function per name.
//
// It is consulted after the shell's own names (keyword, alias, function,
// builtin) and BEFORE the command hash table and PATH, which is exactly
// where a pre-PATH exec rung sits. `type -a` lists the resolver's match and
// then any PATH file, like a function shadowing a file. `type -P` stays a
// pure PATH search and `hash` stays a PATH cache: both are questions about
// files, and the resolver's names need not be files.
//
// nil (the default) changes nothing: a runner without a resolver resolves
// exactly as bash does.
func CommandResolver(fn CommandResolverFunc) RunnerOption {
	return func(r *Runner) error {
		r.commandResolver = fn
		return nil
	}
}

// ServedInProcess names the commands the embedder's exec handlers serve in
// process (an applet by its bare name). Such a name is never entered in the
// command hash table and a hash hit never rewrites its argv[0] to a PATH
// file: bash hashes what it found on PATH, and these names are not run from
// PATH. Without it, the first call reaches the in-process command while the
// lookup also hashes a same-named host file, and every later call runs that
// file instead (or fails where the host has none). `type`, `command -v` and
// `hash` are unchanged. nil (the default) keeps bash's behavior.
func ServedInProcess(fn func(name string) bool) RunnerOption {
	return func(r *Runner) error {
		r.servedInProcess = fn
		return nil
	}
}

// WithAuditLog writes each [AuditEvent] as one JSON object per line.
// It is additive with [WithAuditHandler]; both receive the same event.
func WithAuditLog(w io.Writer) RunnerOption {
	return func(r *Runner) error {
		r.auditLog = w
		return nil
	}
}

// ErrorEvent is delivered to the [WithStructuredErrors] callback when
// the runner emits a known user-facing diagnostic.
type ErrorEvent struct {
	// Kind classifies the diagnostic at a coarse level, such as
	// "builtin", "expand", "exec", or "io".
	Kind string
	// Severity is "error" for diagnostics that affect the command's
	// status. Future warning-only sites may use "warning".
	Severity string
	// Message is the exact diagnostic text written to stderr, minus a
	// trailing newline.
	Message string
	// Pos is the source position associated with the diagnostic.
	Pos syntax.Pos
	// Filename is [Runner.filename] (the parsed script's name) or
	// empty if the runner was driven by -c / a Node value.
	Filename string
	// Function is the innermost shell function active when the
	// diagnostic was produced, if any.
	Function string
	// Command is the command or builtin name associated with the
	// diagnostic, if known.
	Command string
	// ExitStatus is the status assigned by the diagnostic site, when
	// that status is known.
	ExitStatus uint8
}

// WithStructuredErrors registers a callback invoked when the runner
// emits known diagnostics. It gives embedders a typed stream for
// observability and audit trails without parsing bash-shaped stderr.
// The callback runs synchronously; keep it cheap.
func WithStructuredErrors(fn func(ErrorEvent)) RunnerOption {
	return func(r *Runner) error {
		r.structuredErrorHandler = fn
		return nil
	}
}

// WithDeterministic enables deterministic-mode runs targeted at
// agentic harnesses that need reproducible output. When on:
//   - $RANDOM uses a per-runner PRNG seeded from `seed` (or 0 if
//     the caller passed no seed) rather than [crypto/rand].
//   - $SECONDS and $EPOCHSECONDS return a fixed value derived from
//     the runner's start time rather than wall-clock progression.
//   - $$ (PID) is stable: the seed value modulo 2^15 instead of
//     [os.Getpid].
//
// External commands still see the host wall clock and real PIDs;
// determinism is bounded to what the shell itself emits. Embedders
// that need stronger isolation should combine this with a custom
// [ExecHandler]/[OpenHandler] and an [AuditHandler].
func WithDeterministic(seed int64) RunnerOption {
	return func(r *Runner) error {
		r.deterministic = true
		r.deterministicSeed = seed
		// PCG seeded from (seed, seed^0x9E3779B97F4A7C15) — the lo
		// half is the user seed, hi is a hash to avoid weak streams
		// when the user passed 0.
		r.deterministicRng = mathrand.NewPCG(uint64(seed), uint64(seed)^0x9E3779B97F4A7C15)
		return nil
	}
}

// WithPosixMode sets the runner's POSIX mode.
func WithPosixMode(enabled bool) RunnerOption {
	return func(r *Runner) error {
		r.setPosixMode(enabled)
		return nil
	}
}

func (r *Runner) posixOptByName(name string) *bool {
	// Bashy-only `set -o dryrun`, recognized only when EnableDryRunOption was
	// passed (never in the pure bash drop-in or under --posix). Kept out of
	// posixOptsTable so it never appears in `set -o` listings or SHELLOPTS.
	if name == "dryrun" && r.dryRunOpt {
		return &r.dryRun
	}
	for i, opt := range &posixOptsTable {
		if opt.name == name {
			return &r.opts[i]
		}
	}
	return nil
}

func (r *Runner) setPosixMode(enabled bool) {
	r.opts[optPosix] = enabled
	if r.ecfg != nil {
		r.ecfg.Posix = enabled
	}
	if enabled {
		r.opts[optExpandAliases] = true
		// POSIX mode auto-sets POSIXLY_CORRECT=y if not already set. Bash
		// creates this as a shell variable, not an exported environment
		// variable; an inherited exported value keeps its attribute.
		if r.writeEnv != nil && !r.writeEnv.Get("POSIXLY_CORRECT").IsSet() {
			r.writeEnv.Set("POSIXLY_CORRECT", expand.Variable{
				Set: true, Kind: expand.String, Str: "y",
			})
		}
	}
}

// LangVariant returns the parser language implied by the runner's
// current shell options.
func (r *Runner) LangVariant() syntax.LangVariant {
	if r.opts[optPosix] {
		return syntax.LangPOSIX
	}
	return r.Dialect()
}

// VimMode reports whether the vi line-editing mode is active (`set -o vi`).
// Bash defaults to emacs editing; `set -o vi` switches to vi. The interactive
// line editor consults this between prompts so a runtime `set -o vi`/`set +o
// vi` toggle takes effect. Reading the absent/nil map key yields false (emacs).
func (r *Runner) VimMode() bool {
	return r.noOpSetState["vi"]
}

// AliasDefined reports whether name is a defined alias. Intended for
// embedders that route interactive input by its first word (a line router
// must treat an alias like any other command name).
func (r *Runner) AliasDefined(name string) bool {
	_, ok := r.alias[name]
	return ok
}

// LiveVar returns the current value of the named variable, resolved through
// the active scope (locals, globals, and the writable environment overlay).
// Unlike the read-only [Runner.Env] — which holds only the initial
// environment — it reflects assignments made while the runner executes, such
// as an interactive `PS1=...`. Returns the zero [expand.Variable] (unset) when
// the name is not declared. Intended for embedders that must read live shell
// state between runs, e.g. recomputing the prompt each interactive line.
func (r *Runner) LiveVar(name string) expand.Variable {
	// Before the first Run, Reset has not built the writable overlay yet;
	// fall back to the initial environment so callers (e.g. the interactive
	// prompt, shown before the first command) don't dereference a nil overlay.
	if r.writeEnv == nil {
		if r.Env == nil {
			return expand.Variable{}
		}
		return r.Env.Get(name)
	}
	return r.lookupVar(name)
}

// SetLastArgument sets Bash's special `_` variable. Embedders use this after
// performing their own startup bookkeeping commands, which must not replace
// the shell's initial `_` value or its inherited export attribute before user
// code begins.
func (r *Runner) SetLastArgument(value string, exported bool) {
	r.setVar("_", expand.Variable{Set: true, Exported: exported, Kind: expand.String, Str: value})
}

func (r *Runner) posixOptByFlag(flag byte) *bool {
	for i, opt := range &posixOptsTable {
		if opt.flag == flag {
			return &r.opts[i]
		}
	}
	return nil
}

func (r *Runner) bashOptByName(name string) (status *bool, supported bool) {
	for i, opt := range bashOptsTable {
		if opt.name == name {
			index := len(posixOptsTable) + i
			return &r.opts[index], opt.supported
		}
	}
	return nil, false
}

// runnerOpts contains all POSIX Shell and Bash options as one contiguous table.
type runnerOpts [len(posixOptsTable) + len(bashOptsTable)]bool

type localOptFrame struct {
	active       bool
	opts         runnerOpts
	noOpSetState map[string]bool
}

func (r *Runner) markLocalOpts() {
	if n := len(r.localOptStack); n > 0 {
		r.localOptStack[n-1].active = true
	}
}

func (r *Runner) localOptsActive() bool {
	return len(r.localOptStack) > 0 && r.localOptStack[len(r.localOptStack)-1].active
}

type posixOpt struct {
	flag byte   // one-character flag form for this option; a space if none exists
	name string // full name of the option
}

type bashOpt struct {
	name         string
	defaultState bool // Bash's default value for this option
	supported    bool // whether we support the option's non-default state
}

var posixOptsTable = [...]posixOpt{
	// sorted alphabetically by name
	{'a', "allexport"},
	{'e', "errexit"},
	{'n', "noexec"},
	{'C', "noclobber"},
	{'f', "noglob"},
	{'u', "nounset"},
	{'x', "xtrace"},
	{' ', "pipefail"},
	{' ', "posix"},
	{'r', "restricted"},
	{'k', "keyword"},
}

// noOpSetOptions are option names that `set -o NAME` / `set -H` etc. can
// pass but which we silently accept without any runtime effect. This is
// the long-name set; the short-flag set is in
// [posixOptByFlag] / [Params].
// noOpSetOptions maps each `set -o NAME` we accept-and-ignore to its bash 5.3
// noninteractive fallback (true = on, false = off). Options whose defaults
// depend on shell mode are materialized per runner by
// [Runner.initModeSetOptionDefaults] before the first Reset.
var noOpSetOptions = map[string]bool{
	"history":              false, // enabled during interactive startup
	"histexpand":           false, // enabled during interactive startup
	"hashall":              true,  // bash's listing uses "hashall", not "hashcmds"
	"verbose":              false,
	"monitor":              false, // enabled explicitly after acquiring a tty
	"vi":                   false,
	"emacs":                false, // enabled during interactive startup
	"interactive-comments": true,
	"ignoreeof":            false,
	"physical":             false,
	"privileged":           false,
	"functrace":            false,
	"braceexpand":          true, // always on
	"notify":               false,
	"onecmd":               false,
	"errtrace":             false, // set -E, listed by bash
	"nolog":                false, // listed by bash
}

// noOpSetFlagToName maps single-letter option flags to their
// accept-and-ignore long-name counterparts. Used by both the
// short-flag path (`set -b`) and the long-name path (`set -o b`).
var noOpSetFlagToName = map[byte]string{
	'b': "notify",
	'v': "verbose",
	'h': "hashall",
	'H': "histexpand",
	'B': "braceexpand",
	'P': "physical",
	'p': "privileged",
	't': "onecmd",
	'E': "errtrace",
	'T': "functrace",
	'm': "monitor",
}

var bashOptsTable = [...]bashOpt{
	// IMPORTANT: the first six entries — dotglob, expand_aliases,
	// extglob, globstar, nocaseglob, nullglob — are referenced by
	// the optDotGlob…optNullGlob const constants below in that
	// exact order. Don't reorder them. The rest is sorted
	// alphabetically and printed sorted by `shopt`/`shopt -p`.
	{name: "dotglob", supported: true},
	{name: "expand_aliases", supported: true},
	{name: "extglob", supported: true},
	{name: "globstar", supported: true},
	{name: "nocaseglob", supported: true},
	{name: "nullglob", supported: true},
	// Everything below is in alphabetical order.
	{name: "array_expand_once"},
	{name: "assoc_expand_once"},
	{name: "autocd", supported: true},
	{name: "bash_source_fullpath"},
	{name: "cdable_vars"},
	{name: "cdspell"},
	{name: "checkhash", supported: true},
	{name: "checkjobs"},
	{name: "checkwinsize", defaultState: true, supported: true},
	{name: "cmdhist", defaultState: true, supported: true},
	{name: "compat31"},
	{name: "compat32"},
	{name: "compat40"},
	{name: "compat41"},
	{name: "compat42"},
	{name: "compat43"},
	{name: "compat44"},
	{name: "complete_fullquote", defaultState: true, supported: true},
	{name: "direxpand"},
	{name: "dirspell"},
	{name: "execfail"},
	{name: "extdebug", supported: true},
	{name: "extquote", defaultState: true, supported: true},
	{name: "failglob", supported: true},
	{name: "force_fignore", defaultState: true, supported: true},
	{name: "globasciiranges", defaultState: true, supported: true},
	{name: "globskipdots", defaultState: true, supported: true},
	{name: "gnu_errfmt"},
	{name: "histappend"},
	{name: "histreedit"},
	{name: "histverify"},
	{name: "hostcomplete", defaultState: true, supported: true},
	{name: "huponexit", supported: true},
	{name: "inherit_errexit", supported: true},
	{name: "interactive_comments", defaultState: true, supported: true},
	{name: "lastpipe", supported: true},
	{name: "lithist"},
	{name: "localvar_inherit"},
	{name: "localvar_unset"},
	{name: "login_shell"},
	{name: "mailwarn"},
	{name: "no_empty_cmd_completion"},
	{name: "nocasematch", supported: true},
	{name: "noexpand_translation"},
	{name: "patsub_replacement", defaultState: true, supported: true},
	{name: "progcomp", defaultState: true, supported: true},
	{name: "progcomp_alias"},
	{name: "promptvars", defaultState: true, supported: true},
	{name: "restricted_shell"},
	{name: "shift_verbose"},
	{name: "sourcepath", defaultState: true, supported: true},
	{name: "varredir_close"},
	{name: "xpg_echo", supported: true},
}

// To access the shell options arrays without a linear search when we
// know which option we're after at compile time. First come the shell options,
// then the bash options.
const (
	// These correspond to indexes in [shellOptsTable]
	optAllExport = iota
	optErrExit
	optNoExec
	optNoClobber
	optNoGlob
	optNoUnset
	optXTrace
	optPipeFail
	optPosix
	optRestricted
	optKeyword

	// These correspond to indexes (offset by the above seven items) of
	// supported options in [bashOptsTable]
	optDotGlob
	optExpandAliases
	optExtGlob
	optGlobStar
	optNoCaseGlob
	optNullGlob
)

// Reset returns a runner to its initial state, right before the first call to
// Run or Reset.
//
// Typically, this function only needs to be called if a runner is reused to run
// multiple programs non-incrementally. Not calling Reset between each run will
// mean that the shell state will be kept, including variables, options, and the
// current directory.
func (r *Runner) Reset() {
	r.closeGoSourceBridge()
	if !r.usedNew {
		panic("use interp.New to construct a Runner")
	}
	// Reset cancels and joins jobs still owned by this runner. A completed
	// shell file has released its external jobs, which finish independently
	// (or stop when the original caller context is canceled).
	for _, bg := range r.bgProcs {
		if bg != nil && bg.cancel != nil && !bg.detached.Load() {
			bg.cancel()
		}
	}
	for _, bg := range r.bgProcs {
		if bg == nil || bg.cancel == nil || bg.detached.Load() {
			continue
		}
		<-bg.done
		if bg.coprocPid != 0 {
			r.reapCoproc(bg)
		}
	}
	// Reset below replaces r wholesale. Stop subscriptions first: otherwise the
	// replacement drops their done/finished channels while their forwarders stay
	// parked forever. standaloneDefaults is configuration established by an
	// option, so retain it and recreate its subscriptions after the replacement.
	r.stopSignalSubscriptions()
	// A Reset is an ownership boundary. Cancel every blocked channel operation
	// and join every task before replacing the runner and its registry.
	if r.bashPPConcurrent != nil {
		r.bashPPConcurrent.stopAndJoin()
		r.bashPPConcurrent = nil
	}
	if r.execReplacement == nil || r.execReplacement.owner == r {
		r.execReplacement = &execReplacementState{owner: r}
	}
	standaloneDefaults := maps.Clone(r.standaloneDefaults)
	if !r.didReset {
		if r.execReplacement == nil {
			r.execReplacement = new(execReplacementState)
		}
		r.initModeSetOptionDefaults()
		r.origDir = r.Dir
		r.origParams = r.Params
		r.origOpts = r.opts
		r.origDialect = r.dialect
		r.origDryRun = r.dryRun
		r.origDryRunOpt = r.dryRunOpt
		r.origNoOpSetState = maps.Clone(r.noOpSetState)
		r.origStdin = r.stdin
		r.origStdout = r.stdout
		r.origStderr = r.stderr

		if r.execHandler != nil && len(r.execMiddlewares) > 0 {
			panic("interp.ExecHandler should be replaced with interp.ExecHandlers, not mixed")
		}
		if r.execHandler == nil {
			r.bareExecHandler = len(r.execMiddlewares) == 0
			r.execHandler = DefaultExecHandler(2 * time.Second)
		}
		// Middlewares are chained from first to last, and each can call the
		// next in the chain, so we need to construct the chain backwards.
		for _, mw := range slices.Backward(r.execMiddlewares) {
			r.execHandler = mw(r.execHandler)
		}
		// Fill tempDir; only need to do this once given that Env will not change.
		// TMPDIR may be spelled the POSIX way (/tmp, /c/Users/<account>/tmp) on
		// Windows; resolve it to the native directory the way an operand is.
		if dir := r.Env.Get("TMPDIR").String(); pathconv.IsAbs(dir) {
			r.tempDir = shellPathToOS(r.Dir, dir)
		} else {
			r.tempDir = os.TempDir()
		}
		// Clean it as we will later do a string prefix match.
		r.tempDir = filepath.Clean(r.tempDir)
		// Snapshot the process umask once at first Reset. The builtin
		// updates only r.umask afterwards; the process value is never
		// mutated by this runner.
		r.umask = processUmask()
		// Snapshot, once, the signals that were ignored on shell entry. The
		// environment bridge covers self re-execs; the OS disposition snapshot
		// covers a real parent that exec'd us with SIG_IGN inherited.
		r.startupIgnored = startupIgnoredSignals(r.Env.Get(BashyHardIgnoreEnv).String())
		r.parentPID = r.startupParentPID()
		// Windows: descriptors a parent bashy handed over as inheritable
		// handles. A no-op on other platforms.
		r.adoptInheritedHandles(r.Env.Get(BashyInheritedHandlesEnv).String())
		// A standalone host re-applies the bridged dispositions here. The Go
		// runtime may have replaced inherited SIG_IGN before main; the explicit
		// sideband is the supported provenance across that boundary.
		r.restoreBridgedStartupIgnores()
	}
	if len(r.bashPPGoSourceDecls) > 0 {
		r.bashPPUnshareFuncs()
		r.bashPPUnshareTypes()
		r.bashPPUnshareMethods()
	}
	for name := range r.bashPPGoSourceDecls {
		delete(r.bashPPFuncs, name)
		delete(r.bashPPTypes, name)
		delete(r.bashPPMethods, name)
	}
	oldDirFile := r.dirFile
	// reset the internal state
	*r = Runner{
		Env:                r.Env,
		tempDir:            r.tempDir,
		callHandler:        r.callHandler,
		execHandler:        r.execHandler,
		bareExecHandler:    r.bareExecHandler,
		openHandler:        r.openHandler,
		dryRunOpenHandler:  r.dryRunOpenHandler,
		bashPPCustomOpen:   r.bashPPCustomOpen,
		readDirHandler:     r.readDirHandler,
		statHandler:        r.statHandler,
		bgPidCallback:      r.bgPidCallback,
		jobCarrier:         r.jobCarrier,
		sigReset:           r.sigReset,
		standaloneDefaults: standaloneDefaults,

		goSourceEnvironment: r.goSourceEnvironment,
		ownedExecPaths:      r.ownedExecPaths,
		// Initialize before task snapshots are made so every child of this run
		// assigns opaque words from the same registry.
		goSourceUnsafeOpaqueIDs: &goSourceUnsafeOpaqueIDs{},
		// Seeded imports are construction-time configuration ([ForeignImports])
		// owned by the embedder, not per-Run state.
		bashPPSeededImports: r.bashPPSeededImports,

		// The dialect is fixed at construction by [Lang]; a runtime `set -o
		// bashpp` may have changed r.dialect since, so Reset restores the
		// construction-time value from origDialect (mirroring dryRun).
		dialect:          r.origDialect,
		origDialect:      r.origDialect,
		hideBashPPOption: r.hideBashPPOption,

		// These can be set by functions like [Dir] or [Params], but
		// builtins can overwrite them; reset the fields to whatever the
		// constructor set up.
		Dir:                r.origDir,
		Params:             r.origParams,
		opts:               r.origOpts,
		dryRun:             r.origDryRun,
		dryRunOpt:          r.origDryRunOpt,
		noOpSetState:       maps.Clone(r.origNoOpSetState),
		stdin:              r.origStdin,
		origStdinAsyncCopy: r.origStdinAsyncCopy,
		stdout:             r.origStdout,
		stderr:             r.origStderr,

		// Restore an explicitly configured $0 (see [WithArgv0]); a plain
		// runner leaves both empty so the filename-based default applies.
		argv0:     r.origArgv0,
		origArgv0: r.origArgv0,

		origDir:          r.origDir,
		origParams:       r.origParams,
		origOpts:         r.origOpts,
		origDryRun:       r.origDryRun,
		origDryRunOpt:    r.origDryRunOpt,
		origNoOpSetState: maps.Clone(r.origNoOpSetState),
		origStdin:        r.origStdin,
		origStdout:       r.origStdout,
		origStderr:       r.origStderr,

		// emptied below, to reuse the space
		Vars: r.Vars,

		// Preserve user-registered functions across Reset; bash's
		// `BASH_FUNC_*` env imports run at construction time and the
		// resulting functions are part of the initial shell state,
		// not per-Run scratch state.
		Funcs:                  r.Funcs,
		bashPPAgenticFuncs:     r.bashPPAgenticFuncs,
		bashPPDecoratedFuncs:   r.bashPPDecoratedFuncs,
		bashPPNativeDecorators: r.bashPPNativeDecorators,
		bashPPAdvice:           r.bashPPAdvice,
		bashPPPredeclaredCall:  r.bashPPPredeclaredCall,
		funcSources:            r.funcSources,

		// A function's captured Bash++ scope is part of the function, so it
		// is preserved exactly as far as Funcs is. The runner's own current
		// scope is per-Run scratch and is rebuilt below.
		bashPPFuncScopes:    r.bashPPFuncScopes,
		bashPPFuncs:         r.bashPPFuncs,
		bashPPTypes:         r.bashPPTypes,
		bashPPMethods:       r.bashPPMethods,
		bashPPClosures:      r.bashPPClosures,
		bashPPTools:         r.bashPPTools,
		bashPPIssuedHandles: r.bashPPIssuedHandles,

		// disabledBuiltins (`enable -n`, or WithDisabledBuiltins at
		// construction) is part of the shell's persistent state, not per-Run
		// scratch — preserve it across Reset like funcs/aliases.
		disabledBuiltins: r.disabledBuiltins,

		dirStack:        r.dirStack[:0],
		usedNew:         r.usedNew,
		execReplacement: r.execReplacement,

		promptExpand:           r.promptExpand,
		startTime:              r.startTime,
		subshellLevel:          r.subshellLevel,
		umask:                  r.umask,
		startupIgnored:         r.startupIgnored,
		parentPID:              r.parentPID,
		inheritedExitTrap:      r.inheritedExitTrap,
		loginShell:             r.loginShell,
		bashCompatErrors:       r.bashCompatErrors,
		execProcessGroups:      r.execProcessGroups,
		strictPosix:            r.strictPosix,
		bashSource:             slices.Clone(r.bashSource),
		stdinScript:            r.stdinScript,
		auditHandler:           r.auditHandler,
		commandResolver:        r.commandResolver,
		servedInProcess:        r.servedInProcess,
		auditLog:               r.auditLog,
		structuredErrorHandler: r.structuredErrorHandler,
		deterministic:          r.deterministic,
		deterministicSeed:      r.deterministicSeed,
		deterministicRng:       r.deterministicRng,
		interactiveShell:       r.interactiveShell,
		cmdlineNoExec:          r.cmdlineNoExec,
		commandString:          r.commandString,
		standardInput:          r.standardInput,
		mirrorUmask:            r.mirrorUmask,
		inheritedFds:           maps.Clone(r.inheritedFds),
		inheritedHandles:       r.inheritedHandles,
		// fdTable is intentionally not preserved across Reset; a reset
		// runner starts with no inherited non-stdio fds.
	}
	if oldDirFile != nil {
		_ = oldDirFile.Close()
	}
	if len(r.standaloneDefaults) > 0 {
		r.sigMu.Lock()
		r.ensureSignalLoopLocked()
		for name := range r.standaloneDefaults {
			// An ignored-on-entry signal is immutable to the shell. In
			// particular, restoreBridgedStartupIgnores installed a raw SIG_IGN
			// for runtime fault signals above; signal.Notify would overwrite
			// that disposition with the Go handler. Keep the standalone-default
			// configuration metadata, but do not subscribe while the startup
			// hard-ignore owns the signal.
			if r.startupIgnored[name] {
				continue
			}
			sig, ok := signalByName(name)
			if !ok {
				continue
			}
			r.startSignalSubscriptionLocked(name, signalForOS(sig), "", true)
		}
		r.sigMu.Unlock()
	}
	// Ensure we stop referencing any pointers before we reuse bgProcs.
	clear(r.bgProcs)
	r.bgProcs = r.bgProcs[:0]
	r.lastBang = nil

	if r.Vars == nil {
		r.Vars = make(map[string]expand.Variable)
	} else {
		clear(r.Vars)
	}
	if r.deterministic {
		r.deterministicRng = mathrand.NewPCG(uint64(r.deterministicSeed), uint64(r.deterministicSeed)^0x9E3779B97F4A7C15)
	}
	// TODO(v4): Use the supplied Env directly if it implements enough methods.
	r.writeEnv = &overlayEnviron{parent: r.Env}
	if r.Dialect() == syntax.LangBashPP {
		// The script's outermost block. Every other dialect leaves this nil,
		// which is what turns the hooks in vars.go into a single nil check.
		r.bashPPScope = newBashPPScope(nil)
		r.bashPPImports = make(map[string]string)
		if r.bashPPTools.eval == nil {
			// The capability policy, not the bare toolchain adapter. It
			// enforces P2 import-time classification before dispatch.
			r.bashPPTools.eval = newPolicyBashPPEvaluator()
		}
		if r.bashPPFuncScopes == nil {
			r.bashPPFuncScopes = make(map[string]*bashPPScope)
		}
		if r.bashPPTypes == nil {
			r.bashPPTypes = make(map[string]bashPPType)
		}
		if r.bashPPMethods == nil {
			r.bashPPMethods = make(map[string]map[string]*bashPPFunc)
		}
	}
	// The hard-ignore bridge variable is internal: consume it (already
	// snapshotted into startupIgnored) and hide it from the script's scope.
	if r.writeEnv.Get(BashyHardIgnoreEnv).IsSet() {
		r.delVar(BashyHardIgnoreEnv)
	}
	// Likewise the Windows handle handoff, already adopted at first Reset.
	if r.writeEnv.Get(BashyInheritedHandlesEnv).IsSet() {
		r.delVar(BashyInheritedHandlesEnv)
	}
	if r.writeEnv.Get(bashyParentPIDEnv).IsSet() {
		r.delVar(bashyParentPIDEnv)
	}
	// A Windows parent whose exported names collide under case folding
	// hands the full case-sensitive set through [bashyCasedEnv]; give
	// those variables their exact names back, then hide the bridge like
	// the handoffs above.
	if vr := r.writeEnv.Get(bashyCasedEnv); vr.IsSet() {
		r.adoptCasedEnv(vr.String())
		r.delVar(bashyCasedEnv)
	}
	if !r.writeEnv.Get("HOME").IsSet() {
		home, _ := os.UserHomeDir()
		r.setVarString("HOME", shellPathFromOS(home))
	}
	if !r.writeEnv.Get("UID").IsSet() {
		r.setVar("UID", expand.Variable{
			Set:      true,
			Kind:     expand.String,
			ReadOnly: true,
			Str:      strconv.Itoa(os.Getuid()),
		})
	}
	if !r.writeEnv.Get("EUID").IsSet() {
		r.setVar("EUID", expand.Variable{
			Set:      true,
			Kind:     expand.String,
			ReadOnly: true,
			Str:      strconv.Itoa(os.Geteuid()),
		})
	}
	// Bash has no special GID variable: unlike UID and EUID, GID is neither
	// initialized nor readonly, so scripts may freely assign to it.
	// bash auto-exports PWD even when it has to synthesize the variable at
	// startup (variables.c:set_pwd). External utilities such as make rely on
	// recipes inheriting the shell's current directory through PWD.
	r.setExportedVarString("PWD", shellPathFromOS(r.Dir))
	r.setVarString("IFS", " \t\n")
	r.setVarString("OPTIND", "1")
	if r.opts[optPosix] && !r.writeEnv.Get("POSIXLY_CORRECT").IsSet() {
		r.writeEnv.Set("POSIXLY_CORRECT", expand.Variable{
			Set: true, Kind: expand.String, Str: "y",
		})
	}
	if r.startTime.IsZero() {
		r.startTime = time.Now()
	}

	// The stack holds the logical spelling `dirs` prints; on Unix that is
	// r.Dir itself.
	r.dirStack = append(r.dirStack, shellPathFromOS(r.Dir))

	// A restricted shell (set via `-r`/`--restricted`/`rbash` before the
	// first Reset) freezes PATH, SHELL, ENV, and BASH_ENV. The opts were
	// captured during New, but writeEnv only exists now, so apply it here.
	if r.opts[optRestricted] {
		r.markRestrictedVarsReadonly()
	}

	// `-o ignoreeof` set via [Params] at construction is recorded in
	// noOpSetState but its IGNOREEOF variable was deferred (writeEnv did not
	// exist yet); apply it now that the variable environment is ready.
	if r.noOpSetState["ignoreeof"] {
		r.setIgnoreEOFOption(true)
	}
	// Command guests start from the caller's original environment. Record only
	// subsequent shell writes; startup synthesis (including PWD) is not a
	// caller-authored export.
	r.writeEnv.(*overlayEnviron).goCommandTouched = make(map[string]bool)

	r.didReset = true
}

// ExitStatus is a non-zero status code resulting from running a shell node.
type ExitStatus uint8

func (s ExitStatus) Error() string { return fmt.Sprintf("exit status %d", s) }

// SignaledStatus reports that a foreground external command was terminated by
// an operating-system signal. It unwraps to ExitStatus so existing callers
// which only inspect shell status remain source- and behavior-compatible.
// SignalName is canonical and SIG-prefixed when the platform names the signal.
type SignaledStatus struct {
	Status     ExitStatus
	Signal     int
	SignalName string
}

func (s SignaledStatus) Error() string { return s.Status.Error() }

// Unwrap preserves the long-standing errors.As(err, *ExitStatus) contract.
func (s SignaledStatus) Unwrap() error { return s.Status }

// NewExitStatus creates an error which contains the specified exit status code.
//
// Deprecated: use [ExitStatus] directly.
//
//go:fix inline
func NewExitStatus(status uint8) error {
	return ExitStatus(status)
}

// IsExitStatus checks whether error contains an exit status and returns it.
//
// Deprecated: use [errors.As] with [ExitStatus] directly.
//
//go:fix inline
func IsExitStatus(err error) (status uint8, ok bool) {
	var es ExitStatus
	if errors.As(err, &es) {
		return uint8(es), true
	}
	return 0, false
}

// SetLastExitStatus sets the shell status exposed via $? without running a
// command. It is intended for embedders that recover from external parse or
// scheduling errors and need the next shell statement to observe that status.
func (r *Runner) SetLastExitStatus(status uint8) {
	r.lastExit = exitStatus{code: status}
}

// LastSignaledStatus reports whether the most recent Run ended because its
// foreground external command was terminated by an operating-system signal.
// It distinguishes that case from an ordinary command which exits with the
// same conventional 128+signal status.
func (r *Runner) LastSignaledStatus() (SignaledStatus, bool) {
	return r.lastSignaled, r.lastWasSignaled
}

// ExpandDocument expands src as one shell word using the runner's current
// variable scope and command-substitution machinery. It performs no field
// splitting or pathname expansion. Calls must be synchronous with [Runner.Run].
func (r *Runner) ExpandDocument(ctx context.Context, src string) (string, error) {
	if !r.didReset {
		r.Reset()
	}
	r.fillExpandConfig(ctx)
	word, err := syntax.NewParser(syntax.Variant(r.Dialect())).Document(strings.NewReader(src))
	if err != nil {
		return "", err
	}
	return expand.Document(r.ecfg, word)
}

// Run interprets a node, which can be a [*File], [*Stmt], or [Command]. If a non-nil
// error is returned, it will typically contain a command's exit status, which
// can be retrieved with [IsExitStatus].
//
// Run can be called multiple times synchronously to interpret programs
// incrementally. To reuse a [Runner] without keeping the internal shell state,
// call Reset.
//
// Calling Run on an entire [*File] implies an exit, meaning that an exit trap may
// run. Run calls on one Runner, and calls to Subshell while a Run is active,
// must be serialized by the caller.
func (r *Runner) Run(ctx context.Context, node syntax.Node) error {
	if r.goSourceTesting != nil && !r.goSourceTesting.loading {
		return fmt.Errorf("gosource: runner is reserved by a testing session; close it first")
	}
	if !r.didReset {
		r.Reset()
	}
	// A failed child interpreter cancels the owning Go-source Run. Classic
	// shell runs keep the caller's context untouched: background jobs and
	// asynchronous signal senders legitimately outlive a Run, and cancelling
	// a derived context when Run returns would kill them.
	// The runner's own flag is set further down, once the file is entered, so
	// decide from the node being run (or from an enclosing Go-source run).
	goSourceRun := r.bashPPGoSource
	if file, ok := node.(*syntax.File); ok && file.GoSource {
		goSourceRun = true
	}
	if goSourceRun {
		var cancelRun context.CancelFunc
		ctx, cancelRun = context.WithCancel(ctx)
		previousRunCancel := r.bashPPRunCancel
		r.bashPPRunCancel = cancelRun
		defer func() {
			r.bashPPRunCancel = previousRunCancel
			cancelRun()
		}()
	}
	ctx, finishSignalRun := r.beginAsyncSignalRun(ctx)
	defer finishSignalRun()
	previousTaskPolicy := r.bashPPHostedTask
	r.bashPPHostedTask = taskPolicy(ctx)
	defer func() { r.bashPPHostedTask = previousTaskPolicy }()
	defer r.withDeclarations(ctx)()
	r.fillExpandConfig(ctx)
	r.exit = exitStatus{}
	r.expandRunExit = exitStatus{}
	r.bashPPForwardedDeath = 0
	// The CLI drives an exiting statement followed by an empty File to run its
	// EXIT trap. Preserve the statement's signal across that bookkeeping Run;
	// every executable node still starts with a fresh termination record.
	emptyExitTrapRun := false
	if file, ok := node.(*syntax.File); ok && len(file.Stmts) == 0 {
		emptyExitTrapRun = true
	}
	if !emptyExitTrapRun {
		r.lastSignaled = SignaledStatus{}
		r.lastWasSignaled = false
		r.lastWasOwnerSignaled = false
	}
	r.filename = r.incrementalFilename
	runExitTrap := false
	switch node := node.(type) {
	case *syntax.File:
		savedGoFile := r.bashPPGoSourceFile
		r.bashPPGoSourceFile = node
		defer func() { r.bashPPGoSourceFile = savedGoFile }()
		savedFieldTarget, savedFieldReport := r.goSourceFieldTrackTarget, r.goSourceFieldTrackReport
		r.goSourceFieldTrackTarget, r.goSourceFieldTrackReport = r.goSourceFieldTrackForFile(node)
		defer func() {
			r.goSourceFieldTrackTarget, r.goSourceFieldTrackReport = savedFieldTarget, savedFieldReport
		}()
		savedGoSource := r.bashPPGoSource
		r.bashPPGoSource = node.GoSource
		savedConvertHashQY := r.bashPPConvertHashQY
		// A Subshell carries its parent's variables but no Env of its own.
		goflags := ""
		if r.Env != nil {
			goflags = r.Env.Get("GOFLAGS").String()
		}
		r.bashPPConvertHashQY = node.GoSource && bashPPGoFlagsConvertHashQY(goflags)
		if node.GoSource && r.goSourceTesting == nil {
			defer r.closeGoSourceBridge()
		}
		if node.GoSource {
			defer goSourceRaiseGCPacing()()
		}
		defer func() {
			r.bashPPGoSource = savedGoSource
			r.bashPPConvertHashQY = savedConvertHashQY
		}()
		if r.bashPPConvertHashQY {
			for _, source := range node.Sources {
				if source.PackagePath != "" {
					return fmt.Errorf("gosource: unscoped conversion policy for linked packages is unsupported")
				}
			}
		}
		savedAgentic := r.bashPPAgentic
		r.bashPPAgentic = false
		defer func() { r.bashPPAgentic = savedAgentic }()
		r.bashPPFileRun = true
		defer func() { r.bashPPFileRun = false }()
		if r.Dialect() == syntax.LangBashPP {
			processes := r.bashPPProcessRegistry()
			processes.mu.Lock()
			first := processes.next
			processes.mu.Unlock()
			defer processes.cleanupSince(first)
			// Persistent FIFO descriptors opened before the first task are
			// still owned by this File and must be registered for snapshots.
			r.bashPPConcurrency(ctx)
		}
		r.filename = node.Name
		restoreForeign, err := r.bashPPPrepareSourceBlocks(ctx, node)
		if err != nil {
			r.exit.fatal(err)
			break
		}
		defer restoreForeign()
		if r.Dialect() == syntax.LangBashPP && !r.bashPPValidatePackageInitOrder(node) {
			break
		}
		// Package-level type names are visible to the whole package, so they
		// are registered before any declaration statement runs. A nested Run —
		// a testing session's extra file, say — must not consume this file's
		// outstanding registrations, so they are saved across it.
		savedPending := r.bashPPGoSourcePending
		r.bashPPGoSourceRegisterTypes(node)
		defer func() { r.bashPPGoSourcePending = savedPending }()
		var preparedConstants map[syntax.Command]bool
		if r.stdinSourceEligible() && node.Name == "" && len(r.bashSource) > 0 {
			r.stdinSourceActive = true
		}
		runExitTrap = true
		goImportsStarted := false
		for stmtIndex := 0; stmtIndex < len(node.Stmts); stmtIndex++ {
			stmt := node.Stmts[stmtIndex]
			if r.stdinSourceActive && int(stmt.Pos().Offset()) < r.stdinSourceOffset {
				continue
			}
			// Skip the tail of a physical line aborted by a readonly
			// assignment error; resume once the source line changes.
			if r.discardRestOfLine != 0 {
				if stmt.Pos().Line() == r.discardRestOfLine {
					continue
				}
				r.discardRestOfLine = 0
				r.exit.discarding = false
				r.exit.exiting = false
			}
			if node.GoSource {
				if _, isImport := stmt.Cmd.(*syntax.BashPPImport); !isImport && !goImportsStarted {
					goImportsStarted = true
					bridgeContext := ctx
					if r.goSourceTesting != nil {
						// Run's signal scope ends after package initialization;
						// the test session owns the dependency process longer.
						bridgeContext = r.goSourceTesting.context
					}
					if err := r.bashPPStartGoSourceBridge(bridgeContext); err != nil {
						r.exit.fatal(err)
						break
					}
					preparedConstants, err = r.bashPPPreparePackageConstants(ctx, node)
					if err != nil {
						r.exit.fatal(err)
						break
					}
				}
				if preparedConstants[stmt.Cmd] {
					continue
				}
				if r.bashPPGoSourceDecls == nil {
					r.bashPPGoSourceDecls = map[string]bool{}
				}
				switch d := stmt.Cmd.(type) {
				case *syntax.BashPPFuncDecl:
					if d.Receiver == nil {
						r.bashPPGoSourceDecls[d.Name.Value] = true
					}
				case *syntax.BashPPDecl:
					if d.Site == syntax.StartTypeDecl {
						r.bashPPGoSourceDecls[d.Name.Value] = true
					}
				}
			}
			r.verboseStmt(stmt)
			r.stmt(ctx, stmt)
			if r.bashPPBranch == bashPPBranchGoto {
				if at := r.bashPPGotoTarget(node.Stmts); at >= 0 {
					if r.bashPPScope != nil {
						r.bashPPScope.resumeAtLabel(r.bashPPGotoLabel)
					}
					r.bashPPClearBranch()
					stmtIndex = at - 1
					continue
				}
				break
			}
			if r.bashPPGoSource && r.exit.code != 0 {
				r.exit.fatal(ExitStatus(r.exit.code))
			}
			// A DISCARD only aborts the top-level command it
			// occurred in; the next one still runs.
			if r.exit.discarding {
				if r.discardNextStmt {
					r.discardNextStmt = false
					continue
				}
				if r.discardRestOfLine != 0 {
					continue
				}
				r.exit.discarding = false
				r.exit.exiting = false
			}
		}
	case *syntax.Stmt:
		if r.stdinSourceEligible() && r.incrementalFilename == "" && len(r.bashSource) > 0 {
			r.stdinSourceActive = true
		}
		if r.stdinSourceActive && int(node.Pos().Offset()) < r.stdinSourceOffset {
			break
		}
		r.verboseStmt(node)
		r.stmt(ctx, node)
	case syntax.Command:
		if r.stdinSourceEligible() && r.incrementalFilename == "" && len(r.bashSource) > 0 {
			r.stdinSourceActive = true
		}
		r.cmd(ctx, node)
	default:
		return fmt.Errorf("node can only be File, Stmt, or Command: %T", node)
	}
	// A DISCARDed top-level command only aborts itself; the shell (and
	// any caller driving Run statement-by-statement) keeps going.
	r.discardRestOfLine = 0
	r.bashPPDeclRefused = r.bashPPDeclRefused && r.exit.discarding
	if r.exit.discarding {
		r.exit.discarding = false
		r.exit.exiting = false
	}
	ownerSignal := r.finishAsyncOwnerSignal()
	if ownerSignal != nil {
		// Signal termination interrupts foreground work, not the owner's
		// EXIT cleanup. Preserve the caller's cancellation and deadline.
		ctx = r.ownerCallerContext(ctx)
	}
	ownerSignalName := ""
	var ownerStatus *SignaledStatus
	if ownerSignal != nil {
		r.lastWasOwnerSignaled = true
		ownerSignalName, _ = signalName(ownerSignal.signal)
		status := ownerSignal.status()
		ownerStatus = &status
	} else if emptyExitTrapRun && r.lastWasOwnerSignaled {
		// Incremental embedded callers use an empty File for final cleanup.
		// Keep the preceding signal's cleanup rules and terminal status.
		status := r.lastSignaled
		ownerStatus = &status
		ownerSignalName = strings.TrimPrefix(status.SignalName, "SIG")
	}
	_, standalone := r.sigReset.(OSSignalResetter)
	if (runExitTrap || ownerSignal != nil && standalone) && ownerSignalName != "KILL" {
		r.bashPPWait(ctx)
		oldCallStack := r.callStack
		if r.exitTrapCallStack != nil {
			r.callStack = r.exitTrapCallStack
		}
		r.trapCallback(ctx, r.trapCallbacks["EXIT"], "exit")
		r.callStack = oldCallStack
		r.exitTrapCallStack = nil
		r.finishBackgroundOutputBuiltins(ctx)
	}
	if runExitTrap && ownerSignal == nil && ctx.Err() == nil {
		// A file is a shell exit. Its external async children have their
		// own lifetime; a later Reset must not cancel them as it replaces
		// this runner. The caller context still owns cancellation.
		for _, bg := range r.bgProcs {
			if bg != nil && bg.detachOnExit {
				bg.detached.Store(true)
			}
		}
	}
	maps.Insert(r.Vars, r.writeEnv.Each)
	if ownerStatus != nil {
		status := *ownerStatus
		r.exit = exitStatus{code: uint8(status.Status), err: status, exiting: true}
		if ownerSignal != nil && standalone {
			// Before an exec exists, native host death cannot preserve an
			// in-process sender. Take the signal promptly after EXIT cleanup;
			// embedded hosts return the status and leave the sender alive.
			_ = relayAsyncOwnerSignal(ownerSignal.signal)
		}
	}
	if sig := r.bashPPForwardedDeath; sig > 0 {
		// The interpreted Go program was killed by a signal this host proxied
		// to its dependency process. bashPPNativeExitStatus already closed the
		// bridge session (child reaped, output drained, scratch removed,
		// forwarder unsubscribed), so the host can now take the same signal
		// under its default disposition and report WIFSIGNALED to its parent —
		// exactly what a compiled build of the program would have shown. On
		// Windows, a forwarded console break reproduces the native 32-bit exit.
		return relayForwardedProgramDeath(sig)
	}
	// Return the first of: a fatal error, a non-fatal handler error, or the exit code.
	if err := r.exit.err; err != nil {
		if r.exit.code == 0 {
			// This should never happen; too much code relies on checking [exitStatus.code]
			// to see if the last command succeeded or failed. [exitStatus.err] should only be
			// additional information, so fail loudly if the invariant is broken.
			panic("ended up with a non-nil exitStatus.err but a zero exitStatus.code")
		}
		var signaled SignaledStatus
		if errors.As(err, &signaled) {
			r.lastSignaled = signaled
			r.lastWasSignaled = true
			return signaled.Status
		}
		return err
	}
	if code := r.exit.code; code != 0 {
		return ExitStatus(code)
	}
	if code := r.expandRunExit.code; code != 0 {
		return ExitStatus(code)
	}
	return nil
}

// stdinSourceEligible reports whether commands should receive the unread
// script tail as fd 0. A terminal must retain its descriptor identity for
// foreground utilities such as mesg; replacing it with a temporary script
// file preserves bytes but destroys isatty and controlling-session semantics.
func (r *Runner) stdinSourceEligible() bool {
	if !r.stdinScript || r.commandString || r.interactiveShell {
		return false
	}
	return !isTerminalFile(r.stdin)
}

// isTerminalFile reports whether f is a terminal WITHOUT calling f.Fd().
//
// os.File.Fd detaches the descriptor from the runtime poller and puts it back
// into blocking mode, which silently disables SetReadDeadline on that file
// forever after. `read` cancels a blocked stdin read by setting a read
// deadline from a context.AfterFunc, so a single isatty probe on r.stdin was
// enough to make an interrupted `read` block until EOF instead of returning.
// SyscallConn.Control hands out the same descriptor without that side effect.
func isTerminalFile(f *os.File) bool {
	if f == nil {
		return false
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return false
	}
	isTTY := false
	if err := rc.Control(func(fd uintptr) {
		isTTY = term.IsTerminal(int(fd))
	}); err != nil {
		return false
	}
	return isTTY
}

func (r *Runner) verboseStmt(stmt *syntax.Stmt) {
	if stmt == nil || !r.noOpSetState["verbose"] || len(r.bashSource) == 0 {
		return
	}
	src := r.sourceTextRange(stmt.Pos(), stmt.End(), false)
	if src == "" {
		return
	}
	r.errf("%s", src)
	if !strings.HasSuffix(src, "\n") {
		r.errf("\n")
	}
}

// Exited reports whether the last Run call should exit an entire shell. This
// can be triggered by the "exit" built-in command, for example.
//
// Note that this state is overwritten at every Run call, so it should be
// checked immediately after each Run call.
func (r *Runner) Exited() bool {
	return r.exit.exiting
}

// StdinFile returns the file currently used as the runner's standard input.
// It can change after a persistent redirection such as `exec 0<file`.
func (r *Runner) StdinFile() *os.File {
	return r.stdin
}

// Subshell makes a copy of the given [Runner], suitable for use concurrently
// with the original. The copy will have the same environment, including
// variables and functions, but they can all be modified without affecting the
// original.
//
// Subshell is not safe to use concurrently with [Run]. Orchestrating this is
// left up to the caller; no locking is performed.
//
// To replace e.g. stdin/out/err, do [StdIO](r.stdin, r.stdout, r.stderr)(r) on
// the copy.
func (r *Runner) Subshell() *Runner {
	child := r.subshell(true)
	// A public Runner owns any dependency session it starts. Inherited
	// native handles remain tied to the parent's session and fail closed
	// if used in the independent child session.
	child.bashPPTools.bridge = nil
	return child
}

// subshell is like [Runner.subshell], but allows skipping some allocations and copies
// when creating subshells which will not be used concurrently with the parent shell.
// TODO(v4): we should expose this, e.g. SubshellForeground and SubshellBackground.
func (r *Runner) subshell(background bool) *Runner {
	return r.subshellWithBashPPCapture(background, nil)
}

// subshellWithBashPPCapture makes an ordinary shell copy when capture is nil.
// A non-nil capture denotes a native callback execution snapshot: its mutable
// cells are the exact set already authenticated by lexical capture analysis,
// while Go-source declarations and function bodies are immutable and shared.
func (r *Runner) subshellWithBashPPCapture(background bool, capture map[*bashPPCell]bool) *Runner {
	if !r.didReset {
		r.Reset()
	}
	// Keep in sync with the Runner type. Manually copy fields, to not copy
	// sensitive ones like [errgroup.Group], and to do deep copies of slices.
	r.ensureDirFile(r.Dir)
	dirFile, _ := dupRunnerDir(r.dirFile)
	r2 := &Runner{
		goSourceEnvironment:  slices.Clone(r.goSourceEnvironment),
		ownedExecPaths:       slices.Clone(r.ownedExecPaths),
		bashPPAgentic:        r.bashPPAgentic,
		Dir:                  r.Dir,
		dirFile:              dirFile,
		tempDir:              r.tempDir,
		Params:               r.Params,
		callHandler:          r.callHandler,
		execHandler:          r.execHandler,
		dryRun:               r.dryRun,
		dryRunOpt:            r.dryRunOpt,
		origDryRun:           r.origDryRun,
		origDryRunOpt:        r.origDryRunOpt,
		openHandler:          r.openHandler,
		dryRunOpenHandler:    r.dryRunOpenHandler,
		bashPPCustomOpen:     r.bashPPCustomOpen,
		bashPPProcs:          r.bashPPProcs,
		readDirHandler:       r.readDirHandler,
		statHandler:          r.statHandler,
		stdin:                r.stdin,
		asyncStdinExplicit:   r.asyncStdinExplicit,
		origStdinAsyncCopy:   r.origStdinAsyncCopy,
		stdinTTYFallback:     r.stdinTTYFallback,
		stdinDevTTY:          r.stdinDevTTY,
		stdout:               r.stdout,
		stderr:               r.stderr,
		pipelineOutput:       r.pipelineOutput,
		filename:             r.filename,
		curStmtPos:           r.curStmtPos,
		aliasReparseDepth:    r.aliasReparseDepth,
		aliasBase:            r.aliasBase,
		aliasDefOverride:     r.aliasDefOverride,
		enclosingSubshellEnd: r.enclosingSubshellEnd,
		opts:                 r.opts,
		dialect:              r.dialect,
		bashPPTools:          r.bashPPTools,
		bashPPGoSource:       r.bashPPGoSource,
		bashPPConvertHashQY:  r.bashPPConvertHashQY,
		bashPPGoSourceFile:   r.bashPPGoSourceFile,
		bashPPGoSourceDecls:  maps.Clone(r.bashPPGoSourceDecls),
		origDialect:          r.origDialect,
		hideBashPPOption:     r.hideBashPPOption,
		noOpSetState:         maps.Clone(r.noOpSetState),
		tempEnv:              maps.Clone(r.tempEnv),
		usedNew:              r.usedNew,
		exit:                 r.exit,
		lastExit:             r.lastExit,
		bgPidCallback:        r.bgPidCallback,
		jobCarrier:           r.jobCarrier,
		sigReset:             r.sigReset,
		inheritedBang:        r.lastBangProc(),
		cmdHashTable:         maps.Clone(r.cmdHashTable),

		// Share the outermost `time` clause's CPU accumulator so external
		// children launched inside a subshell or pipeline stage (each of
		// which runs on a subshell copy of this runner) still fold their
		// CPU into the one scope that will print the report. The scope is
		// nil outside a `time` clause. See timingScope.
		inTimeClause: r.inTimeClause,
		timing:       r.timing,

		origStdout: r.origStdout, // used for process substitutions

		promptExpand:           r.promptExpand,
		startTime:              r.startTime,
		subshellLevel:          r.subshellLevel + 1,
		asyncList:              r.asyncList,
		asyncProc:              r.asyncProc,
		umask:                  r.umask,
		startupIgnored:         r.startupIgnored,
		parentPID:              r.parentPID,
		loginShell:             r.loginShell,
		bashCompatErrors:       r.bashCompatErrors,
		execProcessGroups:      r.execProcessGroups,
		strictPosix:            r.strictPosix,
		auditHandler:           r.auditHandler,
		commandResolver:        r.commandResolver,
		servedInProcess:        r.servedInProcess,
		auditLog:               r.auditLog,
		structuredErrorHandler: r.structuredErrorHandler,
		deterministic:          r.deterministic,
		deterministicSeed:      r.deterministicSeed,
		deterministicRng:       r.deterministicRng,
		randomSeeded:           r.randomSeeded,
		randomSeed:             r.randomSeed,
		execReplacement:        r.execReplacement,
		interactiveShell:       r.interactiveShell,
		cmdlineNoExec:          r.cmdlineNoExec,
		commandString:          r.commandString,
		standardInput:          r.standardInput,
		// Subshells inherit open fds the way bash does. Clone the map so
		// child mutations (close, dup) don't leak back to the parent;
		// the underlying *os.File handles are shared (single OS fd).
		fdTable:          maps.Clone(r.fdTable),
		fdReadTable:      maps.Clone(r.fdReadTable),
		fdWriteTable:     maps.Clone(r.fdWriteTable),
		fdClosedTable:    maps.Clone(r.fdClosedTable),
		inheritedFds:     maps.Clone(r.inheritedFds),
		inheritedHandles: r.inheritedHandles,

		// Shared by pointer (not cloned): a background subshell must be
		// able to resolve `$COPROC_PID` to the parent's coprocess so that
		// `kill $COPROC_PID` reaches the real child.
		coprocReg: r.coprocReg,

		ulimitOverride: maps.Clone(r.ulimitOverride),
	}
	r2.writeEnv = newOverlayEnviron(r.writeEnv, background)
	// Callback stack depth is invocation state and cannot cross a Runner clone.
	// Preserve only the fact that the child descends from an active callback;
	// its callback-capable dependency requests are routed by request identity,
	// so the child never borrows or waits for the outer Runner's callback frame.
	r2.bashPPTools.callbackDescendant = r.bashPPTools.callbackDescendant || r.bashPPTools.callbackDepth > 0
	r2.bashPPTools.callbackDepth = 0
	r2.bashPPTools.routedDepth = 0
	// A shell copy is a process boundary, even though this interpreter models
	// it with a goroutine. Channel handles are deliberately not serializable.
	r2.bashPPConcurrent = r.bashPPConcurrent
	r2.bashPPIssuedHandles = r.bashPPIssuedHandles
	r2.bashPPChanBoundary = r.bashPPConcurrent != nil
	// Shell-copy descendants of a task remain inside that task's capability
	// and cancellation boundary. They may not regain process-signal or exec
	// replacement authority merely by entering `( ... )` or a pipeline stage.
	r2.bashPPGoTask = r.bashPPGoTask
	r2.bashPPHostedTask = r.bashPPHostedTask
	r2.bashPPHostedDecls = r.bashPPHostedDecls
	r2.bashPPTaskState = r.bashPPTaskState
	// Funcs are copied, since they might be modified.
	r2.Funcs = maps.Clone(r.Funcs)
	r2.bashPPAgenticFuncs = maps.Clone(r.bashPPAgenticFuncs)
	r2.bashPPDecoratedFuncs = maps.Clone(r.bashPPDecoratedFuncs)
	// The native decorator registry and the advice hook are host wiring, not
	// per-run state: without them a decorated call in ( … ), a pipeline stage
	// or $( … ) fails with BASHPP-EDECO-UNDEF.
	r2.bashPPNativeDecorators = r.bashPPNativeDecorators
	r2.bashPPAdvice = r.bashPPAdvice
	// A chain in flight belongs to the parent's frames; a subshell that
	// calls Next on an inherited Call gets the outside-a-chain diagnostic.
	r2.bashPPDecoratorChains = nil
	r2.bashPPDecoratorStack = append([]string(nil), r.bashPPDecoratorStack...)
	r2.funcSources = maps.Clone(r.funcSources)
	r2.bashPPImports = maps.Clone(r.bashPPImports)
	r2.goSourceUnsafeOpaqueIDs = r.goSourceUnsafeOpaqueIDs
	r2.bashPPImportedInterfacePackages = maps.Clone(r.bashPPImportedInterfacePackages)
	r2.bashPPForeignFuncs = maps.Clone(r.bashPPForeignFuncs)
	r2.bashPPForeignModules = append([]*polyglot.Module(nil), r.bashPPForeignModules...)
	r2.bashPPForeignImports = maps.Clone(r.bashPPForeignImports)
	r2.bashPPSeededImports = maps.Clone(r.bashPPSeededImports)
	if capture != nil {
		// Shared by reference, copy-on-write: see [bashPPSharedTables]. The
		// registering side is marked where it publishes the tables, in
		// [Runner.bashPPCallbackFunctionTemplate], which runs on its own
		// goroutine; r2 is not reachable by anyone else yet.
		r2.bashPPShareTables()
		r2.bashPPTypes = r.bashPPTypes
	} else {
		r2.bashPPTypes = maps.Clone(r.bashPPTypes)
	}
	// The bindings map is read-only once installed, but a subshell may enter
	// its own frames, so it gets its own map rather than sharing this one.
	r2.bashPPTypeParamArgs = maps.Clone(r.bashPPTypeParamArgs)
	// A subshell gets a private copy of the typed bindings, exactly as it
	// gets a private copy of the shell's variables. One cloner does the live
	// scope and every captured closure together so that the aliasing between
	// them survives; see [bashPPCloner]. Sharing instead of copying would be
	// a data race for a background subshell, which runs in its own goroutine.
	if r.bashPPScope != nil || len(r.bashPPFuncScopes) > 0 || len(r.bashPPFuncs) > 0 ||
		len(r.bashPPClosures) > 0 {
		// Deliberate resolution of the two sprint118 lines that met here:
		// the clone still carries the dependency session it belongs to (so a
		// native handle resolves against r2), and it still honours the
		// GoSource lexical capture set (so a cell an original Go closure
		// genuinely names is aliased rather than deep copied).
		cloner := newBashPPClonerFor(r2)
		cloner.shared = r.bashPPGoSourceCapture
		cloner.goSourceTask = r.bashPPGoSource && r.bashPPGoSourceCapture != nil
		if capture != nil {
			cloner.shared = capture
			cloner.goSourceTask = true
			cloner.exactCapture = true
		}
		// The GoSource capture-ownership record travels with the copy. It is
		// the superset of every cell ever shared, so a nested launch inside
		// this copy answers a shared cell from the record instead of reading a
		// payload another goroutine may be writing; see
		// [Runner.bashPPGoSourceSharable]. The clone is private to r2, so the
		// two runners never write one map.
		r2.bashPPGoSourceSharableCells = maps.Clone(r.bashPPGoSourceSharableCells)
		r2.bashPPScope = cloner.clone(r.bashPPScope)
		if capture != nil {
			// Go-source declarations, bodies, and their package-level lexical
			// environments are fixed before execution. Calls create bound copies
			// for receivers and type arguments, so callback frames can safely
			// share these descriptors instead of cloning the entire program.
			r2.bashPPFuncScopes = r.bashPPFuncScopes
			r2.bashPPFuncs = r.bashPPFuncs
			r2.bashPPMethods = r.bashPPMethods
			r2.bashPPClosures = r.bashPPClosures
		} else if r.bashPPFuncScopes != nil {
			r2.bashPPFuncScopes = make(map[string]*bashPPScope, len(r.bashPPFuncScopes))
			for name, scope := range r.bashPPFuncScopes {
				r2.bashPPFuncScopes[name] = cloner.clone(scope)
			}
		}
		if capture == nil && r.bashPPFuncs != nil {
			r2.bashPPFuncs = make(map[string]*bashPPFunc, len(r.bashPPFuncs))
			for name, fn := range r.bashPPFuncs {
				r2.bashPPFuncs[name] = fn.cloned(cloner)
			}
		}
		if capture == nil && r.bashPPMethods != nil {
			r2.bashPPMethods = make(map[string]map[string]*bashPPFunc, len(r.bashPPMethods))
			for typ, methods := range r.bashPPMethods {
				copyMethods := make(map[string]*bashPPFunc, len(methods))
				for name, fn := range methods {
					copyMethods[name] = fn.cloned(cloner)
				}
				r2.bashPPMethods[typ] = copyMethods
			}
		}
		// The closure registry is copied INDEX FOR INDEX: a handle already
		// held in a variable the subshell inherited must keep naming the same
		// function, and the same cloner keeps each closure's captured cells
		// aliased to the subshell's copy of the scope rather than the parent's.
		if capture == nil && len(r.bashPPClosures) > 0 {
			r2.bashPPClosures = make([]*bashPPFunc, len(r.bashPPClosures))
			for i, fn := range r.bashPPClosures {
				r2.bashPPClosures[i] = fn.cloned(cloner)
			}
		}
	}
	r2.Vars = make(map[string]expand.Variable)
	r2.alias = maps.Clone(r.alias)
	r2.trapCallbacks = maps.Clone(r.trapCallbacks)
	r2.asyncDefaultIgnored = maps.Clone(r.asyncDefaultIgnored)
	r2.asyncDefaultReset = maps.Clone(r.asyncDefaultReset)
	r2.inheritedExitTrap = r.trapCallbacks["EXIT"] != ""
	// Subshells inherit the ERR trap only with errtrace (set -E), and
	// the DEBUG/RETURN traps only with functrace (set -T); otherwise
	// they reset to their default disposition. This keeps the ERR trap
	// from firing for failed commands inside `$(...)`, pipeline
	// elements, and `( ... )` (bash trap2.sub/trap3.sub).
	if !r.errtraceEnabled() {
		delete(r2.trapCallbacks, "ERR")
	}
	if !r.functraceEnabled() {
		delete(r2.trapCallbacks, "DEBUG")
		delete(r2.trapCallbacks, "RETURN")
	}
	r2.disabledBuiltins = maps.Clone(r.disabledBuiltins)
	r2.readonlyFuncs = maps.Clone(r.readonlyFuncs)
	r2.funcTrace = maps.Clone(r.funcTrace)
	r2.exportedFuncs = maps.Clone(r.exportedFuncs)
	// Subshells inherit "we're inside a function" so that `return`
	// in `$(... return ...)` aborts only the subshell rather than
	// erroring with "can only be done from a func or sourced
	// script". Bash distinguishes these — see comsub.tests.
	r2.inFunc = r.inFunc
	r2.callStack = append([]callFrame(nil), r.callStack...)
	// Errexit-inhibition flags travel through — `(false; echo A)`
	// as the LHS of `&&` runs both statements because the outer
	// `&&` already suppresses errexit for the subshell body.
	r2.noErrExit = r.noErrExit

	r2.dirStack = append(r2.dirBootstrap[:0], r.dirStack...)
	// A subshell's cd must not move the parent's per-drive cwd bookmarks.
	r2.driveCwd = maps.Clone(r.driveCwd)
	// A foreground subshell runs inline in the parent's goroutine, so it can
	// reach back into the parent's pending-signal queue. Background/async
	// subshells run concurrently and must keep using a real OS signal, so
	// they leave sigParent nil.
	if !background {
		r2.sigParent = r
	}
	rebindPipelineWriters(r2)
	r2.fillExpandConfig(r.ectx)
	r2.didReset = true
	return r2
}
