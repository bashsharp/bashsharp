package interp

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #243; Story: #671; Story-ID: 56d156f9118e

func TestNilMapFaultIsPlainRuntimeError(t *testing.T) {
	if got := errBashPPNilMapAssign.bashPPRuntimeErrorText(); got != "assignment to entry in nil map" {
		t.Fatalf("nil map fault text = %q", got)
	}
	if got := errBashPPNilMapAssign.Error(); got != "BASHPP-ENIL-MAP: assignment to nil map" {
		t.Fatalf("nil map refusal = %q", got)
	}
	if got := errBashPPNilDereference.bashPPRuntimeErrorText(); got != "runtime error: invalid memory address or nil pointer dereference" {
		t.Fatalf("nil dereference text = %q", got)
	}
}

func TestCopyArrayValueKeepsExactCapacity(t *testing.T) {
	typ := &syntax.BashPPCollectionType{Kind: "array", Length: &syntax.Lit{Value: "64"}, Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "byte"}}}
	values := make([]any, 64, 80)
	metas := make([]*bashPPCollectionMeta, 64, 96)
	meta := &bashPPCollectionMeta{kind: "array", typ: typ, sequence: metas}
	copied, copiedMeta := bashPPCopyArrayValue(values, meta)
	seq, ok := copied.([]any)
	if !ok {
		t.Fatalf("array copy payload is %T", copied)
	}
	if cap(seq) != 64 || cap(copiedMeta.sequence) != 64 {
		t.Fatalf("array copy capacity: payload=%d metadata=%d, want 64", cap(seq), cap(copiedMeta.sequence))
	}
	if &seq[0] == &values[0] {
		t.Fatal("array copy shares the source backing array")
	}
}

func TestS243UntypedNilCandidateUsesCellIdentity(t *testing.T) {
	r := &Runner{bashPPGoSource: true, bashPPScope: newBashPPScope(nil)}
	typ := &syntax.BashPPCollectionType{Kind: "slice", Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}}}
	target := &bashPPCell{declType: typ}
	literal := &syntax.BashPPIdent{Name: &syntax.Lit{Value: "nil"}}
	untyped, handled, err := r.goSourceNilValueCell(literal)
	if !handled || err != nil {
		t.Fatalf("nil literal: %v %v", handled, err)
	}
	for _, name := range []string{"saved", "tuple_0", "renamed_value"} {
		r.bashPPScope.entries[name] = untyped
		expr := &syntax.BashPPIdent{Name: &syntax.Lit{Value: name}}
		got, handled, err := r.goSourceUntypedNilTemporaryCandidate(target, expr)
		if !handled || err != nil || got.declType != typ || goSourceUntypedNilCell(got) {
			t.Fatalf("%s: candidate=%+v handled=%v err=%v", name, got, handled, err)
		}
		// Even a generated-looking identifier must preserve typed nil.
		r.bashPPScope.entries[name] = got
		if _, handled, err := r.goSourceUntypedNilTemporaryCandidate(target, expr); handled || err != nil {
			t.Fatalf("typed nil %s intercepted: handled=%v err=%v", name, handled, err)
		}
	}
	if goSourceUntypedNilCell(nil) {
		t.Fatal("absent cell is untyped nil")
	}
}
