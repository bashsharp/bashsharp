package interp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"weak"
)

// The worker must reclaim encodings that were never exported.
func s374OwnershipWorker(t *testing.T, body string) string {
	t.Helper()
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	source, err := bashPPNativeSource(context.Background(), bashPPEvalRequest{
		Go:  goBin,
		Dir: t.TempDir(),
		Env: os.Environ(),
		LocalTypes: []bashPPLocalType{
			{Name: "ownershipShape", Decl: "struct { count int; next *ownershipShape }"},
			{Name: "ownershipContainer", Decl: "struct { extra any; alias any; snapshot any; typedNil any }"},
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
	source += body
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
	return binary
}

func TestS374OwnershipUnsentHandles(t *testing.T) {
	binary := s374OwnershipWorker(t, `
func main() {
 for _, count := range []int{256, 512} {
  for i:=0;i<count;i++ { _ = encode(reflect.ValueOf([]byte{byte(i)})) }
  for i:=0;i<20;i++ { bppRuntime.GC();time.Sleep(5*time.Millisecond) }
  handles.Lock();n:=len(handles.values);handles.Unlock()
  fmt.Printf("encoded=%d retained=%d\n",count,n)
  if n>4 { panic(fmt.Sprintf("dead handle table has %d entries, want <=4",n)) }
 }
}
`)
	if output, err := exec.Command(binary).CombinedOutput(); err != nil {
		t.Fatalf("worker: %v: %s", err, output)
	} else {
		t.Log(string(output))
	}
}

func TestS374OwnershipRoundTrips(t *testing.T) {
	binary := s374OwnershipWorker(t, `
func main(){
 bridgeAuth="0123456789abcdef0123456789abcdef"
 encoder:=json.NewEncoder(bppOS.Stdout); decoder:=json.NewDecoder(bppOS.Stdin)
 saved:=&ownershipShape{count:73}
 for {var q request;if decoder.Decode(&q)!=nil{return};releaseHandles(q.Releases);r:=response{}
  switch q.Op {
  case "fresh":r.Values=[]value{encode(reflect.ValueOf([]byte{1,2,3}))}
  case "saved":r.Values=[]value{encode(reflect.ValueOf(saved))}
  case "read":v,err:=decode(*q.Receiver,nil);if err!=nil{panic(err)};r.Values=[]value{encode(v.Elem().Field(0))}
  case "stats":
   for i:=0;i<3;i++{bppRuntime.GC();time.Sleep(time.Millisecond)}
   handles.Lock();r.Values=[]value{encode(reflect.ValueOf(len(handles.values))),encode(reflect.ValueOf(len(handles.leases))),encode(reflect.ValueOf(len(handles.pointers))),encode(reflect.ValueOf(len(handles.reflected)))};handles.Unlock()
  }
  exportHandles(&r);if err:=encoder.Encode(r);err!=nil{panic(err)}
 }
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		in.Close()
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	}()
	encoder, decoder := json.NewEncoder(in), json.NewDecoder(out)
	s := &bashPPNativeSession{id: "0123456789abcdef"}
	call := func(op string, receiver *bashPPBridgeValue) []bashPPBridgeValue {
		t.Helper()
		defer runtime.KeepAlive(receiver)
		if err := encoder.Encode(bashPPBridgeRequest{Op: op, Receiver: receiver, Releases: s.takeHandleReleases()}); err != nil {
			t.Fatal(err)
		}
		var r bashPPBridgeResponse
		if err := decoder.Decode(&r); err != nil {
			t.Fatal(err)
		}
		s.adoptHandleResponse(&r)
		for i := range r.Values {
			r.Values[i].Session = s.id
			s.rememberNativeHandleType(r.Values[i])
		}
		return r.Values
	}
	live := call("saved", nil)[0]
	alias := bashPPCopyBridgeValue(live)
	func() {
		again := call("saved", nil)[0]
		if again.Handle != alias.Handle || again.Lease == alias.Lease {
			t.Fatal("live identity/delivery isolation lost")
		}
	}()
	live = bashPPBridgeValue{}
	for _, count := range []int{256, 512} {
		for i := 0; i < count; i++ {
			call("fresh", nil)
		}
		var stats []bashPPBridgeValue
		for i := 0; i < 40; i++ {
			runtime.GC()
			time.Sleep(time.Millisecond)
			stats = call("stats", nil)
			n, _ := strconv.Atoi(stats[0].Text)
			if s.liveHandleOwnerCount() == 1 && n == 1 {
				break
			}
		}
		s.mu.Lock()
		facts := len(s.handleTypes)
		s.mu.Unlock()
		if facts > 1 {
			t.Fatalf("dead host type facts retained: %d", facts)
		}
		t.Logf("round trips=%d host owners=%d worker values=%s leases=%s pointers=%s reflected=%s", count, s.liveHandleOwnerCount(), stats[0].Text, stats[1].Text, stats[2].Text, stats[3].Text)
		if s.liveHandleOwnerCount() != 1 || stats[0].Text != "1" || stats[1].Text != "1" || stats[2].Text != "1" || stats[3].Text != "0" {
			t.Fatal("dead ownership entries retained")
		}
		if got := call("read", &alias)[0].Text; got != "73" {
			t.Fatalf("live alias changed: %s", got)
		}
	}
	oldHandle, oldLease := alias.Handle, alias.Lease
	alias = bashPPBridgeValue{}
	var stats []bashPPBridgeValue
	for i := 0; i < 40; i++ {
		runtime.GC()
		time.Sleep(time.Millisecond)
		stats = call("stats", nil)
		if stats[0].Text == "0" && s.liveHandleOwnerCount() == 0 {
			break
		}
	}
	if stats[0].Text != "0" || stats[1].Text != "0" || stats[2].Text != "0" || s.liveHandleOwnerCount() != 0 {
		t.Fatal("last alias did not release both processes")
	}
	t.Log("after last alias: host owners=0 worker values=0 leases=0 pointers=0")
	// Native ownership survives removal of the bridge root. A stale release
	// must not revoke a later export of this same native object.
	next := call("saved", nil)[0]
	if next.Handle == oldHandle || next.Lease == oldLease {
		t.Fatal("reused retired identity")
	}
	state := s.handleOwnership()
	state.mu.Lock()
	state.releases = append(state.releases, bashPPHandleRelease{lease: oldLease})
	state.mu.Unlock()
	if got := call("read", &next)[0].Text; got != "73" {
		t.Fatal("native retained value was lost")
	}
	runtime.KeepAlive(next)
}

func TestS374OwnershipCallbackAcknowledgement(t *testing.T) {
	s := &bashPPNativeSession{}
	func() {
		answer := bashPPBridgeRequest{ID: 9, Values: []bashPPBridgeValue{{Kind: "handle", Handle: 3, Lease: 17}}}
		s.adoptHandleValue(&answer.Values[0])
		s.retainHandleReply(&answer)
		if !answer.AckHandles {
			t.Fatal("callback did not request ownership acknowledgement")
		}
	}()
	for i := 0; i < 3; i++ {
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
	if s.liveHandleOwnerCount() != 1 || len(s.takeHandleReleases()) != 0 {
		t.Fatal("callback result released before worker acknowledgement")
	}
	s.ackHandleReply(9)
	var releases []uint64
	for i := 0; i < 40; i++ {
		runtime.GC()
		time.Sleep(time.Millisecond)
		releases = append(releases, s.takeHandleReleases()...)
		if s.liveHandleOwnerCount() == 0 {
			break
		}
	}
	if len(releases) != 1 || releases[0] != 17 || s.liveHandleOwnerCount() != 0 {
		t.Fatalf("acknowledged callback retained: releases=%v owners=%d", releases, s.liveHandleOwnerCount())
	}
}

func TestS374OwnershipSessionCycleCollects(t *testing.T) {
	w := func() weak.Pointer[bashPPNativeSession] {
		s := &bashPPNativeSession{}
		v := bashPPBridgeValue{Kind: "handle", Handle: 1, Lease: 1}
		s.adoptHandleValue(&v)
		// A real session cache can retain a response template. Cleanup arguments
		// must not make its dead session/value cycle into a global runtime root.
		s.valueOfReplies = map[string]bashPPBridgeValue{"shape": v}
		return weak.Make(s)
	}()
	for i := 0; i < 40; i++ {
		runtime.GC()
		time.Sleep(time.Millisecond)
		if w.Value() == nil {
			return
		}
	}
	t.Fatal("owner cleanup roots the session containing its cached value")
}

func TestS374OwnershipMutablePointerField(t *testing.T) {
	binary := s374OwnershipWorker(t, `
func main(){
 holder:=struct{P *ownershipShape}{&ownershipShape{count:1}}
 field:=reflect.ValueOf(&holder).Elem().Field(0)
 func(){v:=encode(field);handles.Lock();handles.values[v.Handle]=field;handles.Unlock()}()
 // receiver_field retains the selected field itself. Its pointer value can
 // subsequently change, but the pointer-index key was minted before that.
 holder.P=&ownershipShape{count:2}
 for i:=0;i<20;i++{bppRuntime.GC();time.Sleep(time.Millisecond)}
 handles.Lock();n,p:=len(handles.values),len(handles.pointers);handles.Unlock()
 if n!=0||p!=0{panic(fmt.Sprintf("released mutable field: values=%d pointer index=%d, want 0/0",n,p))}
 bppRuntime.KeepAlive(holder)
 func(){
  old:=&ownershipShape{count:3};holder.P=old
  first:=encode(field);handles.Lock();handles.values[first.Handle]=field;handles.Unlock()
  holder.P=&ownershipShape{count:4}
  alias:=encode(reflect.ValueOf(old))
  if alias.Handle==first.Handle{panic("mutable pointer field reused stale pointer identity")}
  got,err:=decode(alias,nil);if err!=nil{panic(err)}
  if got.Interface().(*ownershipShape)!=old{panic("live native alias changed")}
  bppRuntime.KeepAlive(first);bppRuntime.KeepAlive(alias)
 }()
 for i:=0;i<20;i++{bppRuntime.GC();time.Sleep(time.Millisecond)}
 handles.Lock();n,p=len(handles.values),len(handles.pointers);keys:=len(handles.pointerKeys);handles.Unlock()
 if n!=0||p!=0||keys!=0{panic(fmt.Sprintf("retired keys: %d/%d/%d",n,p,keys))}
}
`)
	if out, err := exec.Command(binary).CombinedOutput(); err != nil {
		t.Fatalf("worker: %v: %s", err, out)
	}
}
