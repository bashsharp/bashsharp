package interp

// Sprint: #165; Story: #99; Story-ID: ca559d7ee23d

import (
	"runtime"
	"strings"
	"testing"
)

// Negative: a back-reference names an origin of THIS session or nothing —
// no origin, another session's origin, or an origin never registered each
// fail closed rather than binding to arbitrary storage.
func TestSprint165BackReferenceFailsClosed(t *testing.T) {
	kept := &bashPPCell{}
	session := &bashPPNativeSession{id: "session-a", origins: map[uint64]*bashPPOriginRecord{1: newOriginRecord(&bashPPPointer{target: kept})}}
	defer runtime.KeepAlive(kept)
	r := &Runner{}
	r.bashPPTools.bridge = session
	cases := []struct {
		name string
		v    bashPPBridgeValue
		want string
	}{
		{"no origin", bashPPBridgeValue{Kind: "pointer", Session: "session-a"}, "names no origin"},
		{"another session", bashPPBridgeValue{Kind: "pointer", Origin: 1, Session: "session-b"}, "names no origin"},
		{"unregistered origin", bashPPBridgeValue{Kind: "pointer", Origin: 7, Session: "session-a"}, "unknown origin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !bashPPBridgeBackReference(c.v) {
				t.Fatalf("%+v is the back-reference shape", c.v)
			}
			ptr, err := r.bashPPBridgeBackReferencePointer(c.v)
			if err == nil || ptr != nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ptr=%v err=%v; want %q", ptr, err, c.want)
			}
		})
	}
	ptr, err := r.bashPPBridgeBackReferencePointer(bashPPBridgeValue{Kind: "pointer", Origin: 1, Session: "session-a"})
	live, _ := session.originLookup(1)
	// Each lookup materialises a fresh wrapper, so identity is compared
	// by storage, not by wrapper pointer.
	if err != nil || ptr == nil || live == nil || ptr.target != kept || live.target != kept {
		t.Fatalf("a registered origin resolves to its pointer: %v %v", ptr, err)
	}
	// A pointer WITH a pointee is not a back-reference.
	if bashPPBridgeBackReference(bashPPBridgeValue{Kind: "pointer", Origin: 1, Elements: []bashPPBridgeValue{{Kind: "int", Text: "1"}}}) {
		t.Fatal("a pointer with a pointee is not a back-reference")
	}
}

// The transport path marks an origin for the duration of its pointee's walk
// and answers on-path for the same origin met again; leaving unmarks it,
// and the path is nil between walks.
func TestSprint165TransportPathMarksOrigin(t *testing.T) {
	r := &Runner{}
	onPath, leave := r.bashPPTransportEnter(3)
	if onPath || leave == nil {
		t.Fatal("first entry is not on the path")
	}
	if again, _ := r.bashPPTransportEnter(3); !again {
		t.Fatal("the same origin during its own walk is on the path")
	}
	other, leaveOther := r.bashPPTransportEnter(4)
	if other {
		t.Fatal("a different origin is not on the path")
	}
	leaveOther()
	leave()
	if r.bashPPTransportPath != nil {
		t.Fatal("the path is released between walks")
	}
	if again, _ := r.bashPPTransportEnter(3); again {
		t.Fatal("an origin left the path with its walk")
	}
}
