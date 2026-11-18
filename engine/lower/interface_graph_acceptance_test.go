//go:build full

package lower_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// These exact public interpreter programs establish native compiler acceptance
// for interface value graphs. The shared runner compares every output byte and
// exit status after building ordinary Go and removing the generated source.
func TestInterfaceGraphAcceptance(t *testing.T) {
	cases := []struct{ name, source string }{
		// interp/bashpp_embed_test.go:TestBashPPEmbeddedInterfaceValueAndReferenceSemantics
		{"value_copy_and_shared_dynamic_pointer", "type Speaker interface { Speak() }\ntype Voice struct { N int }\nfunc (v Voice) Speak() { printf '%s:' v.N }\ntype Outer struct { Speaker }\nfunc main() {\n v := Voice{N: 1}\n var value Speaker = v\n h := Outer{Speaker: value}\n v.N = 2\n h.Speak()\n\n p := new(Voice)\n p.N = 3\n var reference Speaker = p\n h.Speaker = reference\n copied := h\n p.N = 4\n h.Speak()\n copied.Speak()\n\n h.Speaker = value\n copied.Speak()\n h.Speak()\n}\nmain()\n"},
		// interp/bashpp_embed_test.go:TestBashPPEmbeddedInterfaceDirectMethodShadowsAndEmbeddedInterfacesCompose
		{"direct_method_and_composed_interfaces", "type Reader interface { Read() }\ntype Writer interface { Write() }\ntype ReadWriter interface { Reader; Writer }\ntype Device int\nfunc (d Device) Read() { printf r }\nfunc (d Device) Write() { printf w }\ntype Outer struct { ReadWriter }\nfunc (o Outer) Read() { printf R }\nfunc main() {\n\tvar device Device = 0\n\tvar d ReadWriter = device\n o := Outer{ReadWriter: d}\n o.Read()\n o.Write()\n}\nmain()\n"},
		// interp/bashpp_embed_test.go:TestBashPPEmbeddedInterfaceTypedNilRetainsDynamicType
		{"typed_nil_dispatch", "type Speaker interface { Speak() }\ntype Voice int\nfunc (p *Voice) Speak() { printf nil-pointer }\ntype Outer struct { Speaker }\nfunc main() {\n var p *Voice\n var s Speaker = p\n o := Outer{Speaker: s}\n o.Speaker.Speak()\n}\nmain()\n"},
		// interp/bashpp_embed_test.go:TestBashPPEmbeddedInterfaceTypedNilDereferenceDiagnostic
		{"typed_nil_dereference", "type Speaker interface { Speak() }\ntype Voice struct { N int }\nfunc (p *Voice) Speak() {\n x := p.N\n println(x)\n}\ntype Outer struct { Speaker }\nfunc main() {\n var p *Voice\n var s Speaker = p\n o := Outer{Speaker: s}\n o.Speaker.Speak()\n}\nmain()\n"},
		// interp/bashpp_error_consumer_test.go:TestBashPPPredeclaredErrorInterface
		{"error_interface_embedding_and_assertion", "type Failure string\nfunc (f Failure) Error() string { return f }\ntype RichError interface { error }\nfunc main() {\n var empty error\n emptyNil := empty == nil\n var failure Failure = \"broken\"\n var err error = failure\n message := err.Error()\n original, ok := err.(Failure)\n var embedded RichError = failure\n nested := embedded.Error()\n println(emptyNil, message, original, ok, nested)\n}\nmain()\n"},
		// interp/bashpp_error_consumer_test.go:TestBashPPErrorInterfaceTypedNil
		{"error_interface_typed_nil", "type Failure string\nfunc (f Failure) Error() string { return f }\nfunc main() {\n var p *Failure = nil\n var e error = p\n equal := e == nil\n _, ok := e.(*Failure)\n println(equal, ok)\n}\nmain()\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Record the complete live oracle even when compilation or artifact parity
			// subsequently fails. Later output after a failed declaration is observable.
			var out, diagnostic bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &diagnostic), interp.Env(expand.ListEnviron("PATH=/no-tools")))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			status := 0
			if err := runner.Run(ctx, parse(t, tc.source, "input.bpp")); err != nil {
				var exit interp.ExitStatus
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				status = int(exit)
			}
			t.Logf("interpreter: status=%d stdout=%q stderr=%q", status, out.String(), diagnostic.String())
			execute(t, compile(t, tc.source))
		})
	}
}
