package interp

// Sprint: #243; Story: #676; Story-ID: 8aaa61718184

import (
	"errors"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Destination is the scoped bridge type identity under the session's immutable
// import/local-type registry. Source is a worker-issued reflect.Type token,
// authenticated against a returned handle; neither display strings nor caller
// supplied NativeTypeID fields establish source identity.
type goSourceNativeAdmissionKey struct {
	source      uint64
	destination string
}

// This is an optional authentication cache, not the owner of native handles.
// A miss in nativeAdmissionKey takes the normal dependency type-check path.
// Bound transient (including lazy reflect.ValueOf) handle identities so calls
// with a constant live working set do not accumulate metadata indefinitely.
const goSourceNativeHandleTypeCacheLimit = 4096

func (s *bashPPNativeSession) rememberNativeHandleType(v bashPPBridgeValue) {
	if v.Kind != "handle" || v.Handle == 0 || v.Session != s.id {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.LocalWriter == "" {
		delete(s.localWriters, v.Handle)
	} else {
		if s.localWriters == nil {
			s.localWriters = make(map[uint64]string)
		}
		s.localWriters[v.Handle] = v.LocalWriter
	}
	if v.NativeTypeID == 0 {
		delete(s.handleTypes, v.Handle)
		return
	}
	if s.handleTypes == nil {
		s.handleTypes = make(map[uint64]uint64)
	}
	if _, known := s.handleTypes[v.Handle]; !known && len(s.handleTypes) >= goSourceNativeHandleTypeCacheLimit {
		clear(s.handleTypes)
	}
	s.handleTypes[v.Handle] = v.NativeTypeID
}

func (s *bashPPNativeSession) nativeAdmissionKey(value bashPPBridgeValue, destination string) (goSourceNativeAdmissionKey, bool) {
	if value.Kind != "handle" || value.Session != s.id || destination == "" {
		return goSourceNativeAdmissionKey{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.handleTypes[value.Handle]
	if id == 0 {
		return goSourceNativeAdmissionKey{}, false
	}
	return goSourceNativeAdmissionKey{source: id, destination: destination}, true
}

func (s *bashPPNativeSession) rememberNativeAdmission(key goSourceNativeAdmissionKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interfaceAdmissions == nil {
		s.interfaceAdmissions = make(map[goSourceNativeAdmissionKey]bool)
	}
	s.interfaceAdmissions[key] = true
}

// begin keeps the existing registry-change refusal before any cache hit. Reset
// and bridge replacement allocate a new session and therefore an empty cache.
// Cancellation and closed sessions never become successful cached operations.
func (r *Runner) goSourceNativeAdmission(value bashPPBridgeValue, iface *syntax.BashPPInterfaceType) (*bashPPNativeSession, goSourceNativeAdmissionKey, bool, error) {
	req, err := r.bashPPEvalRequest()
	if err != nil {
		return nil, goSourceNativeAdmissionKey{}, false, err
	}
	s := req.Bridge
	if s == nil {
		return nil, goSourceNativeAdmissionKey{}, false, nil
	}
	if err := r.ectx.Err(); err != nil {
		return nil, goSourceNativeAdmissionKey{}, false, err
	}
	if err := s.begin(r.ectx, req); err != nil {
		return nil, goSourceNativeAdmissionKey{}, false, err
	}
	select {
	case <-s.done:
		return nil, goSourceNativeAdmissionKey{}, false, errors.New("gosource: native dependency session closed")
	default:
	}
	key, ok := s.nativeAdmissionKey(value, r.bashPPBridgeTypeIdentity(iface))
	if !ok {
		return nil, key, false, nil
	}
	s.mu.Lock()
	cached := s.interfaceAdmissions[key]
	s.mu.Unlock()
	return s, key, cached, nil
}
