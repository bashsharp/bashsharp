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

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// Sprint: #243; Story: #672; Story-ID: fa5b3cf5a929

func TestS243Issue672ReflectConcreteMethodSignature(t *testing.T) {
	path := filepath.Join(runtime.GOROOT(), "test", "fixedbugs", "issue30606b.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(source)); got != "9a5475cfbb0a5a2998ddc2c640202ab2261642a1fe8f0a7b7b6160e33f93f6e4" {
		t.Fatalf("fixture changed: %s", got)
	}
	out, stderr, err := runGoSource(t, "s243-issue672", string(source))
	if err != nil || out != "" || stderr != "" {
		t.Fatalf("issue30606b: err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

func TestS243Issue672ReflectConcreteMethodSignatureNegative(t *testing.T) {
	const source = `package main
import "reflect"
type Wrong interface { AssignableTo(any) bool }
func main() { var _ Wrong = reflect.TypeOf(0) }
`
	_, err := gosource.Parse(strings.NewReader(source), "s243-issue672-negative.go", gosource.Options{RunMain: true})
	if err == nil || !strings.Contains(err.Error(), "wrong type for method AssignableTo") {
		t.Fatalf("wrong reflect method signature: %v", err)
	}
}
