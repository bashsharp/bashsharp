package interp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// Exercise structuralPath in the generated dependency helper directly. This
// is the shape of cmd/compile/internal/types.Type.extra: a static interface
// whose dynamic value is a pointer to an interpreter-owned local type.
func TestS281GeneratedWorkerStructuralInterfacePointer(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	source, err := bashPPNativeSource(context.Background(), bashPPEvalRequest{
		Go:  goBin,
		Dir: t.TempDir(),
		Env: os.Environ(),
		LocalTypes: []bashPPLocalType{
			{Name: "funcShape", Decl: "struct { count int; next *funcShape }"},
			{Name: "typeShape", Decl: "struct { extra any; alias any; snapshot any; typedNil any }"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mailboxImports, mailboxSource := bashPPMailboxWorkerSource(false)
	source = strings.Replace(source, "//CALLBACKMAILBOXIMPORTS", mailboxImports, 1)
	source = strings.Replace(source, "//CALLBACKMAILBOX", mailboxSource, 1)
	source = strings.Replace(source, "//CONNECTION", `const bridgeNetwork = "tcp"
const bridgeAddress = "127.0.0.1:1"
const bridgeAuth = "0123456789abcdef0123456789abcdef"
const callbackMailboxPath = ""`, 1)
	source = strings.Replace(source, "func main(){", "func bridgeMain(){", 1)
	source += `
func main() {
	pointer := &funcShape{count: 7}
	pointer.next = pointer
	var typedNil *funcShape
	holder := typeShape{extra: pointer, alias: pointer, snapshot: *pointer, typedNil: typedNil}
	originalPointers.ids[reflect.ValueOf(pointer).Pointer()] = 41
	got := structuralPath(reflect.ValueOf(holder), map[uintptr]bool{})
	extra, alias := got.Fields["extra"], got.Fields["alias"]
	if extra.Interface != "interface {}" || extra.Type != typeID(reflect.TypeFor[*funcShape]()) || extra.Kind != "struct" || extra.Origin != 41 {
		panic(fmt.Sprintf("pointer = %+v", extra))
	}
	if alias.Type != extra.Type || alias.Origin != extra.Origin {
		panic(fmt.Sprintf("alias = %+v; pointer = %+v", alias, extra))
	}
	cycle := extra.Fields["next"]
	if cycle.Kind != "pointer" || cycle.Type != extra.Type || cycle.Origin != extra.Origin {
		panic(fmt.Sprintf("cycle = %+v; pointer = %+v", cycle, extra))
	}
	snapshot := got.Fields["snapshot"]
	if snapshot.Interface != "interface {}" || snapshot.Type != typeID(reflect.TypeFor[funcShape]()) || snapshot.Fields["count"].Text != "7" {
		panic(fmt.Sprintf("snapshot = %+v", snapshot))
	}
	nilPointer := got.Fields["typedNil"]
	if nilPointer.Kind != "nil" || nilPointer.Interface != "interface {}" || nilPointer.Type != typeID(reflect.TypeFor[*funcShape]()) {
		panic(fmt.Sprintf("typed nil = %+v", nilPointer))
	}
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "worker")
	if err := bashPPBuildWorkerImportcfg(context.Background(), goBin, t.TempDir(), os.Environ(), "", dir, path, binary, map[string]string{
		"bridgeAuth": "0123456789abcdef0123456789abcdef",
	}, ""); err != nil {
		t.Fatalf("build generated dependency worker: %v", err)
	}
	if output, err := exec.Command(binary).CombinedOutput(); err != nil {
		t.Fatalf("run generated dependency worker: %v: %s", err, output)
	}
}
