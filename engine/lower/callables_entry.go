package lower

import "strings"

// programEntrySource emits a hygienic private entry and the process main adapter.
// The compiler owns declaration-name collision checks and imports. A mixed unit
// imports e.prefix+"shellexec"; a typed-only unit never imports that backend.
func (e *emitter) programEntrySource(body string, mixedShell bool) string {
	return e.programEntrySourceNamed(body, mixedShell, e.prefix+"execute")
}

// programEntrySourceNamed implements an explicitly named injectable entry.
// The caller validates that an opt-in public name is an available exported Go
// identifier. The default private name is hygienic within the source unit.
func (e *emitter) programEntrySourceNamed(body string, mixedShell bool, entryName string) string {
	rt, p := e.prefix+"rt.", e.prefix+"program"
	entryErr := e.prefix + "entryErr"
	var defaults []string
	// Session options override these per invocation. Do not use the support
	// package's legacy mutable process-wide output handles as entry defaults.
	defaults = append(defaults, rt+"WithStdio("+e.prefix+"os.Stdin,"+e.prefix+"os.Stdout,"+e.prefix+"os.Stderr)")
	// The process operands are the program's positional parameters, one
	// argument per parameter and byte for byte. It is a default like the
	// stdio triple: it is appended before the caller's opts, so an embedder
	// passing rt.WithParams(...) replaces it instead of being replaced by it.
	// os.Args is only read; WithParams copies what it is given.
	defaults = append(defaults, rt+"WithParams("+e.prefix+"os.Args[1:]...)")
	if mixedShell {
		factory := e.prefix + "shellexec.New(" + e.prefix + "shellexec.BashPP()"
		if e.seedsForeignImports() {
			factory += "," + e.foreignImportSeed()
		}
		defaults = append(defaults, rt+"WithShellFactory("+factory+"))")
	}
	entryCall := entryName + "()"
	mainCleanup := ""
	if cleanup := e.foreignMainCleanup(); cleanup != "" {
		// Keep cleanup outside entryName: an importable library may call its
		// entry repeatedly or concurrently, while the generated process main
		// owns the package-level modules. The wrapper also runs its defer before
		// main can use os.Exit, which otherwise skips deferred cleanup.
		mainEntry := e.prefix + "mainEntry"
		entryCall = mainEntry + "()"
		mainCleanup = "func " + mainEntry + "() (status int, err " + e.foreignErrorType() + ") {\n" +
			"defer func() {\n" + cleanup + "}()\n" +
			"return " + entryName + "()\n}\n"
	}
	return "// " + entryName + " runs a fresh runtime instance. Returned errors are not printed by the entry.\n" +
		"func " + entryName + "(opts ..." + rt + "SessionOption) (status int, " + entryErr + " " + e.foreignErrorType() + ") {\n" +
		"defaults := []" + rt + "SessionOption{" + strings.Join(defaults, ",") + "}\n" +
		p + ", " + entryErr + " := " + rt + "NewProgram(append(defaults,opts...)...)\n" +
		"if " + entryErr + " != nil { return " + rt + "ExitCode(" + entryErr + ")," + entryErr + " }\n" +
		entryErr + " = " + p + ".Run(func(" + p + " *" + rt + "Program) {\n" + body + "\n})\n" +
		"if " + entryErr + " != nil { " + entryErr + " = " + rt + "SourceFailure(" + entryErr + "); return " + rt + "ExitCode(" + entryErr + ")," + entryErr + " }\n" +
		"return " + p + ".Status(),nil\n}\n" + mainCleanup +
		"func main() {\nstatus,err := " + entryCall + "\n" +
		"if err != nil { " + e.prefix + "fmt.Fprintln(" + e.prefix + "os.Stderr,err) }\n" +
		"if status != 0 { " + e.prefix + "os.Exit(status) }\n}\n"
}
