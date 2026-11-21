package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// banned are the module paths the certified shell must never link or require:
// bsh is the engine bashy builds on, never the other way round.
var banned = []string{
	"github.com/qiangli/yoke",
	"github.com/qiangli/bashy",
	"github.com/qiangli/ycode",
}

func isBanned(path string) bool {
	for _, b := range banned {
		if path == b || strings.HasPrefix(path, b+"/") {
			return true
		}
	}
	return false
}

// standaloneEnv runs the go command on this module alone: bsh is its own
// module and must build from its go.mod pins, never from a workspace.
func standaloneEnv() []string {
	return append(os.Environ(), "GOWORK=off")
}

// TestBshImportBoundary is the link proof: bsh's whole dependency graph holds
// no Yoke, Bashy, ycode or Genie package. There is no allowlist.
func TestBshImportBoundary(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", ".")
	cmd.Env = standaloneEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	pkgs := strings.Fields(string(out))
	var required = map[string]bool{
		"github.com/bashsharp/bashsharp/cli":       false,
		"github.com/bashsharp/bashsharp/transpile": false,
		"github.com/qiangli/coreutils/cmds/all":    false,
		"github.com/qiangli/bash/interp":           false,
	}
	for _, pkg := range pkgs {
		if _, ok := required[pkg]; ok {
			required[pkg] = true
		}
		if isBanned(pkg) {
			t.Errorf("bsh links banned package %s", pkg)
		}
	}
	for pkg, seen := range required {
		if !seen {
			t.Errorf("bsh does not link %s", pkg)
		}
	}
}

// TestBshModuleRequires keeps go.mod to its three direct requirements and
// out of reach of Yoke, Bashy and ycode, direct or indirect.
func TestBshModuleRequires(t *testing.T) {
	cmd := exec.Command("go", "mod", "edit", "-json")
	cmd.Env = standaloneEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go mod edit -json: %v", err)
	}
	var mod struct {
		Require []struct {
			Path     string
			Indirect bool
		}
		Replace []struct{ Old, New struct{ Path string } }
	}
	if err := json.Unmarshal(out, &mod); err != nil {
		t.Fatal(err)
	}
	direct := map[string]bool{}
	for _, r := range mod.Require {
		if isBanned(r.Path) {
			t.Errorf("go.mod requires banned module %s", r.Path)
		}
		if !r.Indirect {
			direct[r.Path] = true
		}
	}
	want := []string{"github.com/bashsharp/bashsharp", "github.com/qiangli/bash", "github.com/qiangli/coreutils"}
	for _, p := range want {
		if !direct[p] {
			t.Errorf("go.mod does not require %s directly", p)
		}
		delete(direct, p)
	}
	for p := range direct {
		t.Errorf("go.mod has unexpected direct requirement %s", p)
	}
	for _, r := range mod.Replace {
		if isBanned(r.Old.Path) || isBanned(r.New.Path) {
			t.Errorf("go.mod replaces through banned module: %s => %s", r.Old.Path, r.New.Path)
		}
	}
}

func TestInvocationRouting(t *testing.T) {
	for _, tc := range []struct {
		argv0          string
		shell, utility bool
	}{
		{"/bin/bsh", false, false},
		{"/bin/-bsh", false, false},
		{"/bin/bsh.exe", false, false},
		{"/bin/sh", true, false},
		{"/bin/bash", true, false},
		{"/bin/cat", false, true},
		{"/bin/coreutils", false, true},
		{"/bin/not-a-command", false, false},
	} {
		if got := shellInvocation(tc.argv0); got != tc.shell {
			t.Errorf("shellInvocation(%q) = %v, want %v", tc.argv0, got, tc.shell)
		}
		if got := utilityInvocation(tc.argv0); got != tc.utility {
			t.Errorf("utilityInvocation(%q) = %v, want %v", tc.argv0, got, tc.utility)
		}
	}
}

// TestBshBinary builds bsh and checks the three ingredients end to end:
// Bash 5.3 syntax, Bash# on by default, and coreutils served in-process with
// an empty PATH (no host utility can answer).
func TestBshBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the bsh binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix symlinks")
	}
	dir := t.TempDir()
	bsh := filepath.Join(dir, "bsh")
	build := exec.Command("go", "build", "-o", bsh, ".")
	build.Env = standaloneEnv()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	run := func(argv0 string, env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command(argv0, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %q: %v\n%s", argv0, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	base := []string{"HOME=" + dir, "PATH=" + dir + "/empty"}

	if got := run(bsh, base, "--version"); !strings.HasPrefix(got, "bsh, GNU Bash 5.3 compatible") {
		t.Errorf("--version = %q", got)
	}
	if got := run(bsh, base, "-c", `declare -A m=([k]=v); printf '%s\n' "${m[k]^^}" | tr V W`); got != "W" {
		t.Errorf("bash+coreutils pipeline = %q, want W", got)
	}
	if got := run(bsh, base, "-c", `ls -d /`); got != "/" {
		t.Errorf("in-process ls = %q", got)
	}
	// Bash# is the default dialect; --no-bashsharp is plain Bash 5.3, which
	// rejects the Bash# binding as a syntax error.
	if got := run(bsh, base, "-c", `answer := 40 + 2; echo $answer`); got != "42" {
		t.Errorf("Bash# binding = %q, want 42", got)
	}
	plain := exec.Command(bsh, "--no-bashsharp", "-c", `answer := 40 + 2; echo "$answer"`)
	plain.Env = base
	if out, err := plain.CombinedOutput(); err == nil && strings.TrimSpace(string(out)) == "42" {
		t.Errorf("--no-bashsharp accepted Bash# binding: %q", out)
	}
	// coreutils multicall: by link name and by the coreutils self name.
	link := filepath.Join(dir, "basename")
	if err := os.Symlink(bsh, link); err != nil {
		t.Fatal(err)
	}
	if got := run(link, base, "/a/b.txt", ".txt"); got != "b" {
		t.Errorf("basename link = %q", got)
	}
	cu := filepath.Join(dir, "coreutils")
	if err := os.Symlink(bsh, cu); err != nil {
		t.Fatal(err)
	}
	if got := run(cu, base, "echo", "ok"); got != "ok" {
		t.Errorf("coreutils echo = %q", got)
	}
}
