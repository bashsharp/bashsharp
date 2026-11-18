//go:build full

package interp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func s374TraceRequests(t *testing.T) (map[string]int, *sync.Mutex) {
	t.Helper()
	counts := map[string]int{}
	mu := new(sync.Mutex)
	old := bashPPNativeRequestTrace
	bashPPNativeRequestTrace = func(q bashPPBridgeRequest) {
		key := q.Op + " " + q.Selector
		if q.Receiver != nil {
			key += " receiver=" + q.Receiver.Type
		}
		mu.Lock()
		counts[key]++
		mu.Unlock()
	}
	t.Cleanup(func() { bashPPNativeRequestTrace = old })
	return counts, mu
}

func TestS374ResidentNoChannelRequests(t *testing.T) {
	counts, mu := s374TraceRequests(t)
	source := `package main
 type Box struct { c chan int }
 func main(){b:=new(Box);b.c=make(chan int,1);done:=make(chan bool);go func(){b.c<-7;close(b.c);done<-true}();sum:=0;for n:=range b.c{sum+=n};<-done;println(sum,len(b.c),cap(b.c));var nilc chan int;select{case <-nilc:panic("nil");case _,ok:=<-b.c:println(ok)}}`
	p, err := gosource.Parse(strings.NewReader(source), "resident.go", gosource.Options{RunMain: true})
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
	if got := out.String() + stderr.String(); got != "7 0 1\nfalse\n" {
		t.Fatalf("output %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for key, n := range counts {
		if strings.HasPrefix(key, "channel-") {
			t.Errorf("resident program issued %d %s requests", n, key)
		}
	}
}

// Remote-only original fixture, never rewritten or run through native fallback.
func TestS374OriginalKenChannel(t *testing.T) {
	if os.Getenv("S374_REMOTE_KEN") != "1" {
		t.Skip("remote-only original ken/chan")
	}
	counts, mu := s374TraceRequests(t)
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "ken", "chan.go"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := gosource.Parse(bytes.NewReader(source), "ken.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	r, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	err = r.Run(ctx, p.File)
	elapsed := time.Since(start)
	t.Logf("original ken/chan interpreted elapsed=%s", elapsed)
	mu.Lock()
	defer mu.Unlock()
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	channelRequests := 0
	for _, key := range keys {
		t.Logf("native requests: %s = %d", key, counts[key])
		if strings.HasPrefix(key, "channel-") {
			channelRequests += counts[key]
		}
	}
	t.Logf("native channel requests=%d", channelRequests)
	if channelRequests != 0 {
		t.Errorf("resident fixture made %d native channel requests", channelRequests)
	}
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("%v stderr=%q", err, stderr.String())
	}
}
