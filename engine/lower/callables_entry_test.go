//go:build full

package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests compile the emitted entry helper in a separate importable module.
// The body explicitly models one native marked entry and one delegated shell
// command. They do not claim that the main compiler dispatches shell regions.
func TestProgramEntryHelperProviderArtifact(t *testing.T) {
	e := &emitter{prefix: "entry_"}
	body := `mode,_ := entry_program.Session.Get("ASSIST")
 caller:=entry_program
 if mode.Str=="1" {caller=caller.Block()}
 action,err:=caller.Enter(entry_rt.Site{Name:"action"},true)
 if err!=nil {entry_program.Fail(err);return}
 action.Print("native:")
 if err:=action.Session.Shell(action.Context, "agentic { provider \"$LABEL\"; }");err!=nil {
  panic(&entry_rt.ExitError{Status:1,Err:err})
 }
`
	source := `package generated
import(entry_fmt "fmt";entry_os "os";entry_rt "github.com/bashsharp/bashsharp/engine/lower/shellrt";entry_shellexec "github.com/bashsharp/bashsharp/engine/lower/shellrt/shellexec")
` + e.programEntrySourceNamed(body, true, "Execute")
	dir := t.TempDir()
	writeEntryModule(t, dir)
	generated := filepath.Join(dir, "generated", "entry.go")
	writeEntryFile(t, generated, source)
	harness := filepath.Join(dir, "host", "entry_test.go")
	writeEntryFile(t, harness, entryProviderHarness)
	binary := filepath.Join(dir, "provider.test")
	runEntryCommand(t, dir, "go", "test", "-mod=mod", "-race", "-c", "-o", binary, "./host")
	if err := os.Remove(generated); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(harness); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.v")
	command.Dir = t.TempDir()
	command.Env = []string{"PATH=/no-tools"}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("entry artifact: %v\n%s", err, output)
	}
}
func TestProgramEntryHelperTypedOnlyAndDefaultMain(t *testing.T) {
	e := &emitter{prefix: "entry_"}
	source := `package main
import(entry_fmt "fmt";entry_os "os";entry_rt "github.com/bashsharp/bashsharp/engine/lower/shellrt")
` + e.programEntrySource(`panic(&entry_rt.ValueError{Code:"BASHPP-ENIL-DEREF",Message:"dereference of nil pointer"})`, false)
	if strings.Contains(source, "func Execute(") {
		t.Fatal("default entry collides with source Execute")
	}
	dir := t.TempDir()
	writeEntryModule(t, dir)
	path := filepath.Join(dir, "main.go")
	writeEntryFile(t, path, source)
	dependencies := runEntryCommand(t, dir, "go", "list", "-mod=mod", "-deps", ".")
	if strings.Contains(dependencies, "github.com/bashsharp/bashsharp/engine/interp") || strings.Contains(dependencies, "/shellexec") {
		t.Fatalf("typed-only entry links interpreter:\n%s", dependencies)
	}
	binary := filepath.Join(dir, "program")
	runEntryCommand(t, dir, "go", "build", "-mod=mod", "-o", binary, ".")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary)
	command.Dir = t.TempDir()
	command.Env = []string{"PATH=/no-tools"}
	output, err := command.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 2 || string(output) != "BASHPP-ENIL-DEREF: dereference of nil pointer\n" {
		t.Fatalf("main status/message: %v %q", err, output)
	}
}
func writeEntryModule(t *testing.T, dir string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeEntryFile(t, filepath.Join(dir, "go.mod"), "module entryartifact\n\ngo 1.25\n\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => "+filepath.ToSlash(root)+"\n")
}
func writeEntryFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}
func runEntryCommand(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return string(output)
}

const entryProviderHarness = `package host_test
import("bytes";"context";"errors";"fmt";"io";"strings";"sync";"sync/atomic";"testing";"time";"entryartifact/generated";"github.com/bashsharp/bashsharp/engine/interp";rt "github.com/bashsharp/bashsharp/engine/lower/shellrt";"github.com/bashsharp/bashsharp/engine/lower/shellrt/shellexec")
func TestProviderEntryInstances(t *testing.T){
 var group sync.WaitGroup
 for i:=0;i<12;i++ {group.Add(1);go func(index int){defer group.Done();runProvider(t,fmt.Sprint(index),"success",true)}(i)}
 group.Wait()
 for _,mode:=range []string{"status","error","cancel"} {t.Run(mode,func(t *testing.T){runProvider(t,"single",mode,true)})}
 t.Run("denied",func(t *testing.T){runProvider(t,"denied","success",false)})
}
func runProvider(t *testing.T,label,mode string,allowed bool){
 t.Helper();dir:=t.TempDir();var out,diagnostic bytes.Buffer
 var calls,builds atomic.Int32
 providerFailure:=errors.New("provider unavailable")
 ctx:=context.Background();cancel:=func(){}
 if mode=="cancel" {ctx,cancel=context.WithTimeout(ctx,50*time.Millisecond)};defer cancel()
 factory:=shellexec.New(shellexec.BashPP(),shellexec.RunnerOptions(interp.ExecHandler(func(ctx context.Context,args []string)error{
  calls.Add(1);hc:=interp.HandlerCtx(ctx)
  if !hc.Agentic || !rt.Agentic(ctx) || hc.Dir!=dir || len(args)!=2 || args[0]!="provider" || args[1]!=label+" input" {return fmt.Errorf("bad request: args=%q dir=%s scope=%t native=%t",args,hc.Dir,hc.Agentic,rt.Agentic(ctx))}
  input,err:=io.ReadAll(hc.Stdin);if err!=nil{return err};if string(input)!="stdin:"+label{return fmt.Errorf("bad stdin %q",input)}
  switch mode {
  case "cancel": <-ctx.Done();return ctx.Err()
  case "error": return providerFailure
  case "status": fmt.Fprint(hc.Stdout,"partial");fmt.Fprint(hc.Stderr,"provider-status\n");return interp.ExitStatus(7)
  default: fmt.Fprint(hc.Stdout,"reply:"+label);return nil
  }
 })))
 countFactory:=func(state rt.State,stdio rt.Stdio)(rt.ShellRunner,error){builds.Add(1);return factory(state,stdio)}
 assist:="0";if allowed {assist="1"}
 code,err:=generated.Execute(rt.WithContext(ctx),rt.WithDir(dir),rt.WithEnviron("BASHY_AGENTIC=1"),rt.WithVars(map[string]rt.Var{"LABEL":{Kind:rt.Scalar,Str:label+" input"},"ASSIST":{Kind:rt.Scalar,Str:assist}}),rt.WithStdio(strings.NewReader("stdin:"+label),&out,&diagnostic),rt.WithShellFactory(countFactory))
 if builds.Load()!=1 {t.Errorf("factory count=%d",builds.Load())}
 if !allowed {if calls.Load()!=0 || code!=1 || err!=nil || out.Len()!=0 || diagnostic.String()!="action: agentic action requires an explicit agentic { ...; } scope\n" {t.Errorf("denied: calls=%d code=%d error=%v stdout=%q stderr=%q",calls.Load(),code,err,out.String(),diagnostic.String())};return}
 if calls.Load()!=1 {t.Errorf("provider calls=%d",calls.Load())}
 switch mode {
 case "success": if code!=0 || err!=nil || out.String()!="native:reply:"+label || diagnostic.Len()!=0 {t.Errorf("success: code=%d err=%v out=%q stderr=%q",code,err,out.String(),diagnostic.String())}
 case "status": if code!=7 || err!=nil || out.String()!="native:partial" || diagnostic.String()!="provider-status\n" {t.Errorf("status: code=%d err=%v out=%q stderr=%q",code,err,out.String(),diagnostic.String())}
 case "error": if code!=1 || !errors.Is(err,providerFailure) || out.String()!="native:" || diagnostic.Len()!=0 {t.Errorf("error: code=%d err=%v out=%q stderr=%q",code,err,out.String(),diagnostic.String())}
 case "cancel": if code!=1 || !errors.Is(err,context.DeadlineExceeded) || out.String()!="native:" || diagnostic.Len()!=0 {t.Errorf("cancel: code=%d err=%v out=%q stderr=%q",code,err,out.String(),diagnostic.String())}
 }
}
`
