//go:build full

package interp

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
// Protocol boundary negatives use real dependency handles. They are not an
// original-program coverage claim; positive program differentials use Runner.
import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNativeMemberSessionAndAccessBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	session := &bashPPNativeSession{}
	defer session.close()
	req := bashPPEvalRequest{Go: filepath.Join(runtime.GOROOT(), "bin", "go"), Dir: t.TempDir(), Env: os.Environ(), RuntimeEnv: []string{}, Imports: map[string]string{"url": "net/url"}, Stdout: io.Discard, Stderr: io.Discard, Bridge: session}
	values, err := session.request(ctx, req, bashPPBridgeRequest{Op: "call", Selector: "url.Parse", Args: []bashPPBridgeValue{{Kind: "string", Text: "https://ada:secret@example.com/path"}}})
	if err != nil || len(values) != 2 {
		t.Fatalf("real native handle prerequisite: %v %v", values, err)
	}
	root := values[0]
	if root.NativeType != "*net/url.URL" {
		t.Fatalf("ambiguous native pointer type: %q", root.NativeType)
	}
	var forged bashPPBridgeValue
	if err := json.Unmarshal([]byte(`{"Callable":"sync.Once.Do","callable":"sync.Once.Do"}`), &forged); err != nil {
		t.Fatal(err)
	}
	if forged.Callable != "" {
		t.Fatal("worker supplied callback policy provenance")
	}

	fields, err := session.request(ctx, req, bashPPBridgeRequest{Op: "member", Selector: "User", Receiver: &root})
	if err != nil || len(fields) != 1 {
		t.Fatalf("native field: %v %v", fields, err)
	}
	user := fields[0]
	if _, err := session.request(ctx, req, bashPPBridgeRequest{Op: "member", Selector: "username", Receiver: &user}); err == nil || !strings.Contains(err.Error(), "unexported field") {
		t.Fatalf("private storage exposed: %v", err)
	}
	if _, err := session.request(ctx, req, bashPPBridgeRequest{Op: "member", Selector: "username", AccessPackage: "example.com/foreign", Receiver: &user}); err == nil || !strings.Contains(err.Error(), "unexported field") {
		t.Fatalf("foreign package identity exposed private storage: %v", err)
	}
	private, err := session.request(ctx, req, bashPPBridgeRequest{Op: "member", Selector: "username", AccessPackage: "net/url", Receiver: &user})
	if err != nil || len(private) != 1 || private[0].stringText() != "ada" {
		t.Fatalf("declaring package could not read its private storage: %v %v", private, err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			for range 4 {
				out, e := session.request(ctx, req, bashPPBridgeRequest{Op: "call", Selector: "Username", Receiver: &user})
				if e != nil {
					failures <- e
					return
				}
				if len(out) != 1 || out[0].Text != "ada" {
					failures <- errors.New("concurrent member read changed")
					return
				}
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	expired, expire := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer expire()
	if _, err := session.request(expired, req, bashPPBridgeRequest{Op: "member", Selector: "Host", Receiver: &root}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired member request was accepted: %v", err)
	}
	session.close()
	next := &bashPPNativeSession{}
	defer next.close()
	req.Bridge = next
	if _, err := next.request(ctx, req, bashPPBridgeRequest{Op: "member", Selector: "Host", Receiver: &root}); err == nil || !strings.Contains(err.Error(), "another dependency session") {
		t.Fatalf("stale native member accepted by new session: %v", err)
	}
}
