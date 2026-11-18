//go:build full

package lower_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

func TestGoSourceStatusRace(t *testing.T) {
	const source = `package main
import("fmt";"sync")
type counter struct{ value int }
func pair(v int)(int,int){return v,v+1}
func work(done *sync.WaitGroup) {
 defer done.Done()
 c:=counter{}
 for i:=0;i<1000;i++ { c.value=i; a,b:=pair(i); a,b=pair(a); _=b; _=a; c.value++ }
}
func main(){var done sync.WaitGroup;done.Add(4);for i:=0;i<4;i++{go work(&done)};done.Wait();fmt.Println("done")}
`
	testGoSourceStatusArtifact(t, source)
}

func testGoSourceStatusArtifact(t *testing.T, source string) {
	t.Helper()
	program, err := gosource.Parse(strings.NewReader(source), "original.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: "original.go"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	var streams [2][2][]byte
	for i, data := range [][]byte{[]byte(source), result.Source} {
		dir := filepath.Join(root, fmt.Sprint(i))
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		mod := "module control\n\ngo 1.27.0\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + repo + "\n"
		for name, b := range map[string][]byte{"go.mod": []byte(mod), "main.go": data} {
			if err = os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
				t.Fatal(err)
			}
		}
		artifact := filepath.Join(dir, "program")
		cmd := exec.CommandContext(ctx, "go", "build", "-mod=mod", "-race", "-p=2", "-o", artifact, ".")
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build mode%d: %v\n%s\n%s", i, err, b, data)
		}
		if err = os.Remove(filepath.Join(dir, "main.go")); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		cmd = exec.CommandContext(ctx, artifact)
		cmd.Dir = dir
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err = cmd.Run()
		streams[i] = [2][]byte{stdout.Bytes(), stderr.Bytes()}
		if err != nil {
			t.Fatalf("artifact mode%d: %v\nstdout:%s\nstderr:%s\ngenerated:\n%s", i, err, stdout.Bytes(), stderr.Bytes(), result.Source)
		}
	}
	if !bytes.Equal(streams[0][0], streams[1][0]) || !bytes.Equal(streams[0][1], streams[1][1]) {
		t.Fatalf("raw stream mismatch: %q != %q", streams[0], streams[1])
	}
	for _, forbidden := range []string{"rt.Status", "rt.SetStatus(", "rt.Fail(", "rt.Exit()"} {
		if strings.Contains(string(result.Source), forbidden) {
			t.Fatalf("ordinary Go source contains shell status operation %s\n%s", forbidden, result.Source)
		}
	}
}

func TestGoSourceStatusRecoveredPanicRace(t *testing.T) {
	const source = `package main
import("fmt";"sync")
func work(done *sync.WaitGroup,out chan bool){
 defer done.Done()
 defer func(){v:=recover();out<-v=="original"}()
 defer panic("original")
}
func main(){var done sync.WaitGroup;out:=make(chan bool,32);done.Add(32);for i:=0;i<32;i++{go work(&done,out)};done.Wait();close(out);count:=0;for ok:=range out{if ok{count++}};fmt.Println(count)}
`
	testGoSourceStatusArtifact(t, source)
}

// This is an original Go program passed through the actual frontend and
// lowerer, unlike a direct shellrt API test. All sockets use ephemeral ports.
func TestGoSourceStatusEphemeralTCPRace(t *testing.T) {
	const source = `package main
import("fmt";"io";"net";"sync")
func handle(conn net.Conn,done *sync.WaitGroup){
 defer done.Done();defer conn.Close()
 b:=make([]byte,4);_,err:=io.ReadFull(conn,b);if err!=nil{panic(err)}
 if string(b)!="ping"{panic("request")};_,err=conn.Write([]byte("pong"));if err!=nil{panic(err)}
}
func serve(listener net.Listener,done *sync.WaitGroup,handlers *sync.WaitGroup){
 defer done.Done()
 for i:=0;i<8;i++{conn,err:=listener.Accept();if err!=nil{panic(err)};go handle(conn,handlers)}
}
func client(addr string,done *sync.WaitGroup){
 defer done.Done();conn,err:=net.Dial("tcp",addr);if err!=nil{panic(err)};defer conn.Close()
 _,err=conn.Write([]byte("ping"));if err!=nil{panic(err)}
 b:=make([]byte,4);_,err=io.ReadFull(conn,b);if err!=nil{panic(err)};if string(b)!="pong"{panic("response")}
}
func main(){
 listener,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{panic(err)};defer listener.Close()
 var serving,handlers,clients sync.WaitGroup;serving.Add(1);handlers.Add(8);clients.Add(8)
 go serve(listener,&serving,&handlers)
 for i:=0;i<8;i++{go client(listener.Addr().String(),&clients)}
 clients.Wait();serving.Wait();handlers.Wait();fmt.Println("8 ping/pong exchanges")
}
`
	testGoSourceStatusArtifact(t, source)
}

func TestGoSourceStatusOperationPanicRace(t *testing.T) {
	const source = `package main
import("fmt";"sync")
func work(done *sync.WaitGroup,out chan bool){
 defer done.Done();defer func(){v:=recover();out<-fmt.Sprint(v)=="runtime error: negative shift amount"}()
 value,count:=1,-1;value<<=count;panic("unreachable")
}
func main(){var done sync.WaitGroup;out:=make(chan bool,32);done.Add(32);for i:=0;i<32;i++{go work(&done,out)};done.Wait();close(out);count:=0;for ok:=range out{if ok{count++}};fmt.Println(count)}
`
	testGoSourceStatusArtifact(t, source)
}
