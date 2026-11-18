//go:build full

package interp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Hold an actual import-bridge program alive while another go build imports
// the script's package. A loose package-main scratch file in that package
// makes the concurrent consumer fail with "found packages ... and main".
func TestBashPPImportBridgeDoesNotContaminatePackage(t *testing.T) {
	for _, mode := range []string{"call", "values"} {
		t.Run(mode, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				"go.mod":           "module example.test/bridgefixture\n\ngo 1.25\n",
				"subject.go":       "package subject\nconst Value = \"subject\"\n",
				"consumer/main.go": "package main\nimport (\"fmt\"; subject \"example.test/bridgefixture\")\nfunc main(){fmt.Print(subject.Value)}\n",
				"sidecar.txt":      "relative-sidecar",
				"internal/fixtureio/io.go": `package fixtureio
import ("fmt"; "os"; "time")
func Wait() string {
 data,err:=os.ReadFile("sidecar.txt");if err!=nil{panic(err)}
 cwd,err:=os.Getwd();if err!=nil{panic(err)}
 if err=os.WriteFile("bridge.ready",[]byte(cwd),0600);err!=nil{panic(err)}
 for { if _,err=os.Stat("bridge.release");err==nil{break};time.Sleep(time.Millisecond) }
 return string(data)
}
func Emit(){fmt.Print(Wait())}
`,
			}
			for name, body := range files {
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
			if runtime.GOOS == "windows" {
				goBin += ".exe"
			}
			env := setEnvString(os.Environ(), "GOTOOLCHAIN", "local")
			env = setEnvString(env, "GOWORK", "off")
			scratch := t.TempDir()
			env = setEnvString(env, "TMPDIR", scratch)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			release := filepath.Join(root, "bridge.release")
			var stdout, stderr bytes.Buffer
			req := bashPPEvalRequest{Go: goBin, Dir: root, Env: env, Stdout: &stdout, Stderr: &stderr, Imports: map[string]string{"fixtureio": "example.test/bridgefixture/internal/fixtureio"}}
			req.Selector = []string{"fixtureio", "Emit"}
			if mode == "values" {
				req.Selector[1] = "Wait"
				req.Results = 1
			}
			done := make(chan error, 1)
			finished := make(chan struct{})
			t.Cleanup(func() {
				_ = os.WriteFile(release, nil, 0600)
				cancel()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("bridge worker did not clean up")
				}
			})
			go func() {
				defer close(finished)
				if mode == "call" {
					done <- (nativeBashPPEvaluator{}).Call(ctx, req)
					return
				}
				values, err := (nativeBashPPEvaluator{}).Values(ctx, req)
				if err == nil && (len(values) != 1 || values[0] != "relative-sidecar") {
					err = fmt.Errorf("bridge values = %#v", values)
				}
				done <- err
			}()
			// The ready marker is written only by the running bridge executable, after
			// the local internal import built and its cwd-relative sidecar was read.
			deadline := time.NewTimer(60 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
		waitReady:
			for {
				select {
				case err := <-done:
					t.Fatalf("bridge ended before ready: %v; %s", err, stderr.String())
				case <-deadline.C:
					t.Fatal("bridge did not become ready")
				case <-ticker.C:
					if cwd, err := os.ReadFile(filepath.Join(root, "bridge.ready")); err == nil {
						want, _ := filepath.EvalSymlinks(root)
						got, _ := filepath.EvalSymlinks(string(cwd))
						if got != want {
							t.Fatalf("runtime cwd=%q want %q", got, want)
						}
						break waitReady
					}
				}
			}
			consumer := exec.CommandContext(ctx, goBin, "build", "-o", filepath.Join(root, "consumer.bin"), "./consumer")
			consumer.Dir, consumer.Env = root, env
			if out, err := consumer.CombinedOutput(); err != nil {
				t.Fatalf("concurrent import build: %v\n%s", err, out)
			}
			listing := exec.CommandContext(ctx, goBin, "list", "./...")
			listing.Dir, listing.Env = root, env
			if out, err := listing.CombinedOutput(); err != nil || strings.Contains(string(out), "bashpp-") {
				t.Fatalf("package enumeration included bridge scratch: %v\n%s", err, out)
			}
			if paths, _ := filepath.Glob(filepath.Join(root, ".bashpp-eval-*")); len(paths) != 0 {
				t.Fatalf("source tree contaminated: %v", paths)
			}
			dirs, err := filepath.Glob(filepath.Join(scratch, ".bashpp-eval-*"))
			if err != nil || len(dirs) != 1 {
				t.Fatalf("private work directories=%v error=%v", dirs, err)
			}
			info, err := os.Stat(dirs[0])
			if err != nil || info.Mode().Perm()&0077 != 0 {
				t.Fatalf("private directory permissions: %v %v", info, err)
			}
			sources, err := filepath.Glob(filepath.Join(dirs[0], "*.go"))
			if err != nil || len(sources) != 1 {
				t.Fatalf("private source files=%v error=%v", sources, err)
			}
			info, err = os.Stat(sources[0])
			if err != nil || info.Mode().Perm()&0077 != 0 {
				t.Fatalf("private source permissions: %v %v", info, err)
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("bridge call: %v\n%s", err, stderr.String())
				}
			case <-ctx.Done():
				t.Fatal("bridge did not exit after release")
			}
			if mode == "call" && stdout.String() != "relative-sidecar" {
				t.Fatalf("stdout=%q", stdout.String())
			}
			dirs, _ = filepath.Glob(filepath.Join(scratch, ".bashpp-eval-*"))
			if len(dirs) != 0 {
				t.Fatalf("bridge leaked work directory: %v", dirs)
			}
		})
	}
}

func TestBashPPImportBridgeCleansFailedWork(t *testing.T) {
	for _, mode := range []string{"call", "values"} {
		for _, failure := range []string{"build", "runtime", "canceled"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				root := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/cleanup\n\ngo 1.25\n"), 0600); err != nil {
					t.Fatal(err)
				}
				goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
				if runtime.GOOS == "windows" {
					goBin += ".exe"
				}
				env := setEnvString(os.Environ(), "GOTOOLCHAIN", "local")
				env = setEnvString(env, "GOWORK", "off")
				scratch := t.TempDir()
				env = setEnvString(env, "TMPDIR", scratch)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if failure == "canceled" {
					cancel()
				}
				var stdout, stderr bytes.Buffer
				req := bashPPEvalRequest{Go: goBin, Dir: root, Env: env, Stdout: &stdout, Stderr: &stderr, Imports: map[string]string{"strings": "strings"}, Selector: []string{"strings", "Repeat"}, Args: []string{`"x"`, `-1`}, Results: 1}
				if failure == "build" {
					req.Selector[1] = "DoesNotExist"
				}
				var err error
				if mode == "call" {
					err = (nativeBashPPEvaluator{}).Call(ctx, req)
				} else {
					_, err = (nativeBashPPEvaluator{}).Values(ctx, req)
				}
				if err == nil {
					t.Fatalf("%s unexpectedly succeeded", failure)
				}
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 || entries[0].Name() != "go.mod" {
					t.Fatalf("failed bridge leaked artifacts: %v", entries)
				}
				if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
					t.Fatalf("private scratch leaked artifacts: %v %v", entries, err)
				}
			})
		}
	}
}
