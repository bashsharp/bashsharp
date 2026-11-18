package internal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// PowerShellTestSort supplies deterministic Unix-style line sorting as a real
// native child. Windows System32 sort has a different line-ending contract.
func PowerShellTestSort(t interface {
	Helper()
	TempDir() string
	Context() context.Context
	Fatal(...any)
	Fatalf(string, ...any)
}, bin string) {
	t.Helper()
	source := `package main
import ("bufio"; "fmt"; "os"; "sort")
func main() {
 scanner := bufio.NewScanner(os.Stdin)
 var lines []string
 for scanner.Scan() { lines=append(lines,scanner.Text()) }
 if err:=scanner.Err(); err!=nil { fmt.Fprintln(os.Stderr,err); os.Exit(1) }
 sort.Strings(lines)
 for _,line:=range lines { fmt.Fprintln(os.Stdout,line) }
}
`
	file := filepath.Join(t.TempDir(), "sort.go")
	if err := os.WriteFile(file, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(bin, "sort")
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", output, file)
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sort fixture: %v\n%s", err, data)
	}
}
