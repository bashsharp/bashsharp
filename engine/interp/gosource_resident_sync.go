package interp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Host-only ownership. changed allows cancellation without leaving goroutines
// parked forever in Mutex.Lock, RWMutex read/write locks or WaitGroup.Wait
// after a program ends.
// gate serializes the notification bookkeeping with the real Go operations.
//
// changed is a broadcast: every waiter it wakes can proceed (the readers a
// writer's Unlock admits, a WaitGroup's waiters). Exclusive acquirers — Mutex
// lockers and RWMutex writers — instead queue one channel each and are woken
// one at a time, so a release costs one wakeup however many are blocked. A
// woken waiter that fails its retry queues again; one that abandons its wait
// hands the wakeup on.
//
// RWMutex follows Go's writer preference: a blocked Lock registers in
// wpending and excludes new readers until it acquires or is cancelled.
// Readers already blocked when a writer unlocks are admitted ahead of the
// next queued writer: Unlock moves rwaiting into rgranted and bumps repoch,
// and no writer acquires while rgranted is nonzero. TryLock and TryRLock
// never register.
type goSourceResidentSync struct {
	gate     sync.Mutex
	mutex    sync.Mutex
	rwmu     sync.RWMutex
	wg       sync.WaitGroup
	locked   bool
	wlocked  bool
	readers  int
	wpending int
	rwaiting int
	rgranted int
	repoch   uint64
	count    int
	changed  chan struct{}
	queue    []chan struct{}
}

// enqueue registers one exclusive waiter, reusing w while it is still queued.
func (s *goSourceResidentSync) enqueue(w chan struct{}) chan struct{} {
	if w == nil {
		w = make(chan struct{})
		s.queue = append(s.queue, w)
	}
	return w
}

// wakeOne releases the longest-queued exclusive waiter to retry.
func (s *goSourceResidentSync) wakeOne() {
	if len(s.queue) == 0 {
		return
	}
	close(s.queue[0])
	s.queue[0] = nil
	if s.queue = s.queue[1:]; len(s.queue) == 0 {
		s.queue = nil
	}
}

// abandon withdraws w. A waiter already woken passes its wakeup on.
func (s *goSourceResidentSync) abandon(w chan struct{}) {
	if w == nil {
		return
	}
	for i, q := range s.queue {
		if q == w {
			s.queue = append(s.queue[:i:i], s.queue[i+1:]...)
			return
		}
	}
	s.wakeOne()
}

func (s *goSourceResidentSync) notify() {
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
}
func (s *goSourceResidentSync) wake() <-chan struct{} {
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}

func (r *Runner) goSourceResidentSyncType(op string, typ syntax.BashPPTypeExpr, args []bashPPBridgeValue) (bashPPBridgeValue, bool, error) {
	ptr := false
	if pt, ok := typ.(*syntax.BashPPPointerType); ok && op == "assignable" {
		typ, ptr = pt.Element, true
	}
	n, ok := typ.(*syntax.BashPPNamedType)
	if !r.bashPPGoSource || !ok || n.LocalSync == "" {
		return bashPPBridgeValue{}, false, nil
	}
	alias, name, ok := strings.Cut(n.Name.Value, ".")
	if !ok || r.bashPPImports[alias]+"."+name != n.LocalSync {
		return bashPPBridgeValue{}, false, nil
	}
	switch op {
	case "type":
		return bashPPBridgeValue{Kind: "string", Text: n.LocalSync}, true, nil
	case "assignable":
		if len(args) == 1 && residentSyncValue(args[0]) != nil {
			want := n.LocalSync
			if ptr {
				want = "*" + want
			}
			return bashPPBridgeValue{Kind: "bool", Text: strconv.FormatBool(args[0].Type == want)}, true, nil
		}
	case "new", "construct", "address":
		if len(args) > 0 && (len(args[0].Fields) > 0 || len(args[0].Elements) > 0) {
			return bashPPBridgeValue{}, true, fmt.Errorf("resident sync requires zero initialization")
		}
		typ := n.LocalSync
		if op == "address" {
			typ = "*" + typ
		}
		return bashPPBridgeValue{Kind: "handle", Type: typ, residentSync: new(goSourceResidentSync)}, true, nil
	}
	return bashPPBridgeValue{}, false, nil
}

func residentSyncValue(v bashPPBridgeValue) *goSourceResidentSync {
	if v.residentSync != nil {
		return v.residentSync
	}
	for _, e := range v.Elements {
		if s := residentSyncValue(e); s != nil {
			return s
		}
	}
	for _, e := range v.CallArgs {
		if s := residentSyncValue(e); s != nil {
			return s
		}
	}
	for _, e := range v.Fields {
		if s := residentSyncValue(e); s != nil {
			return s
		}
	}
	for _, e := range v.Entries {
		if s := residentSyncValue(e.Key); s != nil {
			return s
		}
		if s := residentSyncValue(e.Value); s != nil {
			return s
		}
	}
	return nil
}

func (r *Runner) goSourceResidentSyncRequest(ctx context.Context, q bashPPBridgeRequest) (values []bashPPBridgeValue, handled bool, err error) {
	var s *goSourceResidentSync
	if q.Receiver != nil {
		s = residentSyncValue(*q.Receiver)
	}
	if s == nil {
		for _, a := range q.Args {
			if residentSyncValue(a) != nil {
				return nil, true, fmt.Errorf("resident sync cannot cross native boundary (%s)", q.Op)
			}
		}
		return nil, false, nil
	}
	// Every resident receiver is claimed here; never serialize host-only state.
	handled = true
	ctx = r.bashPPTaskContext(ctx)
	if err = ctx.Err(); err != nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("native dependency panic: %v", p)
		}
	}()
	if q.Op != "call" {
		return nil, true, fmt.Errorf("unsupported resident sync operation %s", q.Op)
	}
	typ := q.Receiver.Type
	if q.Receiver.Kind == "pointer" && len(q.Receiver.Elements) == 1 {
		typ = q.Receiver.Elements[0].Type
	}
	typ = strings.TrimPrefix(typ, "*")
	switch typ {
	case "sync.Mutex":
		switch q.Selector {
		case "Lock", "TryLock":
			var wake chan struct{}
			leave := func() {
				s.gate.Lock()
				defer s.gate.Unlock()
				s.abandon(wake)
			}
			for {
				s.gate.Lock()
				acquired := s.mutex.TryLock()
				if acquired {
					s.locked = true
				} else if q.Selector == "Lock" {
					wake = s.enqueue(wake)
				}
				s.gate.Unlock()
				if q.Selector == "TryLock" {
					return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: strconv.FormatBool(acquired)}}, true, nil
				}
				if acquired {
					return nil, true, nil
				}
				if !r.bashPPArmBeforeBlock(ctx) {
					leave()
					return nil, true, errBashPPScalarInterrupted
				}
				select {
				case <-wake:
					wake = nil
				case <-ctx.Done():
					leave()
					return nil, true, ctx.Err()
				}
			}
		case "Unlock":
			s.gate.Lock()
			defer s.gate.Unlock()
			if !s.locked {
				return nil, true, fmt.Errorf("sync: unlock of unlocked mutex")
			}
			s.locked = false
			s.mutex.Unlock()
			s.wakeOne()
			return nil, true, nil
		}
	case "sync.RWMutex":
		switch q.Selector {
		case "RLock", "TryRLock":
			waiting, epoch := false, uint64(0)
			// leave withdraws an abandoned wait. A grant this reader will
			// never use is returned so the next writer is not held off.
			leave := func() {
				s.gate.Lock()
				defer s.gate.Unlock()
				if !waiting {
					return
				}
				waiting = false
				if s.repoch != epoch {
					if s.rgranted--; s.readers == 0 && s.rgranted == 0 {
						s.wakeOne()
					}
				} else {
					s.rwaiting--
				}
			}
			for {
				s.gate.Lock()
				granted := waiting && s.repoch != epoch
				acquired := !s.wlocked && (granted || s.wpending == 0) && s.rwmu.TryRLock()
				var wake <-chan struct{}
				if acquired {
					s.readers++
					if granted {
						s.rgranted--
					} else if waiting {
						s.rwaiting--
					}
					waiting = false
				} else {
					wake = s.wake()
					if q.Selector == "RLock" && !waiting {
						waiting, epoch = true, s.repoch
						s.rwaiting++
					}
				}
				s.gate.Unlock()
				if q.Selector == "TryRLock" {
					return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: strconv.FormatBool(acquired)}}, true, nil
				}
				if acquired {
					return nil, true, nil
				}
				if !r.bashPPArmBeforeBlock(ctx) {
					leave()
					return nil, true, errBashPPScalarInterrupted
				}
				select {
				case <-wake:
				case <-ctx.Done():
					leave()
					return nil, true, ctx.Err()
				}
			}
		case "RUnlock":
			s.gate.Lock()
			defer s.gate.Unlock()
			if s.readers <= 0 {
				return nil, true, fmt.Errorf("sync: RUnlock of unlocked RWMutex")
			}
			s.rwmu.RUnlock()
			if s.readers--; s.readers == 0 && s.rgranted == 0 {
				s.wakeOne()
			}
			return nil, true, nil
		case "Lock", "TryLock":
			pending := false
			var queued chan struct{}
			// leave withdraws an abandoned writer and wakes the readers it
			// was excluding.
			leave := func() {
				s.gate.Lock()
				defer s.gate.Unlock()
				if pending {
					pending = false
					s.wpending--
					s.notify()
				}
				s.abandon(queued)
			}
			for {
				s.gate.Lock()
				acquired := !s.wlocked && s.readers == 0 && s.rgranted == 0 && s.rwmu.TryLock()
				if acquired {
					s.wlocked = true
					if pending {
						pending = false
						s.wpending--
					}
				} else if q.Selector == "Lock" {
					queued = s.enqueue(queued)
					if !pending {
						pending = true
						s.wpending++
					}
				}
				s.gate.Unlock()
				if q.Selector == "TryLock" {
					return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: strconv.FormatBool(acquired)}}, true, nil
				}
				if acquired {
					return nil, true, nil
				}
				if !r.bashPPArmBeforeBlock(ctx) {
					leave()
					return nil, true, errBashPPScalarInterrupted
				}
				select {
				case <-queued:
					queued = nil
				case <-ctx.Done():
					leave()
					return nil, true, ctx.Err()
				}
			}
		case "Unlock":
			s.gate.Lock()
			defer s.gate.Unlock()
			if !s.wlocked {
				return nil, true, fmt.Errorf("sync: unlock of unlocked RWMutex")
			}
			s.wlocked = false
			s.rwmu.Unlock()
			s.repoch++
			if s.rwaiting == 0 {
				s.wakeOne()
				return nil, true, nil
			}
			// The admitted readers' last RUnlock wakes the next writer.
			s.rgranted += s.rwaiting
			s.rwaiting = 0
			s.notify()
			return nil, true, nil
		}
	case "sync.WaitGroup":
		switch q.Selector {
		case "Add", "Done":
			delta := -1
			if q.Selector == "Add" {
				if len(q.Args) != 1 {
					return nil, true, fmt.Errorf("WaitGroup.Add requires one argument")
				}
				delta, err = strconv.Atoi(q.Args[0].Text)
				if err != nil {
					return
				}
			}
			s.gate.Lock()
			defer s.gate.Unlock()
			s.wg.Add(delta)
			s.count += delta
			if s.count == 0 {
				s.notify()
			}
			return nil, true, nil
		case "Wait":
			s.gate.Lock()
			if s.count == 0 {
				s.wg.Wait()
				s.gate.Unlock()
				return nil, true, nil
			}
			wake := s.wake()
			s.gate.Unlock()
			if !r.bashPPArmBeforeBlock(ctx) {
				return nil, true, errBashPPScalarInterrupted
			}
			select {
			case <-wake:
				s.gate.Lock()
				defer s.gate.Unlock()
				if s.count != 0 {
					return nil, true, fmt.Errorf("native dependency panic: sync: WaitGroup is reused before previous Wait has returned")
				}
				s.wg.Wait()
				return nil, true, nil
			case <-ctx.Done():
				return nil, true, ctx.Err()
			}
		}
	}
	return nil, true, fmt.Errorf("unsupported resident sync method %s.%s", typ, q.Selector)
}
