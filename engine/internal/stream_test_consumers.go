package internal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// StreamTestConsumers supplies the native downstream processes needed by
// streaming fixtures on Windows. It does not install a shell or enable Unix
// signal tests. Unix hosts use their existing cat, wc and head executables.
func StreamTestConsumers(t interface {
	Helper()
	TempDir() string
	Context() context.Context
	Fatalf(string, ...any)
	Setenv(string, string)
}) {
	t.Helper()
	if runtime.GOOS != "windows" {
		return
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "consumer.go")
	const program = `package main
import("bufio";"bytes";"fmt";"io";"os";"path/filepath";"strings")
func main(){
 switch strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])),".exe") {
 case "cat":
  if _,err:=io.Copy(os.Stdout,os.Stdin);err!=nil{os.Exit(1)}
 case "wc":
  if len(os.Args)!=2||os.Args[1]!="-l"{os.Exit(2)}
  b:=make([]byte,32768); lines:=0
  for {n,err:=os.Stdin.Read(b);lines+=bytes.Count(b[:n],[]byte{'\n'});if err==io.EOF{break};if err!=nil{os.Exit(1)}}
  fmt.Println(lines)
 case "head":
  if len(os.Args)!=3||os.Args[1]!="-n"||os.Args[2]!="1"{os.Exit(2)}
  line,err:=bufio.NewReader(os.Stdin).ReadString('\n');if err!=nil&&err!=io.EOF{os.Exit(1)}
  if _,err:=io.WriteString(os.Stdout,line);err!=nil{os.Exit(1)}
 default:os.Exit(2)
 }
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatalf("stream consumer source: %v", err)
	}
	cat := filepath.Join(dir, "cat.exe")
	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", cat, source).CombinedOutput(); err != nil {
		t.Fatalf("build native stream consumer: %v\n%s", err, out)
	}
	data, err := os.ReadFile(cat)
	if err != nil {
		t.Fatalf("read native stream consumer: %v", err)
	}
	for _, name := range []string{"wc.exe", "head.exe"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
