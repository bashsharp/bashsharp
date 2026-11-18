package interp

import (
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// The callback mailbox is a bounded fast path for the small, synchronous
// original-method callbacks which otherwise require two socket wakeups each.
// The authenticated control connection remains the source of session lifetime
// and the fallback for messages which do not fit. Each slot has one producer
// and one consumer at a time; release/acquire atomics publish the payload.
const (
	bashPPMailboxSlots    = 16
	bashPPMailboxSlotSize = 64 << 10
	bashPPMailboxHeader   = 64
	bashPPMailboxHalf     = (bashPPMailboxSlotSize - bashPPMailboxHeader) / 2
	bashPPMailboxSize     = bashPPMailboxSlots * bashPPMailboxSlotSize
	bashPPMailboxFree     = 0
	bashPPMailboxWriting  = 1
	bashPPMailboxRequest  = 2
	bashPPMailboxServing  = 3
	bashPPMailboxReply    = 4
	// Each slot's worker-parked word and the host-parked count share the
	// header with the state and the two payload sizes.
	bashPPMailboxWorkerParked = 12
	bashPPMailboxHostParked   = 16
	// Both waiters spin briefly, which covers a callback whose body is short,
	// and then park on the control connection.
	bashPPMailboxHostSpin   = 50 * time.Microsecond
	bashPPMailboxWorkerSpin = 100 * time.Microsecond
)

type bashPPCallbackMailbox struct {
	data    []byte
	path    string
	cleanup func() error
	mu      sync.Mutex
	refs    int
	closing bool
	once    sync.Once
	// wake receives the worker's notice that it published a request while
	// the serving request was parked.
	wake chan struct{}
}

func (m *bashPPCallbackMailbox) word(slot, offset int) *uint32 {
	return (*uint32)(unsafe.Pointer(&m.data[slot*bashPPMailboxSlotSize+offset]))
}

func (m *bashPPCallbackMailbox) requestBytes(slot int) []byte {
	start := slot*bashPPMailboxSlotSize + bashPPMailboxHeader
	return m.data[start : start+bashPPMailboxHalf]
}

func (m *bashPPCallbackMailbox) replyBytes(slot int) []byte {
	start := slot*bashPPMailboxSlotSize + bashPPMailboxHeader + bashPPMailboxHalf
	return m.data[start : (slot+1)*bashPPMailboxSlotSize]
}

// take claims the next published callback. Multiple nested request goroutines
// may inspect the map, but the active-callback check in request ensures only
// the interpreter frame which currently owns callback execution calls take.
func (m *bashPPCallbackMailbox) take() (int, bashPPBridgeResponse, bool) {
	if m == nil {
		return 0, bashPPBridgeResponse{}, false
	}
	for slot := 0; slot < bashPPMailboxSlots; slot++ {
		state := m.word(slot, 0)
		if !atomic.CompareAndSwapUint32(state, bashPPMailboxRequest, bashPPMailboxServing) {
			continue
		}
		n := int(atomic.LoadUint32(m.word(slot, 4)))
		var q bashPPBridgeResponse
		if n <= 0 || n > len(m.requestBytes(slot)) {
			q.Error = "gosource: invalid callback mailbox request size"
		} else if err := json.Unmarshal(m.requestBytes(slot)[:n], &q); err != nil {
			q.Error = fmt.Sprintf("gosource: invalid callback mailbox request: %v", err)
		}
		return slot, q, true
	}
	return 0, bashPPBridgeResponse{}, false
}

// answer publishes a reply only when it fits. The caller sends larger replies
// over the authenticated control connection, then publishes a small marker.
func (m *bashPPCallbackMailbox) answer(slot int, answer bashPPBridgeRequest) bool {
	payload, err := json.Marshal(answer)
	if err != nil || len(payload) > len(m.replyBytes(slot)) {
		return false
	}
	copy(m.replyBytes(slot), payload)
	atomic.StoreUint32(m.word(slot, 8), uint32(len(payload)))
	atomic.StoreUint32(m.word(slot, 0), bashPPMailboxReply)
	return true
}

// workerParked reports whether the callback waiting on slot gave up spinning
// and must be woken through the control connection. The worker stores the
// word before it re-reads the slot state, and the host reads it after it
// stores the reply, so one of the two always observes the other.
func (m *bashPPCallbackMailbox) workerParked(slot int) bool {
	return atomic.LoadUint32(m.word(slot, bashPPMailboxWorkerParked)) != 0
}

// park announces that the serving request is about to block. It reports
// false, leaving the request unparked, when a callback was published first.
func (m *bashPPCallbackMailbox) park() bool {
	atomic.AddUint32(m.word(0, bashPPMailboxHostParked), 1)
	for slot := 0; slot < bashPPMailboxSlots; slot++ {
		if atomic.LoadUint32(m.word(slot, 0)) == bashPPMailboxRequest {
			m.unpark()
			return false
		}
	}
	return true
}

func (m *bashPPCallbackMailbox) unpark() {
	atomic.AddUint32(m.word(0, bashPPMailboxHostParked), ^uint32(0))
}

// notify is the control reader's delivery of a worker wake.
func (m *bashPPCallbackMailbox) notify() {
	if m == nil {
		return
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *bashPPCallbackMailbox) close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closing = true
	ready := m.refs == 0
	m.mu.Unlock()
	if ready {
		m.once.Do(func() { _ = m.cleanup() })
	}
}

func (m *bashPPCallbackMailbox) retain() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return false
	}
	m.refs++
	return true
}

func (m *bashPPCallbackMailbox) release() {
	m.mu.Lock()
	m.refs--
	ready := m.closing && m.refs == 0
	m.mu.Unlock()
	if ready {
		m.once.Do(func() { _ = m.cleanup() })
	}
}

func bashPPMailboxWorkerSource(enabled bool) (imports, implementation string) {
	if !enabled {
		return "", `func openCallbackMailbox(){}
func mailboxCallback(response,chan request)(request,bool,error){return request{},false,nil}`
	}
	imports = `
 "os"
 "sync/atomic"
 "syscall"
 bppMailboxTime "time"
`
	implementation = fmt.Sprintf(`
const callbackMailboxSlots=%d
const callbackMailboxSlotSize=%d
const callbackMailboxHeader=%d
const callbackMailboxHalf=%d
const callbackMailboxFree uint32=%d
const callbackMailboxWriting uint32=%d
const callbackMailboxRequest uint32=%d
const callbackMailboxReply uint32=%d
const callbackMailboxWorkerParked=%d
const callbackMailboxHostParked=%d
const callbackMailboxSpinTime=bppMailboxTime.Duration(%d)
var callbackMailbox []byte
func callbackMailboxWord(slot,offset int)*uint32{return (*uint32)(unsafe.Pointer(&callbackMailbox[slot*callbackMailboxSlotSize+offset]))}
func callbackMailboxRequestBytes(slot int)[]byte{start:=slot*callbackMailboxSlotSize+callbackMailboxHeader;return callbackMailbox[start:start+callbackMailboxHalf]}
func callbackMailboxReplyBytes(slot int)[]byte{start:=slot*callbackMailboxSlotSize+callbackMailboxHeader+callbackMailboxHalf;return callbackMailbox[start:(slot+1)*callbackMailboxSlotSize]}
func callbackMailboxSpin(slot int)bool{
 if bppRuntime.GOMAXPROCS(0)==1{return atomic.LoadUint32(callbackMailboxWord(slot,0))==callbackMailboxReply}
 start:=bppMailboxTime.Now()
 for n:=1;;n++{if atomic.LoadUint32(callbackMailboxWord(slot,0))==callbackMailboxReply{return true};if n&255==0&&bppMailboxTime.Since(start)>callbackMailboxSpinTime{return false}}
}
%s
func mailboxCallback(q response,wait chan request)(request,bool,error){
 if callbackMailbox==nil{return request{},false,nil}
 payload,err:=json.Marshal(q);if err!=nil||len(payload)>callbackMailboxHalf{return request{},false,err}
 slot:=-1
 for i:=0;i<callbackMailboxSlots;i++{if atomic.CompareAndSwapUint32(callbackMailboxWord(i,0),callbackMailboxFree,callbackMailboxWriting){slot=i;break}}
 if slot<0{return request{},false,nil}
 copy(callbackMailboxRequestBytes(slot),payload);atomic.StoreUint32(callbackMailboxWord(slot,4),uint32(len(payload)));atomic.StoreUint32(callbackMailboxWord(slot,0),callbackMailboxRequest)
 // A parked server is woken through the control connection. It counts itself
 // parked before its last look at the slots, so this load cannot miss it.
 if atomic.LoadUint32(callbackMailboxWord(0,callbackMailboxHostParked))!=0{sendMessage(response{Op:"callback-mailbox-wake"})}
 // An overflowing reply arrives on wait before its marker is published, so
 // it can be what ends a park; keep it and take the marker that follows.
 var early *request
 for !callbackMailboxSpin(slot){
  if early!=nil{select{case <-handles.stop:atomic.StoreUint32(callbackMailboxWord(slot,0),callbackMailboxFree);return request{},true,fmt.Errorf("interpreter connection closed during callback");default:bppRuntime.Gosched();continue}}
  atomic.StoreUint32(callbackMailboxWord(slot,callbackMailboxWorkerParked),1)
  if atomic.LoadUint32(callbackMailboxWord(slot,0))==callbackMailboxReply{atomic.StoreUint32(callbackMailboxWord(slot,callbackMailboxWorkerParked),0);break}
  select{case m:=<-wait:if m.Op=="callback-reply"{early=&m};case <-handles.stop:atomic.StoreUint32(callbackMailboxWord(slot,callbackMailboxWorkerParked),0);atomic.StoreUint32(callbackMailboxWord(slot,0),callbackMailboxFree);return request{},true,fmt.Errorf("interpreter connection closed during callback")}
  atomic.StoreUint32(callbackMailboxWord(slot,callbackMailboxWorkerParked),0)
 }
 n:=int(atomic.LoadUint32(callbackMailboxWord(slot,8)));var reply request
 if n<=0||n>len(callbackMailboxReplyBytes(slot)){err=fmt.Errorf("invalid callback mailbox reply size")}else{err=json.Unmarshal(callbackMailboxReplyBytes(slot)[:n],&reply)}
 atomic.StoreUint32(callbackMailboxWord(slot,0),callbackMailboxFree)
 if err==nil&&early!=nil{reply=*early}
 return reply,true,err
}
`, bashPPMailboxSlots, bashPPMailboxSlotSize, bashPPMailboxHeader, bashPPMailboxHalf, bashPPMailboxFree, bashPPMailboxWriting, bashPPMailboxRequest, bashPPMailboxReply, bashPPMailboxWorkerParked, bashPPMailboxHostParked, int64(bashPPMailboxWorkerSpin), bashPPMailboxOpenWorkerSource())
	return imports, implementation
}

// bashPPMailboxIdle is the serving request's run of empty polls.
type bashPPMailboxIdle struct {
	polls  uint32
	since  time.Time
	budget time.Duration
}

// bashPPMailboxIdleMaxBudget bounds one spin run. Twice the recent serve gap
// adapts the spin to host speed and callback shape; the cap bounds the burn
// when the stream actually ends, so a slow body still parks instead of
// spinning a processor through it.
const bashPPMailboxIdleMaxBudget = 500 * time.Microsecond

// reset starts a fresh run of empty polls. now is the time of the callback
// just served and lastServe the previous one; the spin budget covers twice
// their gap, so a streaming producer whose turnaround sets that gap is met
// spinning even on a host where the fixed spin would already have expired,
// while a truly idle request still parks after a bounded burn. Without a
// previous serve the fixed host spin applies.
func (i *bashPPMailboxIdle) reset(now, lastServe time.Time) {
	*i = bashPPMailboxIdle{budget: bashPPMailboxHostSpin}
	if lastServe.IsZero() {
		return
	}
	gap := now.Sub(lastServe)
	if gap < 0 {
		gap = 0
	}
	if budget := 2 * gap; budget > i.budget {
		i.budget = min(budget, bashPPMailboxIdleMaxBudget)
	}
}

// The request goroutine is also the mailbox server. spin reports whether an
// empty poll should be repeated at once; after a bounded time it reports that
// the request should park instead. The spin never yields or sleeps: a yield
// wakes an idle scheduler thread on every round, and a timer shorter than a
// millisecond rounds up to one on Linux, either of which then becomes what
// every callback costs. With one processor the spin would only keep the
// control reader and parallel callback frames from running, so the request
// parks on its first empty poll.
func (i *bashPPMailboxIdle) spin() bool {
	i.polls++
	if i.polls == 1 {
		if runtime.GOMAXPROCS(0) == 1 {
			return false
		}
		i.since = time.Now()
		if i.budget <= 0 {
			i.budget = bashPPMailboxHostSpin
		}
		return true
	}
	return i.polls&15 != 0 || time.Since(i.since) < i.budget
}
