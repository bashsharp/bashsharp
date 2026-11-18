//go:build full

package lower_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower"
)

// A method a struct does not declare itself, but reaches through an embedded
// interface, is promoted by Go onto the outer struct — with the interface's own
// public method set, never the private capability method the runtime lowering
// adds. These cases hold that promotion to the same contract a directly typed
// interface receiver has: the caller's program, frame and agentic authority
// reach the implementation, and the compiled artifact says what the source says.
func TestPromotedInterfaceMethodDispatchAcceptance(t *testing.T) {
	cases := []struct {
		name, source, wantOut, wantErr string
		wantStatus                     int
	}{
		// Authority is the whole point: reaching the public method instead would
		// run the marked method with assistance off and print, so a denial here
		// is what proves the private capability branch was taken.
		{
			name: "marked_promoted_denied_outside_scope",
			source: `type Speaker interface { Speak() }
type Voice int
agentic func (v Voice) Speak() { echo spoke }
type Outer struct { Speaker }
var v Voice = 1
h := Outer{Speaker: v}
h.Speak()
`,
			wantErr:    "Speak: agentic action requires an explicit agentic { ...; } scope\n",
			wantStatus: 1,
		},
		{
			name: "marked_promoted_allowed_in_scope",
			source: `type Speaker interface { Speak() }
type Voice int
agentic func (v Voice) Speak() { echo spoke }
type Outer struct { Speaker }
var v Voice = 1
h := Outer{Speaker: v}
agentic { h.Speak(); }
`,
			wantOut: "spoke\n",
		},
		// A handle taken on a promoted method resolves through the embedded
		// interface too, so it carries the private ABI and the invocation is
		// checked against the frame it is made from. Falling through to the
		// public method value here would both drop the authority and give the
		// handle a type the call site cannot invoke.
		{
			name: "promoted_method_handle",
			source: `type Speaker interface { Speak() }
type Voice int
agentic func (v Voice) Speak() { echo handled }
type Outer struct { Speaker }
var v Voice = 1
h := Outer{Speaker: v}
agentic {
 speak := h.Speak
 speak()
}
`,
			wantOut: "handled\n",
		},
		// Promotion through a struct that itself embeds the interface: Go
		// resolves the selector at the shallowest depth that has it, and so does
		// the lowering.
		{
			name: "promoted_two_levels_deep",
			source: `type Speaker interface { Speak(int) }
type Voice int
agentic func (v Voice) Speak(n int) { echo "depth:$n" }
type Middle struct { Speaker }
type Outer struct { Middle }
var v Voice = 1
h := Outer{Middle: Middle{Speaker: v}}
agentic { h.Speak(2); }
`,
			wantOut: "depth:2\n",
		},
		// An embedded concrete type is untouched by this: Go promotes its
		// private method exactly as it promotes the public one, so the direct
		// selector stays the lowering.
		{
			name: "promoted_concrete_embedding_stays_direct",
			source: `type Voice struct { N int }
agentic func (v Voice) Speak() { printf 'concrete:%s\n' v.N }
type Outer struct { Voice }
h := Outer{Voice: Voice{N: 5}}
agentic { h.Speak(); }
`,
			wantOut: "concrete:5\n",
		},
		// A promoted mutation still reaches the dynamic pointer the embedded
		// interface holds; the outer struct is only the path to it.
		{
			name: "promoted_pointer_mutation_through_embedding",
			source: `type Speaker interface { Speak(); Set(int) }
type Voice struct { N int }
func (v Voice) Speak() { printf '%s:' v.N }
func (p *Voice) Set(n int) { p.N = n }
type Outer struct { Speaker }
func main() {
 p := new(Voice)
 p.N = 1
 var reference Speaker = p
 h := Outer{Speaker: reference}
 h.Set(2)
 h.Speak()
 p.Speak()
 (
  h.Set(3)
  h.Speak()
 )
 h.Speak()
}
main()
`,
			wantOut: "2:2:3:2:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, diagnostic, status := genericMethodOracle(t, tc.source)
			t.Logf("source stdout=%q stderr=%q status=%d", out, diagnostic, status)
			if out != tc.wantOut || diagnostic != tc.wantErr || status != tc.wantStatus {
				t.Fatalf("source contract differs from control: out=%q stderr=%q status=%d", out, diagnostic, status)
			}
			execute(t, compile(t, tc.source))
		})
	}
}

// Go refuses a selector that two embeddings at the same depth both supply
// rather than picking one. Guessing a side here would lower the call against
// another implementation's signature, so the compiler refuses too.
func TestPromotedInterfaceMethodAmbiguityDiagnostic(t *testing.T) {
	const source = `type Left interface { Speak() }
type Right interface { Speak() }
type Voice int
agentic func (v Voice) Speak() { echo spoke }
type Outer struct { Left; Right }
var v Voice = 1
h := Outer{Left: v, Right: v}
agentic { h.Speak(); }
`
	_, err := lower.Compile(parse(t, source, "input.bpp"), lower.Options{})
	if err == nil {
		t.Fatal("ambiguous promotion compiled")
	}
	if !strings.Contains(err.Error(), "promoted from several embedded fields") {
		t.Fatalf("diagnostic does not name the ambiguity: %v", err)
	}
}
