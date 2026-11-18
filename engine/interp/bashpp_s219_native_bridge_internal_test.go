// Sprint: #219; Story: #463; Story-ID: a6f104b906d9

package interp

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A handle nested in a written-back slice element is accepted only under this
// session's identity: the writeback authenticates what arrived on its own
// connection, and a handle stamped by any other session — or arriving with no
// session at all — still fails closed at the element rebuild.
func TestS219SliceWritebackForeignSessionHandleRefused(t *testing.T) {
	r := &Runner{}
	r.bashPPTools.bridge = &bashPPNativeSession{id: "session-a"}
	elem := &syntax.BashPPStructType{}
	for _, session := range []string{"", "session-b"} {
		v := bashPPBridgeValue{Kind: "handle", Handle: 7, Type: "reflect.StructField", Session: session}
		if _, _, err := r.bashPPBridgeContents(v, elem); err == nil || !strings.Contains(err.Error(), "belongs to another dependency session") {
			t.Fatalf("session %q: want refusal, got %v", session, err)
		}
	}
	own := bashPPBridgeValue{Kind: "handle", Handle: 7, Type: "reflect.StructField", Session: "session-a"}
	value, meta, err := r.bashPPBridgeContents(own, elem)
	if err != nil || meta == nil || meta.kind != "native" {
		t.Fatalf("own session: value=%v meta=%+v err=%v", value, meta, err)
	}
	// The session stamp reaches a handle nested in a struct element, the shape
	// reflect.StructField crosses back in.
	update := bashPPBridgeValue{Kind: "slice", Elements: []bashPPBridgeValue{{Kind: "struct", Fields: map[string]bashPPBridgeValue{
		"Type": {Kind: "handle", Handle: 9},
		"Name": {Kind: "string", Type: "string", Text: "A"},
	}}}}
	r.bashPPTools.bridge.bashPPAuthenticateCallbackValue(&update)
	if got := update.Elements[0].Fields["Type"].Session; got != "session-a" {
		t.Fatalf("nested handle session = %q, want session-a", got)
	}
	if got := update.Elements[0].Fields["Name"].Session; got != "" {
		t.Fatalf("scalar field stamped with %q", got)
	}
}
