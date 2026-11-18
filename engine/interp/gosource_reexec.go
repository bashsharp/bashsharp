package interp

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// GoSourceReexecPlan supplies the exact host argv prefix which reconstructs
// the current interpreted Go-source program. When that program calls
// os.Executable, the returned path names a temporary native launcher. Running
// the launcher executes plan followed by the launcher's arguments after
// argv[0], while inheriting the child's cwd, environment, and standard files.
//
// The host owns the plan's meaning. In particular, a CLI host should include
// every source, mapped package, identity, test-main fact, and companion input,
// and end the plan with its program-argument separator. This package neither
// discovers inputs nor falls back to executing a native version of the
// interpreted program. The launcher preserves the child's GOROOT and other
// program environment, but pins the private BASHPP_GO selector to the
// authenticated SDK used to build interpreter helpers. Thus a test may replace
// a tool in its GOROOT without making the replaying front end compile itself.
// Go tool identity probes (-V=full) are answered by the native launcher from
// an authenticated, per-replay identity. They never enter the interpreted
// program: its startup may itself use the Go command, whose tool-ID lookup
// would otherwise invoke the same replacement launcher recursively. That
// identity is content-addressed — the interpreter payload and the plan,
// including the contents of every plan argument naming a file — so a later
// session replaying the same program reports the same Go tool identity and the
// build cache carries its output forward. See bashPPReexecReplayIdentity.
// Replays are admitted rather than started on demand: the Go command schedules
// a tool invocation as if it were a short-lived process, so every launcher
// derived from one plan shares a bound on how many replays run at once and
// refuses a replay tree deeper than [reexecReplayDepthLimitEnv]. Operators may
// retune both through [reexecReplayLimitEnv] and [reexecReplayDepthLimitEnv].
// The launcher also forwards os.Interrupt and SIGTERM to its replayed child and
// waits for it, so signalling the launcher process terminates the replayed
// program rather than leaving it running after the launcher exits.
// Without this option, os.Executable retains its former dependency-bridge
// behavior.
func GoSourceReexecPlan(plan ...string) RunnerOption {
	copyPlan := append([]string(nil), plan...)
	return func(r *Runner) error {
		if len(copyPlan) == 0 {
			return fmt.Errorf("gosource: reexec plan requires an executable")
		}
		if !filepath.IsAbs(copyPlan[0]) {
			return fmt.Errorf("gosource: reexec plan executable must be absolute: %s", copyPlan[0])
		}
		r.bashPPTools.reexecPlan = append([]string(nil), copyPlan...)
		return nil
	}
}

// reexecArgv0Env carries the executable identity (argv[0]) a reexec launcher was
// invoked under, so a replayed program keeps that identity across interpreter
// re-entry instead of falling back to the reconstructed source name.
const reexecArgv0Env = "BASHPP_REEXEC_ARGV0"

func (r *Runner) goSourceFreshReexec() bool {
	if len(r.bashPPTools.reexecPlan) == 0 || r.origArgv0 != "" {
		return false
	}
	vr := r.lookupVar(reexecArgv0Env)
	return !vr.IsSet() || vr.String() == ""
}

const (
	reexecPreparedCacheEnv = "BASHPP_REEXEC_PREPARED_CACHE"
	reexecInterpreterIDEnv = "BASHPP_REEXEC_INTERPRETER_ID"
	// reexecReplayDepthEnv counts the replays already above a launcher
	// process. Each launcher raises it for its own child, so a launcher a
	// replayed program reaches through its own Go tool discovery can tell that
	// it is nested rather than starting a fresh replay tree.
	reexecReplayDepthEnv = "BASHPP_REEXEC_REPLAY_DEPTH"
	// reexecReplayLimitEnv overrides how many replays one launcher admits at a
	// single depth; the default is the host's CPU count, and a value below one
	// admits every replay immediately. reexecReplayDepthLimitEnv overrides the
	// depth beyond which a replay is refused as launcher recursion; the default
	// is reexecReplayDepthLimit and a value below one refuses none.
	reexecReplayLimitEnv      = "BASHPP_REEXEC_REPLAY_LIMIT"
	reexecReplayDepthLimitEnv = "BASHPP_REEXEC_REPLAY_DEPTH_LIMIT"
)

// reexecReplayDepthLimit is the default nesting the launcher admits. Replaying
// a program which replaces a Go tool with its own launcher nests one deep: the
// replay's package discovery may reach the replacement, whose replay may do so
// again. Legitimate nesting is therefore shallow and a deeper tree is a
// launcher cycle, which the guard reports instead of multiplying processes
// until the host is exhausted.
const reexecReplayDepthLimit = 4

// goSourceReexecArgv0 reports the argv[0] the Go-source program should observe.
// A reexec launcher supplies its invoked identity through the Runner's own
// environment; consulting the process environment here would leak one replay's
// identity into unrelated Runners hosted by the same process. An explicitly
// configured argv0 remains authoritative for ordinary runs, while the historic
// default remains the source filename.
func (r *Runner) goSourceReexecArgv0() string {
	if len(r.bashPPTools.reexecPlan) != 0 {
		if vr := r.lookupVar(reexecArgv0Env); vr.IsSet() && vr.String() != "" {
			return vr.String()
		}
	}
	if r.origArgv0 != "" {
		return r.origArgv0
	}
	if session := r.bashPPTools.bridge; session != nil {
		session.reexecMu.Lock()
		launcher := session.reexecLauncher
		session.reexecMu.Unlock()
		if launcher != "" {
			return launcher
		}
	}
	return r.filename
}

func (r *Runner) goSourceExecutableCall(ctx context.Context, call *syntax.BashPPCall) ([]bashPPBridgeValue, bool, error) {
	if !r.bashPPGoSource || len(r.bashPPTools.reexecPlan) == 0 || call == nil || call.Ellipsis.IsValid() || len(call.Args) != 0 || len(call.ArgExprs) != 0 {
		return nil, false, nil
	}
	if selector, ok := r.goSourceImportedCallee(call); !ok || selector != "os.Executable" {
		return nil, false, nil
	}
	session := r.bashPPTools.bridge
	if session == nil {
		return nil, true, fmt.Errorf("gosource: reexec launcher requires an active dependency bridge")
	}
	session.mu.Lock()
	connected := session.conn != nil
	session.mu.Unlock()
	if !connected {
		return nil, true, fmt.Errorf("gosource: reexec launcher requires an active dependency bridge")
	}
	req, err := r.bashPPEvalRequest()
	if err != nil {
		return nil, true, err
	}
	launcher, err := session.goSourceReexecLauncher(ctx, req, r.bashPPTools.reexecPlan, r.tempDir)
	if err != nil {
		return nil, true, err
	}
	return []bashPPBridgeValue{
		{Kind: "string", Type: "string", NativeType: "string", Text: launcher},
		{Kind: "nil"},
	}, true, nil
}

// bashPPReexecReplayIdentity derives the identity a launcher reports for the
// program it replays, from authenticated content alone: the interpreter payload
// digest and the plan, including the digest of every plan argument which names
// a readable file. A Go tool identity keys the build cache, so deriving it from
// the session's temporary launcher workspace instead — as the first version of
// this launcher did — gave the same program a fresh identity in every session,
// and every session recompiled every package the replayed tool produced. A
// content-addressed identity lets one session's output serve the next, and
// changes as soon as one of the program's own inputs does.
func bashPPReexecReplayIdentity(interpreterID string, plan []string) string {
	hash := sha256.New()
	field := func(text string) {
		// Length-prefixed: an argument boundary must not be forgeable by an
		// argument which contains the separator.
		fmt.Fprintf(hash, "%d\x00%s", len(text), text)
	}
	field("bashpp-reexec-replay-v1")
	field(interpreterID)
	for i, arg := range plan {
		field(arg)
		if i == 0 {
			// plan[0] is the interpreter, already named by interpreterID.
			continue
		}
		digest, err := bashPPGoDigest(arg)
		if err != nil {
			// A flag, an import path, the plan's program-argument separator, or
			// a file this process may not read. Its text is already hashed.
			continue
		}
		field(digest)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func (s *bashPPNativeSession) goSourceReexecLauncher(ctx context.Context, req bashPPEvalRequest, plan []string, tempDir string) (string, error) {
	s.reexecMu.Lock()
	defer s.reexecMu.Unlock()
	if s.reexecLauncher != "" {
		return s.reexecLauncher, nil
	}
	if s.reexecDir == "" {
		dir, err := os.MkdirTemp(tempDir, ".bashpp-reexec-")
		if err != nil {
			return "", fmt.Errorf("gosource: create reexec launcher workspace: %w", err)
		}
		s.reexecDir = dir
	}
	preparedCache := filepath.Join(s.reexecDir, "prepared")
	if err := os.MkdirAll(preparedCache, 0700); err != nil {
		return "", fmt.Errorf("gosource: create reexec prepared cache: %w", err)
	}
	interpreterID, err := bashPPGoDigest(plan[0])
	if err != nil {
		return "", fmt.Errorf("gosource: authenticate reexec interpreter: %w", err)
	}
	var quoted []string
	for _, arg := range plan {
		quoted = append(quoted, strconv.Quote(arg))
	}
	quotedBuildGo := strconv.Quote(req.internalBuildGo())
	quotedPreparedCache := strconv.Quote(preparedCache)
	quotedInterpreterID := strconv.Quote(interpreterID)
	quotedReplayID := strconv.Quote(bashPPReexecReplayIdentity(interpreterID, plan))
	quotedReplaySlots := strconv.Quote(filepath.Join(s.reexecDir, "replay-slots"))
	quotedArgv0Env := strconv.Quote(reexecArgv0Env)
	quotedPreparedCacheEnv := strconv.Quote(reexecPreparedCacheEnv)
	quotedInterpreterIDEnv := strconv.Quote(reexecInterpreterIDEnv)
	quotedDepthEnv := strconv.Quote(reexecReplayDepthEnv)
	quotedLimitEnv := strconv.Quote(reexecReplayLimitEnv)
	quotedDepthLimitEnv := strconv.Quote(reexecReplayDepthLimitEnv)
	quotedDepthLimit := strconv.Itoa(reexecReplayDepthLimit)
	source := `package main
import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)
const (
	argv0Env = ` + quotedArgv0Env + `
	preparedCacheEnv = ` + quotedPreparedCacheEnv + `
	interpreterIDEnv = ` + quotedInterpreterIDEnv + `
	depthEnv = ` + quotedDepthEnv + `
	limitEnv = ` + quotedLimitEnv + `
	depthLimitEnv = ` + quotedDepthLimitEnv + `
	replayID = ` + quotedReplayID + `
	replaySlots = ` + quotedReplaySlots + `
	defaultDepthLimit = ` + quotedDepthLimit + `
)
func main() {
	plan := []string{` + strings.Join(quoted, ",") + `}
	if len(os.Args) == 2 && os.Args[1] == "-V=full" {
		versionProbe(replayID)
		return
	}
	depth := setting(depthEnv, 0)
	if limit := setting(depthLimitEnv, defaultDepthLimit); limit > 0 && depth >= limit {
		fmt.Fprintf(os.Stderr, "gosource reexec %q: replay nesting reached depth %d of %d; the replayed program re-enters this launcher through its own tool discovery\n", plan, depth, limit)
		os.Exit(127)
	}
	release := admit(depth)
	code := run(plan, depth, os.Stdin, os.Stdout)
	release()
	os.Exit(code)
}
// setting reads a non-negative integer launcher setting, falling back to
// fallback when the variable is unset or unreadable. A malformed value must not
// fail a replay: the setting only bounds how replays are scheduled.
func setting(name string, fallback int) int {
	text := strings.TrimSpace(os.Getenv(name))
	if text == "" {
		return fallback
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return fallback
	}
	return value
}
// admit reserves one of this launcher's replay slots and reports its release. A
// replay is a whole interpreter, but the Go command schedules a tool invocation
// as if it were a short-lived process: one go build starts -p of them, and each
// replay's own package discovery starts another go build which does the same.
// Nothing else relates one replay to another, so unadmitted the live replay
// count is the product of every nesting level's -p instead of a property of the
// host. The slots live beside the launcher, so every copy of it — a replacement
// tool installed into a test GOROOT is a copy — draws on one bound, and each
// depth draws on its own so a replay waiting for its child never holds the slot
// that child needs. Bookkeeping never denies a replay: an unusable slot
// directory admits immediately.
func admit(depth int) func() {
	limit := setting(limitEnv, runtime.NumCPU())
	if limit < 1 {
		return func() {}
	}
	dir := filepath.Join(replaySlots, strconv.Itoa(depth))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return func() {}
	}
	for {
		for slot := 0; slot < limit; slot++ {
			name := filepath.Join(dir, "slot-" + strconv.Itoa(slot))
			file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err == nil {
				_ = file.Close()
				return hold(name)
			}
			if !errors.Is(err, os.ErrExist) {
				return func() {}
			}
			// A launcher killed outright leaves its slot file behind. Its
			// holder refreshes the file while it runs, so an unrefreshed slot
			// is free.
			if info, statErr := os.Stat(name); statErr == nil && time.Since(info.ModTime()) > 5*time.Second {
				_ = os.Remove(name)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func hold(name string) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				_ = os.Chtimes(name, now, now)
			case <-done:
				return
			}
		}
	}()
	return func() { close(done); <-stopped; _ = os.Remove(name) }
}
func command(plan []string, depth int) *exec.Cmd {
	args := append(append([]string(nil), plan[1:]...), os.Args[1:]...)
	cmd := exec.Command(plan[0], args...)
	private := []string{"BASHPP_GO", argv0Env, preparedCacheEnv, interpreterIDEnv, depthEnv}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		keep := true
		for _, hidden := range private {
			if strings.EqualFold(name, hidden) { keep = false; break }
		}
		if keep { cmd.Env = append(cmd.Env, entry) }
	}
	cmd.Env = append(cmd.Env, "BASHPP_GO=" + ` + quotedBuildGo + `)
	cmd.Env = append(cmd.Env, preparedCacheEnv + "=" + ` + quotedPreparedCache + `)
	cmd.Env = append(cmd.Env, interpreterIDEnv + "=" + ` + quotedInterpreterID + `)
	// Count this replay for every launcher the replayed program reaches, so a
	// nested replay draws on its own slots and a launcher cycle is refused
	// instead of multiplying interpreters. See admit.
	cmd.Env = append(cmd.Env, depthEnv + "=" + strconv.Itoa(depth + 1))
	// Propagate the launcher's own invoked argv[0] so the replayed program keeps
	// the executable identity it was invoked under (e.g. go tool compile), rather
	// than the interpreter's reconstructed source name. See goSourceReexecArgv0.
	cmd.Env = append(cmd.Env, argv0Env + "=" + os.Args[0])
	return cmd
}
func run(plan []string, depth int, stdin *os.File, stdout *os.File) int {
	cmd := command(plan, depth)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, os.Stderr
	return wait(plan, cmd)
}
// wait runs cmd while forwarding os.Interrupt and SIGTERM to it, then waits for
// it to exit. Signalling the launcher process alone (not its group) would
// otherwise leave the replayed child running after the launcher returned;
// forwarding the signal and waiting terminates the direct child with the
// launcher.
func wait(plan []string, cmd *exec.Cmd) int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "gosource reexec %q: %v\n", plan, err)
		return 127
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-signals:
				_ = cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	if err == nil { return 0 }
	if exit, ok := err.(*exec.ExitError); ok { return exit.ExitCode() }
	fmt.Fprintf(os.Stderr, "gosource reexec %q: %v\n", plan, err)
	return 127
}
func versionProbe(replayID string) {
	name := filepath.Base(os.Args[0])
	if ext := filepath.Ext(name); strings.EqualFold(ext, ".exe") {
		name = strings.TrimSuffix(name, ext)
	}
	identity := replayID + "\x00" + name + "\x00" + os.Getenv("GOOS") + "\x00" + os.Getenv("GOARCH") + "\x00" + os.Getenv("GOEXPERIMENT")
	buildID := fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
	fmt.Printf("%s version devel buildID=%s\n", name, buildID)
}
`
	sourcePath := filepath.Join(s.reexecDir, "bashpp-reexec-launcher.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		return "", fmt.Errorf("gosource: write reexec launcher: %w", err)
	}
	launcher := filepath.Join(s.reexecDir, "bashpp-reexec-launcher")
	if executableSuffix := goSourceExecutableSuffix(); executableSuffix != "" {
		launcher += executableSuffix
	}
	cmd := exec.CommandContext(ctx, req.internalBuildGo(), "build", "-p", "2", "-o", launcher, sourcePath)
	cmd.Dir = s.reexecDir
	cmd.Env = setEnvString(req.internalBuildEnv(), "CGO_ENABLED", "0")
	var diagnostics bytes.Buffer
	cmd.Stdout, cmd.Stderr = &diagnostics, &diagnostics
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gosource: build reexec launcher: %w: %s", err, diagnostics.String())
	}
	s.reexecLauncher = launcher
	return launcher, nil
}

func (r *Runner) goSourceImportedCallee(call *syntax.BashPPCall) (string, bool) {
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
	if !ok {
		return "", false
	}
	return path + "." + name, true
}
