package interp

import (
	"strings"
	"testing"
)

// Sprint: #249; Story: #715; Story-ID: 90f96d4f4dae
func TestBashPPAsmSymbols(t *testing.T) {
	const text = `#include "textflag.h"
// ·commented(SB) is not a reference
/* ·blocked(SB) is not one either */
DATA ·pointer(SB)/8, $·target(SB)
GLOBL ·pointer(SB), RODATA, $8
GLOBL ·scratch<>(SB), RODATA, $8

TEXT ·jump(SB), NOSPLIT, $8-0
	CALL *·pointer(SB)
	MOVQ ·table+16(SB), AX
	CALL runtime·procyield(SB)
	CALL example.com/other·Helper(SB)
	CALL main·shared(SB)
	RET
`
	defined, referenced := map[string]bool{}, map[string]bool{}
	bashPPAsmSymbols(text, defined, referenced)
	wantDefined := []string{"jump", "pointer"}
	wantReferenced := []string{"pointer", "shared", "table", "target"}
	for _, name := range wantDefined {
		if !defined[name] {
			t.Errorf("defined is missing %q: %v", name, defined)
		}
	}
	if len(defined) != len(wantDefined) {
		t.Errorf("defined = %v, want exactly %v", defined, wantDefined)
	}
	for _, name := range wantReferenced {
		if !referenced[name] {
			t.Errorf("referenced is missing %q: %v", name, referenced)
		}
	}
	if len(referenced) != len(wantReferenced) {
		t.Errorf("referenced = %v, want exactly %v", referenced, wantReferenced)
	}
}

func TestBashPPCompanionTrampolineGo(t *testing.T) {
	got := bashPPCompanionTrampolineGo("target", nil, nil)
	for _, want := range []string{"func target() {", " companionOffFrame(func(){", `callback("\x00gosource.companion.target"`, "if len(out)!=0"} {
		if !strings.Contains(got, want) {
			t.Errorf("trampoline %q does not contain %q", got, want)
		}
	}
	// Results are named, so the protocol can fill them from the goroutine the
	// body runs on and the companion's own frame only waits.
	got = bashPPCompanionTrampolineGo("mix", []string{"int", "string"}, []string{"int", "error"})
	for _, want := range []string{
		"func mix(bpparg0 int, bpparg1 string) (bppres0 int, bppres1 error) {",
		" companionOffFrame(func(){",
		"recv.CallArgs=[]value{encode(reflect.ValueOf(bpparg0)),encode(reflect.ValueOf(bpparg1))}",
		"if len(out)!=2",
		"reflect.TypeFor[error]()",
		"if bppval1.IsValid(){bppres1,_=bppval1.Interface().(error)}",
		" })\n return\n}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("trampoline %q does not contain %q", got, want)
		}
	}
}

// Sprint: #249; Story: #715; Story-ID: 90f96d4f4dae
//
// The frames the helper must keep still are the ones with local bytes and no
// map for them. A frameless companion needs nothing, an annotated one carries
// its own map, and a frame size the scanner cannot read is reported as needing
// the window rather than assumed not to.
func TestBashPPAsmUnmappedFrames(t *testing.T) {
	const text = `#include "textflag.h"
#include "funcdata.h"

DATA ·pointer(SB)/8, $·target(SB)
GLOBL ·pointer(SB), RODATA, $8

TEXT ·leaf(SB), NOSPLIT, $0-16
	RET

TEXT ·mapped(SB), NOSPLIT, $8-0
	NO_LOCAL_POINTERS
	CALL *·pointer(SB)
	RET

TEXT ·funcdata(SB), NOSPLIT, $24-0
	FUNCDATA $1, ·mapped_stkmap(SB)
	CALL *·pointer(SB)
	RET

TEXT ·jump(SB), NOSPLIT, $8-0
	CALL *·pointer(SB)
	RET

TEXT ·hidden<>(SB), NOSPLIT, $16-0
	RET

TEXT ·noframe(SB), NOSPLIT|NOFRAME, $-8-0
	RET

TEXT ·computed(SB), NOSPLIT, $frameSize-0
	RET
`
	unmapped := map[string]bool{}
	bashPPAsmUnmappedFrames(text, unmapped)
	want := []string{"jump", "hidden", "computed"}
	for _, name := range want {
		if !unmapped[name] {
			t.Errorf("unmapped is missing %q: %v", name, unmapped)
		}
	}
	if len(unmapped) != len(want) {
		t.Errorf("unmapped = %v, want exactly %v", unmapped, want)
	}
}
