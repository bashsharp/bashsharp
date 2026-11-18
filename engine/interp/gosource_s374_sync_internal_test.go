//go:build full

package interp

import (
	"bytes"
	"context"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strings"
	"testing"
	"time"
)

func TestS374ResidentSyncNoRequests(t *testing.T) {
	counts, mu := s374TraceRequests(t)
	source := `package main; import "sync"
 type Box struct { sync.Mutex; wg sync.WaitGroup; n int }
 func main(){ b:=new(Box); b.wg.Add(1); go func(){ b.Lock(); b.n++; b.Unlock(); b.wg.Done() }(); b.wg.Wait(); println(b.n) }`
	p, err := gosource.Parse(strings.NewReader(source), "sync.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	r, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = r.Run(ctx, p.File); err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	if got := out.String() + stderr.String(); got != "1\n" {
		t.Fatalf("output %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for key, n := range counts {
		if strings.Contains(key, "sync.") {
			t.Errorf("resident sync issued %d %s requests", n, key)
		}
	}
}

func TestS374ResidentSyncCancellation(t *testing.T) {
	for _, operation := range []string{"Lock", "Wait"} {
		t.Run(operation, func(t *testing.T) {
			r, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			s := new(goSourceResidentSync)
			typ := "sync.Mutex"
			if operation == "Lock" {
				s.mutex.Lock()
				s.locked = true
			} else {
				s.wg.Add(1)
				s.count = 1
				typ = "sync.WaitGroup"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			_, handled, err := r.goSourceResidentSyncRequest(ctx, bashPPBridgeRequest{Op: "call", Selector: operation, Receiver: &bashPPBridgeValue{Kind: "handle", Type: typ, residentSync: s}})
			if !handled || err != context.DeadlineExceeded {
				t.Fatalf("handled=%v error=%v", handled, err)
			}
		})
	}
}

func TestS374ResidentSyncTransportRefusal(t *testing.T) {
	s := new(bashPPNativeSession)
	v := bashPPBridgeValue{Kind: "struct", Fields: map[string]bashPPBridgeValue{"M": {Kind: "handle", Type: "sync.Mutex", residentSync: new(goSourceResidentSync)}}}
	_, err := s.request(context.Background(), bashPPEvalRequest{}, bashPPBridgeRequest{Op: "call", Selector: "fmt.Sprint", Args: []bashPPBridgeValue{v}})
	if err == nil || !strings.Contains(err.Error(), "resident sync cannot enter native transport") {
		t.Fatalf("%v", err)
	}
}
