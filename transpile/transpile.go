package transpile

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/qiangli/bashsharp/front"

	"mvdan.cc/sh/v3/lower"
	"mvdan.cc/sh/v3/syntax"
)

// Reproducible Standalone Build Instructions:
//
// To compile a transpiled Go output into a standalone binary:
// 1. Transpile Bash++ script to Go source:
//    bashy transpile --bashsharp input.bsh -o output.go
// 2. Setup dependency clone (deps/sh pinned to published commit 7146b30e1c1c8c845f6565c9f5c5609f4de93172)
//    and configure module replace directive in app directory (mvdan.cc/sh/v3 => ../deps/sh):
//    cd workspace/app
//    go mod init app
//    go mod edit -require=mvdan.cc/sh/v3@v3.0.0
//    go mod edit -replace=mvdan.cc/sh/v3=../deps/sh
//    go mod tidy
// 3. Build a reproducible binary against the standard library / shell runtime:
//    go build -mod=mod -o myapp output.go
//
// Standalone binaries emitted by the lower compiler depend on the plain runtime
// base (mvdan.cc/sh/v3/lower/shellrt) using standard Go library primitives.
// Dynamic features requiring the shell execution engine are compiled to explicit
// shellrt bridge calls rather than unanalyzed interp invocations.
// See docs/transpile.md for complete build recipe and runtime details.

const sourceMapSchemaVersion = "bashy-transpile-map-v1"

type mapEntry struct {
	GoLine       int    `json:"go_line"`
	GoCol        int    `json:"go_col"`
	SourceLine   uint   `json:"source_line"`
	SourceCol    uint   `json:"source_col"`
	SourceOffset uint   `json:"source_offset"`
	Node         string `json:"node"`
	// SourceFile and SourceFileOffset resolve a mapping back to ONE original
	// input file and an offset within that file's own bytes. They are set only
	// for --source=go, where the program's position space spans several
	// original files; shell input has a single source and omits them, so the
	// artifact stays byte-compatible for existing consumers.
	SourceFile       string `json:"source_file,omitempty"`
	SourceFileOffset uint   `json:"source_file_offset,omitempty"`
}

// mapSource records one original input file and the exact bytes that were
// read, so a harness can prove the tested bytes were the pinned bytes.
type mapSource struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Base   uint   `json:"base"`
	Size   uint   `json:"size"`
}

type sourceMapArtifact struct {
	SchemaVersion string     `json:"schema_version"`
	Origin        string     `json:"origin"`
	GoDigest      string     `json:"go_digest"`
	Mappings      []mapEntry `json:"mappings"`
	// SourceKind is "go" for --source=go and omitted for shell input.
	SourceKind string `json:"source_kind,omitempty"`
	// FrontEnd is the Go front end's version, recorded as evidence of which
	// ingestion produced these positions.
	FrontEnd string      `json:"front_end,omitempty"`
	Sources  []mapSource `json:"sources,omitempty"`
}

// formatDiagnostic renders a lower.Diagnostic. Non-empty Text takes precedence.
// Structured diagnostics with a Code starting with "BASHPP-" are printed as
// exact Code + ": " + Msg without file position prefixes; ordinary LOWER-
// diagnostics retain their positioned format.
func formatDiagnostic(d lower.Diagnostic) string {
	if d.Text != "" {
		return d.Text
	}
	if strings.HasPrefix(d.Code, "BASHPP-") {
		return fmt.Sprintf("%s: %s", d.Code, d.Msg)
	}
	return d.Error()
}

// normPath returns clean absolute path with symlinks resolved if possible.
func normPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	eval, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(eval)
	}
	dir := filepath.Dir(abs)
	base := filepath.Base(abs)
	evalDir, err := filepath.EvalSymlinks(dir)
	if err == nil {
		return filepath.Clean(filepath.Join(evalDir, base))
	}
	return filepath.Clean(abs)
}

// isSameFileOrAlias checks if two paths refer to the same underlying file or directory.
func isSameFileOrAlias(pathA, pathB string) bool {
	if pathA == "" || pathB == "" {
		return false
	}
	normA := normPath(pathA)
	normB := normPath(pathB)
	if normA == normB {
		return true
	}
	statA, errA := os.Stat(pathA)
	statB, errB := os.Stat(pathB)
	if errA == nil && errB == nil && os.SameFile(statA, statB) {
		return true
	}
	lstatA, lerrA := os.Lstat(pathA)
	lstatB, lerrB := os.Lstat(pathB)
	if lerrA == nil && lerrB == nil && os.SameFile(lstatA, lstatB) {
		return true
	}
	return false
}

// Main handles
// 'bashy transpile --bashsharp [--source=go] INPUT -o OUTPUT.go [--map MAPFILE]'.
//
// Sprint 118: --source=go takes the SAME selector as the shell entry point, so
// a corpus recipe spells one language for both product modes. The Go bytes are
// loaded by the sh front end through front.LoadGoSource; malformed Go is
// reported with Go diagnostics and never reparsed as shell.
func Main(args []string) int {
	var bashpp bool
	var output string
	var input string
	var mapFile string
	sourceKind := "sh"
	var goFiles []string
	var goTestFiles []string
	var goXTestFiles []string
	var goPackages []front.GoSourcePackageSpec
	var goLibrary string
	var goImportBase, goImportPath string
	var goTestMain bool
	var goNativeUnit bool
	var goVersion string
	var goVersionSeen bool
	var goTestBuiltins bool
	var goTestBuiltinsSeen bool
	var goCheckerBranchErrors, goCheckerBranchErrorsSeen bool
	var goCheckAfterSyntaxErrors, goCheckAfterSyntaxErrorsSeen bool

	inFlags := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if inFlags && arg == "--" {
			inFlags = false
			continue
		}
		if inFlags && (arg == "--bashsharp" || arg == "--bashpp") {
			bashpp = true
		} else if inFlags && arg == "--source" {
			if i+1 < len(args) {
				sourceKind = args[i+1]
				i++
			} else {
				fmt.Fprintln(os.Stderr, "transpile: missing argument for --source")
				return 2
			}
		} else if inFlags && strings.HasPrefix(arg, "--source=") {
			sourceKind = strings.TrimPrefix(arg, "--source=")
		} else if inFlags && (arg == "--go-test-builtins" || arg == "--go-test-builtins=true") {
			goTestBuiltins, goTestBuiltinsSeen = true, true
		} else if inFlags && strings.HasPrefix(arg, "--go-test-builtins=") {
			fmt.Fprintln(os.Stderr, "transpile: --go-test-builtins: expected true")
			return 2
		} else if inFlags && (arg == "--go-checker-branch-errors" || arg == "--go-checker-branch-errors=true") {
			goCheckerBranchErrors, goCheckerBranchErrorsSeen = true, true
		} else if inFlags && strings.HasPrefix(arg, "--go-checker-branch-errors=") {
			fmt.Fprintln(os.Stderr, "transpile: --go-checker-branch-errors: expected true")
			return 2
		} else if inFlags && (arg == "--go-check-after-syntax-errors" || arg == "--go-check-after-syntax-errors=true") {
			goCheckAfterSyntaxErrors, goCheckAfterSyntaxErrorsSeen = true, true
		} else if inFlags && strings.HasPrefix(arg, "--go-check-after-syntax-errors=") {
			fmt.Fprintln(os.Stderr, "transpile: --go-check-after-syntax-errors: expected true")
			return 2
		} else if inFlags && arg == "--go-version" {
			if i+1 >= len(args) || args[i+1] == "" {
				fmt.Fprintln(os.Stderr, "transpile: missing argument for --go-version")
				return 2
			}
			goVersion, goVersionSeen = args[i+1], true
			i++
		} else if inFlags && strings.HasPrefix(arg, "--go-version=") {
			goVersion, goVersionSeen = strings.TrimPrefix(arg, "--go-version="), true
			if goVersion == "" {
				fmt.Fprintln(os.Stderr, "transpile: missing argument for --go-version")
				return 2
			}
		} else if inFlags && arg == "--go-file" {
			if i+1 < len(args) {
				goFiles = append(goFiles, args[i+1])
				i++
			} else {
				fmt.Fprintln(os.Stderr, "transpile: missing argument for --go-file")
				return 2
			}
		} else if inFlags && strings.HasPrefix(arg, "--go-file=") {
			goFiles = append(goFiles, strings.TrimPrefix(arg, "--go-file="))
		} else if inFlags && arg == "--go-test-file" {
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "transpile: missing argument for --go-test-file")
				return 2
			}
			goTestFiles = append(goTestFiles, args[i+1])
			i++
		} else if inFlags && strings.HasPrefix(arg, "--go-test-file=") {
			goTestFiles = append(goTestFiles, strings.TrimPrefix(arg, "--go-test-file="))
		} else if inFlags && arg == "--go-xtest-file" {
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "transpile: missing argument for --go-xtest-file")
				return 2
			}
			goXTestFiles = append(goXTestFiles, args[i+1])
			i++
		} else if inFlags && strings.HasPrefix(arg, "--go-xtest-file=") {
			goXTestFiles = append(goXTestFiles, strings.TrimPrefix(arg, "--go-xtest-file="))
		} else if inFlags && arg == "--go-native-unit" {
			goNativeUnit = true
		} else if inFlags && arg == "--go-library" {
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "transpile: missing argument for --go-library")
				return 2
			}
			goLibrary = args[i+1]
			i++
		} else if inFlags && strings.HasPrefix(arg, "--go-library=") {
			goLibrary = strings.TrimPrefix(arg, "--go-library=")
		} else if inFlags && (arg == "--go-package" || strings.HasPrefix(arg, "--go-package=")) {
			value, ok := strings.CutPrefix(arg, "--go-package=")
			if !ok {
				if i+1 >= len(args) {
					fmt.Fprintln(os.Stderr, "transpile: missing argument for --go-package")
					return 2
				}
				value = args[i+1]
				i++
			}
			spec, err := front.ParseGoSourcePackage(value)
			if err != nil {
				fmt.Fprintln(os.Stderr, transpileGoSourceDiagnostic(err))
				return 2
			}
			goPackages = append(goPackages, spec)
		} else if inFlags && (arg == "--go-import-base" || strings.HasPrefix(arg, "--go-import-base=")) {
			value, ok := strings.CutPrefix(arg, "--go-import-base=")
			if !ok {
				if i+1 >= len(args) {
					fmt.Fprintln(os.Stderr, "transpile: missing argument for --go-import-base")
					return 2
				}
				value = args[i+1]
				i++
			}
			if value == "" {
				fmt.Fprintln(os.Stderr, "transpile: missing argument for --go-import-base")
				return 2
			}
			goImportBase = value
		} else if inFlags && arg == "--go-test-main" {
			goTestMain = true
			continue
		} else if inFlags && (arg == "--go-import-path" || strings.HasPrefix(arg, "--go-import-path=")) {
			value, ok := strings.CutPrefix(arg, "--go-import-path=")
			if !ok {
				if i+1 >= len(args) {
					fmt.Fprintln(os.Stderr, "transpile: missing argument for --go-import-path")
					return 2
				}
				value = args[i+1]
				i++
			}
			if value == "" {
				fmt.Fprintln(os.Stderr, "transpile: missing argument for --go-import-path")
				return 2
			}
			goImportPath = value
		} else if inFlags && arg == "-o" {
			if i+1 < len(args) {
				output = args[i+1]
				i++
			} else {
				fmt.Fprintln(os.Stderr, "transpile: missing argument for -o")
				return 2
			}
		} else if inFlags && strings.HasPrefix(arg, "-o") && len(arg) > 2 {
			output = arg[2:]
		} else if inFlags && arg == "--map" {
			if i+1 < len(args) {
				mapFile = args[i+1]
				i++
			} else {
				fmt.Fprintln(os.Stderr, "transpile: missing argument for --map")
				return 2
			}
		} else if inFlags && strings.HasPrefix(arg, "--map=") {
			mapFile = strings.TrimPrefix(arg, "--map=")
		} else if !inFlags || arg == "-" || !strings.HasPrefix(arg, "-") {
			if input == "" {
				input = arg
			} else {
				fmt.Fprintf(os.Stderr, "transpile: unexpected argument: %s\n", arg)
				return 2
			}
		} else {
			fmt.Fprintf(os.Stderr, "transpile: unknown flag: %s\n", arg)
			return 2
		}
	}

	switch sourceKind {
	case "sh", "go":
	default:
		fmt.Fprintf(os.Stderr, "transpile: --source: unknown input language %q (expected \"sh\" or \"go\")\n", sourceKind)
		return 2
	}
	goInput := sourceKind == "go"
	if goTestBuiltinsSeen && !goInput {
		fmt.Fprintln(os.Stderr, "transpile: --go-test-builtins requires --source=go")
		return 2
	}
	if goCheckerBranchErrorsSeen && !goInput {
		fmt.Fprintln(os.Stderr, "transpile: --go-checker-branch-errors requires --source=go")
		return 2
	}
	if goCheckAfterSyntaxErrorsSeen && !goInput {
		fmt.Fprintln(os.Stderr, "transpile: --go-check-after-syntax-errors requires --source=go")
		return 2
	}
	if goNativeUnit && (!goInput || goImportPath == "") {
		fmt.Fprintln(os.Stderr, "transpile: --go-native-unit requires --source=go and --go-import-path")
		return 2
	}
	if goNativeUnit && (goLibrary != "" || goTestMain) {
		fmt.Fprintln(os.Stderr, "transpile: --go-native-unit cannot be combined with --go-library or --go-test-main")
		return 2
	}
	if goVersionSeen && !goInput {
		fmt.Fprintln(os.Stderr, "transpile: --go-version requires --source=go")
		return 2
	}
	if len(goFiles) > 0 && !goInput {
		fmt.Fprintln(os.Stderr, "transpile: --go-file requires --source=go")
		return 2
	}
	if (len(goTestFiles) > 0 || len(goXTestFiles) > 0 || goLibrary != "") && !goInput {
		fmt.Fprintln(os.Stderr, "transpile: --go-test-file, --go-xtest-file and --go-library require --source=go")
		return 2
	}
	if (len(goPackages) > 0 || goImportBase != "" || goImportPath != "" || goTestMain) && !goInput {
		fmt.Fprintln(os.Stderr, "transpile: --go-package, --go-import-base, --go-import-path and --go-test-main require --source=go")
		return 2
	}
	if goTestMain && goImportPath == "" {
		fmt.Fprintln(os.Stderr, "transpile: --go-test-main asserts the identity of the program and requires --go-import-path")
		return 2
	}
	if goLibrary != "" && input != "" {
		fmt.Fprintln(os.Stderr, "transpile: --go-library requires Go input files")
		return 2
	}
	if (len(goFiles) > 0 || len(goTestFiles) > 0 || len(goXTestFiles) > 0) && input != "" {
		fmt.Fprintln(os.Stderr, "transpile: --go-file cannot be combined with a file operand")
		return 2
	}
	if input == "" && len(goFiles) == 0 && len(goTestFiles) == 0 && len(goXTestFiles) == 0 {
		fmt.Fprintln(os.Stderr, "transpile: missing INPUT")
		return 2
	}
	if !bashpp {
		fmt.Fprintln(os.Stderr, "transpile: --bashsharp is required")
		return 2
	}
	if output == "" && goLibrary == "" {
		fmt.Fprintln(os.Stderr, "transpile: missing -o OUTPUT.go")
		return 2
	}
	if goLibrary != "" {
		if output != "" {
			fmt.Fprintln(os.Stderr, "transpile: --go-library cannot be combined with -o")
			return 2
		}
		if len(goPackages) > 0 {
			fmt.Fprintln(os.Stderr, "transpile: --go-library refuses --go-package")
			return 2
		}
		if goImportPath == "" {
			fmt.Fprintln(os.Stderr, "transpile: --go-library requires --go-import-path")
			return 2
		}
		if len(goFiles)+len(goTestFiles)+len(goXTestFiles) == 0 {
			fmt.Fprintln(os.Stderr, "transpile: --go-library requires Go input files")
			return 2
		}
		st, err := os.Stat(goLibrary)
		if err != nil || !st.IsDir() {
			fmt.Fprintf(os.Stderr, "transpile: --go-library path is not a directory: %s\n", goLibrary)
			return 2
		}
		return dispatchTranspileLibrary(goLibrary, goFiles, goTestFiles, goXTestFiles, front.GoSourceOptions{
			GoVersion: goVersion, TestBuiltins: goTestBuiltins,
			CheckerBranchErrors: goCheckerBranchErrors, CheckAfterSyntaxErrors: goCheckAfterSyntaxErrors,
			ImportBase: goImportBase, ImportPath: goImportPath, TestMain: goTestMain, PreserveNativeInit: true,
		})
	}
	if len(goTestFiles) > 0 || len(goXTestFiles) > 0 {
		fmt.Fprintln(os.Stderr, "transpile: --go-test-file and --go-xtest-file require --go-library")
		return 2
	}

	if mapFile == "" {
		if strings.HasSuffix(output, ".go") {
			mapFile = strings.TrimSuffix(output, ".go") + ".go.map"
		} else {
			mapFile = output + ".map"
		}
	}

	// Reject all path collisions, including every explicit Go package file.
	// A directory operand's members are only known after collection, so they
	// are checked again below, before anything is written.
	for _, name := range goFiles {
		if code := checkTranspileInputCollision(name, output, mapFile); code != 0 {
			return code
		}
	}
	if input != "" && input != "-" {
		if code := checkTranspileInputCollision(input, output, mapFile); code != 0 {
			return code
		}
	}
	if isSameFileOrAlias(output, mapFile) {
		fmt.Fprintln(os.Stderr, "transpile: output and map path cannot be the same file")
		return 2
	}

	// Validate destination directory vs file types. A directory operand is a
	// package recipe under --source=go, so it is only rejected for shell input.
	if st, err := os.Stat(input); !goInput && input != "" && input != "-" && err == nil && st.IsDir() {
		fmt.Fprintf(os.Stderr, "transpile: input path is a directory: %s\n", input)
		return 2
	}
	if st, err := os.Stat(output); err == nil && st.IsDir() {
		fmt.Fprintf(os.Stderr, "transpile: output path is a directory: %s\n", output)
		return 2
	}
	if st, err := os.Stat(mapFile); err == nil && st.IsDir() {
		fmt.Fprintf(os.Stderr, "transpile: map path is a directory: %s\n", mapFile)
		return 2
	}

	sourceDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
		return 2
	}

	var file *syntax.File
	var goProg *front.GoSourceProgram
	origin := input
	if goInput {
		in, err := collectTranspileGoSource(input, goFiles)
		if err != nil {
			fmt.Fprintln(os.Stderr, transpileGoSourceDiagnostic(err))
			return 2
		}
		// EVERY collected original is checked against the destinations before
		// a single byte is written. A directory recipe expands to files this
		// command never saw on the command line, so checking only the operand
		// would let `transpile --source=go ./pkg -o ./pkg/main.go` overwrite
		// an upstream program with its own generated output — and the whole
		// point of unchanged-source ingestion is that the input survives it.
		for _, f := range in.Files {
			if f.Name == "-" || f.Name == "-c" {
				continue
			}
			if code := checkTranspileInputCollision(f.Name, output, mapFile); code != 0 {
				return code
			}
		}
		packages, err := front.ReadGoSourcePackages(goPackages)
		if err != nil {
			fmt.Fprintln(os.Stderr, transpileGoSourceDiagnostic(err))
			return 2
		}
		file, goProg, err = loadTranspileGoSource(in, front.GoSourceOptions{
			GoVersion: goVersion, TestBuiltins: goTestBuiltins,
			CheckerBranchErrors: goCheckerBranchErrors, CheckAfterSyntaxErrors: goCheckAfterSyntaxErrors,
			Packages: packages, ImportBase: goImportBase, ImportPath: goImportPath, TestMain: goTestMain,
			// Directory compiler phases and explicitly requested module units
			// preserve package names, imports and native initialization. The
			// package map supplies checker dependencies, not flattened code.
			PreserveNativeInit: goNativeUnit || goImportBase != "",
		})
		if err != nil {
			// sh's Go diagnostics carry their own file:line:col positions and
			// are printed verbatim. Bashy's own refusals are re-labelled with
			// this command's name. There is no shell reparse behind either.
			fmt.Fprintln(os.Stderr, transpileGoSourceDiagnostic(err))
			return 2
		}
		// Origin stays the first ORIGINAL file name, never a generated one,
		// so every lowered position resolves against upstream source.
		origin, sourceDir = in.Files[0].Name, in.Dir
	} else {
		f := os.Stdin
		if input != "-" {
			sourcePath, err := filepath.Abs(input)
			if err != nil {
				fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
				return 2
			}
			sourceDir = filepath.Dir(sourcePath)
			f, err = os.Open(input)
			if err != nil {
				fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
				return 2
			}
			defer f.Close()
		}
		parser := syntax.NewParser(syntax.KeepComments(true), syntax.Variant(syntax.LangBashPP))
		file, err = parser.Parse(f, input)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}

	opts := lower.Options{
		Origin: origin,
		Dir:    sourceDir,
	}
	if goProg != nil {
		// Lower against the importer the front end checked with, so an
		// explicit package map is one map for both halves.
		opts.Importer = goProg.Importer
		// A Go-only input lowers to itself (Sprint 152 D1): the generated
		// file keeps the source's package clause. Without it every unit
		// became `package main` with a synthesised main, so gc's optimizer
		// notes on the generated module named a function the original
		// never had (`can inline main`).
		opts.Package = goProg.Package
	}
	res, err := lower.Compile(file, opts)
	if err != nil {
		if el, ok := err.(lower.ErrorList); ok {
			for _, diag := range el {
				fmt.Fprintln(os.Stderr, formatDiagnostic(diag))
			}
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		return 2
	}

	entries := make([]mapEntry, 0, len(res.Mappings))
	for _, m := range res.Mappings {
		entry := mapEntry{
			GoLine:       m.GoLine,
			GoCol:        m.GoCol,
			SourceLine:   m.Pos.Line(),
			SourceCol:    m.Pos.Col(),
			SourceOffset: m.Pos.Offset(),
			Node:         m.Node,
		}
		// For Go input the program's position space spans every original
		// file, so a mapping is only actionable once it names WHICH file and
		// an offset into that file's own bytes. An unresolved position FAILS
		// the run: omitting source_file would publish a map whose entries
		// silently mean "some file", and a consumer cannot tell that apart
		// from shell input, which omits the field legitimately.
		if goProg != nil {
			name, off, ok := goProg.SourceAt(m.Pos)
			if !ok {
				fmt.Fprintf(os.Stderr,
					"transpile: %s: mapping at offset %d resolves to no original source file\n",
					origin, m.Pos.Offset())
				return 2
			}
			entry.SourceFile, entry.SourceFileOffset = name, off
		}
		entries = append(entries, entry)
	}

	goDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(res.Source))
	mapArt := sourceMapArtifact{
		SchemaVersion: sourceMapSchemaVersion,
		Origin:        res.Origin,
		GoDigest:      goDigest,
		Mappings:      entries,
	}
	if goProg != nil {
		mapArt.SourceKind = "go"
		mapArt.FrontEnd = goProg.FrontEnd
		for _, o := range goProg.Origins {
			mapArt.Sources = append(mapArt.Sources, mapSource{
				Name: o.Name, SHA256: o.SHA256, Base: o.Base, Size: o.Size,
			})
		}
	}
	mapData, err := json.MarshalIndent(mapArt, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "transpile: map marshal error: %v\n", err)
		return 2
	}

	return writeOutputsAtomic(output, res.Source, mapFile, mapData)
}

// dispatchTranspileLibrary emits a package as separate native Go files. The
// ordinary and external test packages must stay separate load units: merging
// them would flatten the external package and hide its import edge.
func dispatchTranspileLibrary(outDir string, goFiles, goTestFiles, goXTestFiles []string, base front.GoSourceOptions) int {
	units := [][]string{append(append([]string{}, goFiles...), goTestFiles...), goXTestFiles}
	programs := make([]*front.GoSourceProgram, 0, len(units))
	results := make([]*lower.Result, 0, len(units))
	seen := make(map[string]bool)
	packageName := ""
	var testedFiles []front.GoSourceFile
	for unitIndex, names := range units {
		if len(names) == 0 {
			continue
		}
		in, err := collectTranspileGoSource("", names)
		if err != nil {
			fmt.Fprintln(os.Stderr, transpileGoSourceDiagnostic(err))
			return 2
		}
		opts := base
		opts.Dir = in.Dir
		if unitIndex == 0 {
			testedFiles = in.Files
		} else if len(testedFiles) > 0 && base.ImportPath != "" {
			// cmd/go checks the external test package against ptest — the
			// tested package WITH its in-package test files, which export
			// helpers for exactly that use (go/types' util_test.go) — never
			// against the plain package the module importer would load from
			// the directory. The first unit is that variant; it is handed to
			// the xtest unit as an explicit package under the tested identity,
			// so every import of it (named or dot) resolves there first.
			opts.Packages = append(append([]front.GoSourcePackage(nil), opts.Packages...), front.GoSourcePackage{Path: base.ImportPath, Files: testedFiles})
		}
		prog, err := front.LoadGoSource(in, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, transpileGoSourceDiagnostic(err))
			return 2
		}
		if unitIndex == 0 {
			packageName = prog.Package
		} else if packageName == "" {
			// External-test-only package (e.g. cmd/internal/testdir): there is
			// no ordinary unit to name the tested package, so derive it the way
			// go/build does — the declared xtest name minus its _test suffix,
			// which must leave a non-empty identity behind.
			tested, ok := strings.CutSuffix(prog.Package, "_test")
			if !ok || tested == "" {
				fmt.Fprintf(os.Stderr, "transpile: --go-xtest-file package %q does not name a tested package (want <package>_test)\n", prog.Package)
				return 2
			}
			packageName = tested
		} else if prog.Package != packageName+"_test" {
			fmt.Fprintln(os.Stderr, "transpile: --go-xtest-file must form the external test package")
			return 2
		}
		res, err := lower.Compile(prog.File, lower.Options{
			Origin: in.Files[0].Name, Dir: in.Dir, Package: prog.Package,
			Importer: prog.Importer, Library: true,
		})
		if err != nil {
			if el, ok := err.(lower.ErrorList); ok {
				for _, diag := range el {
					fmt.Fprintln(os.Stderr, formatDiagnostic(diag))
				}
			} else {
				fmt.Fprintln(os.Stderr, err)
			}
			return 2
		}
		for _, generated := range res.Files {
			baseName := filepath.Base(generated.Name)
			if baseName == "." || baseName == string(filepath.Separator) || seen[baseName] {
				fmt.Fprintf(os.Stderr, "transpile: duplicate or unresolved library output basename: %s\n", generated.Name)
				return 2
			}
			seen[baseName] = true
		}
		programs = append(programs, prog)
		results = append(results, res)
	}

	// All validation happens before the first atomic write. This includes map
	// paths, so a duplicate input cannot leave a partially emitted overlay.
	for _, res := range results {
		for _, generated := range res.Files {
			output := filepath.Join(outDir, filepath.Base(generated.Name))
			if st, err := os.Stat(output); err == nil && st.IsDir() {
				fmt.Fprintf(os.Stderr, "transpile: output path is a directory: %s\n", output)
				return 2
			}
			mapPath := output + ".map"
			if st, err := os.Stat(mapPath); err == nil && st.IsDir() {
				fmt.Fprintf(os.Stderr, "transpile: map path is a directory: %s\n", mapPath)
				return 2
			}
		}
	}
	for i, res := range results {
		for _, generated := range res.Files {
			mapData, err := transpileLibraryMap(generated, programs[i])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			output := filepath.Join(outDir, filepath.Base(generated.Name))
			if code := writeOutputsAtomic(output, generated.Source, output+".map", mapData); code != 0 {
				return code
			}
			fmt.Printf("library %s -> %s\n", generated.Name, output)
		}
	}
	return 0
}

func transpileLibraryMap(generated lower.FileResult, prog *front.GoSourceProgram) ([]byte, error) {
	entries := make([]mapEntry, 0, len(generated.Mappings))
	for _, m := range generated.Mappings {
		name, offset, ok := prog.SourceAt(m.Pos)
		if !ok {
			return nil, fmt.Errorf("transpile: %s: mapping at offset %d resolves to no original source file", generated.Name, m.Pos.Offset())
		}
		entries = append(entries, mapEntry{GoLine: m.GoLine, GoCol: m.GoCol, SourceLine: m.Pos.Line(), SourceCol: m.Pos.Col(), SourceOffset: m.Pos.Offset(), Node: m.Node, SourceFile: name, SourceFileOffset: offset})
	}
	art := sourceMapArtifact{SchemaVersion: sourceMapSchemaVersion, Origin: generated.Name,
		GoDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(generated.Source)), Mappings: entries,
		SourceKind: "go", FrontEnd: prog.FrontEnd}
	for _, o := range prog.Origins {
		art.Sources = append(art.Sources, mapSource{Name: o.Name, SHA256: o.SHA256, Base: o.Base, Size: o.Size})
	}
	data, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("transpile: map marshal error: %v", err)
	}
	return data, nil
}

func writeOutputsAtomic(outputPath string, outputData []byte, mapPath string, mapData []byte) int {
	outDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
		return 2
	}
	mapDir := filepath.Dir(mapPath)
	if err := os.MkdirAll(mapDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
		return 2
	}

	tmpOut, err := os.CreateTemp(outDir, ".transpile-out-*.tmp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
		return 2
	}
	tmpOutName := tmpOut.Name()
	defer os.Remove(tmpOutName)

	if _, err := tmpOut.Write(outputData); err != nil {
		tmpOut.Close()
		fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
		return 2
	}
	if err := tmpOut.Sync(); err != nil {
		tmpOut.Close()
		fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
		return 2
	}
	if err := tmpOut.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
		return 2
	}

	tmpMap, err := os.CreateTemp(mapDir, ".transpile-map-*.tmp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
		return 2
	}
	tmpMapName := tmpMap.Name()
	defer os.Remove(tmpMapName)

	if _, err := tmpMap.Write(mapData); err != nil {
		tmpMap.Close()
		fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
		return 2
	}
	if err := tmpMap.Sync(); err != nil {
		tmpMap.Close()
		fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
		return 2
	}
	if err := tmpMap.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
		return 2
	}

	var origOutData []byte
	var origOutMode os.FileMode = 0644
	outExisted := false
	if st, err := os.Stat(outputPath); err == nil {
		if data, err := os.ReadFile(outputPath); err == nil {
			origOutData = data
			origOutMode = st.Mode().Perm()
			outExisted = true
		}
	}

	var origMapData []byte
	var origMapMode os.FileMode = 0644
	mapExisted := false
	if st, err := os.Stat(mapPath); err == nil {
		if data, err := os.ReadFile(mapPath); err == nil {
			origMapData = data
			origMapMode = st.Mode().Perm()
			mapExisted = true
		}
	}

	if err := os.Rename(tmpOutName, outputPath); err != nil {
		fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
		return 2
	}

	if err := os.Rename(tmpMapName, mapPath); err != nil {
		if outExisted {
			if rerr := os.WriteFile(outputPath, origOutData, origOutMode); rerr != nil {
				fmt.Fprintf(os.Stderr, "transpile: rollback failed restoring output file: %v\n", rerr)
			} else if cerr := os.Chmod(outputPath, origOutMode); cerr != nil {
				fmt.Fprintf(os.Stderr, "transpile: rollback failed setting permissions on output file: %v\n", cerr)
			}
		} else {
			if rerr := os.Remove(outputPath); rerr != nil && !os.IsNotExist(rerr) {
				fmt.Fprintf(os.Stderr, "transpile: rollback failed removing output file: %v\n", rerr)
			}
		}
		if mapExisted {
			if rerr := os.WriteFile(mapPath, origMapData, origMapMode); rerr != nil {
				fmt.Fprintf(os.Stderr, "transpile: rollback failed restoring map file: %v\n", rerr)
			} else if cerr := os.Chmod(mapPath, origMapMode); cerr != nil {
				fmt.Fprintf(os.Stderr, "transpile: rollback failed setting permissions on map file: %v\n", cerr)
			}
		}
		fmt.Fprintf(os.Stderr, "transpile: %v\n", err)
		return 2
	}

	return 0
}

// checkTranspileInputCollision refuses to write an output or map file over an
// input. isSameFileOrAlias resolves symlinks and compares inode identity, so a
// symlink or a hard link to an original is refused as the original.
func checkTranspileInputCollision(input, output, mapFile string) int {
	if isSameFileOrAlias(input, output) {
		fmt.Fprintln(os.Stderr, "transpile: input and output path cannot be the same file")
		return 2
	}
	if isSameFileOrAlias(input, mapFile) {
		fmt.Fprintln(os.Stderr, "transpile: input and map path cannot be the same file")
		return 2
	}
	return 0
}

// collectTranspileGoSource reads the original Go bytes for a transpile run.
// It only reads: the bytes handed to the front end are the upstream bytes.
func collectTranspileGoSource(input string, goFiles []string) (front.GoSourceInput, error) {
	res := front.GoSourceResolution{Enabled: true, Files: goFiles}
	operand := input
	var stdin io.Reader
	if operand == "-" || operand == "" {
		operand = ""
		if len(goFiles) == 0 {
			stdin = os.Stdin
		}
	}
	return front.CollectGoSources(res, operand, "", stdin)
}

// loadTranspileGoSource hands collected bytes to the sh front end.
//
// Entry calls are requested only for a package that HAS an entry point. A
// build-only obligation is a real one: `go build` compiles `package p; var X
// int` happily, so a corpus row whose phase is "build" must be able to
// transpile a non-main package too. Asking for RunMain unconditionally made
// the front end reject every such package with "Go execution requires package
// main with func main()", which would have turned a supported obligation into
// a skip.
//
// The main-ness of a package is a fact of the source, and Bashy does not parse
// Go: the front end reports it. So the load is done once WITHOUT entry calls —
// which is also exactly the artifact a non-main package needs — and repeated
// with them only when that first load reports package main and a main
// function. A main package therefore still emits a runnable artifact, and no
// entry call is ever synthesised here.
func loadTranspileGoSource(in front.GoSourceInput, base front.GoSourceOptions) (*syntax.File, *front.GoSourceProgram, error) {
	opts := base
	opts.RunMain, opts.Dir = false, in.Dir
	prog, err := front.LoadGoSource(in, opts)
	if err != nil {
		return nil, nil, err
	}
	// A native unit keeps the source's own main and init; only a flattened
	// program needs the synthetic entry calls RunMain appends.
	if prog.Package != "main" || prog.Main == "" || opts.PreserveNativeInit {
		return prog.File, prog, nil
	}
	opts.RunMain = true
	prog, err = front.LoadGoSource(in, opts)
	if err != nil {
		return nil, nil, err
	}
	return prog.File, prog, nil
}

// transpileGoSourceDiagnostic renders a Go-source error under this command's
// name. The front end's own positioned diagnostics pass through untouched so a
// differential harness can compare them against the Go toolchain byte for byte.
func transpileGoSourceDiagnostic(err error) string {
	msg := err.Error()
	if front.IsGoSourceError(err) {
		return "transpile: " + strings.TrimPrefix(msg, front.Prog+": ")
	}
	return msg
}
