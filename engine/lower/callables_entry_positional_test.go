//go:build full

package lower_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/lower"
)

// runArtifact builds a compiled unit into a real binary, removes the generated
// source, and runs the binary with argv. Removing the source is the point: the
// program under test is the artifact, so nothing it does can be explained by
// the compiler still being around.
func runArtifact(t *testing.T, result *lower.Result, args ...string) (string, string, int) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "generated.go")
	if err := os.WriteFile(source, result.Source, 0600); err != nil {
		t.Fatal(err)
	}
	_, this, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(this)))
	module := "module lowerfixture\n\ngo " + strings.TrimPrefix(runtime.Version(), "go") + "\n"
	if strings.Contains(string(result.Source), "lower/shellrt") {
		module += "require github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + root + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	binary := filepath.Join(dir, "program")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-mod=mod", "-o", binary, "generated.go")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s\n%s", err, out, result.Source)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/no-tools"}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	status := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		status = exit.ExitCode()
	}
	return out.String(), stderr.String(), status
}

// positionalProbe frames every parameter, so a joined, re-split or re-quoted
// argv cannot pass for the original one. The `if` wrapper is what makes this
// one dynamic shell region: a bare printf is lowered to typed Go, and the
// point here is what the *shell* sees.
const positionalProbe = "if true; then\n" +
	"printf 'count=%d\\n' \"$#\"\n" +
	"for a in \"$@\"; do printf '<%s>\\n' \"$a\"; done\n" +
	"fi\n"

func wantPositional(args []string) string {
	var b strings.Builder
	b.WriteString("count=" + decimal(len(args)) + "\n")
	for _, a := range args {
		b.WriteString("<" + a + ">\n")
	}
	return b.String()
}

func decimal(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

// TestEntryPassesProcessArgvToShellPositionals is the end-to-end runtime
// claim: a built artifact, with its source gone, hands the process operands to
// a dynamic shell region as $1, $2, ... byte for byte.
//
// The probe reads the parameters from inside a shell region on purpose. The
// typed-side mapping of "$1" in a Go position is the compiler dispatcher's
// half of this story and is not wired yet; this test pins the runtime bridge
// that the mapping will sit on top of.
func TestEntryPassesProcessArgvToShellPositionals(t *testing.T) {
	t.Parallel()
	result, err := lower.Compile(parse(t, positionalProbe, "input.bpp"), lower.Options{Entry: "Run"})
	if err != nil {
		t.Fatal(err)
	}
	// The emitter prefixes its imports, so match on the tail of the call
	// rather than on a bare package name.
	src := string(result.Source)
	defaults := strings.Index(src, "WithParams(")
	if defaults < 0 || !strings.HasPrefix(src[defaults:], "WithParams(") || !strings.Contains(src[defaults:], "os.Args[1:]...)") {
		t.Fatalf("entry does not seed the process argv:\n%s", src)
	}
	// The default sits in the defaults slice, which is appended before the
	// caller's opts, so an embedder's own WithParams wins.
	overrides := strings.Index(src, "append(defaults, opts...)")
	if overrides < 0 {
		overrides = strings.Index(src, "append(defaults,opts...)")
	}
	if overrides < 0 || defaults > overrides {
		t.Fatalf("process argv default is not applied before caller options:\n%s", src)
	}
	if !strings.Contains(src, "func Run(opts ") {
		t.Fatalf("entry is not importable under its requested name:\n%s", src)
	}

	args := []string{"", "two words", "tab\there", "line\nbreak", "-n", "--", "single'quote", `back\slash`, "$notexpanded", "héllo wörld", "日本語", "🐚"}
	out, stderr, status := runArtifact(t, result, args...)
	if want := wantPositional(args); out != want {
		t.Fatalf("stdout %q, want %q", out, want)
	}
	if stderr != "" || status != 0 {
		t.Fatalf("stderr %q status %d", stderr, status)
	}

	// No operands is an empty parameter list, not the program name and not
	// leftover state.
	out, stderr, status = runArtifact(t, result)
	if want := "count=0\n"; out != want {
		t.Fatalf("stdout %q, want %q", out, want)
	}
	if stderr != "" || status != 0 {
		t.Fatalf("stderr %q status %d", stderr, status)
	}
}
