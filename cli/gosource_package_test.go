package cli

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/front"

	"github.com/qiangli/bash/gosource"
)

func TestSprint198PackageToken(t *testing.T) {
	for _, tc := range []struct {
		source   string
		selected bool
	}{
		{"package main", true}, {" \t\n//go:build linux\n/* unchanged */\npackage main", true},
		{"//line original.go:12\npackage", true}, {"package ! invalid", true},
		{"packaged main", false}, {"\"package\" main", false}, {"command package main", false},
		{"#!/bin/bash\npackage main", false}, {"# shell comment\npackage main", false},
		{"echo first\npackage main", false}, {"/* unterminated package", false},
	} {
		if got := leadingGoPackage([]byte(tc.source)); got != tc.selected {
			t.Errorf("%q: selected=%v", tc.source, got)
		}
	}
}

func TestSprint198PackageInputCollection(t *testing.T) {
	const source = "//go:build linux\n/* prefix */\npackage main\nfunc main(){ x:=`$literal`; x=\"$unchanged\"; _=x }\n"
	for _, form := range []string{"command", "file", "stdin"} {
		t.Run(form, func(t *testing.T) {
			operand, cmd := "", ""
			var stdin io.Reader
			switch form {
			case "command":
				cmd = source
			case "file":
				operand = filepath.Join(t.TempDir(), "source.any-extension")
				if err := os.WriteFile(operand, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
			case "stdin":
				stdin = strings.NewReader(source)
			}
			in, _, selected, err := collectPackageGoSource(operand, cmd, stdin)
			if err != nil || !selected || len(in.Files) != 1 || string(in.Files[0].Data) != source {
				t.Fatalf("selected=%v input=%+v err=%v", selected, in, err)
			}
		})
	}
	const shell = "echo '$literal'\necho second\n"
	reader := strings.NewReader(shell)
	_, replay, selected, err := collectPackageGoSource("", "", reader)
	if consumed := len(shell) - reader.Len(); consumed > len("echo ") {
		t.Fatalf("selector read beyond first shell token: %d bytes", consumed)
	}
	data, readErr := io.ReadAll(replay)
	if err != nil || readErr != nil || selected || string(data) != shell {
		t.Fatalf("shell replay %q selected=%v errors=%v/%v", data, selected, err, readErr)
	}
}

func TestSprint198PackageBeforeShellPreflight(t *testing.T) {
	for _, form := range []string{"command", "file", "stdin", "stdin-args", "command-posix", "command-bash"} {
		for _, source := range []string{
			"//line original.go:10\n/* prefix */\npackage main\nfunc main(){x:=`$literal`;x=\"$unchanged\";_=x}\n",
			"package ! malformed; echo shell-must-not-run\n",
		} {
			t.Run(form+"/"+source, func(t *testing.T) {
				oldArgs, oldOriginal := os.Args, originalArgs
				oldCommand, oldRead, oldForce := *command, *readStdin, *forceI
				oldSel, oldRes, oldErr := startupGoSourceSel, startupGoSource, startupGoSourceErr
				oldBashPP, oldDefault := startupBashPP, AgentOSBashPPDefault
				oldStdin := os.Stdin
				t.Cleanup(func() {
					os.Args, originalArgs = oldArgs, oldOriginal
					*command, *readStdin, *forceI = oldCommand, oldRead, oldForce
					startupGoSourceSel, startupGoSource, startupGoSourceErr = oldSel, oldRes, oldErr
					startupBashPP, AgentOSBashPPDefault = oldBashPP, oldDefault
					os.Stdin = oldStdin
					_ = flag.CommandLine.Parse(nil)
				})
				AgentOSBashPPDefault = true
				startupGoSourceSel, startupGoSource, startupGoSourceErr = front.GoSourceSelection{}, front.GoSourceResolution{}, nil
				*command, *readStdin, *forceI = "", false, false
				os.Args = []string{"bashy"}
				originalArgs = []string{"bashy", "--bashpp"}
				switch form {
				case "command", "command-posix", "command-bash":
					*command = source
				case "file":
					os.Args = append(os.Args, writeGoFixture(t, source))
				case "stdin", "stdin-args":
					f, err := os.CreateTemp(t.TempDir(), "stdin")
					if err != nil {
						t.Fatal(err)
					}
					if _, err = f.WriteString(source); err != nil {
						t.Fatal(err)
					}
					_, _ = f.Seek(0, 0)
					os.Stdin = f
					if form == "stdin-args" {
						*readStdin = true
						os.Args = append(os.Args, "not-a-source-file", "another-argument")
					}
					t.Cleanup(func() { f.Close() })
				}
				if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
					t.Fatal(err)
				}
				want := "selected Go frontend diagnostic"
				if form == "command-posix" {
					os.Args = append(os.Args, "--posix")
					originalArgs = append(originalArgs, "--posix")
					want = "bashy: --source=go is not available in POSIX mode"
				}
				if form == "command-bash" {
					AgentOSBashPPDefault = false
					want = "bashy: --source=go requires the bashy front door"
				}
				called := false
				withGoSourceHook(t, func(files []front.GoSourceFile, _ front.GoSourceOptions) (*front.GoSourceProgram, error) {
					called = true
					if len(files) != 1 || string(files[0].Data) != source {
						t.Fatalf("source changed: %+v", files)
					}
					return nil, errors.New("selected Go frontend diagnostic")
				})
				var err error
				diagnostic := captureStderr(t, func() { err = runAll() })
				wantCalled := form != "command-posix" && form != "command-bash"
				if called != wantCalled || err == nil || strings.TrimSpace(diagnostic) != want {
					t.Fatalf("called=%v err=%v diagnostic=%q", called, err, diagnostic)
				}
			})
		}
	}
}

func TestSprint198PackageGoLexicalSemantics(t *testing.T) {
	oldDialect := startupBashPP
	startupBashPP = front.BashPPResolution{Enabled: true}
	t.Cleanup(func() { startupBashPP = oldDialect })
	const source = "package main\nfunc main(){ x:=`$HOME`; x=x+\" $literal\"; println(x) }\n"
	withGoSourceSelection(t, front.GoSourceResolution{Enabled: true})
	withGoSourceHook(t, func(files []front.GoSourceFile, opts front.GoSourceOptions) (*front.GoSourceProgram, error) {
		p, err := gosource.Load([]gosource.Source{{Name: files[0].Name, Data: files[0].Data}}, gosource.Options{RunMain: opts.RunMain})
		if err != nil {
			return nil, err
		}
		return &front.GoSourceProgram{File: p.File, Package: p.Package, Main: p.Main, InitFunctions: p.InitFunctions}, nil
	})
	in, _, selected, err := collectPackageGoSource("", source, nil)
	if err != nil || !selected {
		t.Fatalf("selection: %v %v", selected, err)
	}
	var runErr error
	out := captureStderr(t, func() { runErr = runGoSourceInput(in, "") })
	if runErr != nil || out != "$HOME $literal\n" {
		t.Fatalf("Go literals/assignment: output=%q error=%v", out, runErr)
	}
}

// Selection must not wait for EOF merely to recognize an ordinary shell word.
// Execution itself keeps the existing run() buffering contract.
func TestSprint198PackageStdinPrefixDoesNotWaitForEOF(t *testing.T) {
	reader, writer := io.Pipe()
	release := make(chan struct{})
	t.Cleanup(func() { reader.Close(); writer.Close() })
	go func() { _, _ = writer.Write([]byte("echo ready\n")); <-release; writer.Close() }()
	type result struct {
		replay   io.Reader
		selected bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		_, replay, selected, err := collectPackageGoSource("", "", reader)
		done <- result{replay, selected, err}
	}()
	var got result
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("shell prefix selection waited for EOF")
	}
	close(release)
	if got.err != nil || got.selected {
		t.Fatalf("selected=%v error=%v", got.selected, got.err)
	}
	data, err := io.ReadAll(got.replay)
	if err != nil || string(data) != "echo ready\n" {
		t.Fatalf("replay=%q error=%v", data, err)
	}
}

func TestSprint198PackageOrdinaryPOSIXNoExec(t *testing.T) {
	for _, mode := range []string{"flag", "option", "environment", "pedantic", "certification"} {
		for _, form := range []string{"command", "stdin", "file"} {
			t.Run(mode+"/"+form, func(t *testing.T) {
				withStrictPosixEnv(t, "bashy", false)
				oldOriginal, oldOpts, oldOff := originalArgs, optsOn, setOff
				oldCommand, oldRead, oldForce := *command, *readStdin, *forceI
				oldSel, oldRes, oldErr := startupGoSourceSel, startupGoSource, startupGoSourceErr
				oldDialect, oldDefault, oldStdin := startupBashPP, AgentOSBashPPDefault, os.Stdin
				t.Cleanup(func() {
					originalArgs, optsOn, setOff = oldOriginal, oldOpts, oldOff
					*command, *readStdin, *forceI = oldCommand, oldRead, oldForce
					startupGoSourceSel, startupGoSource, startupGoSourceErr = oldSel, oldRes, oldErr
					startupBashPP, AgentOSBashPPDefault, os.Stdin = oldDialect, oldDefault, oldStdin
				})
				t.Setenv("BASH_ENV", "")
				unsetTestEnv(t, "BASHY_BASHPP")
				AgentOSBashPPDefault = true
				startupGoSourceSel, startupGoSource, startupGoSourceErr = front.GoSourceSelection{}, front.GoSourceResolution{}, nil
				optsOn, setOff = []string{"noexec"}, nil
				*command, *readStdin, *forceI = "", false, false
				operand := ""
				switch form {
				case "command":
					*command = "package main\n"
				case "file":
					operand = writeGoFixture(t, "package main\n")
				case "stdin":
					f, err := os.Open(writeGoFixture(t, "package main\n"))
					if err != nil {
						t.Fatal(err)
					}
					os.Stdin = f
					t.Cleanup(func() { f.Close() })
				}
				if operand != "" {
					if err := flag.CommandLine.Parse([]string{operand}); err != nil {
						t.Fatal(err)
					}
				}
				os.Args = []string{"bashy"}
				switch mode {
				case "flag":
					os.Args = append(os.Args, "--posix")
				case "option":
					os.Args = append(os.Args, "-o", "posix")
				case "environment":
					t.Setenv("POSIXLY_CORRECT", "1")
				case "pedantic":
					t.Setenv("POSIX_PEDANTIC", "1")
				case "certification":
					os.Args[0] = "sh"
					AgentOSBashPPDefault = false
				}
				originalArgs = append([]string(nil), os.Args...)
				if operand != "" {
					os.Args = append(os.Args, operand)
				}
				called := false
				withGoSourceHook(t, func([]front.GoSourceFile, front.GoSourceOptions) (*front.GoSourceProgram, error) {
					called = true
					return nil, errors.New("unexpected Go route")
				})
				var runErr error
				diagnostic := captureStderr(t, func() { runErr = runAll() })
				if called || runErr != nil || diagnostic != "" {
					t.Fatalf("ordinary POSIX marker: frontend=%v error=%v diagnostic=%q", called, runErr, diagnostic)
				}
			})
		}
	}
}
