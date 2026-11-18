//go:build full

package interp_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Sprint: #243; Story: #673; Story-ID: f24307569417

func issue79874Original(t *testing.T) string {
	t.Helper()
	path := filepath.Join(runtime.GOROOT(), "test", "fixedbugs", "issue79874.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(source)); got != "32257ac1759d8a38e7947ad67221fcd39e204c8742af529605360c5bb1833b54" {
		t.Fatalf("issue79874 original source changed: %s", got)
	}
	return string(source)
}

func TestS243OriginalIssue79874NativeIndexedPointer(t *testing.T) {
	source := issue79874Original(t)
	if !strings.Contains(source, "unsafe.Slice(&s[l], pageSize)") {
		t.Fatal("issue79874 source lost the indexed unsafe.Slice target")
	}
	out, stderr, err := runGoSource(t, "s243-issue79874", source)
	if err != nil || stderr != "" || out != "" {
		t.Fatalf("interpreted issue79874: err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

func TestS243NativeIndexedPointerBoundsAndAliasControls(t *testing.T) {
	out, stderr, err := runGoSource(t, "s243-native-indexed-pointer-bounds", `package main
import("syscall";"unsafe")
func main(){s,err:=syscall.Mmap(-1,0,1,syscall.PROT_READ,syscall.MAP_ANON|syscall.MAP_PRIVATE);if err!=nil{panic(err)};_ = unsafe.Slice(&s[1],1)}`)
	if err == nil || !strings.Contains(stderr, "index out of range") {
		t.Fatalf("native indexed pointer bounds: err=%v stdout=%q stderr=%q", err, out, stderr)
	}
	// Sprint 247 gave local unsafe.Slice a storage-span model: the result
	// aliases the interpreter's own backing, so it stays local and writes
	// are visible both ways. A copy across the native boundary would print
	// 2 and 9.
	out, stderr, err = runGoSource(t, "s243-native-indexed-pointer-local-alias", `package main
import("unsafe";"fmt")
func main(){s:=[]byte{1,2};p:=&s[1];v:=unsafe.Slice(p,1);s[1]=9;fmt.Println(v[0]);v[0]=7;fmt.Println(s[1])}`)
	if err != nil || stderr != "" || out != "9\n7\n" {
		t.Fatalf("local slice alias: err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}
