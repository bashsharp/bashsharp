// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package cli

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/front"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceFailurePrefixesOnlyBareErrors(t *testing.T) {
	// A refusal already carries the shell's name; sh's positioned diagnostics
	// must survive verbatim so their file:line:col still points at Go source.
	if got := goSourceDiagnosticText(front.Errorf("--check requires --source=go")); got != "bashy: --check requires --source=go" {
		t.Errorf("refusal text = %q", got)
	}
	if got := goSourceDiagnosticText(errors.New("hello.go:1:1: expected 'package'")); got != "bashy: hello.go:1:1: expected 'package'" {
		t.Errorf("diagnostic text = %q", got)
	}
}

// TestRunGoSourceInvocationRefusesWithoutFrontEnd exercises the whole
// invocation: an absent front end ends it with exit 2 and a diagnostic. This is
// what `bashy --bashpp --source=go prog.go` does in the default build today.
func TestRunGoSourceInvocationRefusesWithoutFrontEnd(t *testing.T) {
	path := writeGoFixture(t, "package main\n\nfunc main() {}\n")
	withGoSourceSelection(t, front.GoSourceResolution{Enabled: true, Files: []string{path}})
	withGoSourceHook(t, nil)

	var err error
	stderr := captureStderr(t, func() { err = runGoSourceInvocation() })
	if err == nil {
		t.Fatal("want a failure, got nil")
	}
	if got := exitStatusOf(t, err); got != 2 {
		t.Errorf("exit = %d, want 2", got)
	}
	if !strings.Contains(stderr, "not available in this build") {
		t.Errorf("stderr = %q", stderr)
	}
}

// TestRunGoSourceInvocationMalformedStaysGo is the sprint contract's item 6 at
// the invocation level: bytes that a shell would happily run are reported with
// the front end's Go diagnostic and nothing else.
func TestRunGoSourceInvocationMalformedStaysGo(t *testing.T) {
	const body = "package main\n\nif true; then echo shell-ran; fi\n"
	path := writeGoFixture(t, body)
	withGoSourceSelection(t, front.GoSourceResolution{Enabled: true, Files: []string{path}})
	const diagnostic = "prog.go:3:1: syntax error: non-declaration statement outside function body"
	withGoSourceHook(t, func(files []front.GoSourceFile, _ front.GoSourceOptions) (*front.GoSourceProgram, error) {
		if len(files) != 1 || string(files[0].Data) != body {
			t.Errorf("front end saw %+v, want the original bytes", files)
		}
		return nil, errors.New(diagnostic)
	})

	var err error
	stderr := captureStderr(t, func() { err = runGoSourceInvocation() })
	if got := exitStatusOf(t, err); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
	// Verbatim: a differential harness compares this against the Go toolchain.
	if strings.TrimSpace(stderr) != diagnostic {
		t.Errorf("stderr = %q, want %q verbatim", stderr, diagnostic)
	}
	if strings.Contains(stderr, "shell-ran") || strings.Contains(stderr, "command not found") {
		t.Errorf("shell dispatch leaked into a --source=go failure: %q", stderr)
	}
}

// TestRunGoSourceCheckExecutesNothing pins --check: the program is loaded
// WITHOUT entry calls and is never handed to a runner.
func TestRunGoSourceCheckExecutesNothing(t *testing.T) {
	path := writeGoFixture(t, "package main\n\nfunc main() { println(\"ran\") }\n")
	withGoSourceSelection(t, front.GoSourceResolution{Enabled: true, Check: true, Files: []string{path}})

	var sawRunMain bool
	var calls int
	withGoSourceHook(t, func(_ []front.GoSourceFile, opts front.GoSourceOptions) (*front.GoSourceProgram, error) {
		calls++
		sawRunMain = opts.RunMain
		// A program with statements: if --check ever ran it, they would fire.
		file, perr := syntax.NewParser().Parse(strings.NewReader("echo ran\n"), path)
		if perr != nil {
			t.Fatal(perr)
		}
		return &front.GoSourceProgram{File: file, Package: "main", Main: "main"}, nil
	})

	var err error
	stderr := captureStderr(t, func() { err = runGoSourceInvocation() })
	if err != nil {
		t.Fatalf("--check failed: %v (stderr %q)", err, stderr)
	}
	if calls != 1 {
		t.Errorf("front end called %d times, want 1", calls)
	}
	if sawRunMain {
		t.Error("--check must load with RunMain=false so the program carries no entry calls")
	}
}

// TestInvocationScannersAgreeOnValueFlags is review finding 5: the argv
// scanners each stop at the first token that does not look like an option, so
// one of them not knowing that `--source` takes a value made it read `go` as
// the script operand and drop every selector spelled behind it. The observed
// failure was `bashy --source go --bashpp x.go` exiting 2 with "flag provided
// but not defined: -bashpp".
func TestInvocationScannersAgreeOnValueFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantArgs []string
		wantLang string
		wantPP   bool
	}{
		{
			name:     "source before bashpp, separated",
			args:     []string{"bashy", "--source", "go", "--bashpp", "x.go"},
			wantArgs: []string{"bashy", "x.go"},
			wantLang: "go",
			wantPP:   true,
		},
		{
			name:     "source after bashpp, separated",
			args:     []string{"bashy", "--bashpp", "--source", "go", "x.go"},
			wantArgs: []string{"bashy", "x.go"},
			wantLang: "go",
			wantPP:   true,
		},
		{
			name:     "source before bashpp, joined",
			args:     []string{"bashy", "--source=go", "--bashpp", "x.go"},
			wantArgs: []string{"bashy", "x.go"},
			wantLang: "go",
			wantPP:   true,
		},
		{
			name:     "go-file value does not end the option scan",
			args:     []string{"bashy", "--go-file", "a.go", "--bashpp", "--source=go"},
			wantArgs: []string{"bashy"},
			wantLang: "go",
			wantPP:   true,
		},
		{
			name:     "another option's value does not end the scan either",
			args:     []string{"bashy", "--rcfile", "rc", "--source", "go", "--bashpp", "x.go"},
			wantArgs: []string{"bashy", "--rcfile", "rc", "x.go"},
			wantLang: "go",
			wantPP:   true,
		},
		{
			name:     "arguments after the operand are untouched",
			args:     []string{"bashy", "--source", "go", "--bashpp", "x.go", "--source", "sh", "-c", "arg"},
			wantArgs: []string{"bashy", "x.go", "--source", "sh", "-c", "arg"},
			wantLang: "go",
			wantPP:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Main's order: Go-source selectors first, then -c relocation and
			// the Bash++ strip.
			stripped, sel, err := front.StripGoSourceInvocationFlags(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			stripped = stripBashPPInvocationFlags(relocatePendingCommandFlag(stripped))
			if !slices.Equal(stripped, tc.wantArgs) {
				t.Errorf("argv = %q, want %q", stripped, tc.wantArgs)
			}
			if sel.Language != tc.wantLang {
				t.Errorf("--source = %q, want %q", sel.Language, tc.wantLang)
			}
			// front.ResolveBashPP reads the ORIGINAL argv, before any stripping, so
			// its scanner must step over the same values.
			enabled, seen := front.CommandLineBashPP(tc.args)
			if enabled != tc.wantPP || !seen {
				t.Errorf("front.CommandLineBashPP = (%v,%v), want (%v,true)", enabled, seen, tc.wantPP)
			}
		})
	}
}

// TestStartupShellOnlyModeReportsSelection pins the mapping from the parsed
// flags to the refusal, so the refusal cannot silently stop firing when one of
// these modes is reached by a different spelling.
func TestStartupShellOnlyModeReportsSelection(t *testing.T) {
	for _, tc := range []struct {
		flag *bool
		want string
	}{
		{pretty, "--pretty-print"},
		{dumpStrs, "--dump-strings"},
		{dumpShort, "--dump-strings"},
		{dumpPO, "--dump-po-strings"},
	} {
		previous := *tc.flag
		*tc.flag = true
		got := startupShellOnlyMode()
		*tc.flag = previous
		if got != tc.want {
			t.Errorf("startupShellOnlyMode() = %q, want %q", got, tc.want)
		}
	}
	if got := startupShellOnlyMode(); got != "" {
		t.Errorf("startupShellOnlyMode() = %q with no mode selected, want \"\"", got)
	}
}

// TestRunGoSourceInvocationSkipsLogout is review finding 8: a Go program's
// semantics are Go's. A --login invocation must not append shell effects from
// ~/.bash_logout after main returns.
func TestRunGoSourceInvocationSkipsLogout(t *testing.T) {
	home := t.TempDir()
	marker := filepath.Join(home, "logout-ran")
	logout := "printf x > " + marker + "\n"
	if err := os.WriteFile(filepath.Join(home, ".bash_logout"), []byte(logout), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	path := writeGoFixture(t, "package main\n\nfunc main() {}\n")
	withGoSourceSelection(t, front.GoSourceResolution{Enabled: true, Files: []string{path}})
	withGoSourceHook(t, func(_ []front.GoSourceFile, _ front.GoSourceOptions) (*front.GoSourceProgram, error) {
		file, perr := syntax.NewParser().Parse(strings.NewReader(":\n"), path)
		if perr != nil {
			t.Fatal(perr)
		}
		return &front.GoSourceProgram{File: file, Package: "main", Main: "main"}, nil
	})
	previousLogin := *login
	*login = true
	t.Cleanup(func() { *login = previousLogin })

	if err := runGoSourceInvocation(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("~/.bash_logout ran after a --source=go program")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}

	// Positive control: the same fixture, run through the shell path's logout
	// wrapper, DOES source ~/.bash_logout. Without this the assertion above
	// would keep passing if the hook simply stopped working.
	r, err := newRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runWithLoginLogout(r, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control: the shell path did not run ~/.bash_logout: %v", err)
	}
}

// TestRunGoSourceInvocationParameters pins the CLI's half of argument
// handling: the operand is the program (it is $0, not $1), the selectors never
// become positional parameters, and the arguments after the operand are passed
// through in order. Evaluating os.Args inside the program is the interpreter's
// half and is measured separately.
func TestRunGoSourceInvocationParameters(t *testing.T) {
	path := writeGoFixture(t, "package main\n\nfunc main() {}\n")
	withGoSourceSelection(t, front.GoSourceResolution{Enabled: true})
	withGoSourceHook(t, func(_ []front.GoSourceFile, _ front.GoSourceOptions) (*front.GoSourceProgram, error) {
		// A shell fixture standing in for the lowered program: it reports
		// what the runner was actually given.
		file, perr := syntax.NewParser().Parse(strings.NewReader("printf '%s|%s\\n' \"$0\" \"$*\"\n"), path)
		if perr != nil {
			t.Fatal(perr)
		}
		return &front.GoSourceProgram{File: file, Package: "main", Main: "main"}, nil
	})

	previousArgs := os.Args
	os.Args = []string{"bashy", path, "one", "two"}
	t.Cleanup(func() { os.Args = previousArgs })
	if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flag.CommandLine.Parse(nil) })

	stdout, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	previousStdout := os.Stdout
	os.Stdout = stdout
	runErr := runGoSourceInvocation()
	os.Stdout = previousStdout
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	want := path + "|one two\n"
	if string(data) != want {
		t.Errorf("runner saw %q, want %q", data, want)
	}
}

// TestRunGoSourceInvocationPassesModuleDir pins the seam the sh worker is
// landing as interp.GoSourceModuleDir: when it exists, the SOURCE's directory
// is handed to the runner, and the program's working directory is left alone.
func TestRunGoSourceInvocationPassesModuleDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prog.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	previousWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(elsewhere); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousWd) })

	withGoSourceSelection(t, front.GoSourceResolution{Enabled: true, Files: []string{path}})
	withGoSourceHook(t, func(_ []front.GoSourceFile, opts front.GoSourceOptions) (*front.GoSourceProgram, error) {
		if opts.Dir != dir {
			t.Errorf("load Dir = %q, want the SOURCE's directory %q", opts.Dir, dir)
		}
		file, perr := syntax.NewParser().Parse(strings.NewReader(":\n"), path)
		if perr != nil {
			t.Fatal(perr)
		}
		return &front.GoSourceProgram{File: file, Package: "main", Main: "main"}, nil
	})

	var seen string
	previousHook := front.GoSourceModuleDir
	front.GoSourceModuleDir = func(d string) interp.RunnerOption {
		seen = d
		return func(*interp.Runner) error { return nil }
	}
	t.Cleanup(func() { front.GoSourceModuleDir = previousHook })

	if err := runGoSourceInvocation(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if seen != dir {
		t.Errorf("module dir = %q, want %q", seen, dir)
	}
	// The working directory is NOT moved to the source: the harness runs from
	// an assets-only runtime directory and must keep it.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// macOS reports /private/var for /var, so compare resolved paths.
	wantWd, err := filepath.EvalSymlinks(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if gotWd, err := filepath.EvalSymlinks(wd); err != nil {
		t.Fatal(err)
	} else if gotWd != wantWd {
		t.Errorf("working directory moved to %q, want %q", gotWd, wantWd)
	}
}

// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information
// captureStderr runs fn with os.Stderr redirected and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	var sb strings.Builder
	if _, err := io.Copy(&sb, r); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}
func withGoSourceSelection(t *testing.T, res front.GoSourceResolution) {
	t.Helper()
	previous := startupGoSource
	startupGoSource = res
	t.Cleanup(func() { startupGoSource = previous })
}

func writeGoFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prog.go")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func withGoSourceHook(t *testing.T, hook func([]front.GoSourceFile, front.GoSourceOptions) (*front.GoSourceProgram, error)) {
	t.Helper()
	previous := front.GoSourceLoad
	front.GoSourceLoad = hook
	t.Cleanup(func() { front.GoSourceLoad = previous })
}

func exitStatusOf(t *testing.T, err error) int {
	t.Helper()
	var es interp.ExitStatus
	if !errors.As(err, &es) {
		t.Fatalf("want an exit status, got %v", err)
	}
	return int(es)
}
