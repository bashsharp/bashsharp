package interp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// typeparam/cons.go asks the dependency worker to register instantiated local
// generic types. Those registrations must name the declarations the worker
// actually emits, rather than the source package's unqualified names.
func TestS374GeneratedWorkerBuildsLocalGenericTypeRegistrations(t *testing.T) {
	program, err := gosource.Parse(strings.NewReader(`package main
type List[A any] interface{}
type Cons[A any] struct { Head A; Tail List[A] }
type Nil[A any] struct{}
func main() { var _ List[int] = Cons[int]{}; _ = Nil[int]{} }
`), "cons.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{bashPPGoSourceFile: program.File}
	descriptors, _ := r.bashPPBuildLocalTypeDescriptors()
	source, err := bashPPNativeSource(context.Background(), bashPPEvalRequest{
		LocalTypes:   descriptors,
		GenericTypes: r.bashPPBuildGenericBridgeTypes(),
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
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if err := bashPPBuildWorkerImportcfg(context.Background(), goBin, t.TempDir(), os.Environ(), "", dir, path, filepath.Join(dir, "worker"), nil, ""); err != nil {
		t.Fatalf("build generated dependency worker: %v", err)
	}
}

// A generic bridge candidate is not necessarily a local declaration: this
// imported instantiation has a local type argument, but its generic constructor
// belongs to hash/maphash and must retain the registration introduced by S281.
func TestS374GeneratedWorkerRegistersDeclaredImportedGeneric(t *testing.T) {
	program, err := gosource.Parse(strings.NewReader(`package main
import "hash/maphash"
type Thing struct{ Name string }
func main() { _ = make(chan maphash.Hasher[Thing], 1) }
`), "generic.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{bashPPGoSourceFile: program.File, bashPPImports: map[string]string{"maphash": "hash/maphash"}}
	descriptors, _ := r.bashPPBuildLocalTypeDescriptors()
	dir := t.TempDir()
	source, err := bashPPNativeSource(context.Background(), bashPPEvalRequest{
		Go: filepath.Join(runtime.GOROOT(), "bin", "go"), Dir: dir, Env: os.Environ(),
		Imports: map[string]string{"maphash": "hash/maphash"}, LocalTypes: descriptors, GenericTypes: r.bashPPBuildGenericBridgeTypes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source, `"maphash.Hasher[Thing]": reflect.TypeFor[`) {
		t.Fatalf("worker omitted imported generic registration:\n%s", source)
	}
}
