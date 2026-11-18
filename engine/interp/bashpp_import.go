package interp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	goversion "go/version"
	"io"
	"maps"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"

	"github.com/bashsharp/bashsharp/engine/polyglot"
)

type bashPPEvalRequest struct {
	disableFastStdlib bool // internal benchmark/control: preserve the worker route
	Go                string
	// BuildGo and BuildEnv are the isolated toolchain used only for helpers
	// generated or inspected by the interpreter. Go and Env retain the
	// interpreted program's selected toolchain identity and process semantics,
	// while an in-place tool replacement in that GOROOT must not compile the
	// helper which implements the interpreted program.
	BuildGo    string
	BuildEnv   []string
	Dir        string
	Env        []string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Imports    map[string]string
	Selector   []string
	Args       []string
	Results    int
	Bridge     *bashPPNativeSession
	Argv       []string
	ModuleDir  string
	RuntimeEnv []string
	SourceDir  string
	SourceFile string
	EmbedDecls []bashPPEmbedDecl
	// CompanionFiles are non-Go same-package object companions, such as .s
	// files, that may satisfy no-body declarations from the original Go
	// source. They are bound into the dependency helper without compiling the
	// original Go root.
	CompanionFiles []string
	NativeFuncs    []bashPPNativeFuncDecl
	// MappedCompanions are exact, package-qualified companion inputs retained
	// by gosource while flattening explicit dependency packages.
	MappedCompanions []bashPPMappedCompanion
	// RootFiles are the original inputs of the interpreted program's own
	// package. The companion build overlays each one away, so that a build of
	// the package directory compiles no interpreted body.
	RootFiles []string
	// CompanionTrampolines are the original package functions a companion
	// object calls. The helper declares each one; its body is the callback
	// protocol, so the original body stays interpreted.
	CompanionTrampolines []bashPPCompanionTrampoline
	// CgoPackages are the cgo pseudo-package bindings the interpreted
	// program's packages reference; each is built natively with cgo.
	CgoPackages []syntax.CgoPackage
	// PanicOnFault is the interpreted goroutine's runtime/debug
	// SetPanicOnFault state. The native worker handles each request on its own
	// goroutine, so the state must be replayed around memory accesses it
	// performs on behalf of this interpreted goroutine.
	PanicOnFault bool
	// CompanionUnmappedFrames are the companion symbols whose assembly frame
	// carries no locals pointer map. While one of those is live the helper may
	// neither move nor scan the goroutine's stack; see companionOffFrame and
	// companionEnterUnmapped in bashpp_native_worker.go.txt.
	CompanionUnmappedFrames []string
	// LocalTypes materialises the original program's own named types inside
	// the dependency helper. Sprint #118 Story #54 (c3a60493cde9).
	LocalTypes []bashPPLocalType
	// Instances registers the instantiations of imported generic functions
	// the program reaches; see bashpp_sprint171_imported_instances.go.
	Instances    []bashPPImportedInstance
	GenericTypes []string
	// Selectors is the sorted set of imported package-qualified selectors
	// ("alias.Name") the program's source references. When non-empty, the
	// dependency bridge registers reflect entries only for these exported
	// symbols instead of every export of every imported package; empty means
	// no filtering (register everything, the conservative default). See
	// [Runner.bashPPReferencedSelectors].
	Selectors     []string
	CallbackOwner *Runner
	CallbackDepth int
	// ImportPath is the program's declared identity (the compiler's -p,
	// bashy --go-import-path) and TestMain the backend-asserted fact that it
	// is cmd/go's generated test main; both empty for an ordinary program.
	// See [GoSourceIdentity].
	ImportPath string
	TestMain   bool
	// FreshReexec reports that this worker starts the original program under a
	// reexec plan, rather than a launcher replay. The worker uses its own binary
	// as argv[0] in this case; a replay keeps the launcher's invoked identity.
	FreshReexec bool
	// identity is the session drift fingerprint of the program-derived
	// descriptor sets above, and localPlan the derivation of LocalTypes the
	// request path performs lookups against. Both are derived once per program
	// revision by [Runner.bashPPNativeRequestShape]; a request assembled
	// without one derives it on demand. See bashpp_s281_request_plan.go.
	identity     *bashPPSessionIdentity
	identityPlan *bashPPNativeRequestPlan
	localPlan    *bashPPLocalTypePlan
}

func (req bashPPEvalRequest) internalBuildGo() string {
	if req.BuildGo != "" {
		return req.BuildGo
	}
	return req.Go
}

func (req bashPPEvalRequest) internalBuildEnv() []string {
	if req.BuildEnv != nil {
		return req.BuildEnv
	}
	return req.Env
}

// GoSourceLinkFlags supplies the original go command's -ldflags value.
// It is inert unless GOEXPERIMENT contains fieldtrack and the flags name
// a fieldtrack -k target.
func GoSourceLinkFlags(flags string) RunnerOption {
	return func(r *Runner) error { r.bashPPTools.LinkFlags = flags; return nil }
}

type bashPPEvaluator interface {
	Resolve(context.Context, bashPPEvalRequest, string) (string, error)
	Call(context.Context, bashPPEvalRequest) error
}

type bashPPValuesEvaluator interface {
	Values(context.Context, bashPPEvalRequest) ([]any, error)
}

// bashPPToolchain is deliberately package-private. The zero value selects the
// Go toolchain which built this package, never an unrelated executable found
// first on PATH. Tests may inject the exact evaluator and identity under
// review without exposing an evaluator API to embedders.
type bashPPToolchain struct {
	nativeTypes    map[string]types.Type // immutable authenticated export metadata
	goBinary       string
	goRoot         string
	goVersion      string
	buildGoBinary  string
	buildGoRoot    string
	buildGoVersion string
	eval           bashPPEvaluator
	bridge         *bashPPNativeSession
	callbackDepth  int
	// callbackDescendant marks a cloned Runner created while an original
	// callback was active. It is lineage, not callback-stack depth: callback-
	// capable requests from this Runner use their authenticated request route
	// instead of borrowing or waiting for the outer Runner's callback frame.
	callbackDescendant bool
	// routedDepth counts callbacks this runner serves on a routed request;
	// requests they raise are routed too (routedCallbackRequest).
	routedDepth int
	// testingCallbackFrames is the lifetime shared by callback frames spawned
	// beneath one native testing.M.Run. The native scheduler, not an arbitrary
	// retained callback, owns this capability and joins it before M.Run returns.
	testingCallbackFrames *bashPPTestingCallbackFrames
	panicOnFault          bool

	moduleDir  string
	importPath string
	testMain   bool
	// reexecPlan is the host-supplied argv prefix which reconstructs this
	// interpreted Go-source program. A generated launcher appends its child
	// argv to this prefix; see GoSourceReexecPlan.
	reexecPlan []string
	// LinkFlags are the original go command's linker flags for an interpreted
	// Go-source program. Only fieldtrack's -k target is interpreted here.
	LinkFlags string
	// instantiations is the per-file closure of reached generic
	// instantiations; see bashpp_sprint165_runtime_instantiations.go.
	instantiations *bashPPInstantiationIndex
	// localTypeDecls is the immutable source index shared by descriptor,
	// lexical-scope and bridge-metadata planning. Its file key invalidates it.
	localTypeDecls *bashPPLocalTypeDeclCache
	localTypes     *bashPPLocalTypeCache
	// requestPlan is the program-derived part of a native eval request, its
	// session fingerprint and its local type lookups, derived once per
	// revision of the inputs it reads; see bashpp_s281_request_plan.go.
	requestPlan *bashPPNativeRequestPlan
	// requestScans counts the local type descriptor entries that program's
	// request path examined. It is the Story #811 instrument, shared by copied
	// Runners; nil for a Runner assembled without [New].
	requestScans *atomic.Int64
	// bridgeMetadata is immutable after publication and shared safely by
	// subshell copies. Its file and import-map identity keep source replacement
	// and import rebinding from reusing stale generic types or selectors.
	bridgeMetadata *bashPPBridgeMetadataCache
	// requestEnv, buildEnv, and runtimeEnv are the immutable process
	// environments used to start a live dependency session. Once that session
	// is connected, later bridge requests only service callbacks; rebuilding
	// the Runner's shell environment for each callback is both unused and
	// disproportionately hot for image encoders. closeGoSourceBridge clears
	// them with the session.
	requestEnv []string
	buildEnv   []string
	runtimeEnv []string
}

type bashPPBridgeMetadataCache struct {
	file         *syntax.File
	imports      map[string]string
	genericTypes []string
	selectors    []string
}

type bashPPGoReview struct {
	Version string
	GOOS    string
	GOARCH  string
	SHA256  string
}

// bashPPReviewedGoVersion is the one reviewed Go toolchain version for Bash#
// Go imports. Every site below uses it so the next security bump is one line.
const bashPPReviewedGoVersion = "go1.27.2"

// bashPPGoReviews is the testable allow-list for platforms on which P2A may
// execute Go. Each digest reviews the bin/go payload in the official Go
// toolchain module, not whichever executable happens to be on PATH.
// The digests were authenticated from the official release archives: each
// archive SHA-256 was verified against https://go.dev/dl/?mode=json, then the
// archive was extracted and bin/go (bin/go.exe on windows) hashed.
var bashPPGoReviews = []bashPPGoReview{
	{Version: bashPPReviewedGoVersion, GOOS: "darwin", GOARCH: "amd64", SHA256: "9dcac8694df86de211ac2ad95878187847b33763f4b6be12fbc035bec005787c"},
	{Version: bashPPReviewedGoVersion, GOOS: "darwin", GOARCH: "arm64", SHA256: "516c1fbb53b5f8f4852f0c3c379d45bcff53a1032e5cd1a9ca1738f03ba07cb1"},
	{Version: bashPPReviewedGoVersion, GOOS: "linux", GOARCH: "amd64", SHA256: "548d3c32f83fa81152f543eb1526be7884cfe0526eb5e9ce29d3eca14786e7e0"},
	{Version: bashPPReviewedGoVersion, GOOS: "linux", GOARCH: "arm64", SHA256: "0e893a1a4ab5d00e64bcd9b22060a53377f90c076434ff0aa1bd2a330b657691"},
	{Version: bashPPReviewedGoVersion, GOOS: "windows", GOARCH: "amd64", SHA256: "b8cd508a0a7aac883cd750e9e75eb306d219e726bdf1cde29009490729437fbf"},
	{Version: bashPPReviewedGoVersion, GOOS: "windows", GOARCH: "arm64", SHA256: "177b6ec96f7d32378429af6004ca583ca019267aca94729abed2e1cbc2d19ca0"},
}

type bashPPGoIdentityInfo struct {
	Version string
	GOOS    string
	GOARCH  string
	Root    string
	Binary  string
	SHA256  string
}

type nativeBashPPEvaluator struct{}

func (nativeBashPPEvaluator) Resolve(ctx context.Context, req bashPPEvalRequest, path string) (string, error) {
	req = bashPPModuleRequest(req)
	target, err := bashPPImportListTarget(ctx, req, path)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, req.internalBuildGo(), "list", "-json", target)
	cmd.Dir, cmd.Env = req.Dir, req.internalBuildEnv()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, req.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("bash++ import %q: go list: %w", path, err)
	}
	var info struct {
		Name     string
		Standard bool
		Dir      string
	}
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		return "", fmt.Errorf("go list %q: %w", path, err)
	}
	if info.Standard && !syntax.BashPPStdlibImportAllowed(path) && !bashPPIdentityAdmitsInternal(req, path) && !bashPPReviewedCompilerImport(req, path) {
		return "", fmt.Errorf("bash++ import %q: package is not in the reviewed Go standard library", path)
	}
	if !syntax.BashPPValidIdent(info.Name) {
		return "", fmt.Errorf("bash++ import %q: invalid package name %q", path, info.Name)
	}
	if err := validateBashPPImportVisibilityFor(req, info.Dir, path); err != nil {
		return "", err
	}
	return info.Name, nil
}

// GoSourceIdentity declares the identity of the Go program the runner
// executes — its import path, the compiler's -p (bashy --go-import-path) —
// and whether it is cmd/go's generated test main (bashy --go-test-main, a
// fact the backend passes from the site that knows, never inferred from a
// ".test" suffix). With an identity declared, internal-package visibility at
// `import` is cmd/go's rule on identities (syntax.BashPPInternalImportVisible)
// instead of the directory rule, and an internal standard-library package
// outside the reviewed inventory is admitted exactly when that rule admits
// it for this identity — which no dotted (user) identity ever qualifies for.
// Inert for shell source and for a runner without an identity.
func GoSourceIdentity(importPath string, testMain bool) RunnerOption {
	return func(r *Runner) error {
		if testMain && importPath == "" {
			return fmt.Errorf("gosource: the test-main fact asserts the identity of the program and requires an import path")
		}
		r.bashPPTools.importPath, r.bashPPTools.testMain = importPath, testMain
		return nil
	}
}

// bashPPIdentityAdmitsInternal is the one identity-keyed branch in front of
// the reviewed inventory: an INTERNAL standard-library package is admitted
// when the declared identity qualifies under cmd/go's rule. It admits
// nothing else — a non-internal unreviewed package (cmd/go, cmd/compile)
// stays with the inventory's verdict.
func bashPPIdentityAdmitsInternal(req bashPPEvalRequest, path string) bool {
	if req.ImportPath == "" {
		return false
	}
	if _, internal := syntax.BashPPInternalImportElement(path); !internal {
		return false
	}
	return syntax.BashPPInternalImportVisible(req.ImportPath, path, req.TestMain)
}

// bashPPReviewedCompilerImport is the deliberately tiny reviewed exception for
// the Go 1.27.1 compiler package roots exercised on the G1 Linux leaf. Both
// paths are standard packages owned by the pinned SDK's src/cmd tree, but are
// intentionally absent from the public standard-library inventory:
//
//   - cmd/compile imports its host architecture package; and
//   - cmd/compile/internal/base reaches the cmd-vendored telemetry counter.
//
// A test-root identity is accepted only when the backend has asserted the
// test-main fact. This does not admit cmd/* generally, other architectures,
// other vendored telemetry packages, or a sibling command tree.
func bashPPReviewedCompilerImport(req bashPPEvalRequest, path string) bool {
	compiler := req.ImportPath == "cmd/compile" || strings.HasPrefix(req.ImportPath, "cmd/compile/")
	if !compiler && !(req.TestMain && req.ImportPath == "cmd/compile.test") {
		return false
	}
	switch path {
	case "cmd/compile/internal/amd64", "cmd/vendor/golang.org/x/telemetry/counter":
		return true
	}
	return false
}

// validateBashPPImportVisibilityFor applies the request's declared identity
// to internal-package visibility (cmd/go's identity rule) and the directory
// rule to everything else — traversal, vendor and, for a request without an
// identity, internal packages as before.
func validateBashPPImportVisibilityFor(req bashPPEvalRequest, packageDir, importPath string) error {
	// The exact reviewed compiler exceptions have no source directory below
	// req.Dir: test units are checked from a scratch tree. Their ownership was
	// decided above from the declared compiler identity and exact pinned path,
	// so applying the directory vendor rule here would incorrectly reject the
	// cmd-vendored dependency.
	if bashPPReviewedCompilerImport(req, importPath) {
		return nil
	}
	if req.ImportPath == "" {
		return validateBashPPImportVisibility(req.Dir, packageDir, importPath)
	}
	if _, internal := syntax.BashPPInternalImportElement(importPath); internal && !syntax.BashPPInternalImportVisible(req.ImportPath, importPath, req.TestMain) {
		return fmt.Errorf("bash++ import %q: use of internal package %s not allowed", importPath, importPath)
	}
	return validateBashPPImportVisibilityDir(req.Dir, packageDir, importPath, false)
}

func validateBashPPImportVisibility(importerDir, packageDir, importPath string) error {
	return validateBashPPImportVisibilityDir(importerDir, packageDir, importPath, true)
}

// validateBashPPImportVisibilityDir is the directory rule; internalToo
// selects whether "internal" directories are subject to it (false when the
// identity rule already decided them).
func validateBashPPImportVisibilityDir(importerDir, packageDir, importPath string, internalToo bool) error {
	for _, elem := range strings.Split(importPath, "/") {
		if elem == "." || elem == ".." {
			return fmt.Errorf("bash++ import %q: path traversal is not allowed", importPath)
		}
		if elem == "vendor" {
			return fmt.Errorf("bash++ import %q: vendor packages must be imported by their canonical path", importPath)
		}
	}
	cleanImporter, err := filepath.EvalSymlinks(importerDir)
	if err != nil {
		return fmt.Errorf("bash++ import %q: resolve importer directory: %w", importPath, err)
	}
	cleanPackage, err := filepath.EvalSymlinks(packageDir)
	if err != nil {
		return fmt.Errorf("bash++ import %q: resolve package directory: %w", importPath, err)
	}
	for current := filepath.Clean(cleanPackage); ; current = filepath.Dir(current) {
		elem := filepath.Base(current)
		if (elem != "internal" || !internalToo) && elem != "vendor" {
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			continue
		}
		root := filepath.Dir(current)
		if !pathWithin(root, cleanImporter) {
			return fmt.Errorf("bash++ import %q: use of %s package outside allowed tree %q", importPath, elem, root)
		}
		if root == current {
			break
		}
	}
	return nil
}

func pathWithin(root, name string) bool {
	rel, err := filepath.Rel(root, name)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (nativeBashPPEvaluator) Call(ctx context.Context, req bashPPEvalRequest) error {
	if req.Bridge != nil {
		_, err := req.Bridge.legacy(ctx, req)
		return err
	}
	if len(req.Selector) == 0 {
		return errors.New("bash++: selector call requires an imported package")
	}
	var dotPaths []string
	for name, importedPath := range req.Imports {
		if strings.HasPrefix(name, ".:") {
			dotPaths = append(dotPaths, importedPath)
		}
	}
	sort.Strings(dotPaths)
	hasDot := len(dotPaths) > 0
	_, named := req.Imports[req.Selector[0]]
	if len(req.Selector) < 2 && !hasDot {
		return errors.New("bash++: selector call requires an imported package")
	}
	if len(req.Selector) >= 2 && !named {
		return fmt.Errorf("bash++: package %s is not imported", req.Selector[0])
	}
	if !token.IsIdentifier(req.Selector[0]) || token.Lookup(req.Selector[0]).IsKeyword() {
		return fmt.Errorf("bash++: invalid import name %q", req.Selector[0])
	}
	selector := ast.Expr(ast.NewIdent(req.Selector[0]))
	for _, part := range req.Selector[1:] {
		if !token.IsIdentifier(part) || token.Lookup(part).IsKeyword() {
			return fmt.Errorf("bash++: invalid selector %q", part)
		}
		selector = &ast.SelectorExpr{X: selector, Sel: ast.NewIdent(part)}
	}
	args := make([]ast.Expr, len(req.Args))
	usedImports := map[string]bool{req.Selector[0]: named}
	for i, text := range req.Args {
		expr, err := parser.ParseExpr(text)
		if err != nil {
			return fmt.Errorf("bash++: invalid Go argument %d: %w", i+1, err)
		}
		args[i] = expr
		ast.Inspect(expr, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok {
				_, imported := req.Imports[ident.Name]
				usedImports[ident.Name] = imported
			}
			return true
		})
	}
	var importSpecs []ast.Spec
	if !named {
		for _, dotPath := range dotPaths {
			importSpecs = append(importSpecs, &ast.ImportSpec{Name: ast.NewIdent("."), Path: &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(dotPath)}})
		}
	}
	names := make([]string, 0, len(req.Imports))
	for name := range req.Imports {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		importedPath := req.Imports[name]
		if strings.HasPrefix(name, "_:") {
			importSpecs = append(importSpecs, &ast.ImportSpec{Name: ast.NewIdent("_"), Path: &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(importedPath)}})
		} else if usedImports[name] {
			importSpecs = append(importSpecs, &ast.ImportSpec{Name: ast.NewIdent(name), Path: &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(importedPath)}})
		}
	}
	file := &ast.File{Name: ast.NewIdent("main"), Decls: []ast.Decl{
		&ast.GenDecl{Tok: token.IMPORT, Specs: importSpecs},
		&ast.FuncDecl{Name: ast.NewIdent("main"), Type: &ast.FuncType{Params: &ast.FieldList{}},
			Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: &ast.CallExpr{Fun: selector, Args: args}}}}},
	}}
	var src bytes.Buffer
	if err := format.Node(&src, token.NewFileSet(), file); err != nil {
		return fmt.Errorf("bash++: construct selector call: %w", err)
	}
	f, err := bashPPImportTempSource(req.Dir, "bashpp-*.go", req.internalBuildEnv(), bashPPScratchSourceTree)
	if err != nil {
		return err
	}
	name := f.Name()
	defer f.cleanup()
	if _, err := io.Copy(f, &src); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	bin := name + ".bin"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.CommandContext(ctx, req.internalBuildGo(), "build", "-overlay="+f.overlay, "-o", bin, f.buildPath)
	build.Dir, build.Env = req.Dir, req.internalBuildEnv()
	build.Stdout, build.Stderr = req.Stdout, req.Stderr
	if err := build.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir, cmd.Env = req.Dir, req.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = req.Stdin, req.Stdout, req.Stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	return nil
}

func (nativeBashPPEvaluator) Values(ctx context.Context, req bashPPEvalRequest) ([]any, error) {
	if req.Bridge != nil {
		values, err := req.Bridge.legacy(ctx, req)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(values))
		for i := range values {
			out[i] = values[i]
		}
		return out, nil
	}
	if len(req.Selector) < 2 || req.Results < 1 {
		return nil, errors.New("bash++: value call requires an imported selector and result names")
	}
	path, ok := req.Imports[req.Selector[0]]
	if !ok {
		return nil, fmt.Errorf("bash++: package %s is not imported", req.Selector[0])
	}
	for _, part := range req.Selector {
		if !token.IsIdentifier(part) || token.Lookup(part).IsKeyword() {
			return nil, fmt.Errorf("bash++: invalid selector %q", part)
		}
	}
	var src strings.Builder
	src.WriteString("package main\nimport (\nbashppjson \"encoding/json\"\nbashppos \"os\"\n")
	src.WriteString(req.Selector[0])
	src.WriteByte(' ')
	src.WriteString(strconv.Quote(path))
	src.WriteString("\n)\nfunc main() {\n")
	for i := 0; i < req.Results; i++ {
		if i > 0 {
			src.WriteByte(',')
		}
		fmt.Fprintf(&src, "bashppv%d", i)
	}
	src.WriteString(" := ")
	src.WriteString(strings.Join(req.Selector, "."))
	src.WriteByte('(')
	src.WriteString(strings.Join(req.Args, ","))
	src.WriteString(")\n_ = bashppjson.NewEncoder(bashppos.Stdout).Encode([]any{")
	for i := 0; i < req.Results; i++ {
		if i > 0 {
			src.WriteByte(',')
		}
		fmt.Fprintf(&src, "bashppv%d", i)
	}
	src.WriteString("})\n}\n")
	formatted, err := format.Source([]byte(src.String()))
	if err != nil {
		return nil, fmt.Errorf("bash++: construct value call: %w", err)
	}
	f, err := bashPPImportTempSource(req.Dir, "bashpp-values-*.go", req.internalBuildEnv(), bashPPScratchSourceTree)
	if err != nil {
		return nil, err
	}
	name := f.Name()
	defer f.cleanup()
	if _, err := f.Write(formatted); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	bin := name + ".bin"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.CommandContext(ctx, req.internalBuildGo(), "build", "-overlay="+f.overlay, "-o", bin, f.buildPath)
	build.Dir, build.Env, build.Stdout, build.Stderr = req.Dir, req.internalBuildEnv(), req.Stdout, req.Stderr
	if err := build.Run(); err != nil {
		return nil, err
	}
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir, cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = req.Dir, req.Env, req.Stdin, &stdout, req.Stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var values []any
	if err := json.Unmarshal(stdout.Bytes(), &values); err != nil {
		return nil, fmt.Errorf("bash++: decode value call: %w", err)
	}
	return values, nil
}

func (r *Runner) bashPPEvalRequest() (bashPPEvalRequest, error) {
	env := r.bashPPTools.requestEnv
	buildEnv := r.bashPPTools.buildEnv
	runtimeEnv := r.bashPPTools.runtimeEnv
	connected := r.bashPPTools.bridge != nil && r.bashPPTools.bridge.conn != nil
	envFresh := !connected || env == nil
	if envFresh {
		env = nativeExecEnv(environStrings(r.writeEnv))
	}
	runtimeEnvFresh := !connected || runtimeEnv == nil
	if runtimeEnvFresh {
		runtimeEnv = env
	}
	if r.bashPPTools.goBinary == "" {
		identity, err := bashPPGoIdentity()
		if err != nil {
			return bashPPEvalRequest{}, fmt.Errorf("bash++: selected Go toolchain: %w", err)
		}
		r.bashPPTools.goBinary = identity.Binary
		r.bashPPTools.goRoot = identity.Root
		r.bashPPTools.goVersion = identity.Version
	}
	if r.bashPPTools.buildGoBinary == "" {
		// Default to the selected toolchain. When the authenticated bootstrap
		// has the same language/tool ABI, prefer its independent SDK tree for
		// sh-owned generated artifacts. Test binaries are allowed to replace a
		// tool in their GOROOT; inheriting that replacement here can otherwise
		// make a helper build recursively re-enter the interpreted program.
		r.bashPPTools.buildGoBinary = r.bashPPTools.goBinary
		r.bashPPTools.buildGoRoot = r.bashPPTools.goRoot
		r.bashPPTools.buildGoVersion = r.bashPPTools.goVersion
		if r.bashPPTools.goVersion != "" {
			identity, err := bashPPGoModuleIdentity()
			if err != nil {
				identity, err = bashPPReviewedGoIdentity()
			}
			if err == nil && identity.Version == r.bashPPTools.goVersion &&
				identity.Root != r.bashPPTools.goRoot {
				r.bashPPTools.buildGoBinary = identity.Binary
				r.bashPPTools.buildGoRoot = identity.Root
				r.bashPPTools.buildGoVersion = identity.Version
			}
		}
	}
	if envFresh && r.bashPPTools.goRoot != "" {
		env = setEnvString(env, "GOROOT", r.bashPPTools.goRoot)
		env = setEnvString(env, "GOTOOLCHAIN", r.bashPPTools.goVersion)
	}
	buildEnvFresh := !connected || buildEnv == nil
	if buildEnvFresh {
		buildEnv = append([]string(nil), env...)
		if r.bashPPTools.buildGoRoot != "" {
			buildEnv = setEnvString(buildEnv, "GOROOT", r.bashPPTools.buildGoRoot)
			buildEnv = setEnvString(buildEnv, "GOTOOLCHAIN", r.bashPPTools.buildGoVersion)
		}
	}
	if r.bashPPGoSource && r.bashPPTools.bridge == nil {
		// The dependency helper materialises the original local types but
		// never their bodies; a mirrored String/Error asks this runner to run
		// the original body. Sprint #118 Story #54 (c3a60493cde9).
		r.bashPPTools.bridge = &bashPPNativeSession{}
	}
	moduleDir, importPath, testMain := "", "", false
	if r.bashPPGoSource {
		moduleDir = r.bashPPTools.moduleDir
		importPath, testMain = r.bashPPTools.importPath, r.bashPPTools.testMain
	}
	if r.bashPPGoSource {
		if runtimeEnvFresh {
			runtimeEnv = nativeExecEnv(r.bashPPGoSourceEnvironment())
		}
		if runtimeEnvFresh && r.goSourceEnvironment == nil && r.bashPPTools.goRoot != "" {
			runtimeEnv = setEnvString(runtimeEnv, "GOROOT", r.bashPPTools.goRoot)
			runtimeEnv = setEnvString(runtimeEnv, "GOTOOLCHAIN", r.bashPPTools.goVersion)
		}
	}
	// The worker receives these only while it starts. After begin has connected
	// it, no later request reads either environment: preserving the first
	// session values is therefore correct even if an original callback mutates
	// the shell environment while it runs.
	if r.bashPPGoSource && !connected {
		r.bashPPTools.requestEnv, r.bashPPTools.buildEnv, r.bashPPTools.runtimeEnv = env, buildEnv, runtimeEnv
	}
	// Every field below is a function of the loaded program, its imports and
	// its directories; deriving them per request cost O(program size) per
	// native call. See bashpp_s281_request_plan.go.
	plan := r.bashPPNativeRequestShape()
	if plan.err != nil {
		return bashPPEvalRequest{}, plan.err
	}
	var argv []string
	if !connected {
		argv = append([]string{r.goSourceReexecArgv0()}, r.Params...)
	}
	return bashPPEvalRequest{CallbackOwner: r, CallbackDepth: r.bashPPTools.callbackDepth, PanicOnFault: r.bashPPTools.panicOnFault, LocalTypes: plan.localTypes, Instances: plan.instances, GenericTypes: plan.genericTypes, Selectors: plan.selectors, RuntimeEnv: runtimeEnv, ModuleDir: moduleDir, ImportPath: importPath, TestMain: testMain, FreshReexec: r.goSourceFreshReexec(), Argv: argv, Bridge: r.bashPPTools.bridge, Go: r.bashPPTools.goBinary, BuildGo: r.bashPPTools.buildGoBinary, BuildEnv: buildEnv, Dir: r.Dir, Env: env, Stdin: r.stdin,
		Stdout: r.bashPPWriter(r.stdout), Stderr: r.bashPPWriter(r.stderr), Imports: r.bashPPImports, SourceDir: plan.sourceDir, SourceFile: plan.sourceFile, EmbedDecls: plan.embedDecls, CompanionFiles: plan.companionFiles, NativeFuncs: plan.nativeFuncs, MappedCompanions: plan.mapped, CompanionTrampolines: plan.trampolines, CompanionUnmappedFrames: plan.unmappedFrames, RootFiles: plan.rootFiles, CgoPackages: plan.cgo,
		identity: &plan.identity, identityPlan: plan, localPlan: plan.localPlan}, nil
}

func (r *Runner) bashPPBridgeMetadata() *bashPPBridgeMetadataCache {
	cache := r.bashPPTools.bridgeMetadata
	if cache != nil && cache.file == r.bashPPGoSourceFile && maps.Equal(cache.imports, r.bashPPImports) {
		return cache
	}
	cache = &bashPPBridgeMetadataCache{
		file:    r.bashPPGoSourceFile,
		imports: maps.Clone(r.bashPPImports),
	}
	cache.genericTypes = r.bashPPBuildGenericBridgeTypes()
	cache.selectors = r.bashPPBuildReferencedSelectors()
	r.bashPPTools.bridgeMetadata = cache
	return cache
}

func (r *Runner) bashPPGenericBridgeTypes() []string {
	return r.bashPPBridgeMetadata().genericTypes
}

func (r *Runner) bashPPBuildGenericBridgeTypes() []string {
	if r.bashPPGoSourceFile == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, named := range r.bashPPLocalTypeDeclarationIndex().genericBridgeTypes {
		text := r.bashPPBridgeTypeIdentity(named)
		if !seen[text] {
			seen[text] = true
			out = append(out, text)
		}
	}
	sort.Strings(out)
	return out
}

// bashPPReferencedSelectors returns the sorted set of imported
// package-qualified selectors ("alias.Name") the program's source references.
// The dependency bridge uses it to register reflect entries for only the
// exported symbols the program can reach, rather than every exported symbol of
// every imported package. A program that imports a package with a large
// exported surface (the compiler internals, say) reaches only a fraction of it;
// emitting the rest forces the helper's compiler to build reflect data for
// thousands of unreached symbols in one generated file, whose peak memory the
// build cannot afford.
//
// The set is deliberately a superset: an extra selector only keeps a symbol
// that already exists, but a missing one would make the runner send a Selector
// the helper cannot resolve, so this walks both forms the runner turns into a
// symbols-table key — every call target (a [syntax.BashPPCall] whose selector
// chain begins with an imported identifier) and every package-qualified
// expression (a [syntax.BashPPSelectorExpr] on an imported identifier), the
// same shapes bashPPPrepareNativeCall and the native expression evaluator
// resolve to a "get"/"call" request Selector.
// Returning nil (no source file, or no imports) disables filtering, so a caller
// that cannot prove the whole program's reach keeps the register-everything
// default.
func (r *Runner) bashPPReferencedSelectors() []string {
	return r.bashPPBridgeMetadata().selectors
}

func (r *Runner) bashPPBuildReferencedSelectors() []string {
	if r.bashPPGoSourceFile == nil || len(r.bashPPImports) == 0 {
		return nil
	}
	seen := map[string]bool{}
	add := func(alias, name string) {
		if alias == "" || name == "" {
			return
		}
		if _, ok := r.bashPPImports[alias]; !ok {
			return
		}
		seen[alias+"."+name] = true
	}
	for _, ref := range r.bashPPLocalTypeDeclarationIndex().selectorRefs {
		add(ref[0], ref[1])
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for sel := range seen {
		out = append(out, sel)
	}
	sort.Strings(out)
	return out
}

func bashPPTypeExprMentionsNames(typ syntax.BashPPTypeExpr, names map[string]bool) bool {
	if len(names) == 0 {
		return false
	}
	found := false
	syntax.Walk(typ, func(n syntax.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *syntax.BashPPTypeParamType:
			found = true
		case *syntax.BashPPNamedType:
			if len(x.TypeArgs) == 0 && names[x.Name.Value] {
				found = true
			}
		}
		return !found
	})
	return found
}

func (r *Runner) bashPPGoSourceCgoPackages() []syntax.CgoPackage {
	if r.bashPPGoSourceFile == nil {
		return nil
	}
	return r.bashPPGoSourceFile.CgoPackages
}

func setEnvString(env []string, name, value string) []string {
	prefix := name + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

func environStrings(env expand.Environ) []string {
	var out []string
	for name, vr := range env.Each {
		if vr.IsSet() {
			out = append(out, name+"="+vr.String())
		}
	}
	return out
}

func bashPPGoIdentity() (bashPPGoIdentityInfo, error) {
	if injected := strings.TrimSpace(os.Getenv("BASHPP_GO")); injected != "" {
		return bashPPInjectedGoIdentity(injected)
	}
	// The embedder's tool resolver (see polyglot.ToolResolver) hands over
	// the provisioned go the same way an explicit BASHPP_GO would; the host
	// bootstrap below is the standalone engine's path only.
	if polyglot.ToolResolver != nil {
		argv, _, err := polyglot.ToolResolver("go")
		if err != nil {
			return bashPPGoIdentityInfo{}, fmt.Errorf("resolve Go toolchain: %w", err)
		}
		if len(argv) != 1 {
			return bashPPGoIdentityInfo{}, fmt.Errorf("resolve Go toolchain: %q is not a single go binary", argv)
		}
		return bashPPInjectedGoIdentity(argv[0])
	}
	return bashPPReviewedGoIdentity()
}

// bashPPReviewedGoIdentity resolves and authenticates the standalone bootstrap
// SDK without consulting BASHPP_GO or the embedder's resolver.
func bashPPReviewedGoIdentity() (bashPPGoIdentityInfo, error) {
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bootstrapRoot, bootstrap, err := bashPPGoBootstrap(name)
	if err != nil {
		return bashPPGoIdentityInfo{}, fmt.Errorf("resolve Go bootstrap: %w", err)
	}
	cmd := exec.Command(bootstrap, "env", "GOROOT", "GOOS", "GOARCH")
	cmd.Env = setEnvString(os.Environ(), "GOTOOLCHAIN", bashPPReviewedGoVersion)
	cmd.Env = setEnvString(cmd.Env, "GOROOT", bootstrapRoot)
	out, err := cmd.Output()
	if err != nil {
		return bashPPGoIdentityInfo{}, fmt.Errorf("resolve GOTOOLCHAIN=%s: %w", bashPPReviewedGoVersion, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 3 {
		return bashPPGoIdentityInfo{}, fmt.Errorf("resolve GOTOOLCHAIN=%s: unexpected go env output %q", bashPPReviewedGoVersion, out)
	}
	root, goos, goarch := fields[0], fields[1], fields[2]
	path := filepath.Join(root, "bin", name)
	real, err := bashPPGoBinaryPath(path)
	if err != nil {
		return bashPPGoIdentityInfo{}, err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return bashPPGoIdentityInfo{}, err
	}
	if fi.IsDir() || (runtime.GOOS != "windows" && fi.Mode()&0111 == 0) {
		return bashPPGoIdentityInfo{}, fmt.Errorf("%s is not executable", real)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return bashPPGoIdentityInfo{}, err
	}
	versionOut, err := exec.Command(real, "version").Output()
	if err != nil {
		return bashPPGoIdentityInfo{}, fmt.Errorf("verify version: %w", err)
	}
	versionFields := strings.Fields(string(versionOut))
	if len(versionFields) != 4 || versionFields[0] != "go" || versionFields[1] != "version" {
		return bashPPGoIdentityInfo{}, fmt.Errorf("unexpected go version output %q", versionOut)
	}
	if versionFields[3] != goos+"/"+goarch {
		return bashPPGoIdentityInfo{}, fmt.Errorf("go version platform %q disagrees with go env %s/%s", versionFields[3], goos, goarch)
	}
	digest, err := bashPPGoDigest(real)
	if err != nil {
		return bashPPGoIdentityInfo{}, err
	}
	identity := bashPPGoIdentityInfo{Version: versionFields[2], GOOS: goos, GOARCH: goarch,
		Root: root, Binary: real, SHA256: digest}
	if err := validateBashPPGoIdentity(identity, bashPPGoReviews); err != nil {
		return bashPPGoIdentityInfo{}, err
	}
	return identity, nil
}

// bashPPInjectedGoIdentity resolves the toolchain the embedder named in
// BASHPP_GO — bashy's own provisioned go, or a host go the caller vouches for.
// The binary is the caller's deliberate choice, so it is verified for what it
// IS (an executable Go >= 1.27.0 toolchain, probed for its own GOROOT and
// platform) rather than against the bootstrap review list, which governs only
// the toolchain this package selects for itself.
func bashPPInjectedGoIdentity(injected string) (bashPPGoIdentityInfo, error) {
	binary, err := filepath.Abs(injected)
	if err != nil {
		return bashPPGoIdentityInfo{}, err
	}
	binary, err = bashPPGoBinaryPath(binary)
	if err != nil {
		return bashPPGoIdentityInfo{}, fmt.Errorf("BASHPP_GO: %w", err)
	}
	fi, err := os.Stat(binary)
	if err != nil {
		return bashPPGoIdentityInfo{}, fmt.Errorf("BASHPP_GO: %w", err)
	}
	if fi.IsDir() || (runtime.GOOS != "windows" && fi.Mode()&0111 == 0) {
		return bashPPGoIdentityInfo{}, fmt.Errorf("BASHPP_GO: %s is not executable", binary)
	}
	// The injected binary reports ITS OWN root: a host-exported GOROOT
	// (setup-go, many Windows installs) would otherwise be echoed back and
	// forced into every worker beside a different binary. -json: a GOROOT
	// with a space (C:\Program Files\Go) or a devel GOVERSION splits
	// strings.Fields.
	cmd := exec.Command(binary, "env", "-json", "GOROOT", "GOOS", "GOARCH", "GOVERSION")
	environ := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GOROOT=") || strings.HasPrefix(entry, "GOTOOLCHAIN=") {
			continue
		}
		environ = append(environ, entry)
	}
	cmd.Env = append(environ, "GOTOOLCHAIN=local")
	out, err := cmd.Output()
	if err != nil {
		return bashPPGoIdentityInfo{}, fmt.Errorf("BASHPP_GO: %s env: %w", binary, err)
	}
	var reported struct{ GOROOT, GOOS, GOARCH, GOVERSION string }
	if err := json.Unmarshal(out, &reported); err != nil || reported.GOROOT == "" || reported.GOVERSION == "" {
		return bashPPGoIdentityInfo{}, fmt.Errorf("BASHPP_GO: unexpected go env output %q", out)
	}
	root, goos, goarch, goVersion := reported.GOROOT, reported.GOOS, reported.GOARCH, reported.GOVERSION
	if !bashPPInjectedGoSupported(goVersion) {
		return bashPPGoIdentityInfo{}, fmt.Errorf("BASHPP_GO: go toolchain %s is older than the go1.27.0 baseline", goVersion)
	}
	digest, err := bashPPGoDigest(binary)
	if err != nil {
		return bashPPGoIdentityInfo{}, err
	}
	return bashPPGoIdentityInfo{Version: goVersion, GOOS: goos, GOARCH: goarch,
		Root: root, Binary: binary, SHA256: digest}, nil
}

// bashPPInjectedGoSupported reports whether an injected toolchain's GOVERSION
// meets the Go 1.27 baseline; an unparseable version does not.
func bashPPInjectedGoSupported(goVersion string) bool {
	return goversion.IsValid(goVersion) && goversion.Compare(goVersion, "go1.27.0") >= 0
}

// bashPPGoBootstrap returns an absolute Go binary without consulting PATH.
// A -trimpath binary has no linker-recorded GOROOT, so in that case use the
// downloaded Go 1.27 toolchain module and authenticate its reviewed payload
// before executing it.
// bashPPRuntimeGOROOT is runtime.GOROOT, a seam for the stale-build-root test.
var bashPPRuntimeGOROOT = runtime.GOROOT

func bashPPGoBootstrap(name string) (root, binary string, err error) {
	// runtime.GOROOT is the GOROOT baked in at build time. A distributed
	// binary runs on hosts that never had that directory (a Windows binary
	// built on another user's machine looked for C:\Users\<builder>\sdk\...),
	// so trust it only when its go binary actually exists here.
	if root = bashPPRuntimeGOROOT(); root != "" {
		if root, err = filepath.Abs(root); err == nil {
			if binary, err = filepath.Abs(filepath.Join(root, "bin", name)); err == nil {
				if fi, statErr := os.Stat(binary); statErr == nil && !fi.IsDir() {
					return root, binary, nil
				}
			}
		}
	}

	identity, err := bashPPGoModuleIdentity()
	if err != nil {
		return "", "", err
	}
	return identity.Root, identity.Binary, nil
}

// bashPPGoModuleIdentity returns the reviewed Go toolchain module directly,
// independently of runtime.GOROOT and the process GOROOT. The latter is
// intentionally mutable program state and may name a test GOROOT whose tools
// have been replaced in place.
func bashPPGoModuleIdentity() (bashPPGoIdentityInfo, error) {
	modCache := os.Getenv("GOMODCACHE")
	if modCache == "" {
		if goPath := os.Getenv("GOPATH"); goPath != "" {
			for _, entry := range filepath.SplitList(goPath) {
				if entry != "" {
					modCache = filepath.Join(entry, "pkg", "mod")
					break
				}
			}
		}
	}
	if modCache == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return bashPPGoIdentityInfo{}, homeErr
		}
		modCache = filepath.Join(home, "go", "pkg", "mod")
	}
	if !filepath.IsAbs(modCache) {
		return bashPPGoIdentityInfo{}, fmt.Errorf("module cache path %q is not absolute", modCache)
	}
	root := filepath.Join(modCache, "golang.org",
		"toolchain@v0.0.1-"+bashPPReviewedGoVersion+"."+runtime.GOOS+"-"+runtime.GOARCH)
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary, err := bashPPGoBinaryPath(filepath.Join(root, "bin", name))
	if err != nil {
		return bashPPGoIdentityInfo{}, err
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return bashPPGoIdentityInfo{}, err
	}
	digest, err := bashPPGoDigest(binary)
	if err != nil {
		return bashPPGoIdentityInfo{}, err
	}
	identity := bashPPGoIdentityInfo{Version: bashPPReviewedGoVersion, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Root: root, Binary: binary, SHA256: digest}
	if err := validateBashPPGoIdentity(identity, bashPPGoReviews); err != nil {
		return bashPPGoIdentityInfo{}, err
	}
	return identity, nil
}

func bashPPGoDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	closeErr := f.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func validateBashPPGoIdentity(got bashPPGoIdentityInfo, reviews []bashPPGoReview) error {
	wantBinary := filepath.Join(got.Root, "bin", "go")
	if got.GOOS == "windows" {
		wantBinary += ".exe"
	}
	wantBinary, err := bashPPGoBinaryPath(wantBinary)
	if err != nil {
		return err
	}
	wantBinary, err = filepath.Abs(wantBinary)
	if err != nil {
		return err
	}
	if got.Binary != wantBinary {
		return fmt.Errorf("Go binary path %q, want reviewed GOROOT binary %q", got.Binary, wantBinary)
	}
	for _, review := range reviews {
		if got.Version == review.Version && got.GOOS == review.GOOS && got.GOARCH == review.GOARCH {
			if got.SHA256 != review.SHA256 {
				return fmt.Errorf("Go %s %s/%s checksum %s is not reviewed", got.Version, got.GOOS, got.GOARCH, got.SHA256)
			}
			return nil
		}
	}
	return fmt.Errorf("Go toolchain %s %s/%s is not reviewed", got.Version, got.GOOS, got.GOARCH)
}

func (r *Runner) bashPPImport(ctx context.Context, imp *syntax.BashPPImport) {
	if !r.bashPPEnabled() || r.PosixMode() {
		r.shellFallbackImport(ctx, imp)
		return
	}
	if imp.Language != nil {
		return
	}
	req, err := r.bashPPEvalRequest()
	if err != nil {
		r.exit.fatal(err)
		return
	}
	specs := imp.Specs
	if imp.Path != nil {
		specs = []*syntax.BashPPImportSpec{{Alias: imp.Alias, Path: imp.Path}}
	}
	next := make(map[string]string, len(r.bashPPImports)+len(specs))
	for k, v := range r.bashPPImports {
		next[k] = v
	}
	groupPaths := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		pathText := spec.Path.Parts[0].(*syntax.Lit).Value
		path, err := strconv.Unquote(`"` + pathText + `"`)
		if err != nil {
			r.exit.fatal(fmt.Errorf("bash++: invalid interpreted import path: %w", err))
			return
		}
		if pathpkg.Clean(path) != path || strings.HasPrefix(path, "/") || filepath.IsAbs(path) {
			r.exit.fatal(fmt.Errorf("bash++ import %q: path traversal or absolute paths are not allowed", path))
			return
		}
		name := ""
		if path == "C" {
			for _, pkg := range req.CgoPackages {
				if spec.Alias != nil && pkg.Alias == spec.Alias.Value || spec.Alias == nil && pkg.Alias == "C" {
					name = pkg.Alias
					break
				}
			}
			if name == "" {
				r.exit.fatal(fmt.Errorf("bash++ import %q: no authenticated package-scoped cgo metadata", path))
				return
			}
		} else {
			name, err = r.bashPPTools.eval.Resolve(ctx, r.goSourceImporterRequest(req, imp), path)
			if err != nil {
				r.exit.fatal(err)
				return
			}
		}
		if spec.Alias != nil {
			name = spec.Alias.Value
		}
		if r.bashPPGoSource && path != "C" {
			if err := r.bashPPBridgeRegisterScalarTypes(ctx, req, path, name); err != nil {
				r.exit.fatal(err)
				return
			}
		}
		if _, exists := groupPaths[path]; exists {
			r.exit.fatal(fmt.Errorf("bash++: duplicate import path %q", path))
			return
		}
		groupPaths[path] = struct{}{}
		key := name
		if name == "_" || name == "." {
			key = name + ":" + path
		}
		if oldPath, exists := next[key]; exists {
			if oldPath == path {
				continue
			}
			r.exit.fatal(fmt.Errorf("bash++: import name %s already refers to %q", name, oldPath))
			return
		}
		for oldName, oldPath := range next {
			if oldPath == path && !r.bashPPGoSource {
				r.exit.fatal(fmt.Errorf("bash++: import path %q already uses name %s", path, oldName))
				return
			}
		}
		next[key] = path
	}
	r.bashPPImports = next
}

func (r *Runner) shellFallbackImport(ctx context.Context, imp *syntax.BashPPImport) {
	if imp.Path == nil {
		r.errf("bash++ grouped import evaluated with extensions disabled\n")
		r.exit = exitStatus{code: 2}
		return
	}
	call := &syntax.CallExpr{Args: []*syntax.Word{{Parts: []syntax.WordPart{imp.Kw}}}}
	if imp.Alias != nil {
		call.Args = append(call.Args, &syntax.Word{Parts: []syntax.WordPart{imp.Alias}})
	}
	call.Args = append(call.Args, &syntax.Word{Parts: []syntax.WordPart{imp.Path}})
	r.cmd(ctx, call)
}

// goSourceImporterRequest scopes one import's resolution to the identity of
// the package whose file declares it. A linked package of a package map — the
// tested package under cmd/go's test main — imports under its own import path,
// exactly as the front end checked it and as cmd/go applies the internal rule
// per importing package; only the program package keeps the declared program
// identity. An external test package (p_test) has p's visibility.
func (r *Runner) goSourceImporterRequest(req bashPPEvalRequest, imp *syntax.BashPPImport) bashPPEvalRequest {
	if !r.bashPPGoSource || r.bashPPGoSourceFile == nil || imp == nil || req.ImportPath == "" {
		return req
	}
	source, ok := r.bashPPGoSourceFile.SourceAt(imp.Pos())
	if !ok || source.PackagePath == "" {
		return req
	}
	req.ImportPath, req.TestMain = strings.TrimSuffix(source.PackagePath, "_test"), false
	return req
}
