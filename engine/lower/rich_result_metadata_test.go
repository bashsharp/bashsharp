package lower

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// richResultEmitter builds an emitter that knows the source's declared types,
// which is all classification needs: `Ch` must be recognised as the `int` it
// was declared to be, and not by any property of a value.
func richResultEmitter(t *testing.T, source string) *emitter {
	t.Helper()
	e := &emitter{
		prefix:        "bpp_",
		declaredTypes: map[string]*syntax.BashPPDecl{},
		typeNames:     map[string]bool{},
		scopes:        []map[string]bool{{}},
		globals:       map[string]bool{},
		funcs:         map[string]bool{},
		imports:       map[string]string{},
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "input.bpp")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range file.Stmts {
		if declaration, ok := statement.Cmd.(*syntax.BashPPDecl); ok && declaration.Kw.Value == "type" {
			e.typeNames[declaration.Name.Value] = true
			e.declaredTypes[declaration.Name.Value] = declaration
		}
	}
	return e
}

// The declared types of the reference source. `Ch` is an int, and stays one.
const richResultSource = `type Box struct { N int }
type Ch int
type Ticks Ch
`

// TestRichResultClassification fixes the whole decision table. The case that
// motivates the file is `Ch` / `chan int`: a channel payload in an integer
// result is a capability, never a conversion.
func TestRichResultClassification(t *testing.T) {
	e := richResultEmitter(t, richResultSource)
	for _, tc := range []struct {
		name     string
		declared string
		payload  string
		want     richResultClass
	}{
		{"named int result carrying a channel", "Ch", "chan int", richCapability},
		{"receive-only channel in an int result", "Ch", "<-chan int", richCapability},
		{"send-only channel in an int result", "Ch", "chan<- string", richCapability},
		{"channel result declared natively as receive-only", "<-chan int", "<-chan int", richDirect},
		{"declared type resolved through a chain of names", "Ticks", "chan int", richCapability},
		{"channel result declared natively", "chan int", "chan int", richDirect},
		{"pointer result", "*Box", "*Box", richDirect},
		{"struct result", "Box", "Box", richDirect},
		{"convertible integer result", "Ch", "int", richDirect},
		{"unknown payload keeps today's lowering", "Ch", "", richDirect},
		{"unknown declared type keeps today's lowering", "", "chan int", richDirect},
		{"closure in an int result has no representation", "Ch", "func()", richUnrepresentable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := e.classifyRichResult(richResultDescriptor{Declared: tc.declared, Payload: tc.payload})
			if got != tc.want {
				t.Fatalf("classified %s/%s as %s, want %s", tc.declared, tc.payload, got, tc.want)
			}
		})
	}
}

// TestRichResultPlanForReferenceSignature plans the reference callable:
// `func rich() (ptr *Box, value Box, pipe Ch)`. Only the third result leaves
// the ordinary path, so wiring this cannot disturb the first two.
func TestRichResultPlanForReferenceSignature(t *testing.T) {
	e := richResultEmitter(t, richResultSource)
	plan := e.richResultPlanFor([]richResultDescriptor{
		{Index: 0, Name: "ptr", Declared: "*Box", Payload: "*Box"},
		{Index: 1, Name: "value", Declared: "Box", Payload: "Box"},
		{Index: 2, Name: "pipe", Declared: "Ch", Payload: "chan int"},
	})
	if !plan.NeedsFrame() {
		t.Fatal("the reference signature does not ask for a result frame")
	}
	if got := plan.Capabilities(); len(got) != 1 || got[0].Name != "pipe" {
		t.Fatalf("capability results are %v, want just pipe", got)
	}
	for i, want := range []richResultClass{richDirect, richDirect, richCapability} {
		if plan.Classes[i] != want {
			t.Fatalf("result %d is %s, want %s", i, plan.Classes[i], want)
		}
	}
}

// TestRichResultPlanKeepsOrdinarySignaturesOnTheExistingPath is the safety
// property that lets core wire the hooks incrementally: a callable with no
// rich result asks for no frame at all.
func TestRichResultPlanKeepsOrdinarySignaturesOnTheExistingPath(t *testing.T) {
	e := richResultEmitter(t, richResultSource)
	plan := e.richResultPlanFor([]richResultDescriptor{
		{Index: 0, Declared: "int", Payload: "int"},
		{Index: 1, Declared: "bool", Payload: "bool"},
	})
	if plan.NeedsFrame() || len(plan.Capabilities()) != 0 {
		t.Fatalf("an ordinary signature asked for a frame: %v", plan.Classes)
	}
}

// TestRichResultPlanArityFollowsTheSource keeps the descriptor's width a
// property of the signature: the compiler adds no cap of its own, and a wide
// signature plans exactly as a narrow one does.
func TestRichResultPlanArityFollowsTheSource(t *testing.T) {
	e := richResultEmitter(t, richResultSource)
	results := make([]richResultDescriptor, 64)
	for i := range results {
		results[i] = richResultDescriptor{Index: i, Declared: "int", Payload: "int"}
	}
	results[63] = richResultDescriptor{Index: 63, Declared: "Ch", Payload: "chan int"}
	plan := e.richResultPlanFor(results)
	if len(plan.Classes) != 64 || !plan.NeedsFrame() {
		t.Fatalf("planned %d results, frame=%v", len(plan.Classes), plan.NeedsFrame())
	}
	frame, err := e.richResultFrame("bpp_results", RuntimeContext{Context: "bpp_ctx", Session: "bpp_session", Channels: "bpp_channels"}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frame, ", 64)") {
		t.Fatalf("frame emission %q does not size itself from the signature", frame)
	}
}

// TestRichResultEmission fixes the generated calls. Every one of them takes
// its context, session and ownership scope as explicit expressions: none of
// them reaches for a package-level convention or an ambient last result.
func TestRichResultEmission(t *testing.T) {
	e := richResultEmitter(t, richResultSource)
	e.execution = true
	e.programExpr = "bpp_p"
	context := RuntimeContext{Context: "bpp_ctx", Session: "bpp_session", Channels: "bpp_channels"}
	plan := e.richResultPlanFor([]richResultDescriptor{
		{Index: 0, Name: "ptr", Declared: "*Box", Payload: "*Box"},
		{Index: 1, Name: "value", Declared: "Box", Payload: "Box"},
		{Index: 2, Name: "pipe", Declared: "Ch", Payload: "chan int"},
	})

	frame, err := e.richResultFrame("bpp_results", context, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bpp_rt.NewResultFrame(", "bpp_rt.ResultOwner{Channels: bpp_channels, Session: bpp_session}", ", 3)"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame emission %q lacks %q", frame, want)
		}
	}

	direct, err := e.richResultRecord("bpp_results", plan, 0, "bpp_p0")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bpp_rt.SetResult(bpp_results, 0, (*Box)(bpp_p0))"; !strings.Contains(direct, want) {
		t.Fatalf("direct record %q lacks %q", direct, want)
	}

	capability, err := e.richResultRecord("bpp_results", plan, 2, "bpp_ch")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bpp_rt.MustChannelOperation(bpp_rt.SetResultCapability(bpp_results, 2, " + e.goName("pipe") + ", bpp_ch))"; !strings.Contains(capability, want) {
		t.Fatalf("capability record %q lacks %q", capability, want)
	}
	// The channel is passed as itself. Nothing in the emitted text converts it
	// to the declared integer type.
	if strings.Contains(capability, "(Ch)(bpp_ch)") {
		t.Fatalf("capability record converts a channel to Ch: %q", capability)
	}

	transfer, err := e.richResultTransfer("bpp_results", "bpp_sidecars", []string{"&bpp_p", "&bpp_b", "&bpp_ch"}, "bpp_site")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bpp_rt.TransferResults(bpp_results, bpp_sidecars, []any{&bpp_p,&bpp_b,&bpp_ch}, bpp_site)"; !strings.Contains(transfer, want) {
		t.Fatalf("transfer emission %q lacks %q", transfer, want)
	}

	receive, err := e.richCapabilityReceive(context, "bpp_sidecars", "bpp_ch", "int")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bpp_rt.ReceiveCapability[int](bpp_ctx, bpp_sidecars, &bpp_ch)"; !strings.Contains(receive, want) {
		t.Fatalf("receive emission %q lacks %q", receive, want)
	}

	send, err := e.richCapabilitySend(context, "bpp_sidecars", "bpp_assigned", "string", "bpp_assigned")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bpp_rt.SendCapability[string](bpp_ctx, bpp_sidecars, &bpp_assigned, bpp_assigned)"; !strings.Contains(send, want) {
		t.Fatalf("send emission %q lacks %q", send, want)
	}

	fork, err := e.richSidecarFork(context, "bpp_sidecars", "bpp_snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bpp_rt.ForkSidecars(bpp_sidecars, bpp_snapshot, &bpp_rt.ResultOwner{Channels: bpp_channels, Session: bpp_session})"; !strings.Contains(fork, want) {
		t.Fatalf("fork emission %q lacks %q", fork, want)
	}

	native, err := e.richNativeResult("bpp_results", plan, 2, "bpp_site")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bpp_rt.NativeResult[Ch](bpp_results, 2, bpp_site)"; !strings.Contains(native, want) {
		t.Fatalf("native wrapper emission %q lacks %q", native, want)
	}
}

// TestRichResultEmissionRequiresExplicitState refuses to emit anything that
// would depend on ambient runtime state: no context, no sidecar table, no
// emission.
func TestRichResultEmissionRequiresExplicitState(t *testing.T) {
	e := richResultEmitter(t, richResultSource)
	plan := e.richResultPlanFor([]richResultDescriptor{{Index: 0, Declared: "Ch", Payload: "chan int"}})
	if _, err := e.richResultFrame("bpp_results", RuntimeContext{Context: "bpp_ctx"}, plan); err == nil {
		t.Fatal("a frame was emitted without an ownership scope")
	}
	if _, err := e.richResultTransfer("bpp_results", "", []string{"&bpp_ch"}, "bpp_site"); err == nil {
		t.Fatal("a transfer was emitted without a sidecar table")
	}
	full := RuntimeContext{Context: "bpp_ctx", Session: "bpp_session", Channels: "bpp_channels"}
	if _, err := e.richCapabilityReceive(full, "", "bpp_ch", "int"); err == nil {
		t.Fatal("a capability receive was emitted without a sidecar table")
	}
	if _, err := e.richCapabilityReceive(full, "bpp_sidecars", "bpp_ch", ""); err == nil {
		t.Fatal("a capability receive was emitted without an element type")
	}
	if _, err := e.richCapabilitySend(full, "bpp_sidecars", "bpp_ch", "", "bpp_v"); err == nil {
		t.Fatal("a capability send was emitted without an element type")
	}
	if _, err := e.richSidecarFork(full, "bpp_sidecars", ""); err == nil {
		t.Fatal("a sidecar fork was emitted without a child snapshot")
	}
}

// TestRichResultRecordRejectsUnrepresentable is the other half of the public
// boundary: a payload with no representation and no capability is a
// positioned diagnostic, not a zero value quietly written to the cell.
func TestRichResultRecordRejectsUnrepresentable(t *testing.T) {
	e := richResultEmitter(t, richResultSource)
	plan := e.richResultPlanFor([]richResultDescriptor{{Index: 0, Name: "pipe", Declared: "Ch", Payload: "func()"}})
	if _, err := e.richResultRecord("bpp_results", plan, 0, "bpp_fn"); err == nil {
		t.Fatal("an unrepresentable result was emitted")
	} else if !strings.Contains(err.Error(), CodeResult) {
		t.Fatalf("diagnostic %v does not carry %s", err, CodeResult)
	}
	if _, err := e.richResultRecord("bpp_results", plan, 1, "bpp_fn"); err == nil {
		t.Fatal("a result outside the plan was emitted")
	}
	if _, err := e.richNativeResult("bpp_results", plan, 3, "bpp_site"); err == nil {
		t.Fatal("a native result outside the plan was emitted")
	}
}

// TestRichResultUnnamedCarrierZero covers a capability result with no named
// cell, and a carrier that is not an integer. The carrier keeps the zero of
// its own declared type, spelled the one way that works for every scalar
// carrier — so nothing here is specialised to a type named Ch.
func TestRichResultUnnamedCarrierZero(t *testing.T) {
	e := richResultEmitter(t, richResultSource)
	plan := e.richResultPlanFor([]richResultDescriptor{
		{Index: 0, Declared: "Ch", Payload: "chan int"},
		{Index: 1, Declared: "string", Payload: "chan string"},
	})
	for i, want := range []string{
		"bpp_rt.SetResultCapability(bpp_results, 0, *new(Ch), bpp_ch)",
		"bpp_rt.SetResultCapability(bpp_results, 1, *new(string), bpp_ch)",
	} {
		got, err := e.richResultRecord("bpp_results", plan, i, "bpp_ch")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, want) {
			t.Fatalf("record %d is %q, want %q", i, got, want)
		}
	}
}
