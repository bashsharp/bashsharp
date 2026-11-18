//go:build full

package interp

// Sprint: #376; Story: #1549; Story-ID: 91561dbe8537
//
// Narrow internal tests for resident RWMutex: zero bridge requests on the
// certified path, cancellation of parked readers/writers, and refusal to
// serialize host-only sync state into native transport.

import (
	"bytes"
	"context"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strings"
	"testing"
	"time"
)

func TestS376ResidentRWMutexNoRequests(t *testing.T) {
	counts, mu := s374TraceRequests(t)
	source := `package main; import "sync"
 type Box struct { sync.RWMutex; n int }
 func main(){ b:=new(Box); b.RLock(); b.n++; b.RUnlock(); b.Lock(); b.n++; b.Unlock(); println(b.n) }`
	p, err := gosource.Parse(strings.NewReader(source), "rwmutex.go", gosource.Options{RunMain: true})
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
	if got := out.String() + stderr.String(); got != "2\n" {
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

func TestS376ResidentRWMutexCancellation(t *testing.T) {
	for _, operation := range []string{"RLock", "Lock"} {
		t.Run(operation, func(t *testing.T) {
			r, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			s := new(goSourceResidentSync)
			typ := "sync.RWMutex"
			if operation == "RLock" {
				s.rwmu.Lock()
				s.wlocked = true
			} else {
				s.rwmu.RLock()
				s.readers = 1
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

func TestS376ResidentRWMutexTransportRefusal(t *testing.T) {
	s := new(bashPPNativeSession)
	v := bashPPBridgeValue{Kind: "struct", Fields: map[string]bashPPBridgeValue{"M": {Kind: "handle", Type: "sync.RWMutex", residentSync: new(goSourceResidentSync)}}}
	_, err := s.request(context.Background(), bashPPEvalRequest{}, bashPPBridgeRequest{Op: "call", Selector: "fmt.Sprint", Args: []bashPPBridgeValue{v}})
	if err == nil || !strings.Contains(err.Error(), "resident sync cannot enter native transport") {
		t.Fatalf("%v", err)
	}
}
