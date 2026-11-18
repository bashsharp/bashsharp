//go:build full

package interp_test

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

const s281ReexecChild = "BASHPP_S281_REEXEC_CHILD"
const s281ReplacementCompile = "BASHPP_S281_REPLACEMENT_COMPILE"
const s281ReplacementMarker = "BASHPP_S281_REPLACEMENT_MARKER"

const s281ReexecSource = `package main
import (
	"fmt"
	"os"
	"os/exec"
)
func main() {
	if os.Getenv("BASHPP_S281_REEXEC_CHILD") == "1" {
		if os.Getenv("BASHPP_REEXEC_PREPARED_CACHE") != "" || os.Getenv("BASHPP_REEXEC_INTERPRETER_ID") != "" {
			panic("reexec preparation environment leaked into interpreted program")
		}
		fmt.Println("interpreted-child", os.Args[1])
		fmt.Println("child-goroot", os.Getenv("GOROOT"))
		return
	}
	executable, err := os.Executable()
	if err != nil { panic(err) }
	if replacement := os.Getenv("BASHPP_S281_REPLACEMENT_COMPILE"); replacement != "" {
		script := "#!/bin/sh\nprintf invoked > \"$BASHPP_S281_REPLACEMENT_MARKER\"\necho replacement-compiler-invoked >&2\nexit 86\n"
		if err := os.WriteFile(replacement, []byte(script), 0755); err != nil { panic(err) }
	}
	os.Unsetenv("GOSH_PROG")
	cmd := exec.Command(executable, "payload")
	cmd.Env = append(os.Environ(), "BASHPP_S281_REEXEC_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil { panic(fmt.Sprintf("child: %v: %s", err, out)) }
	fmt.Print(string(out))
}
`

const s281ReexecVersionFanoutSource = `package main
import (
	"os"
	"os/exec"
)
func main() {
	executable, err := os.Executable()
	if err != nil { panic(err) }
	os.Unsetenv("GOSH_PROG")
	cmd := exec.Command(executable, "__fanout__")
	cmd.Env = append(os.Environ(), "BASHPP_S281_VERSION_PROBE_LAUNCHER=" + executable)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil { panic(err) }
}
`

const s319ReexecVersionCycleSource = `package main
import (
	"os"
)
func main() {
	launcher, err := os.Executable()
	if err != nil { panic(err) }
	replacement := os.Getenv("BASHPP_S319_REPLACEMENT_TOOL")
	data, err := os.ReadFile(launcher)
	if err != nil { panic(err) }
	if err := os.WriteFile(replacement, data, 0755); err != nil { panic(err) }
}
`

const s281ReexecArgv0Source = `package main
import (
	"fmt"
	"os"
	"path/filepath"
)
func main() {
	name := filepath.Base(os.Args[0])
	if len(os.Args) == 2 && os.Args[1] == "payload" {
		fmt.Println("replayed-argv0", name)
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "-V=full" {
		fmt.Println(name, "version go1.27.1")
		return
	}
	launcher, err := os.Executable()
	if err != nil { panic(err) }
	renamed := os.Getenv("BASHPP_S281_RENAMED_TOOL")
	data, err := os.ReadFile(launcher)
	if err != nil { panic(err) }
	if err := os.WriteFile(renamed, data, 0755); err != nil { panic(err) }
	fmt.Println("copied-launcher", renamed)
}
`

const s281Argv0Source = `package main
import (
	"fmt"
	"os"
)
func main() { fmt.Println(os.Args[0]) }
`

func TestGoSourceS281SelfReexecLauncher(t *testing.T) {
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) >= 2 && args[1] == "payload" {
		if got := os.Getenv(s281ReexecChild); got != "1" {
			t.Fatalf("reexec child environment %s=%q, want 1", s281ReexecChild, got)
		}
		if cache := os.Getenv("BASHPP_REEXEC_PREPARED_CACHE"); !filepath.IsAbs(cache) {
			t.Fatalf("reexec prepared cache = %q, want absolute path", cache)
		}
		if identity := os.Getenv("BASHPP_REEXEC_INTERPRETER_ID"); len(identity) != 64 {
			t.Fatalf("reexec interpreter identity length = %d, want SHA-256", len(identity))
		}
		runS281ReexecProgram(t, os.Stdout, args[1:], nil)
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got := runS281ReexecProgram(t, nil, nil, []string{self, "-test.run=^TestGoSourceS281SelfReexecLauncher$", "--"})
	if !strings.Contains(got, "interpreted-child payload\n") {
		t.Fatalf("reexec output = %q, want interpreted child marker", got)
	}
}

// TestGoSourceS281SelfReexecLauncherArgv0 copies the generated launcher to the
// name of a replacement tool. Both a normal child replay and the native
// launcher's -V=full identity must observe that invoked name, not the
// reconstructed source filename or the host test binary.
func TestGoSourceS281SelfReexecLauncherArgv0(t *testing.T) {
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 2 && (args[1] == "payload" || args[1] == "-V=full") {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		plan := []string{self, "-test.run=^TestGoSourceS281SelfReexecLauncherArgv0$", "--"}
		runS281ReexecSourceProgram(t, s281ReexecArgv0Source, os.Stdout, args[1:], plan)
		return
	}
	name := "renamed-compile-tool"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	renamed := filepath.Join(t.TempDir(), name)
	t.Setenv("BASHPP_S281_RENAMED_TOOL", renamed)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcherOutput := runS281ReexecSourceProgram(t, s281ReexecArgv0Source, nil, nil,
		[]string{self, "-test.run=^TestGoSourceS281SelfReexecLauncherArgv0$", "--"})
	if want := "copied-launcher " + renamed + "\n"; launcherOutput != want {
		t.Fatalf("launcher output = %q, want %q", launcherOutput, want)
	}
	command := exec.Command(renamed, "payload")
	command.Env = s281WithoutEnv(os.Environ(), "GOSH_PROG")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("replay: %v: %s", err, output)
	}
	if want := "replayed-argv0 " + name + "\n"; !strings.Contains(string(output), want) {
		t.Fatalf("reexec output = %q, want %q", output, want)
	}
	command = exec.Command(renamed, "-V=full")
	command.Env = s281WithoutEnv(os.Environ(), "GOSH_PROG")
	output, err = command.CombinedOutput()
	if err != nil {
		t.Fatalf("version probe: %v: %s", err, output)
	}
	versionName := strings.TrimSuffix(name, ".exe")
	if want := versionName + " version devel buildID="; !strings.HasPrefix(string(output), want) {
		t.Fatalf("version probe output = %q, want prefix %q", output, want)
	}
}

func s281WithoutEnv(env []string, name string) []string {
	prefix := name + "="
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return out
}

func TestGoSourceS281ReexecArgv0Selection(t *testing.T) {
	t.Setenv("BASHPP_REEXEC_ARGV0", "process-global-replay")
	withoutReplay := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "BASHPP_REEXEC_ARGV0=") {
			withoutReplay = append(withoutReplay, entry)
		}
	}
	withReplay := append(append([]string(nil), withoutReplay...), "BASHPP_REEXEC_ARGV0=runner-replay")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	plan := []string{self, "-test.run=^$", "--"}
	for _, tc := range []struct {
		name string
		plan []string
		opts []interp.RunnerOption
		want string
	}{
		{name: "default source filename", want: "reexec.go"},
		{name: "explicit argv0", opts: []interp.RunnerOption{interp.WithArgv0("configured-tool")}, want: "configured-tool"},
		{name: "process environment does not leak", plan: plan, opts: []interp.RunnerOption{interp.Env(expand.ListEnviron(withoutReplay...)), interp.WithArgv0("configured-tool")}, want: "configured-tool"},
		{name: "runner replay overrides configured argv0", plan: plan, opts: []interp.RunnerOption{interp.Env(expand.ListEnviron(withReplay...)), interp.WithArgv0("configured-tool")}, want: "runner-replay"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runS281ReexecSourceProgram(t, s281Argv0Source, nil, nil, tc.plan, tc.opts...)
			if got != tc.want+"\n" {
				t.Fatalf("os.Args[0] = %q, want %q", strings.TrimSpace(got), tc.want)
			}
		})
	}
}

// TestGoSourceS281SelfReexecReplacementToolIsolation recreates TestMain's
// replacement-tool ordering without running cmd/compile's large script suite:
// the parent first obtains its replay launcher, then overwrites compile in the
// selected GOROOT, and only then starts the interpreted child. The child must
// build its dependency helper with the independent authenticated SDK, while
// retaining the selected GOROOT in the interpreted process environment.
func TestGoSourceS281SelfReexecReplacementToolIsolation(t *testing.T) {
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) >= 2 && args[1] == "payload" {
		if got := os.Getenv(s281ReexecChild); got != "1" {
			t.Fatalf("reexec child environment %s=%q, want 1", s281ReexecChild, got)
		}
		runS281ReexecProgram(t, os.Stdout, args[1:], nil)
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("the focused replacement is a POSIX executable script")
	}
	goBinary, compileTool := s281ReplacementGOROOT(t)
	fakeRoot := filepath.Dir(filepath.Dir(goBinary))
	marker := filepath.Join(t.TempDir(), "replacement-invoked")
	t.Setenv("BASHPP_GO", goBinary)
	t.Setenv("GOROOT", fakeRoot)
	t.Setenv(s281ReplacementCompile, compileTool)
	t.Setenv(s281ReplacementMarker, marker)

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got := runS281ReexecProgram(t, nil, nil, []string{self, "-test.run=^TestGoSourceS281SelfReexecReplacementToolIsolation$", "--"})
	if !strings.Contains(got, "interpreted-child payload\n") {
		t.Fatalf("reexec output = %q, want interpreted child marker", got)
	}
	if !strings.Contains(got, "child-goroot "+fakeRoot+"\n") {
		t.Fatalf("reexec output = %q, want child GOROOT %q", got, fakeRoot)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("replacement compiler was invoked; marker stat: %v", err)
	}
}

// TestGoSourceS281SelfReexecVersionProbeSingleflight models the Go command's
// toolID probe. Parallel commands must receive the same native-launcher
// identity without entering the replay plan, while a build configuration that
// can change tool behavior must receive a different identity.
func TestGoSourceS281SelfReexecVersionProbeSingleflight(t *testing.T) {
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 2 && args[1] == "-V=full" {
		marker := os.Getenv("BASHPP_S281_VERSION_PROBE_MARKER")
		file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("x"); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(250 * time.Millisecond)
		version := "compile version go1.27.1"
		if experiment := os.Getenv("GOEXPERIMENT"); experiment != "" {
			version += " X:" + experiment
		}
		_, _ = io.WriteString(os.Stdout, version+"\n")
		return
	}
	if len(args) == 2 && args[1] == "__fanout__" {
		launcher := os.Getenv("BASHPP_S281_VERSION_PROBE_LAUNCHER")
		if err := os.Unsetenv("GOSH_PROG"); err != nil {
			t.Fatal(err)
		}
		const processes = 16
		outputs := make(chan []byte, processes)
		errs := make(chan error, processes)
		var group sync.WaitGroup
		for range processes {
			group.Add(1)
			go func() {
				defer group.Done()
				output, err := exec.Command(launcher, "-V=full").CombinedOutput()
				if err != nil {
					errs <- fmt.Errorf("version probe: %w: %s", err, output)
					return
				}
				outputs <- output
			}()
		}
		group.Wait()
		close(outputs)
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		for output := range outputs {
			_, _ = os.Stdout.Write(output)
		}
		if err := os.Setenv("GOEXPERIMENT", "cachekey"); err != nil {
			t.Fatal(err)
		}
		output, err := exec.Command(launcher, "-V=full").CombinedOutput()
		if err != nil {
			t.Fatalf("experiment version probe: %v: %s", err, output)
		}
		_, _ = os.Stdout.Write(output)
		return
	}

	marker := filepath.Join(t.TempDir(), "version-probes")
	t.Setenv("BASHPP_S281_VERSION_PROBE_MARKER", marker)
	t.Setenv("GOEXPERIMENT", "")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	output := runS281ReexecSourceProgram(t, s281ReexecVersionFanoutSource, nil, nil,
		[]string{self, "-test.run=^TestGoSourceS281SelfReexecVersionProbeSingleflight$", "--"})
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.Contains(line, " version devel buildID=") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 17 {
		t.Fatalf("version output lines = %d, want 17; output=%q", len(lines), output)
	}
	if !strings.HasPrefix(lines[0], "bashpp-reexec-launcher version devel buildID=") {
		t.Fatalf("version output = %q, want Go tool identity", lines[0])
	}
	for i, line := range lines[1:16] {
		if line != lines[0] {
			t.Fatalf("parallel version output %d = %q, want %q", i+1, line, lines[0])
		}
	}
	if lines[16] == lines[0] {
		t.Fatalf("GOEXPERIMENT did not change tool identity: %q", lines[16])
	}
	if data, err := os.ReadFile(marker); err == nil {
		t.Fatalf("version probes entered replay plan: %q", data)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// TestGoSourceS319ReexecVersionProbeDoesNotEnterPlan models a Go command
// probing an interpreted replacement compiler. Replaying the compiler to
// answer its identity probe enters package discovery, which invokes the same
// replacement compiler and forms an unbounded process recursion. The native
// launcher must answer -V=full without entering the replay plan at all.
func TestGoSourceS319ReexecVersionProbeDoesNotEnterPlan(t *testing.T) {
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 2 && args[1] == "-V=full" {
		marker := os.Getenv("BASHPP_S319_VERSION_REENTRY_MARKER")
		file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("reentered\n"); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(os.Stdout, "compile version go1.27.1\n")
		return
	}
	dir := t.TempDir()
	replacement := filepath.Join(dir, "compile")
	if runtime.GOOS == "windows" {
		replacement += ".exe"
	}
	marker := filepath.Join(dir, "version-reentry")
	t.Setenv("BASHPP_S319_REPLACEMENT_TOOL", replacement)
	t.Setenv("BASHPP_S319_VERSION_REENTRY_MARKER", marker)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runS281ReexecSourceProgram(t, s319ReexecVersionCycleSource, nil, nil,
		[]string{self, "-test.run=^TestGoSourceS319ReexecVersionProbeDoesNotEnterPlan$", "--"})
	command := exec.Command(replacement, "-V=full")
	command.Env = s281WithoutEnv(os.Environ(), "GOSH_PROG")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("version probe: %v: %s", err, output)
	}
	if !strings.HasPrefix(string(output), "compile version ") {
		t.Fatalf("version probe output = %q, want compiler version line", output)
	}
	if data, err := os.ReadFile(marker); err == nil {
		t.Fatalf("version probe entered replay plan: %q", data)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func s281ReplacementGOROOT(t *testing.T) (goBinary, compileTool string) {
	t.Helper()
	realRoot := runtime.GOROOT()
	fakeRoot := t.TempDir()
	for _, name := range []string{"api", "doc", "lib", "misc", "src", "test", "VERSION", "go.env"} {
		source := filepath.Join(realRoot, name)
		if _, err := os.Stat(source); err == nil {
			if err := os.Symlink(source, filepath.Join(fakeRoot, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(fakeRoot, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	goBinary = filepath.Join(fakeRoot, "bin", "go")
	s281CopyExecutable(t, filepath.Join(realRoot, "bin", "go"), goBinary)

	toolDir := filepath.Join("pkg", "tool", runtime.GOOS+"_"+runtime.GOARCH)
	if err := os.MkdirAll(filepath.Join(fakeRoot, toolDir), 0755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(realRoot, toolDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		source := filepath.Join(realRoot, toolDir, entry.Name())
		target := filepath.Join(fakeRoot, toolDir, entry.Name())
		if entry.Name() == "compile" {
			s281CopyExecutable(t, source, target)
			compileTool = target
		} else if err := os.Symlink(source, target); err != nil {
			t.Fatal(err)
		}
	}
	if compileTool == "" {
		t.Fatal("selected GOROOT has no compile tool")
	}
	return goBinary, compileTool
}

func s281CopyExecutable(t *testing.T, source, target string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0755); err != nil {
		t.Fatal(err)
	}
}

func runS281ReexecProgram(t *testing.T, stdout io.Writer, args, plan []string) string {
	return runS281ReexecSourceProgram(t, s281ReexecSource, stdout, args, plan)
}

func runS281ReexecSourceProgram(t *testing.T, source string, stdout io.Writer, args, plan []string, extra ...interp.RunnerOption) string {
	t.Helper()
	dir := t.TempDir()
	program, err := gosource.Parse(strings.NewReader(source), "reexec.go", gosource.Options{RunMain: true, Importer: lower.NewModuleImporter(dir)})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if stdout == nil {
		stdout = &output
	}
	options := []interp.RunnerOption{
		interp.Lang(syntax.LangBashPP),
		interp.Dir(dir),
		interp.Env(expand.ListEnviron(os.Environ()...)),
		interp.GoSourceEnv(os.Environ()),
		interp.StdIO(nil, stdout, stdout),
		interp.Params(append([]string{"--"}, args...)...),
	}
	if plan != nil {
		options = append(options, interp.GoSourceReexecPlan(plan...))
	}
	options = append(options, extra...)
	runner, err := interp.New(options...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatalf("Run: %v; output=%q", err, output.String())
	}
	return output.String()
}
