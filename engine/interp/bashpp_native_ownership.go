package interp

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// A lease belongs to the value, not to its cell or the session. All normal
// value copies (including task snapshots) copy this pointer. The cleanup only
// queues a release; it never performs network IO or takes the session lock.
type bashPPHandleOwner struct {
	lease   uint64
	padding uint64
}

// Cleanup state must not reference the session: session caches may themselves
// contain owners, and a cleanup argument pointing back to the session would
// turn that cycle into a permanent GC root even after session close.
type bashPPHandleState struct {
	mu       sync.Mutex
	releases []bashPPHandleRelease
	live     atomic.Int64
}
type bashPPHandleRelease struct{ lease, handle uint64 }
type bashPPHandleCleanup struct {
	state   *bashPPHandleState
	release bashPPHandleRelease
}

func (s *bashPPNativeSession) handleOwnership() *bashPPHandleState {
	s.handleReleaseMu.Lock()
	defer s.handleReleaseMu.Unlock()
	if s.handleState == nil {
		s.handleState = new(bashPPHandleState)
	}
	return s.handleState
}
func (s *bashPPNativeSession) liveHandleOwnerCount() int64 { return s.handleOwnership().live.Load() }

func (s *bashPPNativeSession) adoptHandleValue(v *bashPPBridgeValue) {
	if v.Kind == "handle" && v.Lease != 0 && v.handleOwner == nil {
		v.handleOwner = &bashPPHandleOwner{lease: v.Lease}
		state := s.handleOwnership()
		state.live.Add(1)
		runtime.AddCleanup(v.handleOwner, func(c bashPPHandleCleanup) {
			c.state.mu.Lock()
			c.state.releases = append(c.state.releases, c.release)
			c.state.live.Add(-1)
			c.state.mu.Unlock()
		}, bashPPHandleCleanup{state, bashPPHandleRelease{v.Lease, v.Handle}})
	}
	for i := range v.CallArgs {
		s.adoptHandleValue(&v.CallArgs[i])
	}
	for i := range v.Elements {
		s.adoptHandleValue(&v.Elements[i])
	}
	for name, field := range v.Fields {
		s.adoptHandleValue(&field)
		v.Fields[name] = field
	}
	for i := range v.Entries {
		s.adoptHandleValue(&v.Entries[i].Key)
		s.adoptHandleValue(&v.Entries[i].Value)
	}
}

func (s *bashPPNativeSession) adoptHandleResponse(r *bashPPBridgeResponse) {
	if r.Receiver != nil {
		s.adoptHandleValue(r.Receiver)
	}
	if r.Panic != nil {
		s.adoptHandleValue(r.Panic)
	}
	for i := range r.Values {
		s.adoptHandleValue(&r.Values[i])
	}
	for i := range r.PtrUpdates {
		s.adoptHandleValue(&r.PtrUpdates[i])
	}
	for i := range r.Transferred {
		s.adoptHandleValue(&r.Transferred[i].Value)
	}
	for i := range r.SliceUpdates {
		s.adoptHandleValue(&r.SliceUpdates[i].Value)
	}
}

func (s *bashPPNativeSession) takeHandleReleases() []uint64 {
	state := s.handleOwnership()
	state.mu.Lock()
	pending := state.releases
	state.releases = nil
	state.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	leases := make([]uint64, len(pending))
	s.mu.Lock()
	for i, r := range pending {
		leases[i] = r.lease
		// Optional facts can be reauthenticated even if another lease is live.
		delete(s.handleTypes, r.handle)
		delete(s.localWriters, r.handle)
	}
	s.mu.Unlock()
	return leases
}

// A callback result crosses in the other direction. Its interpreter owner
// must survive serialization until the worker has pinned the received value;
// otherwise another goroutine could flush its cleanup before mailbox decode.
func (s *bashPPNativeSession) retainHandleReply(q *bashPPBridgeRequest) {
	var owned func(bashPPBridgeValue) bool
	owned = func(v bashPPBridgeValue) bool {
		if v.handleOwner != nil {
			return true
		}
		for _, x := range v.CallArgs {
			if owned(x) {
				return true
			}
		}
		for _, x := range v.Elements {
			if owned(x) {
				return true
			}
		}
		for _, x := range v.Fields {
			if owned(x) {
				return true
			}
		}
		for _, x := range v.Entries {
			if owned(x.Key) || owned(x.Value) {
				return true
			}
		}
		return false
	}
	found := q.Receiver != nil && owned(*q.Receiver)
	for _, v := range q.Values {
		found = found || owned(v)
	}
	for _, v := range q.SliceBuffers {
		found = found || owned(v.Value)
	}
	if !found {
		return
	}
	q.AckHandles = true
	s.handleReleaseMu.Lock()
	if s.callbackLeaseReplies == nil {
		s.callbackLeaseReplies = make(map[uint64]bashPPBridgeRequest)
	}
	s.callbackLeaseReplies[q.ID] = *q
	s.handleReleaseMu.Unlock()
}

func (s *bashPPNativeSession) ackHandleReply(id uint64) {
	s.handleReleaseMu.Lock()
	delete(s.callbackLeaseReplies, id)
	s.handleReleaseMu.Unlock()
}
