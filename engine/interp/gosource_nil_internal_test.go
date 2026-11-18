package interp

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
// Protocol negatives use a real dependency process; program coverage is the
// separate Runner/native-oracle/source-free-artifact differential suite.
import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGoSourceNativeNilRejectsInvalidIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	session := &bashPPNativeSession{}
	defer session.close()
	req := bashPPEvalRequest{Go: filepath.Join(runtime.GOROOT(), "bin", "go"), Dir: t.TempDir(), Env: os.Environ(), RuntimeEnv: []string{}, Imports: map[string]string{"f": "fmt", "s": "strings"}, Stdout: io.Discard, Stderr: io.Discard, Bridge: session}
	for _, test := range []struct{ name, selector, typ, want string }{
		{"scalar", "f.Sprint", "int", "nil is not assignable to int"},
		{"unknown", "f.Sprint", "UnknownOriginalType", "unregistered nil bridge type"},
		{"incompatible", "s.NewReader", "*int", "nil *int is not assignable to string"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := session.request(ctx, req, bashPPBridgeRequest{Op: "call", Selector: test.selector, Args: []bashPPBridgeValue{{Kind: "nil", Type: test.typ}}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid nil accepted or wrong boundary: %v", err)
			}
		})
	}
}
