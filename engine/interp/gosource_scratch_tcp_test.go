//go:build full && unix

package interp_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func scratchTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := d.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if !d.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(":%x", sha256.Sum256(data))
		}
		tree[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestGoSourceOriginalTCPScratchIntegrity(t *testing.T) {
	// This is the unchanged original fixture, whose literal port is 8090.
	lease := "/tmp/s118-tcp-8090-manager-lease"
	if err := os.Mkdir(lease, 0700); err != nil {
		t.Fatalf("exclusive original TCP proof lease unavailable: %v", err)
	}
	defer os.Remove(lease)
	listener, err := net.Listen("tcp", ":8090")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	src, err := os.ReadFile("testdata/gosource-scratch/tcp-server.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(src))
	if digest != "07d7f4491bd29ae68fa991da93a406d9746cc3dbd91afb97c36b8aa8b6cfec6c" {
		t.Fatalf("original fixture digest changed: %s", digest)
	}
	t.Logf("unchanged original SHA256=%s", digest)
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	runDir := filepath.Join(root, "runtime")
	scratch := filepath.Join(runDir, "tmp")
	for _, dir := range []string{sourceDir, scratch} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(sourceDir, "tcp-server.go")
	if err = os.WriteFile(path, src, 0600); err != nil {
		t.Fatal(err)
	}
	before := scratchTree(t, sourceDir)
	runtimeBefore := scratchTree(t, runDir)
	binary := filepath.Join(root, "oracle")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p=2", "-o", binary, path)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("oracle build: %v %s", err, out)
	}

	driverSource := `package main
import("bytes";"context";"fmt";"os";"path/filepath";"github.com/bashsharp/bashsharp/engine/gosource";"github.com/bashsharp/bashsharp/engine/interp";"github.com/bashsharp/bashsharp/engine/lower";"github.com/bashsharp/bashsharp/engine/syntax")
func main(){
 path:=os.Args[1];source,err:=os.ReadFile(path);if err!=nil{panic(err)}
 p,err:=gosource.Parse(bytes.NewReader(source),path,gosource.Options{RunMain:true,Importer:lower.NewModuleImporter(filepath.Dir(path))});if err!=nil{panic(err)}
 r,err:=interp.New(interp.Lang(syntax.LangBashPP),interp.Dir(os.Args[2]),interp.GoSourceModuleDir(filepath.Dir(path)),interp.StdIO(os.Stdin,os.Stdout,os.Stderr));if err!=nil{panic(err)}
 if err=r.Run(context.Background(),p.File);err!=nil{fmt.Fprintln(os.Stderr,err);os.Exit(1)}
}`
	driverPath := filepath.Join(root, "driver.go")
	if err = os.WriteFile(driverPath, []byte(driverSource), 0600); err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(root, "driver")
	build = exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p=2", "-o", driver, driverPath)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("real Runner driver build: %v %s", err, out)
	}
	for _, mode := range []string{"oracle", "interpreted"} {
		t.Run(mode, func(t *testing.T) {
			var out, errs bytes.Buffer
			cmd := exec.Command(binary)
			if mode == "interpreted" {
				cmd = exec.Command(driver, path, runDir)
			}
			env := []string{}
			for _, v := range os.Environ() {
				if !strings.HasPrefix(v, "TMPDIR=") {
					env = append(env, v)
				}
			}
			cmd.Env = append(env, "TMPDIR="+scratch)
			cmd.Dir = runDir
			cmd.WaitDelay = 5 * time.Second
			cmd.Stdout = &out
			cmd.Stderr = &errs
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			finished := false
			defer func() {
				if !finished {
					cmd.Process.Kill()
					<-done
				}
			}()
			deadline := time.Now().Add(45 * time.Second)
			var conn net.Conn
			for time.Now().Before(deadline) {
				select {
				case err := <-done:
					finished = true
					t.Fatalf("server exited before ready: %v stdout=%q stderr=%q", err, out.String(), errs.String())
				default:
				}
				conn, err = net.DialTimeout("tcp", "127.0.0.1:8090", 100*time.Millisecond)
				if err == nil {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if err != nil {
				cmd.Process.Kill()
				<-done
				finished = true
				t.Fatalf("server unavailable: %v stdout=%q stderr=%q", err, out.String(), errs.String())
			}
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			fmt.Fprint(conn, "hello adapter\n")
			ack, err := bufio.NewReader(conn).ReadString('\n')
			conn.Close()
			if err != nil || ack != "ACK: HELLO ADAPTER\n" {
				t.Fatalf("actual ACK=%q error=%v", ack, err)
			}
			if after := scratchTree(t, sourceDir); !reflect.DeepEqual(before, after) {
				t.Fatalf("source changed while live: %v", after)
			}
			if after := scratchTree(t, runDir); !reflect.DeepEqual(runtimeBefore, after) {
				t.Fatalf("runtime changed while live: %v", after)
			}
			if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			err = <-done
			finished = true
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("termination: %v", err)
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
				t.Fatalf("want SIGTERM143: %v", err)
			}
			deadline = time.Now().Add(5 * time.Second)
			for {
				listener, err = net.Listen("tcp", ":8090")
				if err == nil {
					listener.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("dependency process retained listener after host termination")
				}
				time.Sleep(25 * time.Millisecond)
			}
			if after := scratchTree(t, sourceDir); !reflect.DeepEqual(before, after) {
				t.Fatal("source changed after termination")
			}
			if after := scratchTree(t, runDir); !reflect.DeepEqual(runtimeBefore, after) {
				t.Fatal("runtime changed after termination")
			}
			if out.Len() != 0 || errs.Len() != 0 {
				t.Fatalf("unexpected original streams: stdout=%q stderr=%q", out.String(), errs.String())
			}
			t.Logf("ACK=%q status=143 stdout=%q stderr=%q source/runtime trees identical", ack, out.String(), errs.String())
		})
	}
}
