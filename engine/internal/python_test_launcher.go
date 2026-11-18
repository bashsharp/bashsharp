package internal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

// PythonTestLauncher builds the fixture's declared runtime launcher. Windows
// needs a native executable: a POSIX shebang is not a Windows launch contract.
// marker is the BASHPP_SELECTED_RUNTIME value, or empty for a plain launcher.
func PythonTestLauncher(t interface {
	Helper()
	TempDir() string
	Context() context.Context
	Fatalf(string, ...any)
}, python, path, marker string) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		body := "#!/bin/sh\n"
		if marker != "" {
			body += "export BASHPP_SELECTED_RUNTIME=" + strconv.Quote(marker) + "\n"
		}
		body += "exec " + strconv.Quote(python) + " \"$@\"\n"
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatalf("launcher: %v", err)
		}
		return path
	}
	path += ".exe"
	source := fmt.Sprintf(`package main
import("os";"os/exec")
func main(){
 if %q!=""{os.Setenv("BASHPP_SELECTED_RUNTIME",%q)}
 cmd:=exec.Command(%q,os.Args[1:]...)
 cmd.Stdin,cmd.Stdout,cmd.Stderr=os.Stdin,os.Stdout,os.Stderr
 if err:=cmd.Run();err!=nil{if e,ok:=err.(*exec.ExitError);ok{os.Exit(e.ExitCode())};os.Exit(127)}
}
`, marker, marker, python)
	input := filepath.Join(t.TempDir(), "launcher.go")
	if err := os.WriteFile(input, []byte(source), 0o600); err != nil {
		t.Fatalf("launcher source: %v", err)
	}
	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", path, input).CombinedOutput(); err != nil {
		t.Fatalf("launcher build: %v\n%s", err, out)
	}
	return path
}
