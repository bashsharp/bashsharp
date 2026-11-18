//go:build windows

package interp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A relative executable (./sub/x.exe) after a `cd` inside the shell must run:
// bashy hands scripts /c/… paths, and CreateProcess resolves a relative
// application path against the parent's cwd, not Cmd.Dir.
func TestWindowsRelativeExecAfterCd(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(`C:\Windows\System32\where.exe`)
	if err != nil {
		t.Skip("no where.exe")
	}
	exe := filepath.Join(sub, "w.exe")
	if err := os.WriteFile(exe, src, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, form := range []string{"./sub/w.exe", "sub/w.exe", `"sub\w.exe"`} {
		t.Run(form, func(t *testing.T) {
			var out, errb strings.Builder
			r, _ := interp.New(interp.StdIO(nil, &out, &errb), interp.Dir(dir))
			script := "cd " + filepath.ToSlash(dir) + " && " + form + " /Q where.exe; echo rc=$?"
			f, perr := syntax.NewParser().Parse(strings.NewReader(script), "")
			if perr != nil {
				t.Fatal(perr)
			}
			_ = r.Run(context.Background(), f)
			if !strings.Contains(out.String(), "rc=0") {
				t.Fatalf("form %q: out=%q err=%q", form, out.String(), errb.String())
			}
		})
	}
}

// A native child must receive the path-valued variables in the OS spelling:
// bashy presents $TEMP as /c/Users/…, and go.exe / rustc resolve that
// against the current drive (D:\c\Users\…) and fail to create their work dir.
func TestWindowsExecNativePathEnv(t *testing.T) {
	temp := filepath.ToSlash(t.TempDir())
	if len(temp) < 2 || temp[1] != ':' {
		t.Skipf("temp dir %q is not a drive path", temp)
	}
	msys := "/" + strings.ToLower(temp[:1]) + temp[2:]
	native := filepath.Clean(temp)
	env := append(os.Environ(), "TEMP="+msys, "TMP="+msys)
	var out, errb strings.Builder
	r, _ := interp.New(interp.StdIO(nil, &out, &errb), interp.Env(expand.ListEnviron(env...)))
	script := `echo "shell=$TEMP"; cmd /c "echo child=%TEMP%"; cmd /c "echo child=%TMP%"`
	f, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), f); err != nil {
		t.Fatalf("run: %v; out=%q err=%q", err, out.String(), errb.String())
	}
	got := out.String()
	for _, want := range []string{"shell=" + msys + "\n", "child=" + native + "\r\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q lacks %q (stderr %q)", got, want, errb.String())
		}
	}
	if strings.Count(got, "child="+native) != 2 {
		t.Errorf("output %q: want both children to see %q", got, native)
	}
}

// A bare name found on PATH through PATHEXT is displayed without the
// suffix — `type -p w` and `command -v w` print …\w for w.exe, as bash on
// Cygwin does — while the command itself still runs the .exe.
func TestWindowsPathExtHitKeepsSpelling(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile(`C:\Windows\System32\where.exe`)
	if err != nil {
		t.Skip("no where.exe")
	}
	if err := os.WriteFile(filepath.Join(dir, "w.exe"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errb strings.Builder
	r, _ := interp.New(interp.StdIO(nil, &out, &errb),
		interp.Env(expand.ListEnviron("PATH="+dir, "PATHEXT=.COM;.EXE;.BAT")))
	script := `type -p w; command -v w; w /Q w.exe; echo rc=$?; w.exe /Q w.exe; echo rc=$?; type -p w.exe`
	f, perr := syntax.NewParser().Parse(strings.NewReader(script), "")
	if perr != nil {
		t.Fatal(perr)
	}
	_ = r.Run(context.Background(), f)
	bare := filepath.Join(dir, "w")
	want := bare + "\n" + bare + "\nrc=0\nrc=0\n" + bare + ".exe\n"
	if out.String() != want {
		t.Fatalf("out=%q err=%q\nwant %q", out.String(), errb.String(), want)
	}
}
