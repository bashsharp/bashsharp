package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Exercise the actual Bashy launcher and yoke child. These sizes cross both
// the Windows command-line limit and Linux's usual per-string execve limit.
func TestOwnedExecLargeArgAndEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("builds focused child binaries")
	}
	dir := t.TempDir()
	root := filepath.Join("..", "..")
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	bash, yoke := filepath.Join(dir, "bash"+suffix), filepath.Join(dir, "yoke"+suffix)
	for _, build := range []struct{ out, pkg, dir string }{{bash, "./cmd/bash", root}, {yoke, "github.com/qiangli/yoke/cmd/yoke", root}} {
		args := []string{"build", "-buildvcs=false"}
		if build.pkg == "./cmd/bash" {
			args = append(args, "-tags=ownedexecprobe")
		}
		args = append(args, "-o", build.out, build.pkg)
		cmd := exec.Command("go", args...)
		cmd.Dir = build.dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", build.pkg, err, out)
		}
	}
	script := `v=$(printf '%*s' 200000 '')
n=$(yoke printf '%s|%s|%s' "$v" '' '🙂' | yoke wc -c)
printf '%s\n' "$n"
export v
n=$(yoke printenv v | yoke wc -c)
printf '%s\n' "$n"
`
	cmd := exec.Command(bash, "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("owned child: %v\n%s", err, out)
	}
	lines := strings.Fields(string(out))
	if len(lines) != 2 || lines[0] != "200006" || lines[1] != "200001" {
		t.Fatalf("unexpected counts: %q", out)
	}
	// Multicall chooses the applet from argv[0]. A short native argv that
	// forgets exec -a's spelling would dispatch the wrong command.
	cmd = exec.Command(bash, "-c", `v=$(printf '%*s' 200000 ''); exec -a printf yoke '%s' "$v"`)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err = cmd.CombinedOutput()
	if err != nil || len(out) != 200000 || strings.Trim(string(out), " ") != "" {
		t.Fatalf("argv[0] dispatch: error=%v, output bytes=%d, prefix=%q", err, len(out), out[:min(len(out), 100)])
	}
	// Both the ordinary redirected descriptor and the private frame must
	// survive the same child launch. The large -c positional operand forces
	// the frame on Windows as well as Unix.
	fdScript := `v=$(printf '%*s' 200000 ''); export v; exec 9<<<'pipe-fd'; pad=$(printf '%*s' 200000 ''); exec bash -c ':' "$pad"`
	cmd = exec.Command(bash, "-c", fdScript)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err = cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "fd-ok" {
		t.Fatalf("owned frame and inherited fd: error=%v, bytes=%d, prefix=%q", err, len(out), out[:min(len(out), 400)])
	}
	if runtime.GOOS != "windows" {
		pidScript := `v=$(printf '%*s' 200000 ''); export v; printf '%s\n' "$$"; exec bash -c 'printf "%s %s %s\n" "$$" "${#v}" "${BASHY_OWNED_EXEC_FRAME-unset}"'`
		cmd = exec.Command(bash, "-c", pidScript)
		cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("true exec: %v\n%s", err, out)
		}
		lines = strings.Fields(string(out))
		if len(lines) != 4 || lines[0] != lines[1] || lines[2] != "200000" || lines[3] != "unset" {
			t.Fatalf("exec did not preserve PID/env: %q", out)
		}
	}
}
