// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package front

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// Sprint 118, W1 (Bashy half): explicit Go-source input dispatch.
//
// `bashy --bashpp --source=go original.go` runs an UNCHANGED Go program: the
// bytes are handed to the sh front end (mvdan.cc/sh/v3/gosource), which lexes,
// parses and type-checks them with Go's own rules and returns a positioned
// Bash++ AST. This file owns the selection, its refusals and the collection of
// input files. The standard Go lexer recognizes only the leading package
// marker; parsing and type checking remain entirely in the existing frontend.
//
// The load itself is reached through the [GoSourceLoad] hook, in the shape the
// package already uses for AgentOSDispatch: the pure `bash` drop-in leaves it
// nil (it structurally cannot link the AgentOS surface, and must not grow a Go
// type checker), and cmd/bashy wires it to internal/agentos. While the front
// end is absent the hook stays nil and every Go-source invocation fails with a
// Go-source diagnostic. It never falls back to the shell parser: that fallback
// is what made a malformed Go program look like a passing shell script.
//
// Design of record: docs/plan-source-go-dispatch.md.

// GoSourceFile is one original input file handed to the front end. Data is the
// file's exact bytes; nothing in this package rewrites, reformats, strips or
// appends to them.
type GoSourceFile struct {
	Name string
	Data []byte
}

// GoSourceOrigin describes where one original file landed in the loaded
// program's position space. It mirrors gosource.SourceInfo so a caller can map
// a position back to an original file without holding a live program.
type GoSourceOrigin struct {
	Name   string
	SHA256 string
	Base   uint
	Size   uint
}

// GoSourceOptions are the load-time knobs Bashy sets.
type GoSourceOptions struct {
	// RunMain requests the package's init functions and main entry calls.
	// Loading never executes anything either way; false is what makes
	// --check safe, since the resulting program carries no entry calls.
	RunMain bool
	// PreserveNativeInit keeps Go init declarations native. Library lowering
	// uses it because its output is compiled by the Go toolchain as a package.
	PreserveNativeInit bool
	// Dir is the directory the source came from, used to resolve module
	// imports. It is never the source identity — that stays the file name.
	Dir string
	// GoVersion selects checker language semantics without selecting an SDK.
	GoVersion string
	// TestBuiltins enables the go/types test checker environment, including
	// its predeclared assert and trace functions.
	TestBuiltins bool
	// CheckerBranchErrors leaves label/goto/break/continue errors to the type
	// checker (the types2/go-types test environment, which parses without
	// CheckBranches); default is gc's syntax-stage branch check.
	CheckerBranchErrors bool
	// CheckAfterSyntaxErrors type-checks the partial AST after a syntax
	// rejection and reports both (the check_test runners' environment);
	// default is gc's: a syntax rejection is the complete result.
	CheckAfterSyntaxErrors bool
	// Packages are explicit dependency packages (--go-package), in order.
	// They are the policy-free half of Go's import model — an in-memory
	// importcfg — consulted before the module importer for every import.
	Packages []GoSourcePackage
	// ImportBase is the compiler's -D (--go-import-base): a relative import
	// "./x" means ImportBase/x. Empty refuses relative imports.
	ImportBase string
	// ImportPath is the compiler's -p for the program package
	// (--go-import-path): the identity its resolutions are attributed to.
	ImportPath string
	// TestMain asserts that the program is cmd/go's generated test main
	// (--go-test-main): a FACT the caller passes, never inferred from a
	// ".test" suffix; it requires ImportPath.
	TestMain bool
}

// GoSourcePackage is one explicit dependency package: the import path it is
// registered under and its exact files. The path is an identity, never a
// directory.
type GoSourcePackage struct {
	Path  string
	Files []GoSourceFile
}

// GoSourceResolution is one recorded import resolution, as the front end
// reports it: deterministic for a given input, so it can be listed and
// audited the way `go list` output can.
type GoSourceImportResolution struct {
	From   string   `json:"from"`
	Import string   `json:"import"`
	Path   string   `json:"path"`
	Origin string   `json:"origin"`
	Name   string   `json:"name"`
	Files  []string `json:"files,omitempty"`
}

// GoSourceProgram is the loaded program: a positioned Bash++ AST plus the
// provenance needed to point a diagnostic or a source map back at the original
// bytes.
type GoSourceProgram struct {
	File          *syntax.File
	Package       string
	Main          string
	InitFunctions []string
	Origins       []GoSourceOrigin
	// FrontEnd is the front end's version string, recorded in evidence.
	FrontEnd string
	// Resolutions are every import the checker resolved, in resolution order.
	Resolutions []GoSourceImportResolution
	// Importer is the importer the front end checked the program with (the
	// explicit package map in front of the module importer), for lowering.
	Importer types.Importer
}

// SourceAt maps a position in the loaded program back to the original file and
// the byte offset within that file. It is the same interval arithmetic the sh
// front end applies to its own Sources, kept here so the transpile source map
// can be produced from Origins alone.
func (p *GoSourceProgram) SourceAt(pos syntax.Pos) (string, uint, bool) {
	if p == nil {
		return "", 0, false
	}
	for _, o := range p.Origins {
		if pos.Offset() >= o.Base && pos.Offset() <= o.Base+o.Size {
			return o.Name, pos.Offset() - o.Base, true
		}
	}
	return "", 0, false
}

// GoSourceLoad is the injection point for the sh Go front end. It is nil in
// the pure bash drop-in, which structurally cannot link internal/agentos;
// cmd/bashy sets it there, unconditionally. A nil hook is a refusal, never a
// shell fallback.
var GoSourceLoad func(files []GoSourceFile, opts GoSourceOptions) (*GoSourceProgram, error)

// GoSourcePackageFiles selects the Go files of a directory recipe using the Go
// toolchain's own rules (`//go:build`, `// +build`, _GOOS/_GOARCH suffixes,
// the host build context). It is wired from internal/agentos for the same
// reason as GoSourceLoad: go/build belongs to the AgentOS half, not to the
// pure drop-in. Nil means this build cannot accept a directory recipe.
var GoSourcePackageFiles func(dir string) ([]string, error)

// GoSourceModuleDir carries the SOURCE's module directory to the runner,
// separately from the runner's working directory.
//
// It is wired to sh's `interp.GoSourceModuleDir(dir)` (landed in sh de4ff069)
// from internal/agentos, alongside the other Go-source hooks. It exists
// because the two directories are genuinely different: the harness runs every
// mode from a fresh runtime directory holding only the declared assets, while
// the Go source it names lives, with its `go.mod`, somewhere else. The
// interpreter resolves a Bash++ import by running `go list`; without this
// option it does so in the runner's working directory, so a module import
// resolved only when the two happened to coincide.
//
// Bashy deliberately does NOT close that gap by moving the program's working
// directory to the source: that would resolve the import and break every
// relative asset path in the same stroke, turning a loud failure into a wrong
// answer. The hook takes the same directory the type-checking half already
// resolves modules against through lower.NewModuleImporter, so both halves
// agree by construction. A nil hook (no Go front end linked) leaves the
// interpreter's previous cwd-relative behaviour untouched.
var GoSourceModuleDir func(dir string) interp.RunnerOption

// ErrGoSourceUnavailable reports that this build has no Go front end linked.
var ErrGoSourceUnavailable = errors.New(
	"the Go source front end (mvdan.cc/sh/v3/gosource) is not available in this build")

// GoSourceLanguage names an input language for --source.
type GoSourceLanguage string

const (
	// GoSourceLangShell is the default: the input is shell (or Bash++) source.
	GoSourceLangShell GoSourceLanguage = "sh"
	// GoSourceLangGo selects unchanged Go source input.
	GoSourceLangGo GoSourceLanguage = "go"
)

// GoSourceSelection is the raw, unvalidated result of scanning the command
// line: what the user spelled, before it is checked against the resolved
// dialect and POSIX profile.
type GoSourceSelection struct {
	GoVersion        string
	GoVersionSeen    bool
	TestBuiltins     bool
	TestBuiltinsSeen bool
	// CheckerBranchErrors / CheckAfterSyntaxErrors record
	// --go-checker-branch-errors / --go-check-after-syntax-errors.
	CheckerBranchErrors        bool
	CheckerBranchErrorsSeen    bool
	CheckAfterSyntaxErrors     bool
	CheckAfterSyntaxErrorsSeen bool
	// Language is the last --source value seen, "" when the flag is absent.
	Language string
	// LanguageSeen distinguishes an absent --source from --source=sh.
	LanguageSeen bool
	// Check records --check.
	Check bool
	// Files records every --go-file, in the order given. Bashy does not sort
	// them; the front end owns file ordering.
	Files []string
	// Packages records every --go-package <path>=<file>[,<file>...], in the
	// order given; order is the dependency order the front end checks in.
	Packages []GoSourcePackageSpec
	// ImportBase records --go-import-base.
	ImportBase string
	// ImportPath records --go-import-path, the program package's own path
	// (the compiler's -p); empty attributes its resolutions to the package name.
	ImportPath string
	// TestMain records --go-test-main: the program is cmd/go's generated
	// test main. A fact, never inferred; requires ImportPath.
	TestMain bool
	// List records --go-list: check, then print every import resolution.
	List bool
	// DoubleDash records if -- was explicitly seen, separating arguments.
	DoubleDash bool
}

// GoSourcePackageSpec is one --go-package as spelled: an import path and the
// file names that make up the package.
type GoSourcePackageSpec struct {
	Path  string
	Files []string
}

// Requested reports whether any flag in this group was spelled at all.
func (s GoSourceSelection) Requested() bool {
	return s.LanguageSeen || s.GoVersionSeen || s.TestBuiltinsSeen || s.CheckerBranchErrorsSeen || s.CheckAfterSyntaxErrorsSeen || s.Check || s.List || len(s.Files) > 0 ||
		len(s.Packages) > 0 || s.ImportBase != "" || s.ImportPath != ""
}

// ParseGoSourcePackage splits one --go-package value into its path and files.
func ParseGoSourcePackage(value string) (GoSourcePackageSpec, error) {
	path, files, ok := strings.Cut(value, "=")
	if !ok || path == "" || files == "" {
		return GoSourcePackageSpec{}, Errorf("--go-package: want <importpath>=<file>[,<file>...], got %q", value)
	}
	spec := GoSourcePackageSpec{Path: path}
	for _, name := range strings.Split(files, ",") {
		if name == "" {
			return GoSourcePackageSpec{}, Errorf("--go-package: empty file name in %q", value)
		}
		spec.Files = append(spec.Files, name)
	}
	return spec, nil
}

// GoSourceContext is everything resolution needs beyond the raw selection.
type GoSourceContext struct {
	// Binary is the entry point resolving the selection.
	Binary BashPPBinary
	// BashPP is the resolved Bash++ state, not merely the --bashpp flag.
	BashPP bool
	// Posix is the resolved startup POSIX profile, from any of its spellings.
	Posix bool
	// HasOperand reports whether a file/directory operand was given.
	HasOperand bool
	// ShellOnlyMode names a selected mode that only exists for shell input
	// (--pretty-print, --dump-strings, --dump-po-strings). It is refused
	// rather than ignored: those modes are implemented by constructing the
	// SHELL parser, so accepting one would judge Go bytes by shell syntax —
	// the exact confusion --source=go exists to end.
	ShellOnlyMode string
}

// GoSourceResolution is the validated selection.
type GoSourceResolution struct {
	GoVersion              string
	TestBuiltins           bool
	CheckerBranchErrors    bool
	CheckAfterSyntaxErrors bool
	// Enabled reports that the input is Go source.
	Enabled bool
	// Check requests semantic validation with no execution.
	Check bool
	// Files are the explicit --go-file inputs, empty for operand/stdin input.
	Files []string
	// Packages, ImportBase and List carry the explicit package map and the
	// audit listing request; see GoSourceSelection.
	Packages   []GoSourcePackageSpec
	ImportBase string
	ImportPath string
	TestMain   bool
	List       bool
}

// goSourceError is a refusal that the caller reports on stderr and turns into
// exit status 2, bash's usage-error status. It may wrap a sentinel so a caller
// can test the reason without matching on text.
type goSourceError struct {
	msg string
	err error
}

func (e *goSourceError) Error() string { return e.msg }

func (e *goSourceError) Unwrap() error { return e.err }

// Prog is the program name every refusal is prefixed with ("bashy: …"). The
// bashy front door leaves the default; cmd/bashpp sets its own name.
var Prog = "bashy"

// Errorf makes a Go-source refusal (a usage error, exit status 2) prefixed
// with [Prog].
func Errorf(format string, args ...any) error {
	return &goSourceError{msg: Prog + ": " + fmt.Sprintf(format, args...)}
}

// IsGoSourceError reports whether err is a Go-source selection refusal.
func IsGoSourceError(err error) bool {
	var e *goSourceError
	return errors.As(err, &e)
}

// StripGoSourceInvocationFlags removes the Go-source selector flags from an
// argv-shaped slice and returns what they said. They are consumed here, before
// Go's flag package runs, for the same reason --bashpp is: they are invocation
// selectors, not shell options, and leaving them in argv would either make
// `flag` reject them or turn them into the script's positional arguments.
//
// Scanning follows CommandLineBashPP's rule exactly: stop at `--`, at `-c`
// (whose operand is a command string, not a further flag), or at the first
// operand that does not start with `-`.
func StripGoSourceInvocationFlags(args []string) ([]string, GoSourceSelection, error) {
	out := make([]string, 0, len(args))
	var sel GoSourceSelection
	options := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if i == 0 {
			out = append(out, arg)
			continue
		}
		if options && (arg == "-c" || arg == "--" || arg == "-" || !strings.HasPrefix(arg, "-")) {
			options = false
			if arg == "--" {
				sel.DoubleDash = true
			}
		}
		if !options {
			out = append(out, arg)
			continue
		}
		switch {
		case arg == "--go-test-builtins", arg == "--go-test-builtins=true":
			sel.TestBuiltins, sel.TestBuiltinsSeen = true, true
			continue
		case strings.HasPrefix(arg, "--go-test-builtins="):
			return nil, sel, Errorf("--go-test-builtins: expected true")
		case arg == "--go-checker-branch-errors", arg == "--go-checker-branch-errors=true":
			sel.CheckerBranchErrors, sel.CheckerBranchErrorsSeen = true, true
			continue
		case strings.HasPrefix(arg, "--go-checker-branch-errors="):
			return nil, sel, Errorf("--go-checker-branch-errors: expected true")
		case arg == "--go-check-after-syntax-errors", arg == "--go-check-after-syntax-errors=true":
			sel.CheckAfterSyntaxErrors, sel.CheckAfterSyntaxErrorsSeen = true, true
			continue
		case strings.HasPrefix(arg, "--go-check-after-syntax-errors="):
			return nil, sel, Errorf("--go-check-after-syntax-errors: expected true")
		case arg == "--go-version":
			value, ok := goSourceFlagValue(args, &i)
			if !ok || value == "" {
				return nil, sel, Errorf("--go-version: missing argument")
			}
			sel.GoVersion, sel.GoVersionSeen = value, true
			continue
		case strings.HasPrefix(arg, "--go-version="):
			sel.GoVersion, sel.GoVersionSeen = strings.TrimPrefix(arg, "--go-version="), true
			if sel.GoVersion == "" {
				return nil, sel, Errorf("--go-version: missing argument")
			}
			continue
		case arg == "--check":
			sel.Check = true
			continue
		case arg == "--source":
			value, ok := goSourceFlagValue(args, &i)
			if !ok {
				return nil, sel, Errorf("--source: missing argument")
			}
			sel.Language, sel.LanguageSeen = value, true
			continue
		case strings.HasPrefix(arg, "--source="):
			sel.Language, sel.LanguageSeen = strings.TrimPrefix(arg, "--source="), true
			continue
		case arg == "--go-file":
			value, ok := goSourceFlagValue(args, &i)
			if !ok {
				return nil, sel, Errorf("--go-file: missing argument")
			}
			sel.Files = append(sel.Files, value)
			continue
		case strings.HasPrefix(arg, "--go-file="):
			sel.Files = append(sel.Files, strings.TrimPrefix(arg, "--go-file="))
			continue
		case arg == "--go-package", strings.HasPrefix(arg, "--go-package="):
			value, ok := strings.CutPrefix(arg, "--go-package=")
			if !ok {
				if value, ok = goSourceFlagValue(args, &i); !ok {
					return nil, sel, Errorf("--go-package: missing argument")
				}
			}
			spec, err := ParseGoSourcePackage(value)
			if err != nil {
				return nil, sel, err
			}
			sel.Packages = append(sel.Packages, spec)
			continue
		case arg == "--go-import-base", strings.HasPrefix(arg, "--go-import-base="):
			value, ok := strings.CutPrefix(arg, "--go-import-base=")
			if !ok {
				if value, ok = goSourceFlagValue(args, &i); !ok {
					return nil, sel, Errorf("--go-import-base: missing argument")
				}
			}
			if value == "" {
				return nil, sel, Errorf("--go-import-base: missing argument")
			}
			sel.ImportBase = value
			continue
		case arg == "--go-import-path", strings.HasPrefix(arg, "--go-import-path="):
			value, ok := strings.CutPrefix(arg, "--go-import-path=")
			if !ok {
				if value, ok = goSourceFlagValue(args, &i); !ok {
					return nil, sel, Errorf("--go-import-path: missing argument")
				}
			}
			if value == "" {
				return nil, sel, Errorf("--go-import-path: missing argument")
			}
			sel.ImportPath = value
			continue
		case arg == "--go-test-main":
			sel.TestMain = true
			continue
		case arg == "--go-list":
			sel.List = true
			continue
		}
		out = append(out, arg)
		// Another option's value is not an operand, so the scan must step
		// over it and keep looking for selectors behind it.
		if InvocationFlagTakesValue(arg) && i+1 < len(args) {
			i++
			out = append(out, args[i])
		}
	}
	return out, sel, nil
}

// InvocationFlagTakesValue reports whether an invocation option consumes the
// following argv token.
//
// Every scanner that walks the option prefix of argv — CommandLineBashPP,
// stripBashPPInvocationFlags and StripGoSourceInvocationFlags — must agree on
// this set, because each of them stops at the first token that does not look
// like an option. When one scanner does not know that `--source` takes a
// value, it reads the value (`go`) as the script operand and stops, so every
// selector spelled after it is silently dropped: `bashy --source go --bashpp
// x.go` used to die with "flag provided but not defined: -bashpp". Sharing
// the set is what makes the separated and `=`-joined spellings equivalent in
// either order.
func InvocationFlagTakesValue(arg string) bool {
	switch arg {
	case "-o", "-O", "--rcfile", "--init-file", "-bashy-plus-o", "-bashy-plus-O",
		"--source", "--go-file", "--go-version", "--go-package", "--go-import-base", "--go-import-path":
		return true
	}
	return false
}

// goSourceFlagValue consumes the separated value of a `--flag value` pair.
func goSourceFlagValue(args []string, i *int) (string, bool) {
	if *i+1 >= len(args) {
		return "", false
	}
	*i++
	return args[*i], true
}

// ResolveGoSource validates a raw selection against the resolved dialect and
// POSIX profile. Every refusal is deliberate and none of them degrade into
// shell dispatch.
//
// Go-source input lives strictly inside Bash++: Classic and POSIX activation
// semantics are untouched by this feature, so an invocation that does not
// spell --source=go reaches byte-identical code. POSIX mode is refused rather
// than ignored because --posix is a conformance selection — Sprint 114 made
// the bash drop-in's Bash++/POSIX combination inert precisely so a POSIX claim
// cannot be affected by an extension, and accepting Go input under it would
// reopen that. The bash drop-in refuses outright: cmd/bash cannot link the
// AgentOS hook that carries the front end, and must not grow a Go type checker.
func ResolveGoSource(sel GoSourceSelection, ctx GoSourceContext) (GoSourceResolution, error) {
	lang := GoSourceLangShell
	if sel.LanguageSeen {
		switch GoSourceLanguage(sel.Language) {
		case GoSourceLangShell, GoSourceLangGo:
			lang = GoSourceLanguage(sel.Language)
		default:
			return GoSourceResolution{}, Errorf(
				"--source: unknown input language %q (expected \"sh\" or \"go\")", sel.Language)
		}
	}
	if lang != GoSourceLangGo {
		if sel.TestBuiltinsSeen {
			return GoSourceResolution{}, Errorf("--go-test-builtins requires --source=go")
		}
		if sel.CheckerBranchErrorsSeen {
			return GoSourceResolution{}, Errorf("--go-checker-branch-errors requires --source=go")
		}
		if sel.CheckAfterSyntaxErrorsSeen {
			return GoSourceResolution{}, Errorf("--go-check-after-syntax-errors requires --source=go")
		}
		if sel.GoVersionSeen {
			return GoSourceResolution{}, Errorf("--go-version requires --source=go")
		}
		if sel.Check {
			return GoSourceResolution{}, Errorf("--check requires --source=go")
		}
		if len(sel.Files) > 0 {
			return GoSourceResolution{}, Errorf("--go-file requires --source=go")
		}
		if len(sel.Packages) > 0 {
			return GoSourceResolution{}, Errorf("--go-package requires --source=go")
		}
		if sel.ImportBase != "" {
			return GoSourceResolution{}, Errorf("--go-import-base requires --source=go")
		}
		if sel.ImportPath != "" {
			return GoSourceResolution{}, Errorf("--go-import-path requires --source=go")
		}
		if sel.TestMain {
			return GoSourceResolution{}, Errorf("--go-test-main requires --source=go")
		}
		if sel.List {
			return GoSourceResolution{}, Errorf("--go-list requires --source=go")
		}
		return GoSourceResolution{}, nil
	}
	// POSIX is checked before Bash++ so the conformance refusal is the one
	// reported: on the bash drop-in, --posix already forces the Bash++
	// selector inert, and "requires --bashpp" would be a misleading answer to
	// an invocation that spelled it.
	if ctx.Posix {
		return GoSourceResolution{}, Errorf("--source=go is not available in POSIX mode")
	}
	if !ctx.BashPP {
		return GoSourceResolution{}, Errorf("--source=go requires --bashpp")
	}
	if ctx.Binary != BashPPBinaryBashy {
		return GoSourceResolution{}, Errorf("--source=go requires the bashy front door")
	}
	if len(sel.Files) > 0 && ctx.HasOperand && !sel.DoubleDash {
		return GoSourceResolution{}, Errorf(
			"--go-file cannot be combined with a file operand")
	}
	if ctx.ShellOnlyMode != "" {
		return GoSourceResolution{}, Errorf(
			"%s cannot be combined with --source=go", ctx.ShellOnlyMode)
	}
	// The explicit package map is linked at lowering time: gosource lowers
	// every mapped package from its exact files into the one program the
	// interpreter runs, so the runtime never resolves a mapped path on disk
	// (S151.1). --go-list is itself a check.
	if sel.TestMain && sel.ImportPath == "" {
		return GoSourceResolution{}, Errorf("--go-test-main asserts the identity of the program and requires --go-import-path")
	}
	return GoSourceResolution{Enabled: true, Check: sel.Check || sel.List, Files: sel.Files, GoVersion: sel.GoVersion,
		TestBuiltins: sel.TestBuiltins, CheckerBranchErrors: sel.CheckerBranchErrors, CheckAfterSyntaxErrors: sel.CheckAfterSyntaxErrors,
		Packages: sel.Packages, ImportBase: sel.ImportBase, ImportPath: sel.ImportPath, TestMain: sel.TestMain, List: sel.List}, nil
}

// ReadGoSourcePackages reads the exact bytes of every --go-package file. It
// reads; it never edits or reorders.
func ReadGoSourcePackages(specs []GoSourcePackageSpec) ([]GoSourcePackage, error) {
	out := make([]GoSourcePackage, 0, len(specs))
	for _, spec := range specs {
		pkg := GoSourcePackage{Path: spec.Path}
		for _, name := range spec.Files {
			data, err := os.ReadFile(name)
			if err != nil {
				return nil, err
			}
			pkg.Files = append(pkg.Files, GoSourceFile{Name: name, Data: data})
		}
		out = append(out, pkg)
	}
	return out, nil
}

// WriteGoSourceList prints the recorded import resolutions, one JSON object
// per line, in resolution order. The output is a pure function of the inputs
// — no timestamps, no host paths beyond the names the caller gave — so two
// runs over the same files print identical bytes and can be diffed.
func WriteGoSourceList(w io.Writer, resolutions []GoSourceImportResolution) error {
	enc := json.NewEncoder(w)
	for _, r := range resolutions {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

// GoSourceInput is a collected set of original files plus the directory used
// to resolve module imports.
type GoSourceInput struct {
	Files []GoSourceFile
	Dir   string
}

// CollectGoSources reads the original bytes for one invocation. It reads; it
// never edits. Callers pass exactly one of: explicit --go-file names, a file or
// directory operand, a command string, or a reader for stdin.
func CollectGoSources(res GoSourceResolution, operand, command string, stdin io.Reader) (GoSourceInput, error) {
	switch {
	case len(res.Files) > 0:
		return readGoSourceFiles(res.Files)
	case command != "":
		data, err := io.ReadAll(strings.NewReader(command))
		if err != nil {
			return GoSourceInput{}, err
		}
		return GoSourceInput{Files: []GoSourceFile{{Name: "-c", Data: data}}, Dir: goSourceWorkingDir()}, nil
	case operand != "" && operand != "-":
		info, err := os.Stat(operand)
		if err != nil {
			return GoSourceInput{}, err
		}
		if info.IsDir() {
			names, err := goSourcePackageFiles(operand)
			if err != nil {
				return GoSourceInput{}, err
			}
			in, err := readGoSourceFiles(names)
			if err != nil {
				return GoSourceInput{}, err
			}
			in.Dir = operand
			return in, nil
		}
		return readGoSourceFiles([]string{operand})
	default:
		if stdin == nil {
			return GoSourceInput{}, Errorf("--source=go: no input")
		}
		data, err := io.ReadAll(stdin)
		if err != nil {
			return GoSourceInput{}, err
		}
		return GoSourceInput{Files: []GoSourceFile{{Name: "-", Data: data}}, Dir: goSourceWorkingDir()}, nil
	}
}

func goSourceWorkingDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

func readGoSourceFiles(names []string) (GoSourceInput, error) {
	in := GoSourceInput{Files: make([]GoSourceFile, 0, len(names))}
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			return GoSourceInput{}, err
		}
		in.Files = append(in.Files, GoSourceFile{Name: name, Data: data})
	}
	if len(in.Files) > 0 {
		in.Dir = filepath.Dir(in.Files[0].Name)
	}
	return in, nil
}

// goSourcePackageFiles selects a directory recipe's Go files through the
// [GoSourcePackageFiles] hook, i.e. through go/build in internal/agentos.
//
// The selection is the Go toolchain's own: `//go:build` lines, `// +build`
// comments and _GOOS/_GOARCH filename suffixes all apply, against the host
// build context. That is required for `bashy --bashpp --source=go ./pkg` to
// mean what `go build ./pkg` means on the same host — a package carrying
// main_darwin.go and main_linux.go builds natively either way, and collecting
// both would reject it as a duplicate main.
//
// Explicit --go-file recipes deliberately keep their bypass: a caller that
// names the files has already made the selection (the Tour's OMIT programs
// are passed that way by their own oracle).
func goSourcePackageFiles(dir string) ([]string, error) {
	if GoSourcePackageFiles == nil {
		return nil, &goSourceError{
			msg: Prog + ": --source=go: " + ErrGoSourceUnavailable.Error(),
			err: ErrGoSourceUnavailable,
		}
	}
	names, err := GoSourcePackageFiles(dir)
	if err != nil {
		if IsGoSourceError(err) {
			return nil, err
		}
		return nil, Errorf("--source=go: %v", err)
	}
	if len(names) == 0 {
		return nil, Errorf("--source=go: no Go source files in %s", dir)
	}
	return names, nil
}

// LoadGoSource reaches the front end. A nil hook is reported as a Go-source
// diagnostic, so an unsupported build refuses rather than silently handing the
// bytes to the shell parser.
func LoadGoSource(in GoSourceInput, opts GoSourceOptions) (*GoSourceProgram, error) {
	if GoSourceLoad == nil {
		return nil, &goSourceError{
			msg: Prog + ": --source=go: " + ErrGoSourceUnavailable.Error(),
			err: ErrGoSourceUnavailable,
		}
	}
	if len(in.Files) == 0 {
		return nil, Errorf("--source=go: no input")
	}
	if opts.Dir == "" {
		opts.Dir = in.Dir
	}
	return GoSourceLoad(in.Files, opts)
}
